package skillgit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	skillgitv1 "github.com/Manu343726/toolbox/subsystems/skillgit/skillgitv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// These tests drive the real `git` binary against repositories in temporary directories.
//
// A local path stands in for a remote, so nothing here touches the network and nothing depends
// on the machine's privileges or its credentials: what is under test is this provider's
// decisions, and git is the tool it makes them with.

func testGit(t *testing.T) *Git {
	t.Helper()
	git, err := NewGit("")
	if err != nil {
		t.Skipf("git is not available on this machine: %v", err)
	}
	return git
}

// origin lays out a repository with skills and returns its path, to be cloned from.
//
// The skills go under a *conventional* directory rather than at the root, because that is where
// a repository publishes them and the whole point of reading them is finding them where their
// author put them.
func origin(t *testing.T, git *Git, skillsToWrite map[string]string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "origin")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	for relative, content := range skillsToWrite {
		full := filepath.Join(directory, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	require.NoError(t, git.Init(t.Context(), directory, DefaultIdentity()))
	require.NoError(t, git.Commit(t.Context(), directory, "The first commit.", DefaultIdentity()))
	return directory
}

func document(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n\nBody.\n"
}

func service(t *testing.T) *Service {
	t.Helper()
	built, err := NewService(Options{
		DataDir:   t.TempDir(),
		Git:       testGit(t),
		Identity:  Identity{Name: "A Person", Email: "person@example.com"},
		Now:       func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) },
		GitBinary: "git",
	})
	require.NoError(t, err)
	return built
}

// A registration outlives the process: a deployment that forgot its catalogs on reboot would
// serve a different set of skills each time it came up, and a project's `skills:` list would
// then name references that resolve to nothing.
func TestARegistrationOutlivesTheProcess(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md": document("review", "Use this when reviewing."),
	})
	dataDir := t.TempDir()

	first, err := NewService(Options{DataDir: dataDir, Git: git})
	require.NoError(t, err)
	_, err = first.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.NoError(t, err)

	// A second service over the same data directory, as a restart would be.
	second, err := NewService(Options{DataDir: dataDir, Git: git})
	require.NoError(t, err)
	listed, err := second.ListCatalogs(t.Context(), connect.NewRequest(&skillgitv1.ListCatalogsRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetCatalogs(), 1)
	assert.Equal(t, "team", listed.Msg.GetCatalogs()[0].GetId())
	assert.Equal(t, int32(1), listed.Msg.GetCatalogs()[0].GetSkills())
}

// A name means one catalog. Two checkouts of one repository are both allowed under different
// names — which is occasionally what somebody wants — and one name never means two.
func TestANameMeansOneCatalog(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md": document("review", "Use this when reviewing."),
	})
	provider := service(t)

	_, err := provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.NoError(t, err)

	_, err = provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.Error(t, err, "two catalogs must never share one name")
	assert.Equal(t, api.KindAlreadyExists, api.KindOf(err))

	// The same repository under another name is fine.
	_, err = provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team-again", Remote: remote,
	}))
	require.NoError(t, err)
}

// A catalog this deployment found is read-only, and one it created is not. That is the whole
// difference, and it is what makes "push to somebody else's repository" a separate deliberate
// act rather than a side effect of storing a skill.
func TestACatalogThisDeploymentFoundIsReadOnly(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md": document("review", "Use this when reviewing."),
	})
	provider := service(t)
	_, err := provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.NoError(t, err)

	described, err := provider.DescribeCatalog(t.Context(),
		connect.NewRequest(&skillv1.DescribeCatalogRequest{Catalog: "team"}))
	require.NoError(t, err)
	assert.False(t, described.Msg.GetInfo().GetWritable())

	_, err = provider.PutSkill(t.Context(), connect.NewRequest(&skillv1.PutSkillRequest{
		Catalog: "team",
		Entry:   &skillv1.SkillEntry{Ref: &skillv1.SkillRef{Name: "new-one"}},
		Files: []*skillv1.SkillFileContent{
			{Path: skills.SkillFileName, Content: []byte(document("new-one", "Use this."))},
		},
	}))
	require.Error(t, err, "a deployment that found a repository will not commit to it")
	assert.Equal(t, api.KindDenied, api.KindOf(err))
	assert.Contains(t, err.Error(), "separate, deliberate act")
}

// A catalog this deployment created is writable, and a write is one commit.
func TestACatalogThisDeploymentCreatedIsWritable(t *testing.T) {
	provider := service(t)
	created, err := provider.CreateCatalog(t.Context(), connect.NewRequest(&skillgitv1.CreateCatalogRequest{
		Id:   "mine",
		Seed: &skillgitv1.SkillSeed{Name: "review", Description: "Use this when reviewing."},
	}))
	require.NoError(t, err)
	assert.True(t, created.Msg.GetCatalog().GetReadOnly() == false,
		"a catalog created here is one this deployment owns")
	assert.False(t, created.Msg.GetCatalog().GetSyncable(),
		"and it has no remote, so it is served from this machine's copy")

	described, err := provider.DescribeCatalog(t.Context(),
		connect.NewRequest(&skillv1.DescribeCatalogRequest{Catalog: "mine"}))
	require.NoError(t, err)
	assert.True(t, described.Msg.GetInfo().GetWritable())
}

// Creating a catalog is local: no forge is contacted and no token is needed, so a deployment
// can offer git-backed catalogs on a machine with no account anywhere.
func TestCreatingACatalogNeedsNoAccount(t *testing.T) {
	provider := service(t)
	created, err := provider.CreateCatalog(t.Context(), connect.NewRequest(&skillgitv1.CreateCatalogRequest{
		Id:   "local-only",
		Seed: &skillgitv1.SkillSeed{Name: "review", Description: "Use this when reviewing."},
	}))
	require.NoError(t, err)
	assert.Empty(t, created.Msg.GetCatalog().GetRemote())
	assert.False(t, created.Msg.GetCatalog().GetSyncable())

	// And it serves its seeded skill straight away.
	listed, err := provider.ListSkills(t.Context(), connect.NewRequest(&skillv1.ListSkillsRequest{
		Catalog: "local-only",
	}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetSkills(), 1)
	assert.Equal(t, "review", listed.Msg.GetSkills()[0].GetName())
	assert.Equal(t, "Use this when reviewing.", listed.Msg.GetSkills()[0].GetDescription())
}

// Every modification is its own commit, so a history shows one commit per thing.
func TestEveryModificationIsItsOwnCommit(t *testing.T) {
	git := testGit(t)
	provider := service(t)
	_, err := provider.CreateCatalog(t.Context(), connect.NewRequest(&skillgitv1.CreateCatalogRequest{
		Id: "mine", Seed: &skillgitv1.SkillSeed{Name: "first", Description: "The first."},
	}))
	require.NoError(t, err)
	directory := filepath.Join(provider.registry.DataDir(), "mine")

	before, err := git.run(t.Context(), directory, "rev-list", "--count", "HEAD")
	require.NoError(t, err)

	for _, name := range []string{"second", "third"} {
		_, err := provider.PutSkill(t.Context(), connect.NewRequest(&skillv1.PutSkillRequest{
			Catalog: "mine",
			Entry: &skillv1.SkillEntry{
				Ref:         &skillv1.SkillRef{Name: name},
				Frontmatter: structOf(t, map[string]any{"name": name, "description": "Use this."}),
				Body:        "Body.\n",
			},
			Files: []*skillv1.SkillFileContent{
				{Path: skills.SkillFileName, Content: []byte(document(name, "Use this."))},
			},
		}))
		require.NoError(t, err)
	}

	after, err := git.run(t.Context(), directory, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "3", strings.TrimSpace(after),
		"one commit for the catalog and one for each stored skill: %s was %s", after, before)
}

// The commit identity is configured rather than assumed: the person whose repository it is
// should get the last word on whose name is on the commit.
func TestTheCommitIdentityIsTheConfiguredOne(t *testing.T) {
	git := testGit(t)
	provider := service(t)
	_, err := provider.CreateCatalog(t.Context(), connect.NewRequest(&skillgitv1.CreateCatalogRequest{
		Id: "mine", Seed: &skillgitv1.SkillSeed{Name: "review", Description: "Use this."},
	}))
	require.NoError(t, err)
	directory := filepath.Join(provider.registry.DataDir(), "mine")

	author, err := git.run(t.Context(), directory, "log", "-1", "--format=%an <%ae>")
	require.NoError(t, err)
	assert.Contains(t, author, "A Person <person@example.com>",
		"the deployment said whose name goes on a commit, and that is whose name is on it")
}

// A sync that cannot be completed is reported with the repository's path, and nothing is
// resolved: a catalog that resolved its own conflicts would be choosing a side in somebody
// else's repository.
func TestAConflictIsReportedAndNeverResolved(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md": document("review", "The original."),
	})
	provider := service(t)
	_, err := provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.NoError(t, err)
	checkout := filepath.Join(provider.registry.DataDir(), "team")

	// The same file changed on both sides: the remote by a commit, the checkout by an edit.
	require.NoError(t, os.WriteFile(
		filepath.Join(checkout, ".claude", "skills", "review", "SKILL.md"),
		[]byte(document("review", "Changed in the checkout.")), 0o644))
	upstream := filepath.Join(remote, ".claude", "skills", "review", "SKILL.md")
	require.NoError(t, os.WriteFile(upstream,
		[]byte(document("review", "Changed in the remote.")), 0o644))
	require.NoError(t, git.Commit(t.Context(), remote, "A change upstream.", DefaultIdentity()))
	require.NoError(t, git.Commit(t.Context(), checkout, "A change here.", DefaultIdentity()))

	_, err = provider.SyncCatalog(t.Context(), connect.NewRequest(&skillgitv1.SyncCatalogRequest{
		Id: "team",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), checkout,
		"the error names where to look, because the person who can decide what a conflict "+
			"meant is the person who owns the repository")
	assert.Contains(t, err.Error(), "Nothing has been resolved")
}

// Unregistering and deleting are two operations, and nothing is removed without being asked for
// twice.
func TestUnregisteringAndDeletingAreTwoOperations(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md": document("review", "Use this when reviewing."),
	})
	provider := service(t)
	for _, id := range []string{"kept", "gone"} {
		_, err := provider.RegisterCatalog(t.Context(),
			connect.NewRequest(&skillgitv1.RegisterCatalogRequest{Id: id, Remote: remote}))
		require.NoError(t, err)
	}

	t.Run("unregistering leaves the checkout", func(t *testing.T) {
		checkout := filepath.Join(provider.registry.DataDir(), "kept")
		unregistered, err := provider.UnregisterCatalog(t.Context(),
			connect.NewRequest(&skillgitv1.UnregisterCatalogRequest{Id: "kept"}))
		require.NoError(t, err)
		assert.Equal(t, checkout, unregistered.Msg.GetLocation())
		assert.DirExists(t, checkout, "the skills are still readable on disk")

		// Unregistering twice is reported rather than silently succeeding: "disabled" and
		// "was never enabled" are different facts, and reporting the second as the first
		// would tell a person a change happened that did not.
		_, err = provider.UnregisterCatalog(t.Context(),
			connect.NewRequest(&skillgitv1.UnregisterCatalogRequest{Id: "kept"}))
		require.Error(t, err)
		assert.Equal(t, api.KindNotFound, api.KindOf(err))
	})

	t.Run("deleting removes it, and is a separate call", func(t *testing.T) {
		checkout := filepath.Join(provider.registry.DataDir(), "gone")
		removed, err := provider.DeleteCatalog(t.Context(),
			connect.NewRequest(&skillgitv1.DeleteCatalogRequest{Id: "gone"}))
		require.NoError(t, err)
		assert.True(t, removed.Msg.GetRemoved())
		assert.NoDirExists(t, checkout)

		// And it is gone from the record too, rather than left as a registration whose
		// directory is not there.
		_, err = provider.ListSkills(t.Context(),
			connect.NewRequest(&skillv1.ListSkillsRequest{Catalog: "gone"}))
		require.Error(t, err)
	})

	t.Run("a checkout that is already gone is the outcome that was asked for", func(t *testing.T) {
		_, err := provider.RegisterCatalog(t.Context(),
			connect.NewRequest(&skillgitv1.RegisterCatalogRequest{Id: "reused", Remote: remote}))
		require.NoError(t, err)
		require.NoError(t, removeAll(filepath.Join(provider.registry.DataDir(), "reused")))

		removed, err := provider.DeleteCatalog(t.Context(),
			connect.NewRequest(&skillgitv1.DeleteCatalogRequest{Id: "reused"}))
		require.NoError(t, err, "a directory that is already gone is not a failure")
		assert.False(t, removed.Msg.GetRemoved())
	})
}

// A catalog the deployment does not serve is reported with the ones it does, because a person
// who has to fix a project's `skills:` list needs to know what they could name instead.
func TestAnUnknownCatalogNamesTheOnesThatExist(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md": document("review", "Use this when reviewing."),
	})
	provider := service(t)
	_, err := provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.NoError(t, err)

	_, err = provider.ListSkills(t.Context(), connect.NewRequest(&skillv1.ListSkillsRequest{
		Catalog: "nonexistent",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	assert.Contains(t, err.Error(), "team")

	// And a deployment with no catalogs says so rather than naming nothing.
	empty := service(t)
	_, err = empty.ListSkills(t.Context(), connect.NewRequest(&skillv1.ListSkillsRequest{
		Catalog: "team",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no git-backed skill catalogs registered")
}

// A reference is a `<catalog>.<name>`, and the catalog is always spelled — so a skill's identity
// does not depend on which catalog happened to be asked about.
func TestASkillIsServedUnderItsQualifiedName(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md":    document("review", "Use this when reviewing."),
		".claude/skills/review/notes.md":    "# Notes\n",
		".agents/skills/standards/SKILL.md": document("standards", "Use this when writing."),
	})
	provider := service(t)
	_, err := provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.NoError(t, err)

	entry, err := provider.GetSkill(t.Context(), connect.NewRequest(&skillv1.GetSkillRequest{
		Catalog: "team", Name: "review",
	}))
	require.NoError(t, err)
	assert.Equal(t, "skill://team/review/SKILL.md", entry.Msg.GetEntry().GetUri())
	assert.Equal(t, "Use this when reviewing.", entry.Msg.GetEntry().GetRef().GetDescription())
	assert.Len(t, entry.Msg.GetEntry().GetResources(), 2,
		"the manifest is complete: SKILL.md and the supporting file")

	content, err := provider.ReadSkillFile(t.Context(), connect.NewRequest(&skillv1.ReadSkillFileRequest{
		Catalog: "team", Name: "review", Path: "notes.md",
	}))
	require.NoError(t, err)
	assert.Equal(t, "# Notes\n", string(content.Msg.GetContent()))
	assert.Equal(t, int64(len(content.Msg.GetContent())), content.Msg.GetSize())
}

// A query is answered from the catalog's own names and descriptions, so a large repository is
// not shipped over ConnectRPC to be filtered somewhere else.
func TestFindAnswersFromTheCatalogItself(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md":    document("review", "Use this when reviewing a change."),
		".claude/skills/release/SKILL.md":   document("release", "Use this when releasing."),
		".claude/skills/unrelated/SKILL.md": document("unrelated", "Nothing to do with either."),
	})
	provider := service(t)
	_, err := provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.NoError(t, err)

	found, err := provider.FindSkills(t.Context(), connect.NewRequest(&skillv1.FindSkillsRequest{
		Catalog: "team", Query: "review",
	}))
	require.NoError(t, err)
	assert.True(t, found.Msg.GetSearchable())
	require.Len(t, found.Msg.GetSkills(), 1)
	assert.Equal(t, "review", found.Msg.GetSkills()[0].GetName())

	// And by description, which is the field a model actually selects on.
	byDescription, err := provider.FindSkills(t.Context(), connect.NewRequest(&skillv1.FindSkillsRequest{
		Catalog: "team", Query: "releasing",
	}))
	require.NoError(t, err)
	require.Len(t, byDescription.Msg.GetSkills(), 1)
	assert.Equal(t, "release", byDescription.Msg.GetSkills()[0].GetName())
}

// A skill name is one directory's name, and a catalog's name is one segment of a URI. Neither
// can be a path, or a name becomes a way to read or write somewhere else.
func TestNamesAreOneSegmentAndNotAPath(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md": document("review", "Use this when reviewing."),
	})
	provider := service(t)

	for _, id := range []string{"../escape", "a/b", "", ".hidden", "..", "with space"} {
		t.Run("catalog name refused: "+id, func(t *testing.T) {
			_, err := provider.RegisterCatalog(t.Context(),
				connect.NewRequest(&skillgitv1.RegisterCatalogRequest{Id: id, Remote: remote}))
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
		})
	}

	// And a manifest path that leaves the skill's own directory is refused before anything is
	// created, because a manifest is input like any other.
	_, err := provider.CreateCatalog(t.Context(), connect.NewRequest(&skillgitv1.CreateCatalogRequest{
		Id: "mine", Seed: &skillgitv1.SkillSeed{Name: "review", Description: "Use this."},
	}))
	require.NoError(t, err)
	_, err = provider.PutSkill(t.Context(), connect.NewRequest(&skillv1.PutSkillRequest{
		Catalog: "mine",
		Entry:   &skillv1.SkillEntry{Ref: &skillv1.SkillRef{Name: "review"}},
		Files: []*skillv1.SkillFileContent{
			{Path: skills.SkillFileName, Content: []byte(document("review", "Use this."))},
			{Path: "../../escape.md", Content: []byte("Not inside the skill.\n")},
		},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leaves the skill's own directory")
}

// A whole-skill write carries its content. A manifest with no content is a pin nothing
// describes, so it is refused rather than stored.
func TestAWholeSkillWriteCarriesItsContent(t *testing.T) {
	provider := service(t)
	_, err := provider.CreateCatalog(t.Context(), connect.NewRequest(&skillgitv1.CreateCatalogRequest{
		Id: "mine", Seed: &skillgitv1.SkillSeed{Name: "review", Description: "Use this."},
	}))
	require.NoError(t, err)

	_, err = provider.PutSkill(t.Context(), connect.NewRequest(&skillv1.PutSkillRequest{
		Catalog: "mine",
		Entry:   &skillv1.SkillEntry{Ref: &skillv1.SkillRef{Name: "review"}},
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "pin nothing describes")
}

// A skill replaced wholesale is exactly what was sent: a supporting file the author removed is
// gone, because this framework would otherwise go on serving content they deleted.
func TestAReplacedSkillLeavesNothingBehind(t *testing.T) {
	provider := service(t)
	_, err := provider.CreateCatalog(t.Context(), connect.NewRequest(&skillgitv1.CreateCatalogRequest{
		Id: "mine", Seed: &skillgitv1.SkillSeed{Name: "review", Description: "Use this."},
	}))
	require.NoError(t, err)

	write := func(files ...*skillv1.SkillFileContent) {
		_, err := provider.PutSkill(t.Context(), connect.NewRequest(&skillv1.PutSkillRequest{
			Catalog: "mine",
			Entry:   &skillv1.SkillEntry{Ref: &skillv1.SkillRef{Name: "review"}},
			Files:   files,
		}))
		require.NoError(t, err)
	}
	write(
		&skillv1.SkillFileContent{Path: skills.SkillFileName, Content: []byte(document("review", "Use this."))},
		&skillv1.SkillFileContent{Path: "notes.md", Content: []byte("# Notes\n")},
	)

	// The stored entry comes back with both files, computed from what was written.
	entry, err := provider.GetSkill(t.Context(), connect.NewRequest(&skillv1.GetSkillRequest{
		Catalog: "mine", Name: "review",
	}))
	require.NoError(t, err)
	require.Len(t, entry.Msg.GetEntry().GetResources(), 2)

	// Replaced without the notes file.
	write(&skillv1.SkillFileContent{
		Path: skills.SkillFileName, Content: []byte(document("review", "Use this, revised.")),
	})
	entry, err = provider.GetSkill(t.Context(), connect.NewRequest(&skillv1.GetSkillRequest{
		Catalog: "mine", Name: "review",
	}))
	require.NoError(t, err)
	assert.Len(t, entry.Msg.GetEntry().GetResources(), 1,
		"a file the author removed is not still being served")
	assert.Equal(t, skills.SkillFileName, entry.Msg.GetEntry().GetResources()[0].GetPath())
}

// Paging is by offset into an ordered list, because a checkout can change between calls and an
// opaque handle would resume in the wrong place without saying so.
func TestPagingIsByOffsetAndIsTotal(t *testing.T) {
	skills := map[string]string{}
	for _, name := range []string{"a", "b", "c", "d"} {
		skills[".claude/skills/"+name+"/SKILL.md"] = document(name, "Use this.")
	}
	git := testGit(t)
	remote := origin(t, git, skills)
	provider := service(t)
	_, err := provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.NoError(t, err)

	first, err := provider.ListSkills(t.Context(), connect.NewRequest(&skillv1.ListSkillsRequest{
		Catalog: "team", PageSize: 2,
	}))
	require.NoError(t, err)
	require.Len(t, first.Msg.GetSkills(), 2)
	require.NotEmpty(t, first.Msg.GetNextPageToken())

	second, err := provider.ListSkills(t.Context(), connect.NewRequest(&skillv1.ListSkillsRequest{
		Catalog: "team", PageSize: 2, PageToken: first.Msg.GetNextPageToken(),
	}))
	require.NoError(t, err)
	assert.Len(t, second.Msg.GetSkills(), 2)
	assert.Empty(t, second.Msg.GetNextPageToken(), "the last page says so rather than offering an empty one")

	// A stale token past the end is an empty page, not a failure a caller cannot act on.
	past, err := provider.ListSkills(t.Context(), connect.NewRequest(&skillv1.ListSkillsRequest{
		Catalog: "team", PageSize: 2, PageToken: offsetToken(99),
	}))
	require.NoError(t, err)
	assert.Empty(t, past.Msg.GetSkills())
}

// A repository that publishes its skills where this framework does not look is a working
// catalog holding nothing, and a person who registered it is told why rather than left guessing.
func TestARepositoryWithNoRecognisableSkillsIsReportedNotFailed(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		"README.md": "# A repository with no skills in it.\n",
	})
	provider := service(t)

	registered, err := provider.RegisterCatalog(t.Context(),
		connect.NewRequest(&skillgitv1.RegisterCatalogRequest{Id: "empty", Remote: remote}))
	require.NoError(t, err, "the clone succeeded and the catalog is registered")
	assert.Equal(t, int32(0), registered.Msg.GetSkills())
	assert.NotEmpty(t, registered.Msg.GetCatalog().GetNote())
	assert.Contains(t, registered.Msg.GetCatalog().GetNote(), "no skills this framework can see")
}

// A remote may carry a token in its URL, and a catalog's location is shown to a person
// deciding whether to depend on the content.
func TestACredentialIsNotShownInACatalogsLocation(t *testing.T) {
	for _, named := range []string{
		"https://token:secret@github.com/owner/repo.git",
		"https://secret@github.com/owner/repo.git",
		"git@github.com:owner/repo.git",
	} {
		t.Run(named, func(t *testing.T) {
			shown := describeRemote(RemoteURL(named))
			assert.NotContains(t, shown, "secret", "a credential is not something to show a person")
			assert.NotContains(t, shown, "token:")
		})
	}
}

// The `owner/repo` shorthand resolves to GitHub, because the public directory is an index over
// skills that live in repositories rather than a host they are served from — and pretending
// there were an endpoint to fetch from would be a claim that does not hold.
func TestTheShorthandResolvesToGitHub(t *testing.T) {
	assert.Equal(t, "https://github.com/vercel-labs/skills.git",
		RemoteURL("vercel-labs/skills"))
	assert.Equal(t, "https://github.com/vercel-labs/skills.git",
		RemoteURL("vercel-labs/skills/"))

	// A name that is already something git can use is handed over untouched, because a
	// deployment may clone from a host this framework has never heard of.
	for _, named := range []string{
		"https://example.com/owner/repo.git",
		"git@example.com:owner/repo.git",
		"ssh://git@example.com/owner/repo.git",
		"/srv/local/repo",
		"./relative/repo",
	} {
		assert.Equal(t, named, RemoteURL(named), "%q is already a remote", named)
	}
}

// A checkout is re-read when its files change and not when only their timestamps move, because
// a `git pull` changes the time without changing the content — and a sync that brought in new
// content must be read rather than served from what was there before.
func TestASyncBringsInWhatTheRemoteHas(t *testing.T) {
	git := testGit(t)
	remote := origin(t, git, map[string]string{
		".claude/skills/review/SKILL.md": document("review", "The original."),
	})
	provider := service(t)
	_, err := provider.RegisterCatalog(t.Context(), connect.NewRequest(&skillgitv1.RegisterCatalogRequest{
		Id: "team", Remote: remote,
	}))
	require.NoError(t, err)

	// A second skill lands in the remote.
	require.NoError(t, os.MkdirAll(
		filepath.Join(remote, ".claude", "skills", "added"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(remote, ".claude", "skills", "added", "SKILL.md"),
		[]byte(document("added", "Added upstream.")), 0o644))
	require.NoError(t, git.Commit(t.Context(), remote, "A skill upstream.", DefaultIdentity()))

	synced, err := provider.SyncCatalog(t.Context(), connect.NewRequest(&skillgitv1.SyncCatalogRequest{
		Id: "team",
	}))
	require.NoError(t, err)
	assert.True(t, synced.Msg.GetChanged())
	assert.Equal(t, int32(2), synced.Msg.GetCatalog().GetSkills())

	listed, err := provider.ListSkills(t.Context(), connect.NewRequest(&skillv1.ListSkillsRequest{
		Catalog: "team",
	}))
	require.NoError(t, err)
	assert.Len(t, listed.Msg.GetSkills(), 2, "a sync that brought in content is read, not served stale")
}

// A catalog created locally has no remote to sync from, and says so rather than failing in git's
// words.
func TestALocalCatalogHasNoRemoteToSyncFrom(t *testing.T) {
	provider := service(t)
	_, err := provider.CreateCatalog(t.Context(), connect.NewRequest(&skillgitv1.CreateCatalogRequest{
		Id: "mine", Seed: &skillgitv1.SkillSeed{Name: "review", Description: "Use this."},
	}))
	require.NoError(t, err)

	_, err = provider.SyncCatalog(t.Context(), connect.NewRequest(&skillgitv1.SyncCatalogRequest{
		Id: "mine",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindFailedPrecondition, api.KindOf(err))
	assert.Contains(t, err.Error(), "no remote to sync from")
}

// A deployment that cannot find git says so at construction, rather than a user discovering it
// when a tool refuses to register a catalog.
func TestAGitThatIsNotThereIsRefusedAtConstruction(t *testing.T) {
	_, err := NewService(Options{DataDir: t.TempDir(), GitBinary: "git-that-is-not-installed"})
	require.Error(t, err)
	assert.Equal(t, api.KindFailedPrecondition, api.KindOf(err))
	assert.Contains(t, err.Error(), "unaffected",
		"a deployment serving only its own project's skills does not need git at all")
}

// A provider needs somewhere to keep its checkouts, and that is its own storage rather than a
// project's.
func TestAProviderNeedsADataDirectory(t *testing.T) {
	_, err := NewService(Options{})
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "outside any project")
}

// structOf converts a frontmatter document for a request the provider reads back.
func structOf(t *testing.T, document map[string]any) *structpb.Struct {
	t.Helper()
	converted, err := structpb.NewStruct(document)
	require.NoError(t, err)
	return converted
}
