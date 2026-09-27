// Package knowledgehindsight serves one knowledge base subsystem over the Hindsight backend.
//
// It is a provider host, and it mounts rather than implements: every behaviour that matters lives
// in the root `pkg/knowledge` and the adapter in `pkg/knowledge/hindsight`, and what this package
// does is convert the contract's messages to and from those, holding no logic of its own. The
// split is the framework's rule about providers, and it is the reason a second backend would be a
// second mount rather than a second subsystem.
//
// Three decisions are worth stating before the services, because each is one a reader would
// otherwise have to infer:
//
//   - **The corpus is never written through here.** A corpus file has an editor, hooks, a blame
//     view and a review process, and a tool that wrote it behind somebody's back would fight all
//     four. It would also be a second source of truth that the next reconcile deletes. So
//     `CorpusService` has no write for the corpus, and a `WriteContent` against authored content
//     is refused with a pointer to the file rather than quietly accepted.
//   - **A reconcile is a plan and a confirmation.** Reconciling a thousand files changes what every
//     assistant in a deployment believes, and a person should see that happening. The confirmation
//     carries the digest of the plan they read, so the proposal and the approval cannot be
//     reordered and cannot be applied to a directory that has changed since. It is a value the
//     agent relays and the user answers, so it works on a transport that cannot elicit at all.
//   - **The mount is optional and nothing depends on it.** `ExportWiki` is the feature; the mount
//     is one way to consume it. The FUSE code is behind a build tag, so a host without FUSE still
//     builds this subsystem, still serves every other method, and still exports the same bundle —
//     and `EnableMount` says so by name rather than failing obscurely.
package knowledgehindsight

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	knowledgeconnect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "knowledgehindsight"
	// Version is the reference implementation version.
	Version = "0.1.0"
	// Description is what this subsystem reports as.
	Description = "Serves one knowledge base over the Hindsight memory backend, with a reconciled markdown corpus behind it."
	// ProviderRole is the role this subsystem's provider record declares.
	ProviderRole = "knowledge"
)

// Options configures the provider.
type Options struct {
	// Name is the name the deployment registers this provider under. Empty is `knowledge`.
	Name string
	// ListenAddress is where the server listens. Empty uses the framework default.
	ListenAddress string
	// BackendEndpoint is the memory backend's base URL. Required.
	BackendEndpoint string
	// APIKey authenticates to the backend. Empty means an unauthenticated deployment.
	APIKey string
	// CorpusRoots are the directories of markdown reconciled into bases. Required: a
	// knowledge base with no corpus has the engine half and nothing a person can review.
	CorpusRoots []knowledge.CorpusRoot
	// StateDir is where the ownership record and mount bookkeeping live. It is the
	// deployment's own directory rather than a project's, because a base outlives the
	// project that first named it and a record that moved would be refused as belonging to
	// a different destination.
	StateDir string
	// Owner is the identity written into content's ownership marker. It defaults to the
	// provider's name and it is what authorises a later prune; two deployments sharing one
	// base must not share it.
	Owner string
	// RequestTimeout bounds one backend call.
	RequestTimeout time.Duration
	// BatchSize and Concurrency bound a reconcile's ingest. Zero uses the package default.
	BatchSize   int
	Concurrency int
	// Logger receives this subsystem's own events. Nil uses slog.Default.
	Logger *slog.Logger
}

// Provider is a running provider, holding the state a host would otherwise have to.
type Provider struct {
	opts   Options
	client *kh.Client
	log    *slog.Logger

	// bases resolves a caller-supplied name to a base identifier and holds the corpus
	// configuration each base was created with. It is in process rather than in the backend
	// because the corpus roots are a property of *this deployment* — directories on this
	// host — and the backend has no way to know them.
	bases *baseRegistry

	// mounts holds the running FUSE mounts. It is non-nil on every build so that the
	// service can answer `GetMountStatus` with an accurate "not compiled in" rather than a
	// nil dereference, and every mutating method refuses by name.
	mounts *mountTable
}

// NewService builds the provider without a server.
//
// It is separate from New so that a test, or a host that composes the provider itself, can reach
// the handlers without a listener.
func NewService(options Options) (*Provider, error) {
	if strings.TrimSpace(options.BackendEndpoint) == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "the knowledge backend endpoint is required"}
	}
	if len(options.CorpusRoots) == 0 {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "at least one corpus root is required; a knowledge base with no corpus has the engine half and nothing a person can review",
		}
	}
	if options.Name == "" {
		options.Name = "knowledge"
	}
	if options.Owner == "" {
		options.Owner = options.Name
	}
	if options.StateDir == "" {
		options.StateDir = DefaultStateDir()
	}
	if err := os.MkdirAll(options.StateDir, 0o755); err != nil {
		return nil, &api.Error{Kind: api.KindInternal, Message: "creating the provider state directory", Err: err}
	}
	log := options.Logger
	if log == nil {
		log = slog.Default()
	}

	client, err := kh.New(kh.Options{
		Endpoint:  options.BackendEndpoint,
		APIKey:    options.APIKey,
		Timeout:   options.RequestTimeout,
		UserAgent: fmt.Sprintf("toolbox/%s knowledgehindsight/%s", Name, Version),
	})
	if err != nil {
		return nil, err
	}

	p := &Provider{opts: options, client: client, log: log}
	p.bases = newBaseRegistry(options.CorpusRoots, options.StateDir, options.BatchSize, options.Concurrency)
	p.mounts = newMountTable(p)
	return p, nil
}

// New builds the provider and its server with every service registered.
//
// It deliberately does not fail when the backend is unreachable. A deployment that mounts this
// subsystem while the backend is down should still be able to list its own configuration and
// report why the backend is unusable, and a provider that refused to start would leave an
// operator with nothing to look at.
func New(options Options) (*subsystem.Server, error) {
	p, err := NewService(options)
	if err != nil {
		return nil, err
	}
	registered := strings.TrimSpace(options.Name)
	if registered == "" {
		registered = Name
	}
	// Each generated constructor is called once. Calling it twice to get the path and then
	// the handler would build two handlers, and the one actually served would not be the
	// one whose name was registered.
	contentPath, contentHandler := knowledgeconnect.NewContentServiceHandler(p.contentHandler())
	queryPath, queryHandler := knowledgeconnect.NewQueryServiceHandler(p.queryHandler())
	corpusPath, corpusHandler := knowledgeconnect.NewCorpusServiceHandler(p.corpusHandler())
	mountPath, mountHandler := knowledgeconnect.NewMountServiceHandler(p.mountHandler())
	basePath, baseHandler := knowledgeconnect.NewKnowledgeBaseServiceHandler(p.baseHandler())
	return subsystem.NewServer(subsystem.Config{
		Name:          registered,
		Version:       Version,
		Description:   AdvertisedDescription(),
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{
			{Name: knowledgeconnect.ContentServiceName, Path: contentPath, Handler: contentHandler},
			{Name: knowledgeconnect.QueryServiceName, Path: queryPath, Handler: queryHandler},
			{Name: knowledgeconnect.CorpusServiceName, Path: corpusPath, Handler: corpusHandler},
			{Name: knowledgeconnect.MountServiceName, Path: mountPath, Handler: mountHandler},
			{Name: knowledgeconnect.KnowledgeBaseServiceName, Path: basePath, Handler: baseHandler},
		},
		// A mount has to be able to outlive an RPC, so the provider supervises them in
		// the background for as long as the server is up. Returning blocks until the
		// context is cancelled, which is what the framework expects of Background.
		Background: p.mounts.serve,
		// Readiness is the backend answering, not the process being alive: a provider that
		// reports ready while its backend is unreachable sends a caller into a call that
		// cannot work.
		Health: p.healthy,
	})
}

// AdvertisedDescription is what a host advertises for this subsystem.
//
// It is a function so the text lives next to the provider rather than being copied into a host's
// factory, where a change to one would leave the other lying.
func AdvertisedDescription() string {
	return fmt.Sprintf("%s (Hindsight, expected API %s, FUSE %s)", Description, kh.ExpectedAPIVersion, fuseWord())
}

func fuseWord() string {
	if FuseBuilt() {
		return "available"
	}
	return "not compiled in"
}

// Client exposes the adapter, for a mount and for tests.
func (p *Provider) Client() *kh.Client { return p.client }

// ProviderName is the name this provider is registered under.
func (p *Provider) ProviderName() string { return p.opts.Name }

// StateDir is where this provider keeps its own records.
func (p *Provider) StateDir() string { return p.opts.StateDir }

// Owner is the identity written into content's ownership marker.
func (p *Provider) Owner() string { return p.opts.Owner }

// Options returns the provider's configuration, for a caller assembling a status response.
func (p *Provider) Options() Options { return p.opts }

// resolveBase turns a caller-supplied name into a base identifier.
//
// A caller addresses a base by a friendly name far more often than by an identifier, and a contract
// that made it look the alias up first would push that into every caller. The resolution is in
// process because the corpus configuration is a property of this deployment — a directory on this
// host — and the backend has no way to know it.
func (p *Provider) resolveBase(ctx context.Context, name string) (string, error) {
	return p.bases.resolve(ctx, p.client, name)
}

// resolveBaseForRead resolves a name for an operation that does not write, and is the right one for
// a plan, a listing and a projection: none of them needs the backend to already hold the base.
func (p *Provider) resolveBaseForRead(ctx context.Context, name string) (string, error) {
	return p.bases.resolveLocal(ctx, p.client, name)
}

// DefaultStateDir is where a provider keeps its records when nothing says otherwise.
func DefaultStateDir() string {
	if dir := os.Getenv("TOOLBOX_KNOWLEDGE_STATE_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "toolbox", "knowledge")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "toolbox", "knowledge")
	}
	return filepath.Join(os.TempDir(), "toolbox-knowledge")
}

// healthy reports whether the backend is answering.
//
// It distinguishes "the process is up" from "the deployment is usable", because a provider that
// reports ready while its backend is unreachable sends a caller into a call that cannot work.
func (p *Provider) healthy(ctx context.Context) error {
	_, _, _, err := p.client.CheckVersion(ctx)
	return err
}

// Providers describes this subsystem to a catalog's provider directory.
//
// It contributes **one** record, not one per base, because the bases a deployment has are runtime
// state discovered through the service and this record is what a host reads before anything is
// running.
func Providers(endpoint string) []api.Provider {
	return []api.Provider{{
		ID:                    Name,
		Subsystem:             Name,
		Role:                  ProviderRole,
		Endpoint:              endpoint,
		ServiceNames:          ServiceNames(),
		Status:                api.ServerStatusServing,
		ImplementationVersion: Version,
		Targets:               []string{"markdown-wiki", "fuse-readonly"},
	}}
}

// Descriptors describes this subsystem to a host.
func Descriptors(endpoint string) []*subsystem.Descriptor {
	return []*subsystem.Descriptor{{
		SubsystemName:         Name,
		Endpoint:              endpoint,
		ImplementationVersion: Version,
		APIVersion:            "v1",
		ServiceNames:          ServiceNames(),
	}}
}

// ServiceNames returns every fully-qualified service this provider serves.
//
// It is a function rather than a package-level slice so the list and the registration cannot
// drift: a service added to the contract and forgotten here would be served and never discovered.
func ServiceNames() []string {
	return []string{
		knowledgeconnect.ContentServiceName,
		knowledgeconnect.QueryServiceName,
		knowledgeconnect.CorpusServiceName,
		knowledgeconnect.MountServiceName,
		knowledgeconnect.KnowledgeBaseServiceName,
	}
}
