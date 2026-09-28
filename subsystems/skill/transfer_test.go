package skill

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	skillv1skill "github.com/Manu343726/toolbox/subsystems/skill/skillv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A copy and a move are the only operations in the subsystem that touch two catalogs, so the
// tests need two — and they need the second one to be read-only, because refusing a read-only
// end is the condition that distinguishes a move from a copy and is the one that cannot be
// checked by reading the code.
//
// The catalogs here are a real implementation of the contract over a map rather than a stub that
// records calls. A stub would let a test pass while the service wrote a file the catalog would
// not have accepted, and the whole of what copy and move do is hand bytes to a catalog and trust
// what it says afterwards.

// memoryCatalog is one catalog held in a map: a name, a set of files, and whether it will accept
// a write.
type memoryCatalog struct {
	id       string
	writable bool
	// refuseRemoval makes a write succeed and a removal fail, which is the state a move that
	// fails halfway produces. It is separate from `writable` because a catalog that will not
	// accept a removal is refused *before* anything is written, so the only way to reach the
	// halfway state is to fail after the fact.
	refuseRemoval bool

	mu     sync.Mutex
	skills map[string]map[string][]byte
}

// put stores a skill's files directly, which is how a test seeds one.
func (m *memoryCatalog) put(name string, files map[string][]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make(map[string][]byte, len(files))
	for path, content := range files {
		copied[path] = content
	}
	m.skills[name] = copied
}

// has reports whether the catalog holds a skill.
func (m *memoryCatalog) has(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.skills[name]
	return ok
}

// manifest computes the manifest from the bytes rather than carrying one, so a test cannot
// assert agreement with a manifest it wrote by hand and got wrong.
func (m *memoryCatalog) manifest(name string) []*skillv1.SkillFile {
	m.mu.Lock()
	defer m.mu.Unlock()
	files, ok := m.skills[name]
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	manifest := make([]*skillv1.SkillFile, 0, len(paths))
	for _, path := range paths {
		file := skills.NewFile(path, files[path])
		manifest = append(manifest, &skillv1.SkillFile{
			Path:     file.Path,
			Size:     file.Size,
			Digest:   file.Digest,
			MimeType: skills.MIMETypeFor(path),
		})
	}
	return manifest
}

// entry renders a stored skill as the contract's entry, the way a catalog would.
func (m *memoryCatalog) entry(name string) (*skillv1.SkillEntry, error) {
	m.mu.Lock()
	files, ok := m.skills[name]
	m.mu.Unlock()
	if !ok {
		return nil, api.Errorf(api.KindNotFound, "the %s catalog holds no skill named %q", m.id, name)
	}
	parsed, err := skills.Parse(m.id, name, files[skills.SkillFileName], nil)
	if err != nil {
		return nil, api.WrapError(api.KindInvalid, err,
			"the %s catalog holds a skill named %q whose %s this framework cannot read",
			m.id, name, skills.SkillFileName)
	}
	return &skillv1.SkillEntry{
		Ref: &skillv1.SkillRef{
			Catalog: m.id, Name: name, Description: parsed.Frontmatter.Skill.Description,
		},
		Uri:         skills.SkillURI(m.id, name),
		Frontmatter: structOf(parsed.Frontmatter.Document),
		Body:        parsed.Body,
		Resources:   m.manifest(name),
	}, nil
}

// memoryDirectory is the deployment's catalog directory over a set of in-memory catalogs.
type memoryDirectory struct {
	catalogs map[string]*memoryCatalog
}

func (d *memoryDirectory) Catalogs(context.Context) ([]Catalog, error) {
	ids := make([]string, 0, len(d.catalogs))
	for id := range d.catalogs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	list := make([]Catalog, 0, len(ids))
	for _, id := range ids {
		list = append(list, Catalog{
			Info: &skillv1.CatalogInfo{
				Id:       id,
				Name:     id,
				Writable: d.catalogs[id].writable,
				Location: "memory",
			},
			ProviderID: "memory",
		})
	}
	return list, nil
}

func (d *memoryDirectory) Catalog(_ context.Context, id string) (CatalogClient, error) {
	catalog, ok := d.catalogs[id]
	if !ok {
		return nil, api.Errorf(api.KindNotFound, "this deployment has no %q catalog", id)
	}
	return &memoryClient{catalog: catalog}, nil
}

// memoryClient is one catalog's client. Only the methods a copy and a move use are implemented;
// the rest of the contract is not needed to test these two operations, and a method that
// panics says so rather than returning something plausible.
type memoryClient struct {
	catalog *memoryCatalog
	// liesAbout serves bytes that do not match the manifest, which is the one state a
	// verifying copy has to notice.
	liesAbout bool
}

func (c *memoryClient) DescribeCatalog(
	_ context.Context, request *connect.Request[skillv1.DescribeCatalogRequest],
) (*connect.Response[skillv1.DescribeCatalogResponse], error) {
	return connect.NewResponse(&skillv1.DescribeCatalogResponse{
		Info: &skillv1.CatalogInfo{
			Id:       c.catalog.id,
			Writable: c.catalog.writable,
		},
	}), nil
}

func (c *memoryClient) GetSkill(
	_ context.Context, request *connect.Request[skillv1.GetSkillRequest],
) (*connect.Response[skillv1.GetSkillResponse], error) {
	entry, err := c.catalog.entry(request.Msg.GetName())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&skillv1.GetSkillResponse{Entry: entry}), nil
}

func (c *memoryClient) ReadSkillFile(
	_ context.Context, request *connect.Request[skillv1.ReadSkillFileRequest],
) (*connect.Response[skillv1.ReadSkillFileResponse], error) {
	c.catalog.mu.Lock()
	files, ok := c.catalog.skills[request.Msg.GetName()]
	held, wanted := files[request.Msg.GetPath()]
	c.catalog.mu.Unlock()
	if !ok {
		return nil, api.Errorf(api.KindNotFound,
			"the %s catalog holds no skill named %q", c.catalog.id, request.Msg.GetName())
	}
	if !wanted {
		return nil, api.Errorf(api.KindNotFound,
			"the skill %q has no file at %q", request.Msg.GetName(), request.Msg.GetPath())
	}
	content := held
	if c.liesAbout && request.Msg.GetPath() == skills.SkillFileName {
		content = []byte("---\nname: something-else\ndescription: Not what the manifest said.\n---\n\n# Other\n")
	}
	manifest := c.catalog.manifest(request.Msg.GetName())
	for _, file := range manifest {
		if file.GetPath() == request.Msg.GetPath() {
			return connect.NewResponse(&skillv1.ReadSkillFileResponse{
				Path:     file.GetPath(),
				Content:  content,
				Digest:   file.GetDigest(),
				Size:     file.GetSize(),
				MimeType: file.GetMimeType(),
			}), nil
		}
	}
	return nil, api.Errorf(api.KindNotFound, "no manifest entry for %q", request.Msg.GetPath())
}

func (c *memoryClient) PutSkill(
	_ context.Context, request *connect.Request[skillv1.PutSkillRequest],
) (*connect.Response[skillv1.PutSkillResponse], error) {
	if !c.catalog.writable {
		return nil, api.Errorf(api.KindDenied, "the %s catalog is read-only", c.catalog.id)
	}
	name := strings.TrimSpace(request.Msg.GetEntry().GetRef().GetName())
	if name == "" {
		// The contract puts the name on the entry, and a catalog cannot infer it from
		// the files: they say what a skill contains, not what to call it.
		return nil, api.Errorf(api.KindInvalid,
			"the %s catalog was asked to store a skill with no name", c.catalog.id)
	}
	if request.Msg.GetFailIfExists() && c.catalog.has(name) {
		return nil, api.Errorf(api.KindAlreadyExists,
			"the %s catalog already holds a skill named %q", c.catalog.id, name)
	}
	files := make(map[string][]byte, len(request.Msg.GetFiles()))
	for _, file := range request.Msg.GetFiles() {
		files[file.GetPath()] = file.GetContent()
	}
	c.catalog.put(name, files)
	entry, err := c.catalog.entry(name)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&skillv1.PutSkillResponse{Entry: entry}), nil
}

func (c *memoryClient) DeleteSkill(
	_ context.Context, request *connect.Request[skillv1.DeleteSkillRequest],
) (*connect.Response[skillv1.DeleteSkillResponse], error) {
	if !c.catalog.writable {
		return nil, api.Errorf(api.KindDenied, "the %s catalog is read-only", c.catalog.id)
	}
	if c.catalog.refuseRemoval {
		return nil, api.Errorf(api.KindInternal,
			"the checkout of the %s catalog could not be committed", c.catalog.id)
	}
	ref := &skillv1.SkillRef{Catalog: c.catalog.id, Name: request.Msg.GetName()}
	c.catalog.mu.Lock()
	_, ok := c.catalog.skills[request.Msg.GetName()]
	delete(c.catalog.skills, request.Msg.GetName())
	c.catalog.mu.Unlock()
	return connect.NewResponse(&skillv1.DeleteSkillResponse{Removed: ok, Ref: ref}), nil
}

// The rest of the catalog contract, unimplemented on purpose.
func (c *memoryClient) ListCatalogs(context.Context, *connect.Request[skillv1.ListCatalogsRequest],
) (*connect.Response[skillv1.ListCatalogsResponse], error) {
	panic("a copy and a move do not list catalogs")
}

func (c *memoryClient) ListSkills(context.Context, *connect.Request[skillv1.ListSkillsRequest],
) (*connect.Response[skillv1.ListSkillsResponse], error) {
	panic("a copy and a move do not list skills")
}

func (c *memoryClient) FindSkills(context.Context, *connect.Request[skillv1.FindSkillsRequest],
) (*connect.Response[skillv1.FindSkillsResponse], error) {
	panic("a copy and a move do not search")
}

// standards is a skill with a SKILL.md and one supporting file, so a copy carries something a
// single-file skill would not.
func standards(name string) map[string][]byte {
	frontmatter := fmt.Sprintf(
		"---\nname: %s\ndescription: Use this when writing code here.\n---\n\n# %s\n\nFollow them.\n",
		name, name)
	return map[string][]byte{
		skills.SkillFileName:  []byte(frontmatter),
		"references/style.md": []byte("# Style\n\nTwo spaces.\n"),
	}
}

// twoCatalogs returns a service over two catalogs: one writable, one not.
func twoCatalogs(t *testing.T) (*Service, *memoryCatalog, *memoryCatalog) {
	t.Helper()
	writable := &memoryCatalog{id: "ours", writable: true, skills: map[string]map[string][]byte{}}
	shared := &memoryCatalog{id: "theirs", writable: false, skills: map[string]map[string][]byte{}}
	shared.put("vendor-skill", standards("vendor-skill"))
	service, err := NewService(Options{
		Directory: &memoryDirectory{catalogs: map[string]*memoryCatalog{
			"ours": writable, "theirs": shared,
		}},
	})
	require.NoError(t, err)
	return service, writable, shared
}

// A copy is the whole point of the operation: the skill arrives in the other catalog, byte for
// byte, and the source is untouched — which is what distinguishes it from a move.
func TestACopyCarriesEveryFileAndLeavesTheSourceAlone(t *testing.T) {
	service, ours, theirs := twoCatalogs(t)

	copied, err := service.CopySkill(t.Context(), connect.NewRequest(&skillv1skill.CopySkillRequest{
		SourceCatalog: "theirs",
		SourceName:    "vendor-skill",
		TargetCatalog: "ours",
		TargetName:    "vendor-skill",
	}))
	require.NoError(t, err)

	assert.True(t, ours.has("vendor-skill"), "the target holds the skill")
	assert.True(t, theirs.has("vendor-skill"), "and the source still does, because it was a copy")
	assert.Equal(t, "theirs", copied.Msg.GetSource().GetCatalog())
	assert.Equal(t, "vendor-skill", copied.Msg.GetSource().GetName())

	// Every file, and the bytes identical — a copy that carried the SKILL.md and dropped the
	// supporting file would be a skill whose manifest lied about its own contents.
	require.Len(t, ours.skills["vendor-skill"], 2)
	for path, content := range theirs.skills["vendor-skill"] {
		assert.Equal(t, content, ours.skills["vendor-skill"][path],
			"file %s is byte for byte what the source held", path)
	}

	// The manifest is the target's, computed from what the target was given rather than copied
	// from the source: a catalog recording a claim about content it never saw is a pin nothing
	// describes.
	entry := copied.Msg.GetEntry()
	require.Len(t, entry.GetResources(), 2)
	for _, file := range entry.GetResources() {
		assert.Equal(t, skills.NewFile(file.GetPath(), ours.skills["vendor-skill"][file.GetPath()]).Digest,
			file.GetDigest(), "the manifest for %s describes what the target holds", file.GetPath())
	}
}

// A rename is the move a deployment is most likely to want, and it is the case where the two
// catalogs are the same one — so it has to work rather than be refused as a self-move.
func TestAMoveWithinOneCatalogIsARename(t *testing.T) {
	service, ours, _ := twoCatalogs(t)
	ours.put("before", standards("before"))

	moved, err := service.MoveSkill(t.Context(), connect.NewRequest(&skillv1skill.MoveSkillRequest{
		SourceCatalog: "ours",
		SourceName:    "before",
		TargetCatalog: "ours",
		TargetName:    "after",
	}))
	require.NoError(t, err)

	assert.True(t, ours.has("after"), "the skill is under its new name")
	assert.False(t, ours.has("before"), "and not under the old one")
	assert.True(t, moved.Msg.GetSourceRemoved(),
		"a move that cleared its source says so, so a caller is not left guessing")
	require.Len(t, ours.skills["after"], 2)
}

// The two refusals are what make a move a move, and they are checked before anything is
// fetched — so a move that cannot happen costs a caller nothing.
func TestAMoveIsRefusedForACatalogThatWillNotAcceptIt(t *testing.T) {
	t.Run("a read-only target, refused before the source is read", func(t *testing.T) {
		service, ours, _ := twoCatalogs(t)
		ours.put("source", standards("source"))

		_, err := service.MoveSkill(t.Context(), connect.NewRequest(&skillv1skill.MoveSkillRequest{
			SourceCatalog: "ours", SourceName: "source",
			TargetCatalog: "theirs", TargetName: "source",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read-only")
		assert.Contains(t, err.Error(), "moved into", "and says which end refused what")
		assert.True(t, ours.has("source"), "so nothing was taken out of the source")
	})

	t.Run("a read-only source, which is its own condition", func(t *testing.T) {
		service, ours, _ := twoCatalogs(t)

		_, err := service.MoveSkill(t.Context(), connect.NewRequest(&skillv1skill.MoveSkillRequest{
			SourceCatalog: "theirs", SourceName: "vendor-skill",
			TargetCatalog: "ours", TargetName: "vendor-skill",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "moved out of",
			"a move needs somewhere to remove the skill from, and that is not an inference "+
				"from copying: it is the condition a copy does not have")
		assert.False(t, ours.has("vendor-skill"), "and nothing was written either")
	})
}

// A move that fails after the write has duplicated rather than moved, and the caller is told so
// rather than being handed a failure it would read as "nothing happened".
func TestAMoveThatCannotClearItsSourceReportsTheDuplicate(t *testing.T) {
	source := &memoryCatalog{id: "ours", writable: true, skills: map[string]map[string][]byte{}}
	source.refuseRemoval = true
	target := &memoryCatalog{id: "spare", writable: true, skills: map[string]map[string][]byte{}}
	source.put("skill", standards("skill"))
	service, err := NewService(Options{Directory: &memoryDirectory{catalogs: map[string]*memoryCatalog{
		"ours": source, "spare": target,
	}}})
	require.NoError(t, err)

	moved, err := service.MoveSkill(t.Context(), connect.NewRequest(&skillv1skill.MoveSkillRequest{
		SourceCatalog: "ours", SourceName: "skill",
		TargetCatalog: "spare", TargetName: "skill",
	}))
	require.NoError(t, err, "the write worked, so this is not a failure of the call")

	assert.False(t, moved.Msg.GetSourceRemoved())
	assert.NotEmpty(t, moved.Msg.GetSourceProblem(),
		"and the caller is told what to do about the duplicate rather than left to find it")
	// Both hold it, which is the state being reported. A caller told "the move failed" would
	// look in the source, find the skill, and conclude something worse than a duplicate.
	assert.True(t, target.has("skill"))
	assert.True(t, source.has("skill"))
}

// A copy over an existing name is a decision the caller states, because the alternative
// destroys a version somebody may have pinned.
func TestACopyStatesWhetherItMayOverwrite(t *testing.T) {
	service, ours, _ := twoCatalogs(t)
	ours.put("target", standards("target"))
	ours.put("other", standards("other"))

	t.Run("refused when the caller says not to replace", func(t *testing.T) {
		_, err := service.CopySkill(t.Context(), connect.NewRequest(&skillv1skill.CopySkillRequest{
			SourceCatalog: "theirs", SourceName: "vendor-skill",
			TargetCatalog: "ours", TargetName: "target", FailIfExists: true,
		}))
		require.Error(t, err)
		assert.Contains(t, strings.ToLower(err.Error()), "already holds")
		assert.Equal(t, standards("target"), ours.skills["target"],
			"so the version already there is untouched")
	})

	t.Run("and replaces when the caller says it may", func(t *testing.T) {
		_, err := service.CopySkill(t.Context(), connect.NewRequest(&skillv1skill.CopySkillRequest{
			SourceCatalog: "theirs", SourceName: "vendor-skill",
			TargetCatalog: "ours", TargetName: "target",
		}))
		require.NoError(t, err)
		assert.Equal(t, standards("vendor-skill"), ours.skills["target"])
	})
}

// A move never replaces. A copy may be asked to, because the caller is choosing between two
// versions it can see; a move has no such choice, and the one thing it is for is that the skill
// is not in two places.
func TestAMoveNeverOverwritesTheTarget(t *testing.T) {
	service, ours, _ := twoCatalogs(t)
	ours.put("target", standards("target"))
	ours.put("source", standards("source"))

	_, err := service.MoveSkill(t.Context(), connect.NewRequest(&skillv1skill.MoveSkillRequest{
		SourceCatalog: "ours", SourceName: "source",
		TargetCatalog: "ours", TargetName: "target",
	}))
	require.Error(t, err)
	assert.Equal(t, standards("target"), ours.skills["target"],
		"the version already in the target is what is there afterwards")
	assert.True(t, ours.has("source"), "and the source still has its skill, because nothing was lost")
}

// A copy that carried bytes disagreeing with the manifest would write a skill whose pin
// describes something else, into a catalog where the mismatch is no longer visible.
func TestACopyRefusesACatalogWhoseContentContradictsItsManifest(t *testing.T) {
	ours := &memoryCatalog{id: "ours", writable: true, skills: map[string]map[string][]byte{}}
	liar := &memoryCatalog{id: "liar", writable: true, skills: map[string]map[string][]byte{}}
	liar.put("skill", standards("skill"))
	service, err := NewService(Options{Directory: &liarDirectory{ours: ours, liar: liar}})
	require.NoError(t, err)

	_, err = service.CopySkill(t.Context(), connect.NewRequest(&skillv1skill.CopySkillRequest{
		SourceCatalog: "liar", SourceName: "skill",
		TargetCatalog: "ours", TargetName: "skill",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest",
		"the message has to name the disagreement, because a bare refusal tells a caller nothing")
	assert.False(t, ours.has("skill"), "and nothing was written")
}

// liarDirectory serves one catalog whose bytes do not match the manifest it publishes.
type liarDirectory struct {
	ours *memoryCatalog
	liar *memoryCatalog
}

func (d *liarDirectory) Catalogs(context.Context) ([]Catalog, error) {
	return []Catalog{
		{Info: &skillv1.CatalogInfo{Id: "ours", Writable: true}},
		{Info: &skillv1.CatalogInfo{Id: "liar", Writable: true}},
	}, nil
}

func (d *liarDirectory) Catalog(_ context.Context, id string) (CatalogClient, error) {
	switch id {
	case "ours":
		return &memoryClient{catalog: d.ours}, nil
	case "liar":
		return &memoryClient{catalog: d.liar, liesAbout: true}, nil
	}
	return nil, api.Errorf(api.KindNotFound, "no %q catalog", id)
}

// A name is required on both ends, and the target's is not defaulted to the source's: a call
// that collides must fail on a collision the caller asked for.
func TestBothEndsOfATransferAreNamed(t *testing.T) {
	service, ours, _ := twoCatalogs(t)
	ours.put("source", standards("source"))

	for _, request := range []*skillv1skill.CopySkillRequest{
		{SourceCatalog: "ours", SourceName: "source", TargetCatalog: "ours"},
		{SourceCatalog: "ours", TargetCatalog: "ours", TargetName: "source"},
		{TargetCatalog: "ours", TargetName: "source"},
	} {
		_, err := service.CopySkill(t.Context(), connect.NewRequest(request))
		require.Error(t, err, "request %v", request)
		assert.Contains(t, err.Error(), "needs a name")
	}

	// A catalog that is not named is a different mistake with a different message, because a
	// caller who left a catalog blank and a caller who left a name blank have different fixes.
	_, err := service.CopySkill(t.Context(), connect.NewRequest(&skillv1skill.CopySkillRequest{
		SourceCatalog: "ours", SourceName: "source", TargetName: "source",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "catalog")
}
