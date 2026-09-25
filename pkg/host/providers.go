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
// catalog importing a single provider subsystem. Providers are derived from the
// capabilities the started subsystems already declare, so a subsystem that
// implements one of the framework's contracts participates by declaring the
// capability and nothing else.
//
// It satisfies the catalog's directory interface structurally; no import of the
// catalog is needed, which is what keeps the dependency pointing one way.
type ProviderDirectory struct {
	host    *Host
	mu      sync.RWMutex
	clients core.Resolver
	cache   map[string]any
}

// ProviderDirectory creates a directory over the providers this host started.
func (h *Host) ProviderDirectory() *ProviderDirectory {
	return &ProviderDirectory{host: h, cache: make(map[string]any)}
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

// Providers returns the extension points this host currently runs, derived from
// the capabilities each started subsystem declares. A subsystem that declares no
// extension-point capability is not a provider, whatever else it offers.
//
// The list is read at call time, so a subsystem started after the directory was
// created still shows up.
func (d *ProviderDirectory) Providers(context.Context) ([]api.Provider, error) {
	if d.host == nil {
		return nil, nil
	}
	return d.ProvidersOf(d.host.Endpoints()), nil
}

// ProviderEndpoint is one started subsystem that offers an extension point.
type ProviderEndpoint struct {
	// Subsystem is the started subsystem's name.
	Subsystem string
	// Endpoint is where it is reachable.
	Endpoint string
	// ImplementationVersion is the subsystem's version.
	ImplementationVersion string
	// Capabilities are the capabilities it declares.
	Capabilities []string
	// ServiceNames are the services it serves.
	ServiceNames []string
}

// ProvidersOf derives provider records from started subsystems, so a deployment
// that runs its subsystems elsewhere can reuse the same derivation.
func (d *ProviderDirectory) ProvidersOf(endpoints []ProviderEndpoint) []api.Provider {
	providers := make([]api.Provider, 0, len(endpoints))
	for _, endpoint := range endpoints {
		for _, role := range []string{api.ProviderParser, api.ProviderAdapter, api.ProviderInvoker} {
			provider := providerForRole(endpoint, role)
			if provider != nil {
				providers = append(providers, *provider)
			}
		}
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return providers
}

// providerForRole returns the provider record one role contributes, or nil when
// the subsystem declares no capability for that role.
func providerForRole(endpoint ProviderEndpoint, role string) *api.Provider {
	provider := &api.Provider{
		ID:                    endpoint.Subsystem + "-" + role,
		Subsystem:             endpoint.Subsystem,
		Role:                  role,
		Endpoint:              endpoint.Endpoint,
		ServiceNames:          append([]string(nil), endpoint.ServiceNames...),
		Status:                api.ServerStatusServing,
		ImplementationVersion: endpoint.ImplementationVersion,
	}
	prefix := ""
	switch role {
	case api.ProviderParser:
		prefix = api.CapabilityParse
		provider.ServiceNames = []string{apiv1connect.ApiParserServiceName}
	case api.ProviderAdapter:
		prefix = api.CapabilityRender
		provider.ServiceNames = []string{apiv1connect.ApiAdapterServiceName}
	case api.ProviderInvoker:
		prefix = api.CapabilityInvoke
		provider.ServiceNames = []string{apiv1connect.ApiInvokerServiceName}
	default:
		return nil
	}
	for _, capability := range endpoint.Capabilities {
		if identifier, ok := api.IdentifierFromCapability(capability, prefix); ok {
			switch role {
			case api.ProviderParser:
				provider.Formats = append(provider.Formats, identifier)
			case api.ProviderAdapter:
				provider.Targets = append(provider.Targets, identifier)
			case api.ProviderInvoker:
				provider.Transports = append(provider.Transports, identifier)
			}
			provider.Capabilities = append(provider.Capabilities, capability)
		}
	}
	if len(provider.Capabilities) == 0 {
		return nil
	}
	return provider
}

// Endpoints returns the started subsystems as provider endpoints.
func (h *Host) Endpoints() []ProviderEndpoint {
	descriptors := h.Descriptors()
	endpoints := make([]ProviderEndpoint, 0, len(descriptors))
	for _, descriptor := range descriptors {
		endpoints = append(endpoints, ProviderEndpoint{
			Subsystem:             descriptor.SubsystemName,
			Endpoint:              descriptor.Endpoint,
			ImplementationVersion: descriptor.ImplementationVersion,
			Capabilities:          append([]string(nil), descriptor.Capabilities...),
			ServiceNames:          append([]string(nil), descriptor.ServiceNames...),
		})
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
	// The provider is checked against the ones this host actually runs, rather
	// than against the cache: a cache only holds providers that have been bound
	// before, so consulting it first would refuse the first call of every
	// provider.
	if !d.known(ctx, id) {
		return zero, fmt.Errorf("no provider %q is configured in this host", id)
	}
	if resolver == nil {
		return zero, fmt.Errorf("provider %q cannot be resolved: this host has no resolver configured", id)
	}
	client, err := bindClient(ctx, core.NewClient(core.ClientOptions{Resolver: resolver}), serviceName, constructor)
	if err != nil {
		return zero, fmt.Errorf("resolve provider %q: %w", id, err)
	}
	d.mu.Lock()
	d.cache[id] = client
	d.mu.Unlock()
	return client, nil
}

// known reports whether a provider identifier is one this host currently runs.
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
