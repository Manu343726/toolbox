// Package cliapp is the shared entrypoint used by every standalone subsystem
// command. It starts the subsystem in-process and builds the user-facing
// command tree from gRPC reflection and protobuf descriptors.
package cliapp

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Manu343726/toolbox/pkg/cli"
	"github.com/Manu343726/toolbox/pkg/discovery"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/spf13/cobra"
)

// MCPOptions configures the automatically generated `mcp` subcommand.
type MCPOptions struct {
	// Policy optionally overrides the feature policy derived from subsystem
	// service capabilities.
	Policy toolboxmcp.FeaturePolicy
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
}

// Run starts a subsystem and executes its generated command tree.
func Run(ctx context.Context, options Options) error {
	if options.Factory == nil {
		return fmt.Errorf("subsystem factory is required")
	}
	if options.Name == "" {
		return fmt.Errorf("subsystem name is required")
	}
	server, err := options.Factory()
	if err != nil {
		return err
	}
	if err := server.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	discoveryClient := discovery.New(server.Endpoint())
	generator := cli.NewGenerator(discoveryClient, cli.Options{
		CommandName:       options.Name,
		Description:       options.Description,
		IncludeReflection: options.IncludeReflection || options.MCP.IncludeReflection,
	})
	serviceNames := make([]string, 0)
	for _, service := range server.Services() {
		serviceNames = append(serviceNames, service.Name)
	}
	root, err := generator.Generate(ctx, serviceNames...)
	if err != nil {
		return err
	}
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		return server.Wait(cmd.Context())
	}
	serveCommand := &cobra.Command{
		Use:   "serve",
		Short: "Run the subsystem without invoking an RPC",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return server.Wait(cmd.Context())
		},
	}
	root.AddCommand(serveCommand)
	mcpCommand := &cobra.Command{
		Use:   "mcp",
		Short: "Launch an MCP server generated from this subsystem",
		Long:  "Launch a Model Context Protocol server over stdio. The server reflects this subsystem's RPC services, exposes unary methods as tools, and provides introspection and exposure tools.",
		Args:  cobra.NoArgs,
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
	return root.Execute()
}

// Main is a convenience wrapper for command main functions.
func Main(options Options) {
	if err := Run(context.Background(), options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
