package host

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/core"
)

// ProviderDirectory finds the API extension points available inside one host
// process.
//
// The host is the composition layer, so it is the right place to answer "which
// parsers, adapters, and invokers did this deployment start?" without the API
// catalog importing a single provider subsystem.
//
// A provider declares itself. Each provider subsystem exports the records
// describing what it implements, and the composition hands them to the directory,
// so a subsystem that serves one of the framework's contracts participates by
// saying what it is rather than by being recognized from a capability string. The
// directory used to work the other way round, deriving a provider by scanning a
// flattened capability list for a prefix — which meant the claim that a subsystem
// implemented a contract was silently also the grant to call it, and a capability
// nobody had thought about as authorization was doing double duty.
//
// It satisfies the catalog's directory interface structurally; no import of the
// catalog is needed, which is what keeps the dependency pointing one way.
type ProviderDirectory struct {
	host      *Host
	mu        sync.RWMutex
	clients   core.Resolver
	cache     map[string]any
	providers map[string]api.Provider
}

// ProviderDirectory creates a directory over the providers this host started.
func (h *Host) ProviderDirectory() *ProviderDirectory {
	return &ProviderDirectory{host: h, cache: make(map[string]any), providers: make(map[string]api.Provider)}
}

// Register records the providers a deployment started.
//
// A provider's own subsystem states what it implements, and the composition hands
// that statement over: the knowledge lives with the implementation and the wiring
// lives with the composition, which is the same split every other dependency in
// this host follows.
//
// Registering the same identifier twice replaces the record, because a subsystem
// restarted on a new port is still the same provider and its endpoint has moved.
func (d *ProviderDirectory) Register(providers ...api.Provider) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, provider := range providers {
		identifier := strings.TrimSpace(provider.ID)
		if identifier == "" {
			return fmt.Errorf("a provider needs an identifier")
		}
		provider.ID = identifier
		if provider.Status == "" {
			provider.Status = api.ServerStatusServing
		}
		d.providers[identifier] = provider
	}
	// A newly registered provider has no bound client yet.
	d.cache = make(map[string]any)
	return nil
}

// SetResolver supplies the resolver the directory binds provider clients
// through. The host calls it once the registry is available, because a provider
// may be placed by the registry rather than by the host itself.
func (d *ProviderDirectory) SetResolver(resolver core.Resolver) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clients = resolver
	d.cache = make(map[string]any)
}

// Providers returns the extension points this deployment registered.
func (d *ProviderDirectory) Providers(context.Context) ([]api.Provider, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	providers := make([]api.Provider, 0, len(d.providers))
	for _, provider := range d.providers {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return providers, nil
}

// ProviderEndpoint is one started subsystem that offers an extension point.
type ProviderEndpoint struct {
	// Subsystem is the started subsystem's name.
	Subsystem string
	// Endpoint is where it is reachable.
	Endpoint string
	// ImplementationVersion is the subsystem's version.
	ImplementationVersion string
	// ServiceNames are the services it serves.
	ServiceNames []string
}

// Endpoints returns the started subsystems as provider endpoints.
func (h *Host) Endpoints() []ProviderEndpoint {
	descriptors := h.Descriptors()
	endpoints := make([]ProviderEndpoint, 0, len(descriptors))
	for _, descriptor := range descriptors {
		endpoint := ProviderEndpoint{
			Subsystem:             descriptor.SubsystemName,
			Endpoint:              descriptor.Endpoint,
			ImplementationVersion: descriptor.ImplementationVersion,
			ServiceNames:          append([]string(nil), descriptor.ServiceNames...),
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints
}

// Parser binds a generated client for one parser provider.
func (d *ProviderDirectory) Parser(ctx context.Context, id string) (apiv1connect.ApiParserServiceClient, error) {
	return d.bind(ctx, id, apiv1connect.ApiParserServiceName, apiv1connect.NewApiParserServiceClient)
}

// Adapter binds a generated client for one adapter provider.
func (d *ProviderDirectory) Adapter(ctx context.Context, id string) (apiv1connect.ApiAdapterServiceClient, error) {
	return d.bind(ctx, id, apiv1connect.ApiAdapterServiceName, apiv1connect.NewApiAdapterServiceClient)
}

// Invoker binds a generated client for one invoker provider.
func (d *ProviderDirectory) Invoker(ctx context.Context, id string) (apiv1connect.ApiInvokerServiceClient, error) {
	return d.bind(ctx, id, apiv1connect.ApiInvokerServiceName, apiv1connect.NewApiInvokerServiceClient)
}

func bindClient[T any](
	ctx context.Context,
	client *core.Client,
	serviceName string,
	constructor core.Constructor[T],
) (T, error) {
	return core.Bind(ctx, client, serviceName, constructor)
}

func (d *ProviderDirectory) bind[T any](
	ctx context.Context,
	id, serviceName string,
	constructor core.Constructor[T],
) (T, error) {
	var zero T
	d.mu.RLock()
	resolver := d.clients
	cached := d.cache[id]
	d.mu.RUnlock()
	if client, matches := cached.(T); matches {
		return client, nil
	}
	// The provider is checked against the ones this deployment registered, rather
	// than against the cache: a cache only holds providers that have been bound
	// before, so consulting it first would refuse the first call of every
	// provider.
	if !d.known(ctx, id) {
		return zero, fmt.Errorf("no provider %q is configured in this host", id)
	}
	// A provider is identified by its own identifier, and its record says where it
	// is. Resolving by service name instead would be ambiguous the moment two
	// providers serve the same contract — which is exactly what contributing a
	// format or a transport means — and the wrong provider would answer.
	endpoint := d.endpointOf(ctx, id)
	if endpoint == "" {
		if resolver == nil {
			return zero, fmt.Errorf("provider %q cannot be resolved: it declares no endpoint", id)
		}
		resolved, resolveErr := resolver.Resolve(ctx, serviceName)
		if resolveErr != nil || resolved.URL == "" {
			return zero, fmt.Errorf("resolve provider %q: no endpoint serves %s", id, serviceName)
		}
		endpoint = resolved.URL
	}
	client, err := bindClient(
		ctx,
		core.NewClient(core.ClientOptions{Resolver: core.NewStaticResolver(core.Endpoint{
			Name:         id,
			URL:          endpoint,
			ServiceNames: []string{serviceName},
		})}),
		serviceName,
		constructor,
	)
	if err != nil {
		return zero, fmt.Errorf("bind provider %q at %s: %w", id, endpoint, err)
	}
	d.mu.Lock()
	d.cache[id] = client
	d.mu.Unlock()
	return client, nil
}

// endpointOf returns where one of this deployment's providers is reachable.
func (d *ProviderDirectory) endpointOf(ctx context.Context, id string) string {
	providers, err := d.Providers(ctx)
	if err != nil {
		return ""
	}
	for _, provider := range providers {
		if provider.ID == id {
			return provider.Endpoint
		}
	}
	return ""
}

// known reports whether a provider identifier is one this deployment registered.
func (d *ProviderDirectory) known(ctx context.Context, id string) bool {
	providers, err := d.Providers(ctx)
	if err != nil {
		return false
	}
	for _, provider := range providers {
		if provider.ID == id {
			return true
		}
	}
	return false
}

// Describe renders the provider set for a diagnostic message.
func (d *ProviderDirectory) Describe() string {
	providers, _ := d.Providers(context.Background())
	parts := make([]string, 0, len(providers))
	for _, provider := range providers {
		parts = append(parts, fmt.Sprintf("%s(%s)", provider.ID, provider.Role))
	}
	return strings.Join(parts, ", ")
}
