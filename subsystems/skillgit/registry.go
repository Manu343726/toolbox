package skillgit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/api"
	"go.yaml.in/yaml/v3"
)

// RegistrationsFile is where a deployment records the catalogs it has registered.
//
// It is state that outlives the process on purpose. A deployment that forgot its catalogs on
// reboot would serve a different set of skills each time it came up — and a project's `skills:`
// list would then name references that resolve to nothing, which is the one state a project's
// list must never be in.
const RegistrationsFile = "catalogs.yaml"

// The file holds no credential and this is the file's guarantee. It is readable, is often
// committed, and is frequently pasted into a bug report, so a remote is recorded in the form
// a person needs to read it and never in the form git was handed. What is recorded *about* a
// credential is that one was involved, because a catalog that reaches a private repository
// and one that reaches a public one are not the same thing to depend on. See `Auth` for what
// that does and does not achieve — the short version is that git keeps the remote it was
// cloned from in the checkout's own config, and this framework neither writes nor reads that.

// Registration is one catalog this deployment serves.
type Registration struct {
	// ID is the catalog identifier: the first segment of every reference and URI for a skill
	// in it, and the name a project writes in `skills:`.
	ID string `yaml:"id"`
	// Remote is where it came from, **without any credential in it**. Empty for a catalog
	// created locally and never given a remote.
	//
	// This file is readable, is often committed, and is frequently pasted into a bug report.
	// A remote recorded as named would write a token into all three, while the display path
	// redacting it would make the file look safe. The credential is held by whatever reached
	// the remote — the machine's agent or helper, or a contributor at start — and is recorded
	// only as the fact that it did.
	Remote string `yaml:"remote,omitempty"`
	// Auth states how this catalog authenticates, which is a fact about the machine rather
	// than about the repository and the only part of the answer worth keeping.
	//
	// It is recorded so that "this catalog reaches a private repository" and "this one
	// reaches a public one" are both readable from the file, and so a catalog whose
	// authentication went away is visible rather than failing later at a `git pull` in
	// git's own words.
	Auth Auth `yaml:"auth,omitempty"`
	// Directory is where the checkout is. A relative path resolves against the provider's
	// data directory; an absolute path is used as it is.
	//
	// Both forms exist because a contributing subsystem may fetch somewhere other than the
	// provider's storage — it fetches, and hands over the checkout it fetched — and a
	// contributor that had to fetch into the provider's own directory in order to be able to
	// express the path would be a contributor that could not choose where content lands.
	//
	// It is recorded rather than derived so that a person can see where the content they are
	// being served actually is, which is the whole of what this field is for.
	Directory string `yaml:"directory"`
	// CreatedAt is when it was registered, for a report rather than for a decision.
	CreatedAt string `yaml:"createdAt,omitempty"`
	// ReadOnly is why a catalog is read-only when it is not because it has no remote: a
	// deployment may serve a checkout it does not own and must not write to.
	ReadOnly bool `yaml:"readOnly,omitempty"`
}

// Registry is the set of catalogs a deployment has registered.
//
// It is the one piece of state this subsystem keeps, and it is kept because the alternative
// is a deployment whose skills change every time it restarts.
type Registry struct {
	path string

	mu            sync.RWMutex
	registrations map[string]Registration
}

// NewRegistry opens the registry in a data directory, reading what is already there, and
// adopts any registrations the deployment supplied.
//
// A deployment may start a provider with catalogs already decided, which is what a
// contributing subsystem needs: contributions are applied before anything is started, so a
// provider that could only be told its catalogs over its own service would have none when the
// first project asked for one. A supplied registration whose checkout is already present is
// adopted; one whose checkout is missing is cloned by the caller, which is the same work a
// person registering a catalog causes.
func NewRegistry(dataDir string, supplied ...Registration) (*Registry, error) {
	registry := &Registry{
		path:          filepath.Join(dataDir, RegistrationsFile),
		registrations: map[string]Registration{},
	}
	if err := registry.Load(); err != nil {
		return nil, err
	}
	for _, registration := range supplied {
		if err := registry.Add(registration); err != nil {
			return nil, err
		}
	}
	if len(supplied) > 0 {
		if err := registry.Save(); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// DataDir is the directory a deployment keeps this provider's catalogs in.
func (r *Registry) DataDir() string { return filepath.Dir(r.path) }

// Load reads the registrations from disk.
//
// A missing file is an empty set rather than an error: a deployment that has registered no
// catalogs yet is a deployment with none, and that is the normal state of a fresh install. A
// file that is there and unreadable *is* an error, because it means the deployment cannot tell
// which catalogs it promised to serve and would serve a different set from the one recorded.
func (r *Registry) Load() error {
	content, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return api.WrapError(api.KindInternal, err,
			"reading the registered skill catalogs at %s", r.path)
	}
	var document struct {
		Catalogs []Registration `yaml:"catalogs"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		return api.WrapError(api.KindInvalid, err,
			"the registered skill catalogs at %s are not readable, so this deployment cannot "+
				"tell which catalogs it promised to serve. Fix or remove the file; it is "+
				"deleted here so that a deployment with no catalogs is one that says so",
			r.path)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, registration := range document.Catalogs {
		r.registrations[registration.ID] = registration
	}
	return nil
}

// Save writes the registrations, through a temporary file and a rename.
//
// An interrupted write must not leave a deployment with a registry it cannot read: that would
// be worse than an empty one, because an empty one means "no catalogs" and an unreadable one
// means "cannot tell what you agreed to serve".
func (r *Registry) Save() error {
	r.mu.RLock()
	registrations := make([]Registration, 0, len(r.registrations))
	for _, registration := range r.registrations {
		registrations = append(registrations, registration)
	}
	r.mu.RUnlock()
	sort.Slice(registrations, func(i, j int) bool { return registrations[i].ID < registrations[j].ID })

	rendered, err := yaml.Marshal(struct {
		Catalogs []Registration `yaml:"catalogs"`
	}{Catalogs: registrations})
	if err != nil {
		return api.WrapError(api.KindInternal, err, "rendering the registered skill catalogs")
	}
	header := "# Skill catalogs this deployment serves, maintained by Toolbox.\n" +
		"#\n" +
		"# A catalog registered here outlives this process: a deployment that forgot its\n" +
		"# catalogs on reboot would serve a different set of skills each time it came up, and\n" +
		"# a project's skills: list would then name references that resolve to nothing.\n"
	temporary := r.path + ".tmp"
	if err := os.WriteFile(temporary, append([]byte(header), rendered...), 0o600); err != nil {
		return api.WrapError(api.KindInternal, err,
			"writing the registered skill catalogs to %s", temporary)
	}
	if err := os.Rename(temporary, r.path); err != nil {
		_ = os.Remove(temporary)
		return api.WrapError(api.KindInternal, err,
			"replacing the registered skill catalogs at %s", r.path)
	}
	return nil
}

// All returns every registration, ordered by identifier, so a report is reproducible.
func (r *Registry) All() []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	registrations := make([]Registration, 0, len(r.registrations))
	for _, registration := range r.registrations {
		registrations = append(registrations, registration)
	}
	sort.Slice(registrations, func(i, j int) bool { return registrations[i].ID < registrations[j].ID })
	return registrations
}

// Get returns one registration by identifier.
func (r *Registry) Get(id string) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	registration, found := r.registrations[strings.TrimSpace(id)]
	return registration, found
}

// Add records a registration, refusing a name that is taken.
//
// Two checkouts of one repository can therefore both be registered, which is occasionally what
// somebody wants, and a name already in use is an error rather than two catalogs silently
// sharing one directory and one identity.
func (r *Registry) Add(registration Registration) error {
	id := strings.TrimSpace(registration.ID)
	if id == "" {
		return api.Errorf(api.KindInvalid, "a catalog needs a name to be registered under")
	}
	if err := ValidCatalogID(id); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, taken := r.registrations[id]; taken {
		return api.Errorf(api.KindAlreadyExists,
			"a catalog is already registered as %q, from %s. A name means one catalog, so "+
				"register this one under another name — two checkouts of one repository are "+
				"both allowed, and sharing one name is not",
			id, describeLocation(existing))
	}
	r.registrations[id] = registration
	return nil
}

// Forget removes a registration, leaving whatever is on disk.
//
// Unregistering and deleting are two operations on purpose: unregistering forgets the remote
// and leaves the checkout, so the skills are still readable on disk and nothing is removed
// without being asked for twice. Deleting removes the checkout as well, and it is named for
// that.
func (r *Registry) Forget(id string) (Registration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	registration, found := r.registrations[strings.TrimSpace(id)]
	if found {
		delete(r.registrations, strings.TrimSpace(id))
	}
	return registration, found
}

// DirectoryFor is where a catalog's checkout lives, from its name.
//
// The name is used as a path segment, which is why it is validated before it gets here: an
// identifier carrying a separator would be a path, and a catalog's name is not one.
func (r *Registry) DirectoryFor(id string) string {
	return filepath.Join(r.DataDir(), strings.TrimSpace(id))
}

// Path resolves a registration's recorded directory to a path on this machine.
//
// A relative one is the common case and resolves against the provider's own data directory, so
// moving that directory moves the catalog with it. An absolute one is used as it is, because a
// contributing subsystem may keep its checkouts somewhere else and a contributor that had to
// place content in the provider's storage in order to be able to express the path would be a
// contributor that could not choose where content lands.
func (r *Registry) Path(registration Registration) string {
	return RegistryPath(r.DataDir(), registration)
}

// RegistryPath resolves a registration's directory against a data directory, and is the one
// place that resolution happens — so a construction-time check and every later read cannot
// disagree about where a checkout is.
func RegistryPath(dataDir string, registration Registration) string {
	directory := strings.TrimSpace(registration.Directory)
	if directory == "" {
		return ""
	}
	if filepath.IsAbs(directory) {
		return filepath.Clean(directory)
	}
	return filepath.Join(dataDir, directory)
}

// It is deliberately looser than a skill's name: a catalog is a deployment's own label, and a
// deployment may call its catalogs what it likes. What it may not do is carry a separator, a
// dot-dot, or whitespace, because it is one segment of a URI *and* a directory this provider
// creates — and a name that can escape its own directory is a name that can be made to write
// somewhere else.
// ValidCatalogID refuses an identifier that could not be one segment of a URI and one
// directory's name.
//
// It is exported because a contributing subsystem derives a catalog name before it composes a
// provider, and it needs the same rule the provider will apply — a second, looser copy would
// let a name through that the provider then refuses, which is a contributor's error reported
// as a provider's.
func ValidCatalogID(id string) error {
	if strings.TrimSpace(id) != id {
		return api.Errorf(api.KindInvalid,
			"a catalog's name %q has whitespace around it; write it without the padding", id)
	}
	if strings.ContainsAny(id, "/\\ \t\n\r") {
		return api.Errorf(api.KindInvalid,
			"a catalog's name %q contains a separator or whitespace. A catalog is one segment "+
				"of a skill's URI and one directory here, so it can be neither", id)
	}
	if id == "." || id == ".." || strings.HasPrefix(id, ".") {
		return api.Errorf(api.KindInvalid,
			"a catalog's name %q starts with a dot or is a relative path element, and a "+
				"catalog's name is a directory this provider creates", id)
	}
	return nil
}

func describeLocation(registration Registration) string {
	if registration.Remote == "" {
		return "a catalog created locally"
	}
	return describeRemote(registration.Remote)
}

// DefaultDataDir is where this provider keeps its checkouts when a deployment names no
// directory of its own.
//
// It is under the user's configuration directory rather than a project's, and that is the
// whole point: a catalog outlives the project that first named one of them, so a provider
// whose storage were a project's would lose every catalog the moment a project stopped using
// it — and a project's `skills:` list would then name references that resolve to nothing.
func DefaultDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		// Without a configuration directory there is no obvious home for a deployment's
		// storage, and the working directory is where a person can at least see it.
		return "skillcatalogs"
	}
	return filepath.Join(base, "toolbox", "skillcatalogs")
}
