// Package cliapp is the shared entrypoint used by every standalone subsystem
// command. It starts the subsystem in-process and builds the user-facing
// command tree from gRPC reflection and protobuf descriptors.
package cliapp

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Manu343726/toolsbox/pkg/cli"
	"github.com/Manu343726/toolsbox/pkg/discovery"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	"github.com/spf13/cobra"
)

// Options configures a standalone subsystem command.
type Options struct {
	// Name is the root command and subsystem name.
	Name string
	// Description is displayed in generated help.
	Description string
	// Factory constructs the subsystem server. It is the programmatic
	// in-process entrypoint supplied by the subsystem package.
	Factory subsystem.Factory
	// IncludeInfrastructure exposes health, registry, and documentation RPCs
	// in the generated command tree. It is false by default.
	IncludeInfrastructure bool
	// Output receives generated command output. Defaults to os.Stdout.
	Output io.Writer
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
		CommandName:           options.Name,
		Description:           options.Description,
		IncludeInfrastructure: options.IncludeInfrastructure,
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
	if options.Output != nil {
		root.SetOut(options.Output)
		root.SetErr(options.Output)
	}
	root.SetContext(ctx)
	return root.Execute()
}

// Main is a convenience wrapper for command main functions.
func Main(options Options) {
	if err := Run(context.Background(), options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
