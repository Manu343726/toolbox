// Package cliapp is the shared entrypoint used by every standalone subsystem
// command. It starts the subsystem in-process and builds the user-facing
// command tree from gRPC reflection and protobuf descriptors.
package cliapp

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/cli"
	"github.com/Manu343726/toolbox/pkg/core"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/spf13/cobra"
)

// MCPOptions configures the automatically generated `mcp` subcommand.
type MCPOptions struct {
	// Policy decides which of the subsystem's operations the generated MCP may
	// expose. The zero value permits nothing; a command serving one subsystem
	// states its own, so a user who asked for this subsystem gets this subsystem.
	Policy api.Policy
	// InitialExposure controls whether generated feature tools are present
	// immediately. The zero value exposes all allowed unary features.
	InitialExposure toolboxmcp.InitialExposure
	// IncludeReflection exposes the protocol's reflection services, which
	// describe a contract rather than provide a capability. It is false by
	// default, because a client that already knows the contract has no use for it.
	IncludeReflection bool
	// ServiceName optionally restricts the generated MCP to one mounted
	// service. Empty includes every service on the subsystem.
	ServiceName string
}

// Options configures a standalone subsystem command.
type Options struct {
	// Name is the root command and subsystem name.
	Name string
	// Description is displayed in generated help.
	Description string
	// Factory constructs the subsystem server. It is the programmatic
	// in-process entrypoint supplied by the subsystem package.
	Factory subsystem.Factory
	// IncludeReflection exposes the protocol's reflection services, which
	// describe a contract rather than provide a capability. It is false by default.
	IncludeReflection bool
	// Output receives generated command output. Defaults to os.Stdout.
	Output io.Writer
	// Args optionally supplies command arguments for programmatic callers and
	// tests. Nil uses the process arguments in normal command mains.
	Args []string
	// MCP configures the automatically generated `mcp` subcommand.
	MCP MCPOptions
	// Core is the address of a running core, as host:port. When it is set the command
	// calls that deployment instead of starting a private instance of the subsystem, so
	// state written through the command is state the deployment holds.
	//
	// Empty takes the address from the environment or a configuration file, and falls back
	// to a private instance when neither names a core. The address is not a registry
	// lookup: this command knows which contract it serves, and a core's address is where
	// contracts are served. That is what keeps a standalone command from depending on the
	// registry subsystem to find its own way home.
	Core string
	// ResolverFor builds a resolver for a core at an address, and returns how to release it.
	//
	// It is a function rather than a resolver because a core's address is the registry's,
	// not the address of every service the core hosts: a peer is found by asking the core
	// what it holds. Building that resolver needs the registry's contract, which belongs to
	// the registry subsystem, so a command that wants to reach a core asks for the resolver
	// rather than this package reaching for the contract itself.
	//
	// Nil means a command given a core address cannot resolve a peer, and says so, because
	// sending a call to the core's own address would get a 404 that reads as "that operation
	// does not exist" rather than as "you asked the wrong place".
	ResolverFor func(coreAddress string) (core.Resolver, func(), error)
	// Output receives the resolution report. Nil uses the command's error stream.
	Report io.Writer
}

// Run starts a subsystem and executes its generated command tree.
//
// When a core address is configured, the command calls that deployment instead: nothing is
// started, and the operations are described from the contracts this binary links. That is
// the difference between a command that reaches the deployment's state and one that
// reaches a private copy of it, and for a write the difference is the whole point — a write
// to a private instance reports success and is gone by the next invocation.
func Run(ctx context.Context, options Options) error {
	if options.Factory == nil {
		return fmt.Errorf("subsystem factory is required")
	}
	if options.Name == "" {
		return fmt.Errorf("subsystem name is required")
	}

	// Composed but not started: a factory builds a server and a server binds its port only
	// when it starts, so the command tree is built — and a help screen printed — without a
	// port being held or a listener that has to be released.
	//
	// The core flag is read when a call is made rather than here, because cobra fills it
	// during parsing, after this. So the tree is built from the contracts this binary links
	// and the target decides per call whether a core or a private instance answers.
	commandTarget, serviceNames, err := newTarget(options, nil)
	if err != nil {
		return err
	}
	source, err := commandTarget.source()
	if err != nil {
		return err
	}
	generator := cli.NewGenerator(source, cli.Options{
		CommandName:       options.Name,
		Description:       options.Description,
		IncludeReflection: options.IncludeReflection || options.MCP.IncludeReflection,
	})
	root, err := generator.Generate(ctx, serviceNames...)
	if err != nil {
		return err
	}
	// Persistent, so it is readable before and after a command name: a caller who typed
	// "knowledge --core host:port search" and one who typed "knowledge search --core
	// host:port" meant the same thing, and a flag that only worked in one position would
	// make the other look like a different deployment.
	//
	// The default is whatever the caller set programmatically, so a program embedding this
	// runner keeps its address and a person at a shell can override it.
	coreFlag := root.PersistentFlags().String("core", options.Core,
		"Address of the core this command calls, as host:port; empty runs a private instance of this subsystem")
	// Handed over now that the flag exists, so the value cobra parsed is the one a call
	// resolves against.
	commandTarget.flag = func() string { return *coreFlag }
	// No operation named: start the subsystem and serve, or — when a core is configured —
	// say there is nothing here to serve, because the core is already serving. A command
	// pointed at a core that blocked anyway would look like a deployment that had come up
	// and was not answering.
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		return commandTarget.serve(cmd.Context(), cmd)
	}
	serveCommand := &cobra.Command{
		Use:   "serve",
		Short: "Run the subsystem without invoking an RPC",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := commandTarget.localOnly("serving the subsystem"); err != nil {
				return err
			}
			instance, err := commandTarget.privateInstance(cmd.Context())
			if err != nil {
				return err
			}
			return instance.Wait(cmd.Context())
		},
	}
	root.AddCommand(serveCommand)
	mcpCommand := &cobra.Command{
		Use:   "mcp",
		Short: "Launch an MCP server generated from this subsystem",

		Long: "Launch a Model Context Protocol server over stdio. The server reflects this subsystem's RPC services, exposes unary methods as tools, and provides introspection and exposure tools.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			minimal, err := cmd.Flags().GetBool("minimal")
			if err != nil {
				return err
			}
			includeReflection, err := cmd.Flags().GetBool("include-reflection")
			if err != nil {
				return err
			}
			serviceName, err := cmd.Flags().GetString("service")
			if err != nil {
				return err
			}
			if serviceName == "" {
				serviceName = options.MCP.ServiceName
			}
			initialExposure := options.MCP.InitialExposure
			if minimal {
				initialExposure = toolboxmcp.ExposeNoFeatures
			}
			mcpOptions := toolboxmcp.Options{
				Name:            options.Name,
				Description:     options.Description,
				Policy:          options.MCP.Policy,
				InitialExposure: initialExposure,
				// A standalone subsystem command serves that subsystem's own
				// surface, so a health or registry service it mounts is part of
				// what the command offers. What an agent gets is decided by the
				// policy and by exposure, not by the name of a service.
				IncludeReflection: includeReflection || options.MCP.IncludeReflection,
			}
			if err := commandTarget.localOnly("generating a Model Context Protocol server"); err != nil {
				return err
			}
			server, err := commandTarget.privateInstance(cmd.Context())
			if err != nil {
				return err
			}
			var bridge *toolboxmcp.Server
			if serviceName != "" {
				bridge, err = toolboxmcp.NewFromSubsystemService(cmd.Context(), server, serviceName, mcpOptions)
			} else {
				bridge, err = toolboxmcp.NewFromSubsystem(cmd.Context(), server, mcpOptions)
			}
			if err != nil {
				return err
			}
			return bridge.ServeStdio(cmd.Context())
		},
	}
	mcpFlags := mcpCommand.Flags()
	mcpFlags.Bool("minimal", false, "Start with only introspection tools; expose RPC features explicitly")
	mcpFlags.Bool("include-reflection", false, "Include the protocol's reflection services, which describe contracts rather than provide capabilities")
	// The flag this replaces excluded services by name. Nothing is excluded by name
	// any more, so it only ever meant the reflection services; it is kept so an
	// existing command line keeps working.
	mcpFlags.Bool("include-infrastructure", false, "Deprecated: use --include-reflection")
	_ = mcpFlags.MarkDeprecated("include-infrastructure", "use --include-reflection; services are no longer excluded by name")
	_ = mcpFlags.MarkHidden("include-infrastructure")
	mcpFlags.String("service", "", "Expose only one mounted service by fully-qualified protobuf name")
	root.AddCommand(mcpCommand)
	if options.Output != nil {
		root.SetOut(options.Output)
		root.SetErr(options.Output)
	}
	root.SetContext(ctx)
	if options.Args != nil {
		root.SetArgs(options.Args)
	}
	// A failed call names the core it was aimed at, because a dial error on its own leaves a
	// reader to work out which of the two modes they are in — and a write that failed
	// against an absent core is not a write that happened privately.
	reportFailedCalls(root, commandTarget.coreAddress)
	defer commandTarget.shutdown()
	return root.Execute()
}

// Main is a convenience wrapper for command main functions.
func Main(options Options) {
	if err := Run(context.Background(), options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
