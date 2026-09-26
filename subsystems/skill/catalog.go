package skill

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/skills"
	"github.com/Manu343726/toolbox/pkg/skills/skillv1/skillv1connect"
)

// CatalogClient calls one catalog provider.
type CatalogClient = skillv1connect.SkillCatalogServiceClient

// CatalogDirectory finds the catalogs available in a deployment.
//
// The aggregator never imports a catalog subsystem. It asks the directory which catalogs
// exist, and the deployment's composition root supplies the directory: a host wires the
// subsystems it started, while a deployment that runs catalogs elsewhere wires a
// registry-backed one. That is what lets a deployment gain a new source of skills by adding a
// subsystem, with no change to this one.
//
// Resolution is by **identifier**, never by contract name, because several providers serve the
// same contract on purpose. A deployment that integrated two git-backed catalogs has two
// catalogs serving `SkillCatalogService`, and nothing but their identifiers tells them apart.
type CatalogDirectory interface {
	// Catalogs returns the available catalogs, ordered by identifier.
	Catalogs(context.Context) ([]api.Provider, error)
	// Catalog returns a client for one catalog provider.
	Catalog(context.Context, string) (CatalogClient, error)
}

// StaticDirectory is a directory over catalogs whose clients are already known. It is used by
// tests, by a deployment configured from a file, and by a host that starts its own catalogs.
type StaticDirectory struct {
	mu        sync.RWMutex
	providers []api.Provider
	clients   map[string]CatalogClient
}

// NewStaticDirectory creates a directory from providers and their clients.
//
// A provider with no client is refused rather than accepted and called later: a catalog the
// directory cannot reach is a deployment that would report an empty skill set, and an empty
// skill set looks exactly like a project that offers nothing.
func NewStaticDirectory(providers []api.Provider, clients map[string]CatalogClient) (*StaticDirectory, error) {
	directory := &StaticDirectory{clients: make(map[string]CatalogClient, len(clients))}
	for _, provider := range providers {
		id := strings.TrimSpace(provider.ID)
		if id == "" {
			return nil, fmt.Errorf("a %s provider has no identifier", skills.ProviderRole)
		}
		if provider.Role != skills.ProviderRole {
			return nil, fmt.Errorf("provider %q is a %s, not a %s",
				id, provider.Role, skills.ProviderRole)
		}
		if _, reachable := clients[id]; !reachable {
			return nil, fmt.Errorf("catalog provider %q has no client, so a deployment "+
				"listing it would report an empty skill set rather than a missing one", id)
		}
		directory.providers = append(directory.providers, provider.Clone())
	}
	for id, client := range clients {
		directory.clients[id] = client
	}
	sort.Slice(directory.providers, func(i, j int) bool { return directory.providers[i].ID < directory.providers[j].ID })
	return directory, nil
}

// Catalogs implements CatalogDirectory.
func (d *StaticDirectory) Catalogs(context.Context) ([]api.Provider, error) {
	result := make([]api.Provider, 0, len(d.providers))
	for _, provider := range d.providers {
		result = append(result, provider.Clone())
	}
	return result, nil
}

// Catalog implements CatalogDirectory.
func (d *StaticDirectory) Catalog(_ context.Context, id string) (CatalogClient, error) {
	provider, err := d.provider(id)
	if err != nil {
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	client, reachable := d.clients[provider.ID]
	if !reachable {
		return nil, fmt.Errorf("catalog %q has no client", provider.ID)
	}
	return client, nil
}

func (d *StaticDirectory) provider(id string) (api.Provider, error) {
	trimmed := strings.TrimSpace(id)
	for _, provider := range d.providers {
		if provider.ID == trimmed {
			return provider, nil
		}
	}
	return api.Provider{}, api.Errorf(api.KindNotFound,
		"no catalog is integrated as %q; this deployment has: %s", trimmed, catalogNames(d.providers))
}

// ResolverDirectory is a directory that binds a catalog client when one is first asked for.
//
// It resolves the provider's endpoint through the deployment's core and then constructs the
// typed client, so a catalog the deployment cannot reach is a failure at the point of use
// rather than a nil client that panics later. Resolution happens before the client is
// constructed: there is nothing to call if there is nowhere to call it.
type ResolverDirectory struct {
	client *core.Client

	mu        sync.RWMutex
	providers []api.Provider
	clients   map[string]CatalogClient
}

// NewResolverDirectory creates a directory over catalogs reached through a core.
func NewResolverDirectory(client *core.Client, providers []api.Provider) (*ResolverDirectory, error) {
	if client == nil {
		return nil, fmt.Errorf("a catalog directory needs a core client to resolve endpoints")
	}
	directory := &ResolverDirectory{
		client:    client,
		providers: make([]api.Provider, 0, len(providers)),
		clients:   make(map[string]CatalogClient),
	}
	for _, provider := range providers {
		id := strings.TrimSpace(provider.ID)
		if id == "" {
			return nil, fmt.Errorf("a %s provider has no identifier", skills.ProviderRole)
		}
		if provider.Role != skills.ProviderRole {
			return nil, fmt.Errorf("provider %q is a %s, not a %s",
				id, provider.Role, skills.ProviderRole)
		}
		directory.providers = append(directory.providers, provider.Clone())
	}
	sort.Slice(directory.providers, func(i, j int) bool { return directory.providers[i].ID < directory.providers[j].ID })
	return directory, nil
}

// Catalogs implements CatalogDirectory.
func (d *ResolverDirectory) Catalogs(context.Context) ([]api.Provider, error) {
	result := make([]api.Provider, 0, len(d.providers))
	for _, provider := range d.providers {
		result = append(result, provider.Clone())
	}
	return result, nil
}

// Catalog implements CatalogDirectory.
func (d *ResolverDirectory) Catalog(ctx context.Context, id string) (CatalogClient, error) {
	provider, err := d.provider(id)
	if err != nil {
		return nil, err
	}
	d.mu.RLock()
	cached, bound := d.clients[provider.ID]
	d.mu.RUnlock()
	if bound {
		return cached, nil
	}
	// Resolve first, then construct: the generated client is built against an endpoint that
	// is known to exist, which is what makes a missing catalog a message rather than a
	// connection refused from somewhere deeper.
	client, err := core.Bind(ctx, d.client, skills.CatalogService, skillv1connect.NewSkillCatalogServiceClient)
	if err != nil {
		return nil, api.WrapError(api.KindUnavailable, err,
			"resolving catalog provider %q", provider.ID)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, raced := d.clients[provider.ID]; raced {
		return existing, nil
	}
	d.clients[provider.ID] = client
	return client, nil
}

func (d *ResolverDirectory) provider(id string) (api.Provider, error) {
	trimmed := strings.TrimSpace(id)
	for _, provider := range d.providers {
		if provider.ID == trimmed {
			return provider, nil
		}
	}
	return api.Provider{}, api.Errorf(api.KindNotFound,
		"no catalog is integrated as %q; this deployment has: %s", trimmed, catalogNames(d.providers))
}

func catalogNames(providers []api.Provider) string {
	if len(providers) == 0 {
		return "none"
	}
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, provider.ID)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// callCatalog runs one catalog operation and reports a failure as this subsystem's own.
//
// The error is wrapped rather than returned as the catalog said it, because a caller of this
// subsystem is not necessarily calling the catalog: a skill's file could not be read because
// the catalog was unreachable, and saying "the catalog was unreachable" is the fact.
func callCatalog[T any](
	ctx context.Context, client CatalogClient, what string,
	call func(context.Context) (*connect.Response[T], error),
) (*T, error) {
	// T is the response *message* type and the unwrap below is what turns a transport
	// response into the value a caller reads. Returning a pointer keeps the generated
	// client's own shape, so a caller cannot accidentally copy a message it did not mean to.
	response, err := call(ctx)
	if err != nil {
		return nil, &api.Error{
			Kind:    kindOfCatalogError(err),
			Message: fmt.Sprintf("a catalog failed to %s", what),
			Err:     err,
		}
	}
	return response.Msg, nil
}

// kindOfCatalogError reads a catalog's own error as a kind, so a failure crosses this subsystem
// as what went wrong rather than as a ConnectRPC code.
//
// A catalog that answered with a plain error is unclassified, and unclassified is internal: a
// failure nobody classified is a bug rather than a request the caller should change.
func kindOfCatalogError(err error) api.ErrorKind {
	if kind := api.KindOf(err); kind != "" {
		return kind
	}
	if connect.CodeOf(err) != connect.CodeUnknown {
		// The catalog spoke ConnectRPC, so its code is a fact about the failure even though
		// this package does not speak it directly.
		switch connect.CodeOf(err) {
		case connect.CodeNotFound:
			return api.KindNotFound
		case connect.CodeInvalidArgument, connect.CodeFailedPrecondition:
			return api.KindInvalid
		case connect.CodePermissionDenied:
			return api.KindDenied
		case connect.CodeAlreadyExists:
			return api.KindAlreadyExists
		case connect.CodeUnimplemented:
			return api.KindUnsupported
		case connect.CodeUnavailable:
			return api.KindUnavailable
		}
	}
	return api.KindInternal
}

// connectFailure turns a classified failure into the ConnectRPC error a handler returns.
//
// A kind rather than a code is what travels between here and a transport: a caller that speaks
// neither transport can still read what went wrong, and a transport that speaks both maps the
// same kind the same way in both.
func connectFailure(err error) error {
	if err == nil {
		return nil
	}
	return connect.NewError(connectCode(err), err)
}

// connectCode maps this package's error kinds onto ConnectRPC codes.
func connectCode(err error) connect.Code {
	switch api.KindOf(err) {
	case api.KindInvalid:
		return connect.CodeInvalidArgument
	case api.KindNotFound:
		return connect.CodeNotFound
	case api.KindDenied:
		return connect.CodePermissionDenied
	case api.KindAlreadyExists:
		return connect.CodeAlreadyExists
	case api.KindFailedPrecondition:
		return connect.CodeFailedPrecondition
	case api.KindUnsupported:
		return connect.CodeUnimplemented
	case api.KindUnavailable:
		return connect.CodeUnavailable
	default:
		return connect.CodeInternal
	}
}
