package apitools

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

// ParserClient calls one parser provider.
type ParserClient = apiv1connect.ApiParserServiceClient

// AdapterClient calls one adapter provider. An adapter renders a description
// into a target representation.
type AdapterClient = apiv1connect.ApiAdapterServiceClient

// InvokerClient calls one invoker provider. An invoker executes an operation
// against a live server.
type InvokerClient = apiv1connect.ApiInvokerServiceClient

// ProviderDirectory finds the extension points available in a deployment.
//
// The catalog never imports a parser or adapter subsystem. It asks the
// directory which providers exist, and the deployment's composition root
// supplies the directory: a host wires the subsystems it started, while a
// deployment that runs providers elsewhere wires a registry-backed directory.
// That is what lets a user add support for a new API format or transport by
// adding a subsystem, with no change to this one.
type ProviderDirectory interface {
	// Providers returns the available providers, ordered by identifier.
	Providers(context.Context) ([]api.Provider, error)
	// Parser returns a client for one parser provider.
	Parser(context.Context, string) (ParserClient, error)
	// Adapter returns a client for one adapter provider.
	Adapter(context.Context, string) (AdapterClient, error)
	// Invoker returns a client for one invoker provider.
	Invoker(context.Context, string) (InvokerClient, error)
}

// StaticDirectory is a directory over providers whose endpoints are already
// known. It is used by tests, by a deployment configured from a file, and by a
// host that starts its own providers.
type StaticDirectory struct {
	mu        sync.RWMutex
	providers []api.Provider
	parsers   map[string]ParserClient
	adapters  map[string]AdapterClient
	invokers  map[string]InvokerClient
}

// NewStaticDirectory creates a directory from providers and their clients.
func NewStaticDirectory(
	providers []api.Provider,
	parsers map[string]ParserClient,
	adapters map[string]AdapterClient,
	invokers ...map[string]InvokerClient,
) (*StaticDirectory, error) {
	directory := &StaticDirectory{
		parsers:  make(map[string]ParserClient, len(parsers)),
		adapters: make(map[string]AdapterClient, len(adapters)),
		invokers: make(map[string]InvokerClient),
	}
	for id, client := range parsers {
		if client == nil {
			return nil, fmt.Errorf("parser client for %q is nil", id)
		}
		directory.parsers[id] = client
	}
	for id, client := range adapters {
		if client == nil {
			return nil, fmt.Errorf("adapter client for %q is nil", id)
		}
		directory.adapters[id] = client
	}
	for _, set := range invokers {
		for id, client := range set {
			if client == nil {
				return nil, fmt.Errorf("invoker client for %q is nil", id)
			}
			directory.invokers[id] = client
		}
	}
	declared := make(map[string]bool)
	for _, provider := range providers {
		id := strings.TrimSpace(provider.ID)
		if id == "" {
			return nil, fmt.Errorf("provider id is required")
		}
		if declared[id] {
			return nil, fmt.Errorf("provider %q is declared more than once", id)
		}
		declared[id] = true
		if provider.Role == api.ProviderParser {
			if _, ok := directory.parsers[id]; !ok {
				return nil, fmt.Errorf("parser provider %q has no client", id)
			}
		}
		if provider.Role == api.ProviderAdapter {
			if _, ok := directory.adapters[id]; !ok {
				return nil, fmt.Errorf("adapter provider %q has no client", id)
			}
		}
		if provider.Role == api.ProviderInvoker {
			if _, ok := directory.invokers[id]; !ok {
				return nil, fmt.Errorf("invoker provider %q has no client", id)
			}
		}
		directory.providers = append(directory.providers, provider.Clone())
	}
	sort.Slice(directory.providers, func(i, j int) bool { return directory.providers[i].ID < directory.providers[j].ID })
	return directory, nil
}

// Providers implements ProviderDirectory.
func (d *StaticDirectory) Providers(context.Context) ([]api.Provider, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]api.Provider, 0, len(d.providers))
	for _, provider := range d.providers {
		result = append(result, provider.Clone())
	}
	return result, nil
}

// Parser implements ProviderDirectory.
func (d *StaticDirectory) Parser(_ context.Context, id string) (ParserClient, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	client, ok := d.parsers[strings.TrimSpace(id)]
	if !ok {
		return nil, fmt.Errorf("%w: parser provider %q", ErrNoProvider, id)
	}
	return client, nil
}

// Adapter implements ProviderDirectory.
func (d *StaticDirectory) Adapter(_ context.Context, id string) (AdapterClient, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	client, ok := d.adapters[strings.TrimSpace(id)]
	if !ok {
		return nil, fmt.Errorf("%w: adapter provider %q", ErrNoProvider, id)
	}
	return client, nil
}

// Invoker implements ProviderDirectory.
func (d *StaticDirectory) Invoker(_ context.Context, id string) (InvokerClient, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	client, ok := d.invokers[strings.TrimSpace(id)]
	if !ok {
		return nil, fmt.Errorf("%w: invoker provider %q", ErrNoProvider, id)
	}
	return client, nil
}

// ResolverDirectory resolves provider clients through a core.Resolver and
// constructs them with the generated framework clients. The provider list comes
// from the deployment — a host's own subsystems, or a registry lookup — and the
// resolver places them, so a provider can run in another process without this
// subsystem importing it.
//
// This is the typed-client path: resolve first with core.Bind, then call the
// generated client. A provider that cannot be resolved fails before any call,
// and reflection is never used to substitute for the generated contract.
type ResolverDirectory struct {
	providers []api.Provider
	client    *core.Client

	mu       sync.Mutex
	parsers  map[string]ParserClient
	adapters map[string]AdapterClient
	invokers map[string]InvokerClient
}

// NewResolverDirectory creates a directory that resolves providers through the
// supplied resolver.
func NewResolverDirectory(providers []api.Provider, resolver core.Resolver) (*ResolverDirectory, error) {
	if resolver == nil {
		return nil, fmt.Errorf("provider resolver is required")
	}
	directory := &ResolverDirectory{
		providers: make([]api.Provider, 0, len(providers)),
		client:    core.NewClient(core.ClientOptions{Resolver: resolver}),
		parsers:   make(map[string]ParserClient),
		adapters:  make(map[string]AdapterClient),
		invokers:  make(map[string]InvokerClient),
	}
	for _, provider := range providers {
		id := strings.TrimSpace(provider.ID)
		if id == "" {
			return nil, fmt.Errorf("provider id is required")
		}
		if provider.Endpoint == "" {
			return nil, fmt.Errorf("provider %q has no endpoint", id)
		}
		directory.providers = append(directory.providers, provider.Clone())
	}
	sort.Slice(directory.providers, func(i, j int) bool { return directory.providers[i].ID < directory.providers[j].ID })
	return directory, nil
}

// Providers implements ProviderDirectory.
func (d *ResolverDirectory) Providers(context.Context) ([]api.Provider, error) {
	result := make([]api.Provider, 0, len(d.providers))
	for _, provider := range d.providers {
		result = append(result, provider.Clone())
	}
	return result, nil
}

// Parser implements ProviderDirectory.
func (d *ResolverDirectory) Parser(ctx context.Context, id string) (ParserClient, error) {
	provider, err := d.provider(id, api.ProviderParser)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if client, ok := d.parsers[provider.ID]; ok {
		return client, nil
	}
	client, err := core.Bind(ctx, d.client, apiv1connect.ApiParserServiceName, apiv1connect.NewApiParserServiceClient)
	if err != nil {
		return nil, fmt.Errorf("resolve parser provider %q: %w", provider.ID, err)
	}
	d.parsers[provider.ID] = client
	return client, nil
}

// Adapter implements ProviderDirectory.
func (d *ResolverDirectory) Adapter(ctx context.Context, id string) (AdapterClient, error) {
	provider, err := d.provider(id, api.ProviderAdapter)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if client, ok := d.adapters[provider.ID]; ok {
		return client, nil
	}
	client, err := core.Bind(ctx, d.client, apiv1connect.ApiAdapterServiceName, apiv1connect.NewApiAdapterServiceClient)
	if err != nil {
		return nil, fmt.Errorf("resolve adapter provider %q: %w", provider.ID, err)
	}
	d.adapters[provider.ID] = client
	return client, nil
}

// Invoker implements ProviderDirectory.
func (d *ResolverDirectory) Invoker(ctx context.Context, id string) (InvokerClient, error) {
	provider, err := d.provider(id, api.ProviderInvoker)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if client, ok := d.invokers[provider.ID]; ok {
		return client, nil
	}
	client, err := core.Bind(ctx, d.client, apiv1connect.ApiInvokerServiceName, apiv1connect.NewApiInvokerServiceClient)
	if err != nil {
		return nil, fmt.Errorf("resolve invoker provider %q: %w", provider.ID, err)
	}
	d.invokers[provider.ID] = client
	return client, nil
}

func (d *ResolverDirectory) provider(id, role string) (api.Provider, error) {
	id = strings.TrimSpace(id)
	for _, provider := range d.providers {
		if provider.ID == id {
			if provider.Role != role {
				return api.Provider{}, fmt.Errorf(
					"%w: provider %q is a %s, not a %s", ErrNoProvider, id, provider.Role, role,
				)
			}
			return provider, nil
		}
	}
	return api.Provider{}, fmt.Errorf("%w: %s provider %q", ErrNoProvider, role, id)
}
