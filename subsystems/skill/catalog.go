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
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	"github.com/Manu343726/toolbox/pkg/skills/skillv1/skillv1connect"
)

// CatalogClient calls one catalog provider.
type CatalogClient = skillv1connect.SkillCatalogServiceClient

// Catalog is one catalog a deployment can serve, and the provider that holds it.
//
// The two are separate because a provider may hold several catalogs and a catalog belongs to
// exactly one provider: a git-backed host holds a checkout per registered remote, and each is a
// catalog with its own name, its own content and its own pin.
type Catalog struct {
	// Info is what the catalog says about itself.
	Info *skillv1.CatalogInfo
	// ProviderID is the identifier of the provider holding it.
	ProviderID string
}

// ID is the catalog's identifier: the first segment of every reference and URI for a skill in
// it.
func (c Catalog) ID() string { return c.Info.GetId() }

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
//
// **A provider is not a catalog.** One provider may hold many catalogs — a git-backed host
// holds a checkout per registered remote — so a directory is built by asking each catalog
// provider which catalogs it holds rather than by reading a provider's own name as a
// catalog's. That is load-bearing rather than cosmetic: a directory that treated a provider's
// identifier as a catalog's name would resolve exactly one of a host's catalogs and leave the
// rest unreachable, which a project's `skills:` list would then name with no way to be served.
type CatalogDirectory interface {
	// Catalogs returns the available catalogs, ordered by identifier.
	Catalogs(context.Context) ([]Catalog, error)
	// Catalog returns a client for one catalog.
	Catalog(context.Context, string) (CatalogClient, error)
}

// ProviderLister reports the provider subsystems a deployment has running.
//
// It is an interface rather than a slice so the aggregator reads the deployment's *current*
// providers rather than a snapshot taken when it was built. A provider subsystem restarting on a
// new port is still the same provider, and a catalog registered after this process started is a
// catalog a project may name — so a list captured once would be quietly out of date.
type ProviderLister interface {
	// Providers returns the available providers.
	Providers(context.Context) ([]api.Provider, error)
}

// StaticDirectory is a directory over catalogs whose clients are already known. It is used by
// tests, by a deployment configured from a file, and by a host that starts its own catalogs.
type StaticDirectory struct {
	mu       sync.RWMutex
	catalogs []Catalog
	clients  map[string]CatalogClient
}

// NewStaticDirectory creates a directory from catalogs and their clients.
//
// A client is keyed by the *provider* rather than by the catalog, because that is what it is: a
// client reaches a provider, and which catalog a call is for is in the request. Keying it by
// catalog would need one client per catalog for a provider that serves many, which is the same
// connection opened as many times.
func NewStaticDirectory(
	catalogs []Catalog, clients map[string]CatalogClient,
) (*StaticDirectory, error) {
	directory := &StaticDirectory{clients: make(map[string]CatalogClient, len(clients))}
	for _, catalog := range catalogs {
		id := strings.TrimSpace(catalog.ID())
		if id == "" {
			return nil, api.Errorf(api.KindInvalid,
				"a catalog reached the directory with no identifier, and it is the first "+
					"segment of every reference and URI for a skill in it")
		}
		if catalog.ProviderID == "" {
			return nil, api.Errorf(api.KindInvalid,
				"the catalog %q names no provider, so nothing can be asked of it", id)
		}
		if _, reachable := clients[catalog.ProviderID]; !reachable {
			return nil, api.Errorf(api.KindFailedPrecondition,
				"the catalog %q is served by %q, which has no client, so a deployment listing "+
					"it would report an empty skill set rather than a missing one", id, catalog.ProviderID)
		}
		directory.catalogs = append(directory.catalogs, catalog)
	}
	for id, client := range clients {
		directory.clients[id] = client
	}
	directory.sort()
	return directory, nil
}

func (d *StaticDirectory) sort() {
	sort.Slice(d.catalogs, func(i, j int) bool { return d.catalogs[i].ID() < d.catalogs[j].ID() })
}

// Catalogs implements CatalogDirectory.
func (d *StaticDirectory) Catalogs(context.Context) ([]Catalog, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	listed := make([]Catalog, 0, len(d.catalogs))
	listed = append(listed, d.catalogs...)
	return listed, nil
}

// Catalog implements CatalogDirectory.
func (d *StaticDirectory) Catalog(_ context.Context, id string) (CatalogClient, error) {
	trimmed := strings.TrimSpace(id)
	for _, catalog := range d.catalogs {
		if catalog.ID() != trimmed {
			continue
		}
		d.mu.RLock()
		defer d.mu.RUnlock()
		client, reachable := d.clients[catalog.ProviderID]
		if !reachable {
			return nil, api.Errorf(api.KindFailedPrecondition,
				"the catalog %q is served by %q, which has no client", trimmed, catalog.ProviderID)
		}
		return client, nil
	}
	return nil, unknownCatalog(trimmed, d.ids())
}

func (d *StaticDirectory) ids() []string {
	ids := make([]string, 0, len(d.catalogs))
	for _, catalog := range d.catalogs {
		ids = append(ids, catalog.ID())
	}
	return ids
}

// ResolverDirectory is a directory that discovers catalogs by asking the deployment's
// providers, and binds a client when one is first needed.
type ResolverDirectory struct {
	client   *core.Client
	provider ProviderLister

	mu      sync.RWMutex
	clients map[string]CatalogClient
	// discovered caches the catalogs this directory found, so a page of skills does not ask
	// every provider what it holds. It is dropped when a client is bound, because a provider
	// that just started serving may hold catalogs the directory has not seen.
	discovered []Catalog
}

// NewResolverDirectory creates a directory over catalogs reached through a core and listed by
// the deployment.
//
// It resolves nothing here: the provider list is read per request, which is what lets the
// directory be built while the host is still starting and still be correct afterwards.
func NewResolverDirectory(lister ProviderLister, client *core.Client) (*ResolverDirectory, error) {
	if client == nil {
		return nil, fmt.Errorf("a catalog directory needs a core client to resolve endpoints")
	}
	if lister == nil {
		return nil, fmt.Errorf("a catalog directory needs a provider list to resolve identifiers " +
			"against; a directory with nothing in it would report every reference as unknown")
	}
	return &ResolverDirectory{client: client, provider: lister, clients: map[string]CatalogClient{}}, nil
}

// Catalogs implements CatalogDirectory.
//
// Every catalog provider is asked what it holds, and the answers are merged by catalog
// identifier. A provider that cannot be asked — one that is registered but not answering — is
// skipped rather than failing the whole listing, because a deployment with one unreachable
// provider and three working ones has catalogs, and reporting none of them would be worse than
// reporting three.
func (d *ResolverDirectory) Catalogs(ctx context.Context) ([]Catalog, error) {
	d.mu.RLock()
	cached := d.discovered
	d.mu.RUnlock()
	if len(cached) > 0 {
		return append([]Catalog(nil), cached...), nil
	}

	providers, err := d.catalogProviders(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]Catalog{}
	for _, provider := range providers {
		client, err := d.clientFor(ctx, provider.ID)
		if err != nil {
			continue
		}
		response, err := callCatalog(ctx, client, "list the catalogs it holds",
			func(ctx context.Context) (*connect.Response[skillv1.ListCatalogsResponse], error) {
				return client.ListCatalogs(ctx, connect.NewRequest(&skillv1.ListCatalogsRequest{}))
			})
		if err != nil {
			continue
		}
		for _, info := range response.GetCatalogs() {
			id := strings.TrimSpace(info.GetId())
			if id == "" {
				continue
			}
			if _, already := byID[id]; already {
				// Two providers claiming one catalog identifier is a deployment that cannot
				// say which content a reference resolves to, and resolving it either way would
				// be a guess. The first wins in this process and the deployment is reported by
				// the name it is asked for.
				continue
			}
			byID[id] = Catalog{Info: info, ProviderID: provider.ID}
		}
	}
	listed := make([]Catalog, 0, len(byID))
	for _, catalog := range byID {
		listed = append(listed, catalog)
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].ID() < listed[j].ID() })
	d.mu.Lock()
	d.discovered = listed
	d.mu.Unlock()
	return append([]Catalog(nil), listed...), nil
}

// Catalog implements CatalogDirectory.
func (d *ResolverDirectory) Catalog(ctx context.Context, id string) (CatalogClient, error) {
	trimmed := strings.TrimSpace(id)
	catalogs, err := d.Catalogs(ctx)
	if err != nil {
		return nil, err
	}
	for _, catalog := range catalogs {
		if catalog.ID() == trimmed {
			return d.clientFor(ctx, catalog.ProviderID)
		}
	}
	ids := make([]string, 0, len(catalogs))
	for _, catalog := range catalogs {
		ids = append(ids, catalog.ID())
	}
	return nil, unknownCatalog(trimmed, ids)
}

// catalogProviders returns the deployment's providers that serve catalogs, sorted so a message
// that lists them is reproducible.
func (d *ResolverDirectory) catalogProviders(ctx context.Context) ([]api.Provider, error) {
	providers, err := d.provider.Providers(ctx)
	if err != nil {
		return nil, err
	}
	catalogs := make([]api.Provider, 0, len(providers))
	for _, provider := range providers {
		if provider.Role != skills.ProviderRole {
			// A provider of some other role is not a catalog, and reading it as one would
			// resolve a reference to a contract that serves something else entirely.
			continue
		}
		catalogs = append(catalogs, provider.Clone())
	}
	sort.Slice(catalogs, func(i, j int) bool { return catalogs[i].ID < catalogs[j].ID })
	return catalogs, nil
}

// clientFor binds a client to one provider, once.
//
// The endpoint is resolved before the client is constructed, so a provider the deployment cannot
// reach is a message rather than a nil client that fails somewhere deeper.
func (d *ResolverDirectory) clientFor(ctx context.Context, providerID string) (CatalogClient, error) {
	d.mu.RLock()
	cached, bound := d.clients[providerID]
	d.mu.RUnlock()
	if bound {
		return cached, nil
	}
	client, err := core.Bind(ctx, d.client, skills.CatalogService, skillv1connect.NewSkillCatalogServiceClient)
	if err != nil {
		return nil, api.WrapError(api.KindUnavailable, err, "resolving catalog provider %q", providerID)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, raced := d.clients[providerID]; raced {
		return existing, nil
	}
	d.clients[providerID] = client
	// A newly bound provider may hold catalogs this directory has not seen, so what was
	// discovered is dropped rather than kept for the life of the process.
	d.discovered = nil
	return client, nil
}

// unknownCatalog reports a reference naming no catalog this deployment serves, with the ones
// it does.
//
// A person who has to fix a project's `skills:` list needs to know what they could name
// instead, so a message that only says "no such catalog" sends them looking for the answer
// somewhere the framework already had.
func unknownCatalog(id string, available []string) error {
	if len(available) == 0 {
		return api.Errorf(api.KindNotFound,
			"this deployment serves no skill catalogs beyond the project's own, so %q is not "+
				"one. A deployment with no catalog providers is a working deployment — it "+
				"serves the project's own skills", id)
	}
	sort.Strings(available)
	return api.Errorf(api.KindNotFound,
		"no skill catalog is named %q. This deployment serves: %s", id, strings.Join(available, ", "))
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
	// response into the value a caller reads. Returning a pointer keeps the generated client's
	// own shape, so a caller cannot accidentally copy a message it did not mean to.
	response, err := call(ctx)
	if err != nil {
		return nil, &api.Error{
			Kind:    api.ConnectKind(err),
			Message: fmt.Sprintf("a catalog failed to %s", what),
			Err:     err,
		}
	}
	return response.Msg, nil
}

// connectFailure turns a classified failure into the ConnectRPC error a handler returns.
func connectFailure(err error) error { return api.ConnectError(err) }
