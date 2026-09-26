package skill

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	skillv1skill "github.com/Manu343726/toolbox/subsystems/skill/skillv1"
)

// Service is the deployment's view of its skills.
//
// It is read-only over the skills themselves. A write RPC here that did not write a catalog
// would misreport where a skill lives, and the only write it performs is to a project's
// configuration file — the reconciliation the whole design rests on: "adding a skill to the
// project" means naming it, and naming is a configuration change.
type Service struct {
	directory     CatalogDirectory
	local         *LocalCatalog
	project       ConfigSource
	pins          *PinStore
	exposed       ExposedTools
	version       string
	pageSizeLimit int32
}

// NewService builds the service, so a deployment composing it in process can hold it rather than
// only serve it.
func NewService(options Options) (*Service, error) {
	projectDir := strings.TrimSpace(options.ProjectDir)
	pins := NewPinStore(projectDir)
	if err := pins.Load(); err != nil {
		// A deployment that cannot read its own project's pins refuses to start. Serving
		// skills as though nothing had changed would be the alternative, and it is the one
		// that hides a catalog's content having moved under a project.
		return nil, err
	}
	var project ConfigSource
	if options.Config != nil {
		project = options.Config
	}
	return &Service{
		directory:     options.Directory,
		local:         NewLocalCatalog(projectDir),
		project:       project,
		pins:          pins,
		exposed:       options.ExposedTools,
		version:       versionOrDefault(options.Version),
		pageSizeLimit: 200,
	}, nil
}

func versionOrDefault(version string) string {
	if version == "" {
		return Version
	}
	return version
}

// Local returns the implicit local catalog, so a deployment composing this in process can reach
// a project's own skills without a round trip.
func (s *Service) Local() *LocalCatalog { return s.local }

// PinStore returns the project's pins.
func (s *Service) PinStore() *PinStore { return s.pins }

// effectiveSet is a project's skills: everything in the local catalog, plus every reference it
// names.
//
// The two halves are for different purposes and it is worth being precise about why. A local
// skill needs no ceremony and cannot be forgotten, because a person who put a skill in their own
// project has already said they want it. A remote skill is named explicitly, so the
// configuration file records that this project depends on somebody else's content — a fact
// worth being able to read, diff and review.
func (s *Service) effectiveSet(ctx context.Context) ([]*skillv1skill.ProjectSkill, error) {
	declared, err := s.declaredReferences()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var skillsFound []*skillv1skill.ProjectSkill

	// Local first and without a round trip: it is a directory this process can read, and a
	// deployment that had to reach its own project over ConnectRPC to find a skill a person
	// put there would be a deployment that fails whenever its own loopback is unavailable.
	names, err := s.local.Names()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		reference := skills.Reference{Catalog: skills.LocalCatalog, Name: name}
		skill, err := s.local.Skill(name)
		if err != nil {
			// A skill this framework cannot read is reported by Validate, whose whole job is
			// to say what is wrong. Skipping it here means a listing stays a listing, and a
			// broken skill does not make every other skill unreachable.
			continue
		}
		seen[reference.String()] = true
		skillsFound = append(skillsFound, &skillv1skill.ProjectSkill{
			Ref:         &skillv1.SkillRef{Catalog: skills.LocalCatalog, Name: name, Description: skill.Description},
			Uri:         reference.URI(),
			Description: skill.Description,
			Catalog:     skills.LocalCatalog,
			Declared:    false,
			Outdated:    s.localOutdated(reference),
		})
	}

	for _, reference := range declared {
		key := reference.String()
		if seen[key] {
			// A reference naming a local skill is refused at the configuration boundary, so
			// reaching here would mean two spellings of one skill. Skipping rather than
			// failing keeps a listing total.
			continue
		}
		seen[key] = true
		entry, err := s.fetchEntry(ctx, reference)
		if err != nil {
			// A reference that does not resolve is reported by CheckPins as a missing
			// reference, which is a different failure from drift. A listing is not where a
			// person learns that a catalog is gone.
			continue
		}
		skillsFound = append(skillsFound, &skillv1skill.ProjectSkill{
			Ref: &skillv1.SkillRef{
				Catalog: reference.Catalog, Name: reference.Name,
				Description: entry.GetRef().GetDescription(),
			},
			Uri:         reference.URI(),
			Description: entry.GetRef().GetDescription(),
			Catalog:     reference.Catalog,
			Declared:    true,
			Outdated:    s.pinOutdated(reference, entry),
		})
	}
	sortSkills(skillsFound)
	return skillsFound, nil
}

func (s *Service) declaredReferences() ([]skills.Reference, error) {
	if s.project == nil {
		return nil, nil
	}
	return s.project.References()
}

// localOutdated reports drift on a local skill.
//
// A local skill is pinned like any other, even though a person edits it directly: a project that
// pinned one and then edited it has changed the content that was agreed, and the report is how
// that becomes visible. Nothing is blocked — the edited skill is what the person wanted.
func (s *Service) localOutdated(reference skills.Reference) bool {
	pin, pinned := s.pins.Get(reference)
	if !pinned {
		return false
	}
	template, err := s.local.TemplateFor(reference.Name)
	if err != nil {
		return false
	}
	outdated, _, _, _ := Drift(pin, template.Files)
	return outdated
}

func (s *Service) pinOutdated(reference skills.Reference, entry *skillv1.SkillEntry) bool {
	pin, pinned := s.pins.Get(reference)
	if !pinned {
		return false
	}
	outdated, _, _, _ := Drift(pin, fromManifest(entry.GetResources()))
	return outdated
}

func sortSkills(found []*skillv1skill.ProjectSkill) {
	sort.Slice(found, func(i, j int) bool {
		return found[i].Ref.GetCatalog()+"."+found[i].Ref.GetName() <
			found[j].Ref.GetCatalog()+"."+found[j].Ref.GetName()
	})
}

// fromManifest converts a contract manifest into the reader's own file list, so a pin taken from
// a catalog and a pin taken from a directory are the same shape and drift is one comparison
// rather than two.
func fromManifest(manifest []*skillv1.SkillFile) []skills.File {
	files := make([]skills.File, 0, len(manifest))
	for _, file := range manifest {
		files = append(files, skills.File{
			Path:     file.GetPath(),
			Size:     file.GetSize(),
			Digest:   file.GetDigest(),
			MIMEType: file.GetMimeType(),
		})
	}
	return files
}

// fetchEntry reads one skill from the catalog that owns it.
func (s *Service) fetchEntry(ctx context.Context, reference skills.Reference) (*skillv1.SkillEntry, error) {
	if reference.IsLocal() {
		return s.localEntry(reference)
	}
	client, err := s.catalog(ctx, reference.Catalog)
	if err != nil {
		return nil, err
	}
	response, err := callCatalog(ctx, client, "read the skill "+reference.String(),
		func(ctx context.Context) (*connect.Response[skillv1.GetSkillResponse], error) {
			return client.GetSkill(ctx, connect.NewRequest(&skillv1.GetSkillRequest{
				Catalog: reference.Catalog,
				Name:    reference.Name,
			}))
		})
	if err != nil {
		return nil, err
	}
	entry := response.GetEntry()
	if entry == nil {
		return nil, api.Errorf(api.KindNotFound,
			"the catalog %q answered with no skill for %q", reference.Catalog, reference.Name)
	}
	return entry, nil
}

// localEntry reads a local skill into the contract's shape.
func (s *Service) localEntry(reference skills.Reference) (*skillv1.SkillEntry, error) {
	skill, err := s.local.Skill(reference.Name)
	if err != nil {
		return nil, err
	}
	template, err := s.local.TemplateFor(reference.Name)
	if err != nil {
		return nil, err
	}
	frontmatter, err := toStruct(template.Frontmatter.Document)
	if err != nil {
		return nil, err
	}
	return &skillv1.SkillEntry{
		Ref: &skillv1.SkillRef{
			Catalog: skills.LocalCatalog, Name: reference.Name, Description: skill.Description,
		},
		Uri:         reference.URI(),
		Frontmatter: frontmatter,
		Body:        template.Body,
		Resources:   toManifest(template.Files),
		MaxFiles:    skills.MaxFiles,
		MaxBytes:    skills.MaxBytes,
	}, nil
}

// catalog resolves a catalog by identifier and returns a client for it.
func (s *Service) catalog(ctx context.Context, id string) (CatalogClient, error) {
	if s.directory == nil {
		return nil, api.Errorf(api.KindFailedPrecondition,
			"this deployment has integrated no catalogs, so it has no %q catalog. A deployment "+
				"serving only a project's own skills is a working deployment", id)
	}
	client, err := s.directory.Catalog(ctx, id)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// unmet reports which of a skill's declared requirements this deployment cannot provide.
//
// It is a check, not a grant. A dependency is a statement of need, and the useful thing to do
// with one this deployment cannot meet is to say so — a reader that silently satisfied it would
// be worse than one that reported it, because the author would never learn the tool is missing.
func (s *Service) unmet(ctx context.Context, entry *skillv1.SkillEntry) ([]skills.Unmet, error) {
	if s.exposed == nil {
		// A deployment that does not know its own tool surface cannot tell a reader that a
		// dependency is missing, and guessing would be worse than saying nothing.
		return nil, nil
	}
	exposed, err := s.exposed.ExposedTools(ctx)
	if err != nil {
		return nil, err
	}
	available := make(map[string]bool, len(exposed))
	for _, name := range exposed {
		available[strings.ToLower(strings.TrimSpace(name))] = true
	}
	// The requirements are read from the frontmatter rather than from a field of the entry,
	// because they are a feature of the skill and the entry is the skill: a requirement stated
	// in either input dialect — Toolbox's typed block or a client's own — is the same fact and
	// has to be checked the same way.
	skill, err := skillsFromEntry(entry)
	if err != nil {
		return nil, err
	}
	var unmet []skills.Unmet
	for _, requirement := range skill.Tools {
		name := strings.ToLower(strings.TrimSpace(requirement.Name))
		if name == "" || available[name] {
			continue
		}
		unmet = append(unmet, skills.Unmet{
			Requirement: requirement,
			Reason:      "this deployment exposes no tool by that name",
		})
	}
	return unmet, nil
}

// skillsFromEntry reads the typed skill out of a catalog's entry, so the requirements, the
// enabled flag and everything else are read by one reader rather than by each caller.
func skillsFromEntry(entry *skillv1.SkillEntry) (skills.Skill, error) {
	document := entry.GetFrontmatter().AsMap()
	front, err := skills.SkillFromFrontmatter(document)
	if err != nil {
		return skills.Skill{}, err
	}
	return front.Skill, nil
}

// page applies a page size and token to an ordered list, so every listing method behaves the
// same way and none of them can return a list longer than the caller agreed to.
func page[T any](items []T, size int32, token string) ([]T, string) {
	start := 0
	if token != "" {
		// A token is an index into the ordered list rather than an opaque handle, because
		// the list is derived from catalogs that can change between calls and an opaque
		// handle would resume in the wrong place without saying so.
		if parsed, err := parseIndex(token); err == nil {
			start = parsed
		}
	}
	if start > len(items) {
		start = len(items)
	}
	rest := items[start:]
	if size <= 0 || int(size) >= len(rest) {
		return rest, ""
	}
	return rest[:size], indexToken(start + int(size))
}

func indexToken(index int) string { return "offset:" + strconv.Itoa(index) }

func parseIndex(token string) (int, error) {
	value, found := strings.CutPrefix(token, "offset:")
	if !found {
		return 0, api.Errorf(api.KindInvalid, "%q is not a page token", token)
	}
	offset, err := strconv.Atoi(value)
	if err != nil || offset < 0 {
		return 0, api.Errorf(api.KindInvalid, "%q is not a page offset", token)
	}
	return offset, nil
}

// pinAndCheck records a first-use pin and reports drift.
//
// A skill nobody has pinned yet is pinned here rather than refused: a project's `skills:` list is
// the permission, and a person naming a skill has already decided to use it. The pin is
// recorded so that a *later* change is visible.
func (s *Service) pinAndCheck(ctx context.Context, reference skills.Reference, entry *skillv1.SkillEntry) (bool, error) {
	if _, pinned := s.pins.Get(reference); !pinned {
		if err := s.pins.Record(reference, fromManifest(entry.GetResources())); err != nil {
			return false, err
		}
		return false, nil
	}
	pin, _ := s.pins.Get(reference)
	outdated, _, _, _ := Drift(pin, fromManifest(entry.GetResources()))
	return outdated, nil
}
