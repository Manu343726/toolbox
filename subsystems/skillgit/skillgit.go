// Package skillgit serves agent skills from git repositories.
//
// It is a **provider host**, not one provider: a deployment registers as many catalogs as it
// likes, one per remote, and each is served under its own identifier. That is the same shape as
// the registry's — one subsystem, many registrations, each resolved by identifier — and for the
// same reason. A deployment that wants a team's coding standards *and* a public directory is
// two registrations of one subsystem, and a subsystem that hard-coded one remote would make
// the second one a second subsystem.
//
// It implements two contracts, and the split is deliberate:
//
//   - `SkillCatalogService`, once per registered catalog, for reading skills. This is the
//     framework's catalog contract, and which catalog a call is for is in the request's own
//     `catalog` field rather than in the endpoint — so a deployment runs one provider and a
//     deployment with a dozen still runs one.
//   - `SkillGitService`, for deciding which sources exist at all: register, create, sync, push,
//     unregister, delete. A catalog contract is about skills; these operations are about where
//     skills come from, and mixing them would make every catalog provider responsible for
//     deciding what catalogs there are.
//
// A catalog is read-only unless the deployment owns it. The framework clones, reads and syncs;
// it commits and pushes only when a catalog was created here or a caller asked to push. Every
// modification is its own commit, because a history showing one commit per thing is a history
// somebody can read, and the commit identity is configured rather than assumed — the deployment
// is a machine and the person whose repository it is should get the last word on whose name is
// on the commit.
//
// Nothing about a conflict is resolved. A sync that cannot complete is reported with the
// repository's path, because deciding what a conflict meant is a decision about somebody else's
// repository and the only person who can make it is the person who owns it.
package skillgit

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	catalogconnect "github.com/Manu343726/toolbox/pkg/skills/skillv1/skillv1connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	skillgitv1 "github.com/Manu343726/toolbox/subsystems/skillgit/skillgitv1"
	managementconnect "github.com/Manu343726/toolbox/subsystems/skillgit/skillgitv1/skillgitv1connect"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	// Name is the stable subsystem name.
	Name = "skillgit"
	// Version is the reference implementation version.
	Version = "0.1.0"
	// Description is what a catalog reports this subsystem as.
	Description = "Serves agent skills from git repositories, one catalog per registered remote."
)

// Options configures the provider.
type Options struct {
	// DataDir is where this provider keeps its checkouts and its registration record. It is
	// the provider's own storage, outside any project: a deployment's catalogs outlive the
	// project that first named one of them.
	DataDir string
	// Git is the git operations. Nil means the `git` on this machine's PATH, and its absence
	// is an error at construction rather than at first use.
	Git *Git
	// GitBinary names the git executable, for a deployment that has it somewhere else.
	GitBinary string
	// Identity is whose name goes on a commit.
	Identity Identity
	// Now supplies the time a registration is stamped with, so a test does not depend on the
	// clock.
	Now func() time.Time
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Service is the provider: the catalog contract for every registered catalog, and the
// management service for the set of them.
type Service struct {
	registry *Registry
	git      *Git
	identity Identity
	now      func() time.Time
	version  string

	// directories caches one directory-backed catalog per registration, so a skill is read
	// through the same type whether it came from a project or from a checkout.
	mu          sync.RWMutex
	directories map[string]*skills.Directory
}

// NewService builds the provider.
//
// The registry is read at construction, so a deployment learns at start which catalogs it
// promised to serve rather than at the first request for one.
func NewService(options Options) (*Service, error) {
	dataDir := strings.TrimSpace(options.DataDir)
	if dataDir == "" {
		return nil, api.Errorf(api.KindInvalid,
			"the %s provider needs a data directory to keep its checkouts in; it is the "+
				"provider's own storage, outside any project", Name)
	}
	git := options.Git
	if git == nil {
		built, err := NewGit(options.GitBinary)
		if err != nil {
			return nil, err
		}
		git = built
	}
	registry, err := NewRegistry(dataDir)
	if err != nil {
		return nil, err
	}
	identity := options.Identity
	if !identity.Valid() {
		identity = DefaultIdentity()
	}
	clock := options.Now
	if clock == nil {
		clock = time.Now
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	return &Service{
		registry:    registry,
		git:         git,
		identity:    identity,
		now:         clock,
		version:     version,
		directories: map[string]*skills.Directory{},
	}, nil
}

// New builds the provider's server, serving both contracts on one endpoint.
func New(options Options) (*subsystem.Server, error) {
	service, err := NewService(options)
	if err != nil {
		return nil, err
	}
	catalogPath, catalogHandler := catalogconnect.NewSkillCatalogServiceHandler(service)
	managementPath, managementHandler := managementconnect.NewSkillGitServiceHandler(service)
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       service.version,
		Description:   Description,
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{
			{Name: catalogconnect.SkillCatalogServiceName, Path: catalogPath, Handler: catalogHandler},
			{Name: managementconnect.SkillGitServiceName, Path: managementPath, Handler: managementHandler},
		},
	})
}

// Providers describes this subsystem to a catalog's provider directory.
//
// It contributes **one** provider record, not one per registered catalog, because a
// deployment's registrations are runtime state and this record is what a host reads before
// anything is running. The catalogs themselves are found through the service: a deployment
// asks which are registered, and each is served under its own identifier. Recording one per
// registration would mean the record changed as catalogs came and went, which is a
// composition-time list describing something that only exists at runtime.
func Providers(endpoint string) []api.Provider {
	return []api.Provider{{
		ID:                    Name,
		Subsystem:             Name,
		Role:                  skills.ProviderRole,
		Endpoint:              endpoint,
		ServiceNames:          []string{catalogconnect.SkillCatalogServiceName},
		Status:                api.ServerStatusServing,
		ImplementationVersion: Version,
	}}
}

// Descriptors describes this subsystem to a host.
func Descriptors(endpoint string) []*subsystem.Descriptor {
	return []*subsystem.Descriptor{{
		SubsystemName:         Name,
		Endpoint:              endpoint,
		ImplementationVersion: Version,
		APIVersion:            "v1",
		ServiceNames: []string{
			catalogconnect.SkillCatalogServiceName,
			managementconnect.SkillGitServiceName,
		},
	}}
}

// directory returns the read-only directory-backed catalog for one registration.
//
// A registration that is read-only is wrapped in a directory catalog that is read-only twice
// over: the type never writes, and the *registration* may be a checkout this deployment does not
// own. The distinction is recorded rather than inferred so that a person can be told which kind
// of catalog they are about to depend on.
func (s *Service) directory(registration Registration) (*skills.Directory, error) {
	s.mu.RLock()
	cached, found := s.directories[registration.ID]
	s.mu.RUnlock()
	if found {
		return cached, nil
	}
	built, err := skills.NewDirectory(skills.DirectoryOptions{
		ID:             registration.ID,
		Root:           filepath.Join(s.registry.DataDir(), registration.Directory),
		Subdirectories: skills.ConventionalSkillDirectories(),
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, raced := s.directories[registration.ID]; raced {
		return existing, nil
	}
	s.directories[registration.ID] = built
	return built, nil
}

// registration resolves a catalog identifier to what the deployment registered under it.
//
// A catalog this provider does not serve is reported as such, with the ones it does, because
// a reference naming a catalog that is not there is a project's list that would resolve to
// nothing and the person who has to fix it needs to know what they could name instead.
func (s *Service) registration(id string) (Registration, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		trimmed = skills.LocalCatalog
	}
	registration, found := s.registry.Get(trimmed)
	if found {
		return registration, nil
	}
	names := make([]string, 0, 4)
	for _, other := range s.registry.All() {
		names = append(names, other.ID)
	}
	if len(names) == 0 {
		return Registration{}, api.Errorf(api.KindNotFound,
			"this deployment has no git-backed skill catalogs registered, so %q is not one. "+
				"Register one, or name a catalog the deployment has", trimmed)
	}
	return Registration{}, api.Errorf(api.KindNotFound,
		"no skill catalog is registered as %q. This deployment has: %s", trimmed, strings.Join(names, ", "))
}

// entry reads one skill from one catalog, in the contract's shape.
func (s *Service) entry(registration Registration, name string) (*skillv1.SkillEntry, error) {
	catalog, err := s.directory(registration)
	if err != nil {
		return nil, err
	}
	template, err := catalog.Cached(name)
	if err != nil {
		return nil, err
	}
	skill, err := template.Skill()
	if err != nil {
		return nil, err
	}
	if !skill.IsEnabled() {
		return nil, api.Errorf(api.KindNotFound,
			"the skill %s.%s is switched off in its own frontmatter, with %senabled: false",
			registration.ID, name, skills.ToolboxPrefix)
	}
	frontmatter, err := structpb.NewStruct(template.Frontmatter.Document)
	if err != nil {
		return nil, api.WrapError(api.KindInternal, err,
			"converting the frontmatter of %s.%s", registration.ID, name)
	}
	resources := make([]*skillv1.SkillFile, 0, len(template.Files))
	for _, file := range template.Files {
		resources = append(resources, &skillv1.SkillFile{
			Path:     file.Path,
			Size:     file.Size,
			Digest:   file.Digest,
			MimeType: file.MIMEType,
		})
	}
	reference := skills.Reference{Catalog: registration.ID, Name: name}
	return &skillv1.SkillEntry{
		Ref: &skillv1.SkillRef{
			Catalog: registration.ID, Name: name, Description: skill.Description,
		},
		Uri:         reference.URI(),
		Frontmatter: frontmatter,
		Body:        template.Body,
		Resources:   resources,
		MaxFiles:    skills.MaxFiles,
		MaxBytes:    skills.MaxBytes,
	}, nil
}

// status renders one registration for a person deciding whether to depend on it.
//
// The head and the skill count are read from the checkout rather than from the registration,
// because a catalog's content is whatever its checkout holds and a status that described the
// registration alone would be describing the intention rather than the thing.
func (s *Service) status(ctx context.Context, registration Registration) (*skillgitv1.CatalogStatus, error) {
	path := filepath.Join(s.registry.DataDir(), registration.Directory)
	status := &skillgitv1.CatalogStatus{
		Id:        registration.ID,
		Name:      registration.ID,
		Remote:    describeRemote(registration.Remote),
		Location:  path,
		ReadOnly:  registration.ReadOnly,
		CreatedAt: registration.CreatedAt,
	}
	if _, err := os.Stat(path); err != nil {
		// A registration whose checkout is gone is reported as it is rather than refused: the
		// record is the deployment's, and a person removing a directory by hand has not done
		// anything the framework should refuse to describe.
		return status, nil
	}
	if head, err := s.git.Head(ctx, path); err == nil {
		status.Head = head
	}
	if clean, err := s.git.Clean(ctx, path); err == nil {
		status.Dirty = !clean
	}
	if hasRemote, err := s.git.HasRemote(ctx, path); err == nil {
		status.Syncable = hasRemote
	}
	if catalog, err := s.directory(registration); err == nil {
		if names, err := catalog.Names(); err == nil {
			status.Skills = int32(len(names))
		}
	}
	return status, nil
}

// sortedStatuses renders every registration, ordered by identifier.
func (s *Service) sortedStatuses(ctx context.Context) ([]*skillgitv1.CatalogStatus, error) {
	registrations := s.registry.All()
	statuses := make([]*skillgitv1.CatalogStatus, 0, len(registrations))
	for _, registration := range registrations {
		status, err := s.status(ctx, registration)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].GetId() < statuses[j].GetId() })
	return statuses, nil
}

func stamp(clock func() time.Time) string { return clock().UTC().Format(time.RFC3339) }

// seed writes a skill into a new catalog, so a created catalog is a catalog rather than an
// empty directory.
//
// It is optional, and that is a real choice: a deployment that wants an empty repository gets
// one, and a person creating a catalog through a tool almost always wants something in it.
func writeSeed(root string, seed *skillgitv1.SkillSeed) error {
	if seed == nil || strings.TrimSpace(seed.GetName()) == "" {
		return nil
	}
	name := strings.TrimSpace(seed.GetName())
	if err := validateCatalogID(name); err != nil {
		return err
	}
	directory := filepath.Join(root, filepath.FromSlash(skills.ConventionalSkillDirectories()[0]), name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return api.WrapError(api.KindInternal, err, "creating %s", directory)
	}
	body := seed.GetBody()
	if strings.TrimSpace(body) == "" {
		body = "Describe what this skill is for, and what to do."
	}
	document := "---\nname: " + name + "\ndescription: " +
		seed.GetDescription() + "\n---\n\n# " + name + "\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(directory, skills.SkillFileName),
		[]byte(document), 0o644); err != nil {
		return api.WrapError(api.KindInternal, err, "writing the first skill of a new catalog")
	}
	return nil
}
