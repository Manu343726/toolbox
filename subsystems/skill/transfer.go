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

// Copying and moving a skill between catalogs, composed here rather than served by a catalog.
//
// A copy and a move both span two catalogs, and two catalogs need not share a provider: the
// git-backed host holds a checkout per remote, and a deployment may run two of them. The contract
// a catalog implements is about *one source of skills*, and a provider asked to move a skill
// between catalogs would have to resolve a catalog it does not hold — which is the coupling the
// provider pattern exists to avoid, and which would also mean every provider implemented these
// twice.
//
// So they are built here out of reads and writes a catalog already serves: `GetSkill` and
// `ReadSkillFile` to read, `PutSkill` to write, `DeleteSkill` to remove. Every provider gains
// them without being changed, and they work between catalogs held by different providers.

// CopySkill writes a skill into another catalog and leaves the source as it is.
//
// A copy that fails halfway has duplicated a skill, which is recoverable: the source is still
// there, and the duplicate is the target's — which a caller can see and remove. That is why the
// two operations are separate rather than one with a flag, and why this one's failure is not
// the same kind of thing as a move's.
func (s *Service) CopySkill(
	ctx context.Context, request *connect.Request[skillv1skill.CopySkillRequest],
) (*connect.Response[skillv1skill.CopySkillResponse], error) {
	source, target, err := s.endpoints(request.Msg.GetSourceCatalog(), request.Msg.GetSourceName(),
		request.Msg.GetTargetCatalog(), request.Msg.GetTargetName())
	if err != nil {
		return nil, err
	}
	_, content, err := s.read(ctx, source)
	if err != nil {
		return nil, err
	}
	// The target is checked *after* the read, because a caller that asked for a skill this
	// deployment does not serve deserves to hear that, rather than to hear that some catalog
	// is read-only on the way to finding out.
	if err := s.requireWritable(ctx, target, "copied into"); err != nil {
		return nil, err
	}
	if err := s.write(ctx, target, content, request.Msg.GetFailIfExists()); err != nil {
		return nil, err
	}
	stored, err := s.fetchEntry(ctx, target)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&skillv1skill.CopySkillResponse{
		Entry:  stored,
		Source: refOf(source),
	}), nil
}

// MoveSkill writes a skill into another catalog and removes it from this one.
//
// Both ends are checked before anything is fetched, and that order is the point: a move that
// cannot happen should cost a caller nothing, and the two conditions are different. The target
// must accept a write, or the skill has nowhere to go. The source must accept a removal, or the
// skill would be duplicated rather than moved — and a source that will not accept a removal is
// its own condition, not an inference from copying.
//
// A removal that fails *after* the write is reported rather than raised: the skill is then in
// both catalogs, which is a duplicate and not a loss, and a caller told "the move failed" would
// look in the source, find the skill, and conclude something worse than a duplicate.
func (s *Service) MoveSkill(
	ctx context.Context, request *connect.Request[skillv1skill.MoveSkillRequest],
) (*connect.Response[skillv1skill.MoveSkillResponse], error) {
	source, target, err := s.endpoints(request.Msg.GetSourceCatalog(), request.Msg.GetSourceName(),
		request.Msg.GetTargetCatalog(), request.Msg.GetTargetName())
	if err != nil {
		return nil, err
	}
	if err := s.requireWritable(ctx, target, "moved into"); err != nil {
		return nil, err
	}
	if err := s.requireWritable(ctx, source, "moved out of"); err != nil {
		return nil, err
	}
	_, content, err := s.read(ctx, source)
	if err != nil {
		return nil, err
	}
	// A move never replaces. `PutSkill` can be asked not to overwrite, and a copy can choose,
	// because the caller is deciding what happens to a version that already exists. A move has
	// no such choice to offer: replacing would destroy whatever the target held, and the one
	// thing a move is for is that the skill is *not* in two places.
	if err := s.write(ctx, target, content, true); err != nil {
		return nil, err
	}
	stored, err := s.fetchEntry(ctx, target)
	if err != nil {
		return nil, err
	}
	removed, problem := s.remove(ctx, source)
	response := &skillv1skill.MoveSkillResponse{
		Entry:         stored,
		Source:        refOf(source),
		SourceRemoved: removed,
		SourceProblem: problem,
	}
	return connect.NewResponse(response), nil
}

// endpoints validates both ends of a copy or a move and returns them as references.
//
// Both names are required, and neither is defaulted. The target's name is the one that matters:
// defaulting it to the source's would make a call that collides fail on a collision the caller
// never asked for, and a caller who wants the same name is saying so.
func (s *Service) endpoints(sourceCatalog, sourceName, targetCatalog, targetName string) (
	skills.Reference, skills.Reference, error,
) {
	source, err := referenceOf(sourceCatalog, sourceName, "the skill to read")
	if err != nil {
		return skills.Reference{}, skills.Reference{}, err
	}
	target, err := referenceOf(targetCatalog, targetName, "the skill to write")
	if err != nil {
		return skills.Reference{}, skills.Reference{}, err
	}
	return source, target, nil
}

// referenceOf builds a reference from a catalog and a name, naming what the pair is for.
//
// Only emptiness is refused here. What a catalog will *accept* as a name is the catalog's own
// rule, applied where the write happens: a second copy of it in this package would be a second
// statement of the same fact, and the two would drift the first time a provider tightened its
// own check.
func referenceOf(catalog, name, what string) (skills.Reference, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return skills.Reference{}, api.Errorf(api.KindInvalid,
			"%s needs a name, and a name is one directory's name within a catalog", what)
	}
	return skills.Reference{Catalog: strings.TrimSpace(catalog), Name: trimmed}, nil
}

// requireWritable refuses a catalog that will not accept a write, naming what was being
// attempted and which catalog refused.
//
// Writability is *declared* rather than discovered by attempting the write, which is what
// `CatalogInfo.writable` is for: a move that cannot happen then costs nothing, and the caller is
// told which catalog refused and what it was trying to do there.
func (s *Service) requireWritable(ctx context.Context, reference skills.Reference, doing string) error {
	info, err := s.describe(ctx, reference.Catalog)
	if err != nil {
		return err
	}
	if info.GetWritable() {
		return nil
	}
	return api.Errorf(api.KindDenied,
		"the %s catalog is read-only, so a skill cannot be %s. It was registered from "+
			"elsewhere rather than created by this deployment, and this deployment will not "+
			"write to somebody else's repository. A catalog created here can be written to",
		reference.Catalog, doing)
}

// describe reads what a catalog says about itself.
func (s *Service) describe(ctx context.Context, catalog string) (*skillv1.CatalogInfo, error) {
	client, err := s.catalog(ctx, catalog)
	if err != nil {
		return nil, err
	}
	response, err := callCatalog(ctx, client, "describe the catalog "+catalog,
		func(ctx context.Context) (*connect.Response[skillv1.DescribeCatalogResponse], error) {
			return client.DescribeCatalog(ctx, connect.NewRequest(&skillv1.DescribeCatalogRequest{
				Catalog: catalog,
			}))
		})
	if err != nil {
		return nil, err
	}
	return response.GetInfo(), nil
}

// read fetches a whole skill: its manifest and every file the manifest lists.
//
// Complete by construction, because a manifest that is not complete cannot be a pin — so a copy
// cannot write a skill whose supporting files it did not read, and this walks the manifest
// rather than a directory listing, which is the authority on what a skill contains.
func (s *Service) read(ctx context.Context, reference skills.Reference) (
	*skillv1.SkillEntry, []*skillv1.SkillFileContent, error,
) {
	entry, err := s.fetchEntry(ctx, reference)
	if err != nil {
		return nil, nil, err
	}
	resources := entry.GetResources()
	if len(resources) == 0 {
		return nil, nil, api.Errorf(api.KindFailedPrecondition,
			"the %s catalog returned a manifest for %q listing no files, and a skill with no "+
				"files is a pin nothing describes", reference.Catalog, reference.Name)
	}
	client, err := s.catalog(ctx, reference.Catalog)
	if err != nil {
		return nil, nil, err
	}
	content := make([]*skillv1.SkillFileContent, 0, len(resources))
	for _, resource := range resources {
		file, err := s.readFile(ctx, client, reference, resource.GetPath())
		if err != nil {
			return nil, nil, err
		}
		content = append(content, file)
	}
	return entry, content, nil
}

// readFile fetches one file of a skill, checking it against the manifest it was listed in.
//
// The check is `verifyFile`, the same one a served file goes through, rather than a digest
// comparison written here: a copy that carried a file whose bytes disagree with the manifest
// would write a skill whose pin describes something else, and the mismatch is the only moment
// at which it can be noticed. Two statements of what a digest means is two that can differ.
func (s *Service) readFile(
	ctx context.Context, client CatalogClient, reference skills.Reference, path string,
) (*skillv1.SkillFileContent, error) {
	response, err := callCatalog(ctx, client, "read "+reference.String()+"/"+path,
		func(ctx context.Context) (*connect.Response[skillv1.ReadSkillFileResponse], error) {
			return client.ReadSkillFile(ctx, connect.NewRequest(&skillv1.ReadSkillFileRequest{
				Catalog: reference.Catalog,
				Name:    reference.Name,
				Path:    path,
			}))
		})
	if err != nil {
		return nil, err
	}
	claimed := skills.File{
		Path:   response.GetPath(),
		Size:   response.GetSize(),
		Digest: response.GetDigest(),
	}
	if err := verifyFile(reference, claimed, response.GetContent()); err != nil {
		return nil, err
	}
	return &skillv1.SkillFileContent{Path: response.GetPath(), Content: response.GetContent()}, nil
}

// write lays a whole skill into a catalog.
func (s *Service) write(
	ctx context.Context, reference skills.Reference, content []*skillv1.SkillFileContent, failIfExists bool,
) error {
	client, err := s.catalog(ctx, reference.Catalog)
	if err != nil {
		return err
	}
	_, err = callCatalog(ctx, client, "write "+reference.String(),
		func(ctx context.Context) (*connect.Response[skillv1.PutSkillResponse], error) {
			return client.PutSkill(ctx, connect.NewRequest(&skillv1.PutSkillRequest{
				Catalog: reference.Catalog,
				// The name is stated and the manifest is not. The name is what the
				// catalog is being asked to call this, and it cannot be inferred: the
				// files say what the skill contains, not what to call it. The manifest
				// is left for the catalog to compute from the bytes it is given,
				// because a catalog recording a claim about content it never saw is a
				// pin nothing describes.
				Entry: &skillv1.SkillEntry{
					Ref: &skillv1.SkillRef{Catalog: reference.Catalog, Name: reference.Name},
				},
				Files:        content,
				FailIfExists: failIfExists,
			}))
		})
	return err
}

// remove deletes a skill from a catalog, reporting whether it went and why not.
func (s *Service) remove(ctx context.Context, reference skills.Reference) (bool, string) {
	client, err := s.catalog(ctx, reference.Catalog)
	if err != nil {
		return false, err.Error()
	}
	response, err := callCatalog(ctx, client, "remove "+reference.String(),
		func(ctx context.Context) (*connect.Response[skillv1.DeleteSkillResponse], error) {
			return client.DeleteSkill(ctx, connect.NewRequest(&skillv1.DeleteSkillRequest{
				Catalog: reference.Catalog,
				Name:    reference.Name,
			}))
		})
	if err != nil {
		return false, err.Error()
	}
	if response.GetRemoved() {
		return true, ""
	}
	return false, "the " + reference.Catalog + " catalog reported that it held no such skill to remove"
}

// refOf is a reference in the contract's shape, for a response to name what it acted on.
func refOf(reference skills.Reference) *skillv1.SkillRef {
	return &skillv1.SkillRef{Catalog: reference.Catalog, Name: reference.Name}
}
