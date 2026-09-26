package skills_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tree lays out a directory from slash-separated relative paths to contents.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for relative, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

func skillDocument(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n\nBody.\n"
}

// A directory of skills is a read-only catalog, and it is the same reading a project's own
// skills and a checkout get — which is what stops a manifest meaning one thing in one place and
// another in the other.
func TestADirectoryIsAReadOnlyCatalog(t *testing.T) {
	root := tree(t, map[string]string{
		"code-review/SKILL.md":   skillDocument("code-review", "Use this when reviewing."),
		"release-notes/SKILL.md": skillDocument("release-notes", "Use this when releasing."),
		"notes.md":               "# Not a skill: no directory, no document.\n",
	})
	catalog, err := skills.NewDirectory(skills.DirectoryOptions{ID: "some-team", Root: root})
	require.NoError(t, err)

	assert.Equal(t, "some-team", catalog.ID())
	assert.False(t, catalog.Writable(),
		"the files belong to whoever put them there, and a framework that wrote into a "+
			"repository would be writing into somebody else's")
	assert.Equal(t, root, catalog.Location(),
		"where the bytes came from is the one fact a person deciding to depend on this needs")

	names, err := catalog.Names()
	require.NoError(t, err)
	assert.Equal(t, []string{"code-review", "release-notes"}, names,
		"ordered, and a file rather than a directory is not a skill")
	assert.True(t, catalog.Has("code-review"))
	assert.False(t, catalog.Has("notes"))

	template, err := catalog.Template("code-review")
	require.NoError(t, err)
	skill, err := template.Skill()
	require.NoError(t, err)
	assert.Equal(t, "Use this when reviewing.", skill.Description)
}

// A repository publishes its skills under whichever convention its own clients read, and a
// source that only looked at one place would find none of them. The list is the ecosystem's,
// read from its installer rather than invented.
func TestARepositorysSkillsAreFoundWhereverItPutsThem(t *testing.T) {
	root := tree(t, map[string]string{
		".claude/skills/from-claude/SKILL.md":     skillDocument("from-claude", "Claude Code's."),
		".agents/skills/from-agents/SKILL.md":     skillDocument("from-agents", "The standard's."),
		".opencode/skills/from-opencode/SKILL.md": skillDocument("from-opencode", "opencode's."),
		// A directory under a convention with no SKILL.md is not a skill, and a file named
		// SKILL.md at the convention's root is not one either — a skill is a directory with
		// a document in it.
		".claude/skills/README.md": "Not a skill.\n",
		".github/workflows/ci.yml": "name: ci\n",
	})
	catalog, err := skills.NewDirectory(skills.DirectoryOptions{
		ID:             "skills.sh",
		Root:           root,
		Subdirectories: skills.ConventionalSkillDirectories(),
	})
	require.NoError(t, err)

	names, err := catalog.Names()
	require.NoError(t, err)
	assert.Equal(t, []string{"from-agents", "from-claude", "from-opencode"}, names)

	t.Run("the conventional list is the ecosystem's", func(t *testing.T) {
		conventions := skills.ConventionalSkillDirectories()
		assert.Contains(t, conventions, ".claude/skills")
		assert.Contains(t, conventions, ".agents/skills")
		assert.Contains(t, conventions, ".opencode/skills")
		assert.Contains(t, conventions, ".codex/skills")
		assert.Greater(t, len(conventions), 10,
			"a list of two would be this framework's own convention wearing the ecosystem's name")
	})
}

// A convention the repository does not use is not an error. A deployment carries thirty of them
// and a repository uses one, so reporting an error for the twenty-nine that were skipped would
// make every repository look broken.
func TestAConventionTheRepositoryDoesNotUseIsNotAnError(t *testing.T) {
	root := tree(t, map[string]string{
		".claude/skills/only-one/SKILL.md": skillDocument("only-one", "Use this."),
	})
	catalog, err := skills.NewDirectory(skills.DirectoryOptions{
		ID: "some-team", Root: root, Subdirectories: skills.ConventionalSkillDirectories(),
	})
	require.NoError(t, err)

	names, err := catalog.Names()
	require.NoError(t, err)
	assert.Equal(t, []string{"only-one"}, names)
}

// A source that has no directory is an empty catalog, not a failure: a project that has not
// added a skill offers nothing rather than being broken.
func TestAnAbsentDirectoryIsAnEmptyCatalog(t *testing.T) {
	catalog, err := skills.NewDirectory(skills.DirectoryOptions{
		ID: "some-team", Root: filepath.Join(t.TempDir(), "not-there"),
	})
	require.NoError(t, err)

	names, err := catalog.Names()
	require.NoError(t, err)
	assert.Empty(t, names)

	_, err = catalog.Template("anything")
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))

	t.Run("and so is no directory at all", func(t *testing.T) {
		empty, err := skills.NewDirectory(skills.DirectoryOptions{ID: "some-team"})
		require.NoError(t, err)
		names, err := empty.Names()
		require.NoError(t, err)
		assert.Empty(t, names)
	})
}

// A skill's name is one directory's name, and a name carrying a separator is a path somebody
// would like this to read. The reader validates names; this is the boundary that keeps a name
// from being one.
func TestASkillNameIsOneDirectorysName(t *testing.T) {
	root := tree(t, map[string]string{
		"code-review/SKILL.md": skillDocument("code-review", "Use this when reviewing."),
	})
	catalog, err := skills.NewDirectory(skills.DirectoryOptions{ID: "some-team", Root: root})
	require.NoError(t, err)

	for _, name := range []string{"../../etc", "a/b", "", ".", "..", `a\b`} {
		t.Run("refused: "+name, func(t *testing.T) {
			_, err := catalog.SkillPath(name)
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
		})
	}
}

// A file a skill does not have is reported with what it does have, because a message listing
// nothing is not a message.
func TestAFileTheSkillDoesNotHaveNamesWhatItDoes(t *testing.T) {
	root := tree(t, map[string]string{
		"code-review/SKILL.md":          skillDocument("code-review", "Use this when reviewing."),
		"code-review/references/one.md": "# One\n",
	})
	catalog, err := skills.NewDirectory(skills.DirectoryOptions{ID: "some-team", Root: root})
	require.NoError(t, err)

	_, _, err = catalog.File("code-review", "references/absent.md")
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	assert.Contains(t, err.Error(), "references/one.md")
	assert.Contains(t, err.Error(), "SKILL.md")
}

// The digest a file is served with comes from the manifest and the bytes come from the disk,
// and the two are returned side by side rather than one derived from the other.
func TestAFileComesWithItsManifestsClaimAndItsActualBytes(t *testing.T) {
	document := skillDocument("code-review", "Use this when reviewing.")
	root := tree(t, map[string]string{
		"code-review/SKILL.md":          document,
		"code-review/references/one.md": "# One\n",
	})
	catalog, err := skills.NewDirectory(skills.DirectoryOptions{ID: "some-team", Root: root})
	require.NoError(t, err)

	content, entry, err := catalog.File("code-review", "references/one.md")
	require.NoError(t, err)
	assert.Equal(t, "# One\n", string(content))
	assert.Equal(t, int64(len(content)), entry.Size)
	assert.Equal(t, skills.NewFile("references/one.md", content).Digest, entry.Digest,
		"the manifest's claim describes the bytes, because it was computed from them")
	assert.Equal(t, "text/markdown", entry.MIMEType)
}

// A cached read is re-read when the skill's files change, and not when only their timestamps
// move — a checkout and a `git pull` change the time without changing the content, and a
// modification time is a fact about the machine rather than about the skill.
func TestACachedReadFollowsTheContentAndNotTheClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "code-review")
	require.NoError(t, os.MkdirAll(path, 0o755))
	document := filepath.Join(path, "SKILL.md")
	require.NoError(t, os.WriteFile(document, []byte(skillDocument("code-review", "First.")), 0o644))

	catalog, err := skills.NewDirectory(skills.DirectoryOptions{
		ID: "local", Root: filepath.Dir(path),
	})
	require.NoError(t, err)

	first, err := catalog.Cached("code-review")
	require.NoError(t, err)
	firstSkill, err := first.Skill()
	require.NoError(t, err)
	assert.Equal(t, "First.", firstSkill.Description)

	// The same read again is the same template, served from the cache.
	again, err := catalog.Cached("code-review")
	require.NoError(t, err)
	assert.Equal(t, first.Files, again.Files)

	t.Run("a changed file is re-read", func(t *testing.T) {
		require.NoError(t, os.WriteFile(document,
			[]byte(skillDocument("code-review", "Second.")), 0o644))
		changed, err := catalog.Cached("code-review")
		require.NoError(t, err)
		changedSkill, err := changed.Skill()
		require.NoError(t, err)
		assert.Equal(t, "Second.", changedSkill.Description,
			"a person editing a skill must see the edit, and a long-running process would "+
				"otherwise serve the version it started with")
	})

	t.Run("a new file is noticed", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(path, "references.md"),
			[]byte("# References\n"), 0o644))
		grown, err := catalog.Cached("code-review")
		require.NoError(t, err)
		assert.Len(t, grown.Files, 2)
	})
}

// A catalog with no identifier is refused, because the identifier is the first segment of every
// reference and URI for a skill in it and there is nothing to address it by without one.
func TestACatalogNeedsAnIdentifier(t *testing.T) {
	_, err := skills.NewDirectory(skills.DirectoryOptions{Root: t.TempDir()})
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "two catalogs may hold a skill of the same name")
}

// A hidden directory is not part of a catalog. A person hiding one is keeping it out of the
// way, and serving it anyway would ignore the only signal they gave.
func TestAHiddenDirectoryIsNotPartOfACatalog(t *testing.T) {
	root := tree(t, map[string]string{
		"visible/SKILL.md":             skillDocument("visible", "Use this."),
		".hidden/SKILL.md":             skillDocument("hidden", "Not this."),
		".claude/skills/kept/SKILL.md": skillDocument("kept", "Use this too."),
	})
	catalog, err := skills.NewDirectory(skills.DirectoryOptions{ID: "local", Root: root})
	require.NoError(t, err)
	names, err := catalog.Names()
	require.NoError(t, err)
	assert.Equal(t, []string{"visible"}, names)
}

// A directory whose name does not match its skill's name is still listed — a listing that hid
// it would leave a person with content nothing will serve — and Validate is where the mismatch
// is reported. This asserts the listing half, so the split is deliberate rather than a gap.
func TestAMismatchedNameIsListedEvenThoughItCannotBeServed(t *testing.T) {
	root := tree(t, map[string]string{
		"code-review/SKILL.md": skillDocument("something-else", "Use this."),
	})
	catalog, err := skills.NewDirectory(skills.DirectoryOptions{ID: "local", Root: root})
	require.NoError(t, err)

	names, err := catalog.Names()
	require.NoError(t, err)
	assert.Equal(t, []string{"code-review"}, names,
		"it has a SKILL.md, so the catalog has a skill; whether this framework can read it is "+
			"a different question, and Validate answers that one")

	template, err := catalog.Template("code-review")
	require.NoError(t, err, "the template is read; it is Validate that refuses it")
	err = template.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match")
}
