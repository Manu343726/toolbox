package skill

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	skillv1skill "github.com/Manu343726/toolbox/subsystems/skill/skillv1"
)

// ListSkills returns every skill the project may use, from every integrated catalog.
func (s *Service) ListSkills(
	ctx context.Context, request *connect.Request[skillv1skill.ListSkillsRequest],
) (*connect.Response[skillv1skill.ListSkillsResponse], error) {
	found, err := s.effectiveSet(ctx)
	if err != nil {
		return nil, connectFailure(err)
	}
	if only := strings.TrimSpace(request.Msg.GetCatalog()); only != "" {
		// The list is built by pointer throughout, because a generated message carries a lock
		// and copying one by value is a race waiting for the first concurrent read.
		filtered := make([]*skillv1skill.ProjectSkill, 0, len(found))
		for _, one := range found {
			if one.GetCatalog() == only {
				filtered = append(filtered, one)
			}
		}
		found = filtered
	}
	shown, next := page(found, request.Msg.GetPageSize(), request.Msg.GetPageToken())
	return connect.NewResponse(&skillv1skill.ListSkillsResponse{
		Skills:        shown,
		NextPageToken: next,
	}), nil
}

// GetSkill returns one skill, as a template.
//
// What is returned is the portable artefact and the thing a pin is taken against. What a
// particular client receives is a distillation of it, which the Model Context Protocol endpoint
// serves rather than this method — because the projection depends on the client asking, and
// this call has no client to depend on.
func (s *Service) GetSkill(
	ctx context.Context, request *connect.Request[skillv1skill.GetSkillRequest],
) (*connect.Response[skillv1skill.GetSkillResponse], error) {
	reference, err := s.reference(request.Msg.GetRef())
	if err != nil {
		return nil, connectFailure(err)
	}
	entry, err := s.fetchEntry(ctx, reference)
	if err != nil {
		return nil, connectFailure(err)
	}
	outdated, err := s.pinAndCheck(ctx, reference, entry)
	if err != nil {
		return nil, connectFailure(err)
	}
	unmet, err := s.unmet(ctx, entry)
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1skill.GetSkillResponse{
		Entry:    entry,
		PinnedIn: s.pinFile(),
		Outdated: outdated,
		Unmet:    unmetRefs(unmet),
	}), nil
}

// FindSkill searches the integrated catalogs.
//
// The query is pushed down rather than answered by fetching everything, so a large catalog is
// not shipped here to be filtered. A catalog that cannot search is reported, because a caller
// that wanted a search and got a short list would not know it had to do the work itself.
func (s *Service) FindSkill(
	ctx context.Context, request *connect.Request[skillv1skill.FindSkillRequest],
) (*connect.Response[skillv1skill.FindSkillResponse], error) {
	query := strings.TrimSpace(request.Msg.GetQuery())
	if query == "" {
		return nil, connectFailure(api.Errorf(api.KindInvalid,
			"a search needs something to look for"))
	}
	only := strings.TrimSpace(request.Msg.GetCatalog())

	catalogs, err := s.catalogs(ctx)
	if err != nil {
		return nil, connectFailure(err)
	}
	// The local catalog is searched in process rather than over ConnectRPC, for the same
	// reason it is listed in process: it is a directory this process can read, and a
	// deployment that had to reach its own project to search it would fail whenever its own
	// loopback was unavailable.
	var found []*skillv1skill.ProjectSkill
	var unsearchable []string
	if only == "" || only == skills.LocalCatalog {
		names, err := s.local.Names()
		if err != nil {
			return nil, connectFailure(err)
		}
		for _, name := range names {
			skill, err := s.local.Skill(name)
			if err != nil || !matchesQuery(name, skill.Description, query) {
				continue
			}
			reference := skills.Reference{Catalog: skills.LocalCatalog, Name: name}
			found = append(found, &skillv1skill.ProjectSkill{
				Ref: &skillv1.SkillRef{
					Catalog: skills.LocalCatalog, Name: name, Description: skill.Description,
				},
				Uri:         reference.URI(),
				Description: skill.Description,
				Catalog:     skills.LocalCatalog,
			})
		}
	}

	for _, catalog := range catalogs {
		if only != "" && catalog.GetId() != only {
			continue
		}
		client, err := s.directory.Catalog(ctx, catalog.GetId())
		if err != nil {
			return nil, connectFailure(err)
		}
		response, err := callCatalog(ctx, client, "search for "+query,
			func(ctx context.Context) (*connect.Response[skillv1.FindSkillsResponse], error) {
				return client.FindSkills(ctx, connect.NewRequest(&skillv1.FindSkillsRequest{
					Catalog:  catalog.GetId(),
					Query:    query,
					PageSize: request.Msg.GetPageSize(),
				}))
			})
		if err != nil {
			return nil, connectFailure(err)
		}
		if !response.GetSearchable() {
			unsearchable = append(unsearchable, catalog.GetId())
			continue
		}
		for _, match := range response.GetSkills() {
			reference := skills.Reference{Catalog: catalog.GetId(), Name: match.GetName()}
			found = append(found, &skillv1skill.ProjectSkill{
				Ref: &skillv1.SkillRef{
					Catalog: catalog.GetId(), Name: match.GetName(), Description: match.GetDescription(),
				},
				Uri:         reference.URI(),
				Description: match.GetDescription(),
				Catalog:     catalog.GetId(),
			})
		}
	}
	sortSkills(found)
	shown, next := page(found, request.Msg.GetPageSize(), request.Msg.GetPageToken())
	return connect.NewResponse(&skillv1skill.FindSkillResponse{
		Skills:               shown,
		NextPageToken:        next,
		UnsearchableCatalogs: unsearchable,
	}), nil
}

// ReadSkillFile returns one file of a skill, with the digest and size its manifest gave, so a
// caller can verify what it fetched.
func (s *Service) ReadSkillFile(
	ctx context.Context, request *connect.Request[skillv1skill.ReadSkillFileRequest],
) (*connect.Response[skillv1skill.ReadSkillFileResponse], error) {
	reference, err := s.reference(request.Msg.GetRef())
	if err != nil {
		return nil, connectFailure(err)
	}
	relative := strings.TrimSpace(request.Msg.GetPath())
	if relative == "" {
		return nil, connectFailure(api.Errorf(api.KindInvalid,
			"a file read needs a path within the skill"))
	}
	if reference.IsLocal() {
		content, file, err := s.local.ReadFile(reference.Name, relative)
		if err != nil {
			return nil, connectFailure(err)
		}
		if err := verifyFile(reference, file, content); err != nil {
			return nil, connectFailure(err)
		}
		return connect.NewResponse(&skillv1skill.ReadSkillFileResponse{
			Path:     file.Path,
			Content:  content,
			Digest:   file.Digest,
			Size:     file.Size,
			MimeType: file.MIMEType,
		}), nil
	}
	client, err := s.catalog(ctx, reference.Catalog)
	if err != nil {
		return nil, connectFailure(err)
	}
	response, err := callCatalog(ctx, client, "read "+relative+" of "+reference.String(),
		func(ctx context.Context) (*connect.Response[skillv1.ReadSkillFileResponse], error) {
			return client.ReadSkillFile(ctx, connect.NewRequest(&skillv1.ReadSkillFileRequest{
				Catalog: reference.Catalog, Name: reference.Name, Path: relative,
			}))
		})
	if err != nil {
		return nil, connectFailure(err)
	}
	// The content is verified against the manifest here rather than passed on, because this
	// is the one place a file's bytes and a file's digest are both in hand. The specification
	// has a host verify what it fetched; a server that verifies it first can refuse to serve
	// at all, which is a better outcome than a host discovering it.
	if err := verifyFile(reference, skills.File{
		Path:     response.GetPath(),
		Size:     response.GetSize(),
		Digest:   response.GetDigest(),
		MIMEType: response.GetMimeType(),
	}, response.GetContent()); err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1skill.ReadSkillFileResponse{
		Path:     response.GetPath(),
		Content:  response.GetContent(),
		Digest:   response.GetDigest(),
		Size:     response.GetSize(),
		MimeType: response.GetMimeType(),
	}), nil
}

// ListCatalogs returns the catalogs this deployment has integrated.
//
// The implicit local catalog is included, because a caller asking what a project can reach
// should not have to know that one of its sources needs no configuration to exist.
func (s *Service) ListCatalogs(
	ctx context.Context, _ *connect.Request[skillv1skill.ListCatalogsRequest],
) (*connect.Response[skillv1skill.ListCatalogsResponse], error) {
	integrated, err := s.catalogs(ctx)
	if err != nil {
		return nil, connectFailure(err)
	}
	listed := make([]*skillv1.CatalogInfo, 0, len(integrated)+1)
	listed = append(listed, &skillv1.CatalogInfo{
		Id:   skills.LocalCatalog,
		Name: "This project",
		Description: "The skills in this project's own skills directory. They are part of the " +
			"project without being named, and the framework never writes to them: a person " +
			"owns these files.",
		Writable: false,
		Location: s.local.Location(),
	})
	listed = append(listed, integrated...)
	return connect.NewResponse(&skillv1skill.ListCatalogsResponse{Catalogs: listed}), nil
}

// CheckPins reports which of the project's named skills have changed underneath it.
//
// Drift is reported and the skill is still served. The pin is the agreement, and nothing here
// decides on the user's behalf what to do about a change they were not asked about.
func (s *Service) CheckPins(
	ctx context.Context, request *connect.Request[skillv1skill.CheckPinsRequest],
) (*connect.Response[skillv1skill.CheckPinsResponse], error) {
	asked := make([]skills.Reference, 0, len(request.Msg.GetRefs()))
	for _, written := range request.Msg.GetRefs() {
		reference, err := s.reference(written)
		if err != nil {
			return nil, connectFailure(err)
		}
		asked = append(asked, reference)
	}

	declared, err := s.declaredReferences()
	if err != nil {
		return nil, connectFailure(err)
	}
	checked := asked
	if len(checked) == 0 {
		checked = declared
	}

	response := skillv1skill.CheckPinsResponse{PinnedIn: s.pinFile()}
	for _, reference := range checked {
		entry, err := s.fetchEntry(ctx, reference)
		if err != nil {
			// A reference that no longer resolves is a different failure from drift: a
			// catalog that was unregistered leaves a project's list naming something that is
			// not there, and a person needs to hear that rather than see a shorter list.
			response.Missing = append(response.Missing, &skillv1skill.MissingSkillRef{
				Ref:    reference.String(),
				Reason: err.Error(),
			})
			continue
		}
		pin, pinned := s.pins.Get(reference)
		if !pinned {
			continue
		}
		outdated, added, removed, changed := Drift(pin, fromManifest(entry.GetResources()))
		if !outdated {
			continue
		}
		response.Outdated = append(response.Outdated, &skillv1skill.ProjectSkill{
			Ref:         entry.GetRef(),
			Uri:         reference.URI(),
			Description: entry.GetRef().GetDescription(),
			Catalog:     reference.Catalog,
			Declared:    true,
			Outdated:    true,
		})
		_ = added
		_ = removed
		_ = changed
	}
	return connect.NewResponse(&response), nil
}

// Validate checks every skill the project may use, and fails a deployment at start rather than
// at first use.
//
// A skill whose frontmatter this framework cannot read is a deployment that will serve a broken
// skill to the first model that asks for it, and a person would rather hear at start.
func (s *Service) Validate(
	ctx context.Context, request *connect.Request[skillv1skill.ValidateRequest],
) (*connect.Response[skillv1skill.ValidateResponse], error) {
	only := strings.TrimSpace(request.Msg.GetCatalog())
	response := skillv1skill.ValidateResponse{}

	names, err := s.local.Names()
	if err != nil {
		return nil, connectFailure(err)
	}
	for _, name := range names {
		if only != "" && only != skills.LocalCatalog {
			break
		}
		reference := skills.Reference{Catalog: skills.LocalCatalog, Name: name}
		template, err := s.local.TemplateFor(name)
		if err != nil {
			response.Invalid = append(response.Invalid, invalidSkill(reference, err))
			continue
		}
		if err := template.Validate(); err != nil {
			response.Invalid = append(response.Invalid, invalidSkill(reference, err))
			continue
		}
		response.Checked++
	}

	declared, err := s.declaredReferences()
	if err != nil {
		return nil, connectFailure(err)
	}
	for _, reference := range declared {
		if only != "" && reference.Catalog != only {
			continue
		}
		entry, err := s.fetchEntry(ctx, reference)
		if err != nil {
			response.Invalid = append(response.Invalid, invalidSkill(reference, err))
			continue
		}
		// The entry is read back into the reader's own form, so a skill a catalog served is
		// checked by the same code that checks a skill from a directory. A catalog that could
		// produce a skill this framework cannot read would be caught here rather than by a
		// model.
		if _, err := skillsFromEntry(entry); err != nil {
			response.Invalid = append(response.Invalid, invalidSkill(reference, err))
			continue
		}
		if err := validateEntry(entry); err != nil {
			response.Invalid = append(response.Invalid, invalidSkill(reference, err))
			continue
		}
		response.Checked++
	}
	return connect.NewResponse(&response), nil
}

// AddSkill adds a qualified reference to the project's skills.
//
// The write happens only when the caller confirms, and the response states the file and the
// change when it does not. That is the configuration-confirmation rule, and it is a field
// rather than a separate call so that the two cannot be reordered: a caller that has not been
// told what the change is cannot have set it.
func (s *Service) AddSkill(
	ctx context.Context, request *connect.Request[skillv1skill.AddSkillRequest],
) (*connect.Response[skillv1skill.AddSkillResponse], error) {
	applied, err := s.commit(ctx, request.Msg.GetRef(), request.Msg.GetConfirm(), true)
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1skill.AddSkillResponse{
		Summary: applied.Summary, File: applied.File, Applied: applied.Applied, Skills: applied.After,
	}), nil
}

// EnableSkill adds a reference that is known to the project but not included.
//
// It is the same write as AddSkill under a name that says what a caller means by it, because a
// tool whose name says "enable" and whose effect is not obvious from the name is a tool nobody
// can use safely.
func (s *Service) EnableSkill(
	ctx context.Context, request *connect.Request[skillv1skill.EnableSkillRequest],
) (*connect.Response[skillv1skill.EnableSkillResponse], error) {
	applied, err := s.commit(ctx, request.Msg.GetRef(), request.Msg.GetConfirm(), true)
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1skill.EnableSkillResponse{
		Summary: applied.Summary, File: applied.File, Applied: applied.Applied, Skills: applied.After,
	}), nil
}

// DisableSkill removes a reference from the project's skills.
//
// It writes the project's configuration file, subject to the same confirmation as the other
// two. Nothing is refused by asking whether the skill is in use: a person disabling a skill
// their project names is a decision they have made.
func (s *Service) DisableSkill(
	ctx context.Context, request *connect.Request[skillv1skill.DisableSkillRequest],
) (*connect.Response[skillv1skill.DisableSkillResponse], error) {
	applied, err := s.commit(ctx, request.Msg.GetRef(), request.Msg.GetConfirm(), false)
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1skill.DisableSkillResponse{
		Summary: applied.Summary, File: applied.File, Applied: applied.Applied, Skills: applied.After,
	}), nil
}

// applied is the outcome of a proposed configuration change, whether or not it was written.
type applied struct {
	// Summary names the file and the change, for a person to confirm or to see confirmed.
	Summary string
	// File is the configuration file the change applies to.
	File string
	// Applied is whether the change was written.
	Applied bool
	// After is the project's skills as they now are.
	After []string
}

// commit proposes a change to the project's configuration and, when the caller confirms, makes
// it.
//
// The three write methods differ only in direction and in their response type, so the decision
// — what the change is, and whether it may be made — is made once here rather than three times
// where the three could disagree.
func (s *Service) commit(ctx context.Context, written string, confirm, add bool) (applied, error) {
	reference, err := s.reference(written)
	if err != nil {
		return applied{}, err
	}
	if s.project == nil {
		return applied{}, api.Errorf(api.KindFailedPrecondition,
			"this deployment has no project configuration, so there is no project to record a "+
				"skill in. A deployment serving only its own configuration has no project skills")
	}

	var change SkillChange
	if add {
		// A skill that cannot be reached is refused before anything is written. Adding a
		// reference to a catalog that is not there would leave a project naming something
		// that resolves to nothing, which is the one state a project's list must never be in.
		//
		// A local reference is exempt: it is refused by the configuration boundary instead,
		// which is where the reason lives — a local skill is part of the project whatever a
		// directory read says, so checking whether it is reachable would be checking the
		// wrong thing and reporting the wrong reason.
		if !reference.IsLocal() {
			if _, err := s.fetchEntry(ctx, reference); err != nil {
				return applied{}, err
			}
		}
		change, err = s.project.ProposeAddition(reference)
	} else {
		change, err = s.project.ProposeRemoval(reference)
	}
	if err != nil {
		return applied{}, err
	}

	outcome := applied{Summary: change.Summary(), File: change.File(), After: change.After()}
	if !confirm || !change.Changed() {
		return outcome, nil
	}
	if err := change.Apply(); err != nil {
		// The summary says so rather than reporting a change that was not made. A caller
		// told "applied" when nothing was written would be worse than a caller told the truth.
		outcome.Summary = change.Summary() + " — but the change was not written: " + err.Error()
		return outcome, err
	}
	outcome.Applied = true
	if !add {
		// A skill the project no longer names has no pin. Leaving one behind would report
		// drift for a skill nothing serves.
		if err := s.pins.Forget(reference); err != nil {
			outcome.Summary = change.Summary() + " — but its pin was not forgotten: " + err.Error()
		}
	}
	return outcome, nil
}

// reference reads a written reference, refusing one that is not qualified.
func (s *Service) reference(written string) (skills.Reference, error) {
	parsed, err := skills.ParseReference(written)
	if err != nil {
		return skills.Reference{}, err
	}
	return parsed, nil
}

func (s *Service) pinFile() string {
	if s.pins == nil {
		return ""
	}
	return s.pins.Path()
}

func (s *Service) catalogs(ctx context.Context) ([]*skillv1.CatalogInfo, error) {
	if s.directory == nil {
		return nil, nil
	}
	providers, err := s.directory.Catalogs(ctx)
	if err != nil {
		return nil, err
	}
	listed := make([]*skillv1.CatalogInfo, 0, len(providers))
	for _, provider := range providers {
		// What a catalog says about itself is asked for rather than read off the provider
		// record, because whether a catalog can be written to is the catalog's own fact. A
		// provider record that claimed otherwise would make a write look like it would
		// succeed.
		client, err := s.directory.Catalog(ctx, provider.ID)
		if err != nil {
			return nil, err
		}
		response, err := callCatalog(ctx, client, "describe itself",
			func(ctx context.Context) (*connect.Response[skillv1.DescribeCatalogResponse], error) {
				return client.DescribeCatalog(ctx, connect.NewRequest(&skillv1.DescribeCatalogRequest{
					Catalog: provider.ID,
				}))
			})
		if err != nil {
			return nil, err
		}
		listed = append(listed, response.GetInfo())
	}
	return listed, nil
}

func invalidSkill(reference skills.Reference, err error) *skillv1skill.InvalidSkillRef {
	kind := api.KindOf(err)
	if kind == "" {
		kind = api.KindInternal
	}
	return &skillv1skill.InvalidSkillRef{Ref: reference.String(), Reason: err.Error(), Kind: string(kind)}
}

func unmetRefs(unmet []skills.Unmet) []*skillv1.SkillRef {
	refs := make([]*skillv1.SkillRef, 0, len(unmet))
	for _, one := range unmet {
		refs = append(refs, &skillv1.SkillRef{
			Name:        one.Requirement.Name,
			Description: one.Requirement.Description,
		})
	}
	return refs
}

func matchesQuery(name, description, query string) bool {
	needle := strings.ToLower(query)
	return strings.Contains(strings.ToLower(name), needle) ||
		strings.Contains(strings.ToLower(description), needle)
}
