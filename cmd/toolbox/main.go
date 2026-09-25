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
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/config"
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
	// Persistent, because every subcommand resolves the same four things: which
	// subsystems, which policy, which core, and which workspace. A local flag on the
	// root is invisible to a subcommand, so `toolbox mcp --all` would read a flag it
	// never had.
	flags := root.PersistentFlags()
	flags.StringSlice("component", nil, "Subsystem names to launch; repeatable or comma-separated")
	flags.Bool("all", false, "Launch all built-in subsystems")
	flags.String("policy", "", "Path to a policy document deciding which operations may be exposed; the default grants every read and nothing that changes state")
	flags.String("core", "", "Address of the core this process belongs to, as host:port; a flag beats the environment, which beats the configuration file")
	flags.Int("port", 0, "Port of the core on its own, leaving the host from the rest of the chain")
	flags.String("scope", "", "Workspace this client works in; a single core serves many projects, each with its own configuration, policy, and knowledge")

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
	mcpFlags.Bool("include-reflection", false, "Include the protocol's reflection services, which describe contracts rather than provide capabilities")
	// Kept so an existing command line keeps working. Nothing is excluded by name
	// any more, so the flag it replaces only ever meant the reflection services.
	mcpFlags.Bool("include-infrastructure", false, "Deprecated: use --include-reflection")
	_ = mcpFlags.MarkDeprecated("include-infrastructure", "use --include-reflection; services are no longer excluded by name")
	_ = mcpFlags.MarkHidden("include-infrastructure")
	mcpFlags.String("mcp-source", "reflection", "Where the tool surface comes from: reflection reads served contracts directly, catalog registers every subsystem in the API catalog first")
	mcpFlags.String("policy", "", "Path to a policy document deciding which operations may be exposed; the default grants every read and nothing that changes state")
	mcpFlags.StringSlice("service", nil, "Only expose these fully-qualified services; repeatable or comma-separated")
	root.AddCommand(mcpCommand)

	// The operation commands, generated from the contracts this binary links. Built
	// eagerly and for the whole built-in set, because a help screen listing only some of a
	// deployment's operations would be a help screen that lies. A --component selection
	// narrows what a command runs, not what the tree offers.
	//
	// A failure here is a build of the binary being wrong rather than a condition a caller
	// can act on, so it stops the process with the reason instead of producing a command
	// tree with a hole in it.
	if err := addOperationCommands(root); err != nil {
		panic(fmt.Sprintf("generate operation commands: %v", err))
	}

	daemonCommand := &cobra.Command{
		Use:   "daemon",
		Short: "Run the core as a long-lived process other clients join",
		Long: "Run the core as a long-lived process at a fixed address. Subsystems that did not " +
			"start inside it register with the core's registry, and an agent connects to the " +
			"Model Context Protocol endpoint over the network instead of launching a subprocess.",
		Args: cobra.NoArgs,
		RunE: runDaemon,
	}
	root.AddCommand(daemonCommand)
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
	resolved, err := resolveConfig(cmd)
	if err != nil {
		return err
	}
	reportConfig(cmd, resolved)
	if all && len(components) > 0 {
		return fmt.Errorf("--all cannot be combined with --component")
	}
	minimal, err := cmd.Flags().GetBool("minimal")
	if err != nil {
		return err
	}
	includeReflection, err := cmd.Flags().GetBool("include-reflection")
	if err != nil {
		return err
	}
	// The replaced flag is read for compatibility and means the same thing now.
	if legacy, err := cmd.Flags().GetBool("include-infrastructure"); err == nil && legacy {
		includeReflection = true
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

	h, catalog, err := buildHost(resolved, "")
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
	if err := registerStartedSubsystems(cmd.Context(), h, false); err != nil {
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
		Name:              "toolbox",
		Description:       "Aggregated Model Context Protocol server for Toolbox subsystems.",
		Policy:            catalog.policy,
		InitialExposure:   initialExposure,
		IncludeReflection: includeReflection,
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
	includeReflection, err := cmd.Flags().GetBool("include-reflection")
	if err != nil {
		return err
	}
	// The replaced flag is read for compatibility and means the same thing now.
	if legacy, err := cmd.Flags().GetBool("include-infrastructure"); err == nil && legacy {
		includeReflection = true
	}
	seed, err := catalog.registerSubsystems(cmd.Context(), includeReflection)
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
			Name:              "toolbox",
			Description:       "Model Context Protocol server for Toolbox subsystems, built from the API catalog.",
			Policy:            catalog.policy,
			InitialExposure:   initialExposure,
			IncludeReflection: includeReflection,
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
	resolved, err := resolveConfig(cmd)
	if err != nil {
		return err
	}
	reportConfig(cmd, resolved)
	if all && len(components) > 0 {
		return fmt.Errorf("--all cannot be combined with --component")
	}
	if !all && len(components) == 0 {
		all = true
	}

	h, _, err := buildHost(resolved, "")
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
		if err := registerStartedSubsystems(cmd.Context(), h, false); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return h.Shutdown(context.Background())
}

// buildHost composes the host and the catalog it fills.
//
// The configuration is the deployment's answer to two questions at once: where the
// core is, and what an agent may call. Both are read here rather than by each
// consumer, because the catalog, the seeder, and the gateway all have to be
// answering from one policy and one address.
func buildHost(resolved config.Config, registryAddress string, mounts ...subsystem.Mount) (*host.Host, *sharedCatalog, error) {
	h := host.New()
	// The API catalog is given the host's own provider directory, so it finds the
	// parsers, adapters, and invokers this process starts without importing a
	// single provider subsystem.
	providers := h.ProviderDirectory()
	// The catalog is composed explicitly rather than through a factory, because the
	// automatic exposure path needs the very service the subsystem serves: a host
	// that registers its own subsystems writes into this store, and the MCP gateway
	// reads the same one. Two views of one catalog, not two catalogs.
	// A provider declares itself: each provider subsystem exports the records
	// describing what it implements, and the composition hands them over. The
	// directory used to derive them by scanning capability strings, which meant the
	// claim that a subsystem implemented a contract was silently also the grant to
	// call it.
	if err := providers.Register(providerRecords(h)...); err != nil {
		return nil, nil, err
	}

	// One policy, read once, held by all three consumers: the catalog that
	// authorizes exposure, the seeder that asks for it, and the gateway that
	// reports it. Three readers of one document is the whole point; three policies
	// that could disagree is the failure.
	// Named for what it is rather than for its type: the package imported as
	// "policy" is the reference capability service, and a local of the same name
	// would shadow it inside the factory map below.
	surface, from, err := loadPolicy(resolved.PolicyPath)
	if err != nil {
		return nil, nil, err
	}
	if from != "" {
		fmt.Fprintf(os.Stderr, "toolbox: policy read from %s\n", from)
	}
	catalogStore := apitools.NewMemory(apitools.StoreOptions{Policy: surface})
	catalogService := apitools.NewService(catalogStore, providers)
	catalog := &sharedCatalog{service: catalogService, policy: surface, config: resolved}
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
		// The registry is the core's own endpoint, so a daemon gives it the configured
		// address. An in-process core passes none and keeps the ephemeral loopback
		// port: nothing outside the process needs to find it.
		"registry": func() (*subsystem.Server, error) {
			options := registry.Options{}
			if registryAddress != "" {
				options.ListenAddress = registryAddress
			}
			if len(mounts) > 0 {
				options.Mounts = mounts
			}
			return registry.New(options)
		},
		"skill":    func() (*subsystem.Server, error) { return skill.New(skill.Options{}) },
		"tool":     func() (*subsystem.Server, error) { return tool.New(tool.Options{}) },
		"workflow": func() (*subsystem.Server, error) { return workflow.New(workflow.Options{}) },
	}
	for name, factory := range factories {
		if err := h.Register(name, factory); err != nil {
			return nil, nil, err
		}
	}
	h.OnStarted(func(context.Context, *subsystem.Descriptor) error {
		// A subsystem's endpoint is only known once it has started, so the resolver
		// the providers are bound through is installed here, and the provider
		// records are registered with the addresses they actually reached.
		if err := providers.Register(providerRecords(h)...); err != nil {
			return err
		}
		providers.SetResolver(core.NewStaticResolver(hostEndpoints(h)...))
		return nil
	})
	catalog.host = h
	return h, catalog, nil
}

// providerRecords collects what every started provider subsystem says it
// implements, addressed where it actually reached.
//
// The records come from the provider subsystems themselves — apigrpc.Providers and
// its two siblings — so a format or a transport is claimed by whoever implements
// it, and adding one is a change to that subsystem rather than to anything that
// has to recognize it.
func providerRecords(h *host.Host) []api.Provider {
	endpoints := make(map[string]string, len(h.Servers()))
	for name, server := range h.Servers() {
		endpoints[name] = server.Endpoint()
	}
	records := make([]api.Provider, 0, 9)
	for _, provider := range apigrpc.Providers(endpoints["apigrpc"]) {
		records = append(records, provider)
	}
	for _, provider := range apiopenapi.Providers(endpoints["apiopenapi"]) {
		records = append(records, provider)
	}
	for _, provider := range apimcp.Providers(endpoints["apimcp"]) {
		records = append(records, provider)
	}
	return records
}

// sharedCatalog is the API catalog a host serves and, in the automatic path, fills:
// the same service, whether it is reached in process or over ConnectRPC.
type sharedCatalog struct {
	host    *host.Host
	service *apitools.Service
	policy  api.Policy
	// config is the resolved core address, policy path, and scope. Every call this
	// process makes carries the scope, so one core can serve many projects.
	config config.Config
}

// registerSubsystems describes every started subsystem and stores it in the
// catalog, exposing the operations each subsystem declared capabilities for.
//
// This is the whole automatic path: each subsystem's contract is read from the
// subsystem itself, the capabilities its manifest declared are attached to the
// services they belong to, the description is stored, and its exposable operations
// are exposed. Nothing is written per subsystem, and nothing is exposed that no
// declared capability covers.
func (c *sharedCatalog) registerSubsystems(ctx context.Context, includeReflection bool) (host.SeedResult, error) {
	if c.host == nil {
		return host.SeedResult{}, fmt.Errorf("the host is not composed yet")
	}
	return c.host.RegisterInto(ctx, c.service.Registrar(), protocontract.NewDescriptor(protocontract.Descriptor{}), host.SeedOptions{
		IncludeReflection: includeReflection,
		// The same document the catalog authorizes with and the gateway reports
		// with. Three readers of one policy is the point; three policies that could
		// disagree is the failure.
		Policy: c.policy,
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
		})
	}
	return endpoints
}

// selfLease is how long a core's registration of its own subsystems stays valid.
//
// It is a lease rather than nothing on purpose: a lease is what makes a crashed peer's
// registration disappear, so a core that is genuinely running has to keep renewing. A
// registration that outlived its process would be a directory entry pointing at a port
// nobody is listening on, and every client would resolve to it and fail on the call.
const selfLease = 30 * time.Second

// registerStartedSubsystems registers every started subsystem with the core's own registry.
//
// With keepRenewing set, the registrations are renewed until the context is cancelled. A
// core that registered once and stopped renewing would remove itself from its own
// directory after the lease, which is how a long-lived core ends up unable to resolve the
// subsystems it is serving — the failure looks like a directory bug and is not one.
func registerStartedSubsystems(ctx context.Context, h *host.Host, keepRenewing bool) error {
	registryServer, ok := h.Servers()["registry"]
	if !ok {
		return nil
	}
	client := registryv1connect.NewRegistryServiceClient(http.DefaultClient, registryServer.Endpoint())
	names := make([]string, 0, len(h.Servers()))
	for _, descriptor := range h.Descriptors() {
		registration := &registryv1.ServiceDescriptor{
			SubsystemName:         descriptor.SubsystemName,
			Endpoint:              descriptor.Endpoint,
			ImplementationVersion: descriptor.ImplementationVersion,
			ApiVersion:            descriptor.APIVersion,
			ServiceNames:          append([]string(nil), descriptor.ServiceNames...),
			Dependencies:          append([]string(nil), descriptor.Dependencies...),
			Description:           descriptor.Description,
		}
		if _, err := client.Register(ctx, connect.NewRequest(&registryv1.RegisterRequest{Descriptor_: registration, LeaseSeconds: int64(selfLease.Seconds())})); err != nil {
			return fmt.Errorf("register subsystem %q: %w", descriptor.SubsystemName, err)
		}
		names = append(names, descriptor.SubsystemName)
	}
	if keepRenewing {
		go renewRegistrations(ctx, client, names)
	}
	return nil
}

// renewRegistrations extends the lease on each registration until the context ends.
//
// A third of the lease, so two consecutive renewals can fail before the registration
// lapses: a core that renews at exactly the lease interval would lapse on a single slow
// round trip, and one that renews on a long interval would lapse on a brief network
// partition even though it is running. A lapsed registration is re-registered by the
// registry's own view only if something registers again, so the loop registers rather than
// heartbeats — a heartbeat for a lapsed lease is not found.
func renewRegistrations(ctx context.Context, client registryv1connect.RegistryServiceClient, names []string) {
	ticker := time.NewTicker(selfLease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, name := range names {
				_, err := client.Heartbeat(ctx, connect.NewRequest(&registryv1.HeartbeatRequest{
					SubsystemName: name,
					LeaseSeconds:  int64(selfLease.Seconds()),
				}))
				if err != nil {
					// Reported on stderr rather than returned: the core is still serving, and a
					// renewal that failed is a fact an operator needs, not a reason to stop
					// the process the operator started.
					fmt.Fprintf(os.Stderr, "toolbox: renew the registration for %q: %v\n", name, err)
				}
			}
		}
	}
}
