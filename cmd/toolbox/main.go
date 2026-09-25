// Command toolbox launches one or more built-in Toolbox subsystems in a
// single process. Feature modules remain independently buildable; this host
// only composes their programmatic entrypoints.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/host"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
	"github.com/Manu343726/toolbox/pkg/protocontract"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	agent "github.com/Manu343726/toolbox/subsystems/agent"
	apigrpc "github.com/Manu343726/toolbox/subsystems/apigrpc"
	apimcp "github.com/Manu343726/toolbox/subsystems/apimcp"
	apiopenapi "github.com/Manu343726/toolbox/subsystems/apiopenapi"
	apitools "github.com/Manu343726/toolbox/subsystems/apitools"
	documentation "github.com/Manu343726/toolbox/subsystems/documentation"
	health "github.com/Manu343726/toolbox/subsystems/health"
	knowledge "github.com/Manu343726/toolbox/subsystems/knowledge"
	model "github.com/Manu343726/toolbox/subsystems/model"
	policy "github.com/Manu343726/toolbox/subsystems/policy"
	prompt "github.com/Manu343726/toolbox/subsystems/prompt"
	registry "github.com/Manu343726/toolbox/subsystems/registry"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	registryv1connect "github.com/Manu343726/toolbox/subsystems/registry/registryv1/registryv1connect"
	skill "github.com/Manu343726/toolbox/subsystems/skill"
	tool "github.com/Manu343726/toolbox/subsystems/tool"
	workflow "github.com/Manu343726/toolbox/subsystems/workflow"
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
		Use:           "toolbox",
		Short:         "Composable Toolbox subsystem host",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runServe,
	}
	flags := root.Flags()
	flags.StringSlice("component", nil, "Subsystem names to launch; repeatable or comma-separated")
	flags.Bool("all", false, "Launch all built-in subsystems")

	mcpCommand := &cobra.Command{
		Use:   "mcp",
		Short: "Launch an aggregated MCP server for discovered subsystems",
		Long:  "Launch one Model Context Protocol server over stdio containing the reflected services of the selected built-in subsystems.",
		Args:  cobra.NoArgs,
		RunE:  runMCP,
	}
	mcpFlags := mcpCommand.Flags()
	mcpFlags.StringSlice("component", nil, "Subsystem names to expose; repeatable or comma-separated")
	mcpFlags.Bool("all", false, "Expose all built-in subsystems")
	mcpFlags.Bool("minimal", false, "Start with only introspection tools")
	mcpFlags.Bool("include-infrastructure", false, "Include health, registry, documentation, and reflection services")
	mcpFlags.String("mcp-source", "reflection", "Where the tool surface comes from: reflection reads served contracts directly, catalog registers every subsystem in the API catalog first")
	mcpFlags.StringSlice("service", nil, "Only expose these fully-qualified services; repeatable or comma-separated")
	root.AddCommand(mcpCommand)
	return root
}

func runMCP(cmd *cobra.Command, _ []string) error {
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
	minimal, err := cmd.Flags().GetBool("minimal")
	if err != nil {
		return err
	}
	includeInfrastructure, err := cmd.Flags().GetBool("include-infrastructure")
	if err != nil {
		return err
	}
	serviceFilter, err := cmd.Flags().GetStringSlice("service")
	if err != nil {
		return err
	}
	source, err := cmd.Flags().GetString("mcp-source")
	if err != nil {
		return err
	}
	source = api.NormalizeIdentifier(source)
	switch source {
	case "reflection", "catalog":
	case "":
		source = "reflection"
	default:
		return fmt.Errorf("--mcp-source must be reflection or catalog, not %q", source)
	}
	if !all && len(components) == 0 {
		all = true
	}

	h, catalog, err := buildHost()
	if err != nil {
		return err
	}
	if !all {
		if err := h.Select(components...); err != nil {
			return err
		}
	}
	if err := h.Start(cmd.Context()); err != nil {
		return err
	}
	defer func() { _ = h.Shutdown(context.Background()) }()
	if err := registerStartedSubsystems(cmd.Context(), h); err != nil {
		return err
	}
	initialExposure := toolboxmcp.ExposeAllowedFeatures
	if minimal {
		initialExposure = toolboxmcp.ExposeNoFeatures
	}
	// The catalog path registers what the host runs, then builds the gateway from
	// the catalog. The reflection path reads the served contracts directly. Both
	// produce tools with the same names, so an agent sees one surface either way;
	// what differs is that the catalog can be exposed per operation, re-published in
	// another format, and kept when the gateway restarts.
	if source == "catalog" {
		return runCatalogMCP(cmd, h, catalog, initialExposure, minimal)
	}
	descriptors := h.Descriptors()
	if len(serviceFilter) > 0 {
		descriptors, err = filterDescriptorsByService(descriptors, serviceFilter)
		if err != nil {
			return err
		}
	}
	bridge, err := toolboxmcp.NewFromDescriptors(cmd.Context(), descriptors, toolboxmcp.Options{
		Name:                  "toolbox",
		Description:           "Aggregated Model Context Protocol server for Toolbox subsystems.",
		InitialExposure:       initialExposure,
		IncludeInfrastructure: includeInfrastructure,
	})
	if err != nil {
		return err
	}
	return bridge.ServeStdio(cmd.Context())
}

// runCatalogMCP builds the gateway from the API catalog rather than from
// reflection: every started subsystem is described, registered, and its declared
// operations exposed, and the tools are generated from those descriptions.
func runCatalogMCP(
	cmd *cobra.Command,
	h *host.Host,
	catalog *sharedCatalog,
	initialExposure toolboxmcp.InitialExposure,
	minimal bool,
) error {
	includeInfrastructure, err := cmd.Flags().GetBool("include-infrastructure")
	if err != nil {
		return err
	}
	seed, err := catalog.registerSubsystems(cmd.Context(), includeInfrastructure)
	if err != nil {
		return err
	}
	for _, warning := range seed.Warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "toolbox: "+warning)
	}
	registered := 0
	for _, entry := range seed.Seeded {
		if entry.Exposed > 0 {
			registered++
			continue
		}
		if entry.Skipped != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "toolbox: %s was not registered: %s\n", entry.Subsystem, entry.Skipped)
		}
	}
	bridge, err := toolboxmcp.NewFromAPICatalog(cmd.Context(), catalog.service.Catalog(), catalog.service.Invoker(), toolboxmcp.APICatalogOptions{
		Options: toolboxmcp.Options{
			Name:                  "toolbox",
			Description:           "Model Context Protocol server for Toolbox subsystems, built from the API catalog.",
			InitialExposure:       initialExposure,
			IncludeInfrastructure: includeInfrastructure,
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(
		cmd.ErrOrStderr(),
		"toolbox: %d of %d subsystems registered from their own contracts, %d operations exposed\n",
		registered, len(seed.Seeded), seed.Exposed(),
	)
	return bridge.ServeStdio(cmd.Context())
}

func filterDescriptorsByService(descriptors []*subsystem.Descriptor, serviceNames []string) ([]*subsystem.Descriptor, error) {
	wanted := make(map[string]bool, len(serviceNames))
	for _, name := range serviceNames {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("service name cannot be empty")
		}
		wanted[name] = true
	}
	found := make(map[string]bool, len(wanted))
	result := make([]*subsystem.Descriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor == nil {
			continue
		}
		clone := *descriptor
		clone.ServiceNames = make([]string, 0, len(descriptor.ServiceNames))
		for _, name := range descriptor.ServiceNames {
			if wanted[name] {
				clone.ServiceNames = append(clone.ServiceNames, name)
				found[name] = true
			}
		}
		if len(clone.ServiceNames) > 0 {
			result = append(result, &clone)
		}
	}
	for name := range wanted {
		if !found[name] {
			return nil, fmt.Errorf("service %q is not provided by the selected subsystems", name)
		}
	}
	return result, nil
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

	h, _, err := buildHost()
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

func buildHost() (*host.Host, *sharedCatalog, error) {
	h := host.New()
	// The API catalog is given the host's own provider directory, so it finds the
	// parsers, adapters, and invokers this process starts without importing a
	// single provider subsystem.
	providers := h.ProviderDirectory()
	// The catalog is composed explicitly rather than through a factory, because the
	// automatic exposure path needs the very service the subsystem serves: a host
	// that registers its own subsystems writes into this store, and the MCP gateway
	// reads the same one. Two views of one catalog, not two catalogs.
	catalogStore := apitools.NewMemory(apitools.StoreOptions{})
	catalogService := apitools.NewService(catalogStore, providers)
	catalog := &sharedCatalog{service: catalogService}
	factories := map[string]subsystem.Factory{
		"agent": func() (*subsystem.Server, error) { return agent.New(agent.Options{}) },
		"apitools": func() (*subsystem.Server, error) {
			return apitools.NewServiceServer(catalogService, apitools.Options{Store: catalogStore, Directory: providers})
		},
		"apiopenapi":    func() (*subsystem.Server, error) { return apiopenapi.New(apiopenapi.Options{}) },
		"apigrpc":       func() (*subsystem.Server, error) { return apigrpc.New(apigrpc.Options{}) },
		"apimcp":        func() (*subsystem.Server, error) { return apimcp.New(apimcp.Options{}) },
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
			return nil, nil, err
		}
	}
	h.OnStarted(func(context.Context, *subsystem.Descriptor) error {
		// A subsystem's endpoint is only known once it has started, so the
		// resolver the providers are bound through is installed here.
		providers.SetResolver(core.NewStaticResolver(hostEndpoints(h)...))
		return nil
	})
	catalog.host = h
	return h, catalog, nil
}

// sharedCatalog is the API catalog a host serves and, in the automatic path, fills:
// the same service, whether it is reached in process or over ConnectRPC.
type sharedCatalog struct {
	host    *host.Host
	service *apitools.Service
}

// registerSubsystems describes every started subsystem and stores it in the
// catalog, exposing the operations each subsystem declared capabilities for.
//
// This is the whole automatic path: each subsystem's contract is read from the
// subsystem itself, the capabilities its manifest declared are attached to the
// services they belong to, the description is stored, and its exposable operations
// are exposed. Nothing is written per subsystem, and nothing is exposed that no
// declared capability covers.
func (c *sharedCatalog) registerSubsystems(ctx context.Context, includeInfrastructure bool) (host.SeedResult, error) {
	if c.host == nil {
		return host.SeedResult{}, fmt.Errorf("the host is not composed yet")
	}
	return c.host.RegisterInto(ctx, c.service.Registrar(), protocontract.NewDescriptor(protocontract.Descriptor{}), host.SeedOptions{
		IncludeInfrastructure: includeInfrastructure,
	})
}

// hostEndpoints describes the started subsystems as resolvable endpoints, so a
// provider client is bound the same way any other service client is.
func hostEndpoints(h *host.Host) []core.Endpoint {
	descriptors := h.Descriptors()
	endpoints := make([]core.Endpoint, 0, len(descriptors))
	for _, descriptor := range descriptors {
		endpoints = append(endpoints, core.Endpoint{
			Name:         descriptor.SubsystemName,
			URL:          descriptor.Endpoint,
			ServiceNames: append([]string(nil), descriptor.ServiceNames...),
			Capabilities: append([]string(nil), descriptor.Capabilities...),
		})
	}
	return endpoints
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
