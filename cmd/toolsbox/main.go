// Command toolsbox launches one or more built-in Toolsbox subsystems in a
// single process. Feature modules remain independently buildable; this host
// only composes their programmatic entrypoints.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/host"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	agent "github.com/Manu343726/toolsbox/subsystems/agent"
	documentation "github.com/Manu343726/toolsbox/subsystems/documentation"
	health "github.com/Manu343726/toolsbox/subsystems/health"
	knowledge "github.com/Manu343726/toolsbox/subsystems/knowledge"
	model "github.com/Manu343726/toolsbox/subsystems/model"
	policy "github.com/Manu343726/toolsbox/subsystems/policy"
	prompt "github.com/Manu343726/toolsbox/subsystems/prompt"
	registry "github.com/Manu343726/toolsbox/subsystems/registry"
	registryv1 "github.com/Manu343726/toolsbox/subsystems/registry/registryv1"
	registryv1connect "github.com/Manu343726/toolsbox/subsystems/registry/registryv1/registryv1connect"
	skill "github.com/Manu343726/toolsbox/subsystems/skill"
	tool "github.com/Manu343726/toolsbox/subsystems/tool"
	workflow "github.com/Manu343726/toolsbox/subsystems/workflow"
	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "toolsbox",
		Short:         "Composable Toolsbox subsystem host",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runServe,
	}
	flags := root.Flags()
	flags.StringSlice("component", nil, "Subsystem names to launch; repeatable or comma-separated")
	flags.Bool("all", false, "Launch all built-in subsystems")
	return root
}

func runServe(cmd *cobra.Command, _ []string) error {
	components, err := cmd.Flags().GetStringSlice("component")
	if err != nil {
		return err
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}
	if all && len(components) > 0 {
		return fmt.Errorf("--all cannot be combined with --component")
	}
	if !all && len(components) == 0 {
		all = true
	}

	h, err := buildHost()
	if err != nil {
		return err
	}
	if !all {
		if err := h.Select(components...); err != nil {
			return err
		}
	}
	ctx := cmd.Context()
	if err := h.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = h.Shutdown(context.Background()) }()

	if all {
		if err := registerStartedSubsystems(cmd.Context(), h); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return h.Shutdown(context.Background())
}

func buildHost() (*host.Host, error) {
	h := host.New()
	factories := map[string]subsystem.Factory{
		"agent":         func() (*subsystem.Server, error) { return agent.New(agent.Options{}) },
		"documentation": func() (*subsystem.Server, error) { return documentation.New(documentation.Options{}) },
		"health":        func() (*subsystem.Server, error) { return health.New(health.Options{}) },
		"knowledge":     func() (*subsystem.Server, error) { return knowledge.New(knowledge.Options{}) },
		"model":         func() (*subsystem.Server, error) { return model.New(model.Options{}) },
		"policy":        func() (*subsystem.Server, error) { return policy.New(policy.Options{}) },
		"prompt":        func() (*subsystem.Server, error) { return prompt.New(prompt.Options{}) },
		"registry":      func() (*subsystem.Server, error) { return registry.New(registry.Options{}) },
		"skill":         func() (*subsystem.Server, error) { return skill.New(skill.Options{}) },
		"tool":          func() (*subsystem.Server, error) { return tool.New(tool.Options{}) },
		"workflow":      func() (*subsystem.Server, error) { return workflow.New(workflow.Options{}) },
	}
	for name, factory := range factories {
		if err := h.Register(name, factory); err != nil {
			return nil, err
		}
	}
	return h, nil
}

func registerStartedSubsystems(ctx context.Context, h *host.Host) error {
	registryServer, ok := h.Servers()["registry"]
	if !ok {
		return nil
	}
	client := registryv1connect.NewRegistryServiceClient(http.DefaultClient, registryServer.Endpoint())
	for _, descriptor := range h.Descriptors() {
		capabilities := make([]*registryv1.Capability, 0, len(descriptor.Capabilities))
		for _, name := range descriptor.Capabilities {
			capabilities = append(capabilities, &registryv1.Capability{Name: name})
		}
		registration := &registryv1.ServiceDescriptor{
			SubsystemName:         descriptor.SubsystemName,
			Endpoint:              descriptor.Endpoint,
			ImplementationVersion: descriptor.ImplementationVersion,
			ApiVersion:            descriptor.APIVersion,
			ServiceNames:          append([]string(nil), descriptor.ServiceNames...),
			Capabilities:          capabilities,
			Dependencies:          append([]string(nil), descriptor.Dependencies...),
			Description:           descriptor.Description,
		}
		if _, err := client.Register(ctx, connect.NewRequest(&registryv1.RegisterRequest{Descriptor_: registration, LeaseSeconds: 30})); err != nil {
			return fmt.Errorf("register subsystem %q: %w", descriptor.SubsystemName, err)
		}
	}
	return nil
}
