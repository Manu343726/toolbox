package skillgit

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
)

// The catalog contract, served for every registered catalog.
//
// The methods are thin on purpose. Resolving which catalog a call is for, reading a skill
// through the shared directory reader, and refusing a write against a checkout this deployment
// does not own is the whole of it — because the reading is `pkg/skills`'s and the *decision*
// about who owns a checkout belongs here.

// DescribeCatalog says what this catalog is and whether it can be written to.
func (s *Service) DescribeCatalog(
	ctx context.Context, request *connect.Request[skillv1.DescribeCatalogRequest],
) (*connect.Response[skillv1.DescribeCatalogResponse], error) {
	registration, err := s.registration(request.Msg.GetCatalog())
	if err != nil {
		return nil, connectFailure(err)
	}
	catalog, err := s.directory(registration)
	if err != nil {
		return nil, connectFailure(err)
	}
	names, err := catalog.Names()
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1.DescribeCatalogResponse{
		Info: &skillv1.CatalogInfo{
			Id:          registration.ID,
			Name:        registration.ID,
			Description: descriptionFor(registration, len(names)),
			// A catalog this provider owns is writable; one it merely serves is not. The
			// distinction is the registration's, because "this deployment made it" and "this
			// deployment found it" are different facts about the same checkout.
			Writable: !registration.ReadOnly,
			Location: catalog.Location(),
		},
	}), nil
}

// ListCatalogs returns every catalog this provider holds.
//
// It is the provider's own answer rather than the aggregator's guess, because a provider is
// not a catalog: this one holds a checkout per registered remote, and a deployment's catalogs
// are those names rather than anything about this subsystem. A provider serving a single
// catalog answers with that one.
func (s *Service) ListCatalogs(
	ctx context.Context, _ *connect.Request[skillv1.ListCatalogsRequest],
) (*connect.Response[skillv1.ListCatalogsResponse], error) {
	registrations := s.registry.All()
	listed := make([]*skillv1.CatalogInfo, 0, len(registrations))
	for _, registration := range registrations {
		catalog, err := s.directory(registration)
		if err != nil {
			return nil, connectFailure(err)
		}
		names, err := catalog.Names()
		if err != nil {
			return nil, connectFailure(err)
		}
		listed = append(listed, &skillv1.CatalogInfo{
			Id:   registration.ID,
			Name: registration.ID,
			Description: descriptionFor(registration, len(names)) +
				" A skill in it is a directory with a SKILL.md, read the same way a project's " +
				"own skills are read.",
			Writable: !registration.ReadOnly,
			Location: catalog.Location(),
		})
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].GetId() < listed[j].GetId() })
	return connect.NewResponse(&skillv1.ListCatalogsResponse{Catalogs: listed}), nil
}

// ListSkills returns every skill this catalog holds, as references.
//
// References and descriptions rather than whole entries, because a client decides what to load
// from descriptions and should not fetch a manifest to read one. The query is answered here
// rather than by the caller fetching everything and filtering, so a large repository is not
// shipped to be sorted somewhere else.
func (s *Service) ListSkills(
	_ context.Context, request *connect.Request[skillv1.ListSkillsRequest],
) (*connect.Response[skillv1.ListSkillsResponse], error) {
	registration, catalog, err := s.resolve(request.Msg.GetCatalog())
	if err != nil {
		return nil, connectFailure(err)
	}
	names, err := catalog.Names()
	if err != nil {
		return nil, connectFailure(err)
	}
	prefix := strings.ToLower(strings.TrimSpace(request.Msg.GetNamePrefix()))
	var listed []*skillv1.SkillRef
	for _, name := range names {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(name), prefix) {
			continue
		}
		described := ""
		if template, err := catalog.Cached(name); err == nil {
			described = template.Frontmatter.Skill.Description
		}
		listed = append(listed, &skillv1.SkillRef{
			Catalog: registration.ID, Name: name, Description: described,
		})
	}
	shown, next := paginate(len(listed), request.Msg.GetPageSize(), request.Msg.GetPageToken())
	return connect.NewResponse(&skillv1.ListSkillsResponse{
		Skills:        listed[:shown],
		NextPageToken: next,
	}), nil
}

// FindSkills answers a query about this catalog's own skills.
//
// It is answered here rather than by the caller listing and filtering, which is the whole point
// of the method existing on the contract: a repository with thousands of skills is not shipped
// over ConnectRPC so that somebody can discard most of them.
func (s *Service) FindSkills(
	_ context.Context, request *connect.Request[skillv1.FindSkillsRequest],
) (*connect.Response[skillv1.FindSkillsResponse], error) {
	registration, catalog, err := s.resolve(request.Msg.GetCatalog())
	if err != nil {
		return nil, connectFailure(err)
	}
	query := strings.ToLower(strings.TrimSpace(request.Msg.GetQuery()))
	if query == "" {
		return nil, connectFailure(api.Errorf(api.KindInvalid, "a search needs something to look for"))
	}
	names, err := catalog.Names()
	if err != nil {
		return nil, connectFailure(err)
	}
	var found []*skillv1.SkillRef
	for _, name := range names {
		template, err := catalog.Cached(name)
		if err != nil {
			// A skill this framework cannot read is not a match, and the reason it cannot
			// read it is reported by the aggregator's Validate rather than by a search that
			// silently omitted it.
			continue
		}
		description := template.Frontmatter.Skill.Description
		if !strings.Contains(strings.ToLower(name), query) &&
			!strings.Contains(strings.ToLower(description), query) {
			continue
		}
		found = append(found, &skillv1.SkillRef{
			Catalog: registration.ID, Name: name, Description: description,
		})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].GetName() < found[j].GetName() })
	shown, next := paginate(len(found), request.Msg.GetPageSize(), request.Msg.GetPageToken())
	return connect.NewResponse(&skillv1.FindSkillsResponse{
		// Searchable is true, and the reason is worth stating: the method is answered from
		// the checkout's own index of names and descriptions, so nothing is shipped to be
		// filtered elsewhere.
		Searchable:    true,
		Skills:        found[:shown],
		NextPageToken: next,
	}), nil
}

// GetSkill returns one skill with its complete manifest.
func (s *Service) GetSkill(
	_ context.Context, request *connect.Request[skillv1.GetSkillRequest],
) (*connect.Response[skillv1.GetSkillResponse], error) {
	registration, err := s.registration(request.Msg.GetCatalog())
	if err != nil {
		return nil, connectFailure(err)
	}
	entry, err := s.entry(registration, request.Msg.GetName())
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1.GetSkillResponse{Entry: entry}), nil
}

// ReadSkillFile returns one file of a skill, with the digest and size its manifest gave.
//
// The digest comes from the manifest and the bytes come from the checkout, and neither is
// derived from the other: a caller verifying what it fetched needs the manifest's claim and
// the actual content, and a provider that recomputed the digest here would be checking the
// content against itself.
func (s *Service) ReadSkillFile(
	_ context.Context, request *connect.Request[skillv1.ReadSkillFileRequest],
) (*connect.Response[skillv1.ReadSkillFileResponse], error) {
	_, catalog, err := s.resolve(request.Msg.GetCatalog())
	if err != nil {
		return nil, connectFailure(err)
	}
	relative := strings.TrimSpace(request.Msg.GetPath())
	if relative == "" {
		return nil, connectFailure(api.Errorf(api.KindInvalid, "a file read needs a path"))
	}
	content, entry, err := catalog.File(request.Msg.GetName(), relative)
	if err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1.ReadSkillFileResponse{
		Path:     entry.Path,
		Content:  content,
		Digest:   entry.Digest,
		Size:     entry.Size,
		MimeType: entry.MIMEType,
	}), nil
}

// PutSkill stores a whole skill in a catalog this deployment owns.
//
// It is refused for a read-only one *before* anything is written, with the reason, because that
// is what makes "copy and move" answerable rather than appearing to succeed. And the write is
// one commit: a modification is a change, and a history showing one commit per thing is a
// history somebody can read.
func (s *Service) PutSkill(
	ctx context.Context, request *connect.Request[skillv1.PutSkillRequest],
) (*connect.Response[skillv1.PutSkillResponse], error) {
	registration, err := s.registration(request.Msg.GetCatalog())
	if err != nil {
		return nil, connectFailure(err)
	}
	if err := s.requireWritable(registration); err != nil {
		return nil, connectFailure(err)
	}
	entry := request.Msg.GetEntry()
	name := strings.TrimSpace(entry.GetRef().GetName())
	if name == "" {
		return nil, connectFailure(api.Errorf(api.KindInvalid, "a skill to store needs a name"))
	}
	if err := ValidCatalogID(name); err != nil {
		return nil, connectFailure(err)
	}
	catalog, err := s.directory(registration)
	if err != nil {
		return nil, connectFailure(err)
	}
	if request.Msg.GetFailIfExists() && catalog.Has(name) {
		return nil, connectFailure(api.Errorf(api.KindAlreadyExists,
			"the %s catalog already holds a skill named %q, and this call refused to replace it",
			registration.ID, name))
	}
	if err := s.writeSkill(registration, request); err != nil {
		return nil, connectFailure(err)
	}
	stored, err := s.entry(registration, name)
	if err != nil {
		return nil, connectFailure(err)
	}
	// The entry comes back from the checkout rather than from the request, so what a caller is
	// told was stored is what the catalog now holds — including the manifest, which is
	// computed from the bytes that were written rather than copied from what was asked for.
	if err := s.git.Commit(ctx, s.checkout(registration),
		"Store the skill "+name+".", s.identity); err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1.PutSkillResponse{Entry: stored}), nil
}

// DeleteSkill removes a whole skill from a catalog this deployment owns.
func (s *Service) DeleteSkill(
	ctx context.Context, request *connect.Request[skillv1.DeleteSkillRequest],
) (*connect.Response[skillv1.DeleteSkillResponse], error) {
	registration, err := s.registration(request.Msg.GetCatalog())
	if err != nil {
		return nil, connectFailure(err)
	}
	if err := s.requireWritable(registration); err != nil {
		return nil, connectFailure(err)
	}
	name := strings.TrimSpace(request.Msg.GetName())
	catalog, err := s.directory(registration)
	if err != nil {
		return nil, connectFailure(err)
	}
	path, err := catalog.SkillPath(name)
	if err != nil {
		// A catalog that does not hold it is reported as not holding it, rather than as a
		// failure: "removed: false" is the honest answer and it is in the response's contract
		// for exactly this case.
		return connect.NewResponse(&skillv1.DeleteSkillResponse{
			Removed: false,
			Ref:     &skillv1.SkillRef{Catalog: registration.ID, Name: name},
		}), nil
	}
	// The checkout's path is resolved before anything is removed, so a name that does not
	// hold a skill is reported rather than deleting whatever happened to be at that path.
	contents, err := os.ReadDir(path)
	if err != nil {
		return nil, connectFailure(api.WrapError(api.KindInternal, err, "reading %s", path))
	}
	_ = contents
	if err := removeAll(path); err != nil {
		return nil, connectFailure(err)
	}
	// One commit, after the removal: a modification is a change, and a history showing one
	// commit per thing is a history somebody can read.
	if err := s.git.Commit(ctx, s.checkout(registration),
		"Remove the skill "+name+".", s.identity); err != nil {
		return nil, connectFailure(err)
	}
	return connect.NewResponse(&skillv1.DeleteSkillResponse{
		Removed: true,
		Ref:     &skillv1.SkillRef{Catalog: registration.ID, Name: name},
	}), nil
}

// resolve resolves a catalog identifier to a registration and its reader.
func (s *Service) resolve(id string) (Registration, *skills.Directory, error) {
	registration, err := s.registration(id)
	if err != nil {
		return Registration{}, nil, err
	}
	catalog, err := s.directory(registration)
	if err != nil {
		return Registration{}, nil, err
	}
	return registration, catalog, nil
}

func (s *Service) checkout(registration Registration) string {
	return s.registry.Path(registration)
}

// requireWritable refuses a write to a catalog this deployment does not own, with a reason.
//
// A deployment that found a repository and cloned it is serving somebody else's content, and
// committing to it is a decision about their repository rather than about this project. The
// distinction is the registration's `read_only`, which is set when a catalog is registered from
// a remote rather than created here.
func (s *Service) requireWritable(registration Registration) error {
	if !registration.ReadOnly {
		return nil
	}
	return api.Errorf(api.KindDenied,
		"the %s catalog is read-only: it was registered from %s rather than created by this "+
			"deployment, so this deployment will not write to it. A catalog created here can "+
			"be written to, and pushing to a registered one is a separate, deliberate act",
		registration.ID, describeRemote(registration.Remote))
}

// writeSkill lays a whole skill down in a checkout, replacing whatever was there.
//
// The whole skill is written rather than a file at a time, because a copy and a move read one
// skill and write another, and because a whole-skill write is what a git-backed commit is.
func (s *Service) writeSkill(
	registration Registration, request *connect.Request[skillv1.PutSkillRequest],
) error {
	entry := request.Msg.GetEntry()
	files := request.Msg.GetFiles()
	if len(files) == 0 {
		// A whole-skill write with no content is a manifest describing nothing, and a pin
		// nothing describes is worse than no write at all.
		return api.Errorf(api.KindInvalid,
			"a skill being stored must carry its files, including its %s. A manifest with no "+
				"content is a pin nothing describes", skills.SkillFileName)
	}
	name := strings.TrimSpace(entry.GetRef().GetName())
	root := filepath.Join(
		s.checkout(registration),
		filepath.FromSlash(skills.ConventionalSkillDirectories()[0]),
		name,
	)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return api.WrapError(api.KindInternal, err, "creating %s", root)
	}
	// Every file the request carried, written verbatim — including the SKILL.md, so what is
	// stored is exactly what the caller sent rather than a rendering of it. A manifest naming
	// a file nobody wrote would be a pin nothing describes.
	written := make(map[string]bool, len(files))
	for _, file := range files {
		relative := strings.TrimSpace(file.GetPath())
		if relative == "" {
			return api.Errorf(api.KindInvalid, "a file being written has no path")
		}
		if err := writeSkillFile(root, relative, file.GetContent()); err != nil {
			return err
		}
		written[filepath.Clean(filepath.FromSlash(relative))] = true
	}
	// A skill replaced wholesale leaves nothing of the old one behind, because a stale
	// supporting file is content the author removed and this framework would still be
	// serving — and the manifest would not mention it, so no client would be offered it and
	// nobody would know it was there.
	return pruneUnlisted(root, written)
}

// writeSkillFile writes one supporting file, refusing a path that escapes the skill.
//
// A manifest is input like any other, and a path carrying `../` would write outside the
// skill's own directory. The reader validates names; this validates paths, and it validates
// them before anything is created.
func writeSkillFile(root, relative string, content []byte) error {
	full, err := safeJoin(root, relative)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return api.WrapError(api.KindInternal, err, "creating %s", filepath.Dir(full))
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		return api.WrapError(api.KindInternal, err, "writing %s", relative)
	}
	return nil
}

// safeJoin resolves a manifest path inside a skill, refusing one that leaves it.
func safeJoin(root, relative string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", api.Errorf(api.KindInvalid,
			"the manifest path %q leaves the skill's own directory, so it is refused rather "+
				"than written", relative)
	}
	full := filepath.Join(root, cleaned)
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", api.WrapError(api.KindInternal, err, "resolving %s", root)
	}
	resolved, err := filepath.Abs(full)
	if err != nil {
		return "", api.WrapError(api.KindInternal, err, "resolving %s", full)
	}
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return "", api.Errorf(api.KindInvalid,
			"the manifest path %q leaves the skill's own directory, so it is refused rather "+
				"than written", relative)
	}
	return full, nil
}

// pruneUnlisted removes files the new manifest does not list.
//
// A skill replaced wholesale should be exactly what the caller sent. Leaving a supporting file
// the author deleted would be serving content they removed, and the manifest would not mention
// it, so a client would never be offered it and nobody would know it was there.
func pruneUnlisted(root string, listed map[string]bool) error {
	return filepath.WalkDir(root, func(current string, item fs.DirEntry, err error) error {
		if err != nil || item.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		if listed[relative] {
			return nil
		}
		return os.Remove(current)
	})
}

func removeAll(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return api.WrapError(api.KindInternal, err, "removing %s", path)
	}
	return nil
}

func descriptionFor(registration Registration, count int) string {
	where := "a catalog this deployment created"
	if registration.Remote != "" {
		where = "a clone of " + describeRemote(registration.Remote)
	}
	return "Skills from " + where + ", held in a git repository. Each one is a directory with a " +
		"SKILL.md, read the same way a project's own skills are read."
}

// paginate applies a page size and token to a count, returning how many to show and the token
// for what follows.
//
// A token is an offset rather than an opaque handle, because the list is derived from a
// checkout that can change between calls and an opaque handle would resume in the wrong place
// without saying so. A token past the end is not an error: the repository may have shrunk since
// the page was issued, and a caller holding a stale token should get an empty page rather than a
// failure it cannot act on.
func paginate(total int, size int32, token string) (int, string) {
	start := 0
	if token != "" {
		if offset, err := parseOffset(token); err == nil {
			start = offset
		}
	}
	if start > total {
		start = total
	}
	rest := total - start
	if size <= 0 || int(size) >= rest {
		return rest, ""
	}
	return int(size), offsetToken(start + int(size))
}

// connectFailure turns a classified failure into the ConnectRPC error a handler returns.
func connectFailure(err error) error { return api.ConnectError(err) }

// offsetToken and parseOffset are a page token as an offset into an ordered list.
func offsetToken(offset int) string { return "offset:" + strconv.Itoa(offset) }

func parseOffset(token string) (int, error) {
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
