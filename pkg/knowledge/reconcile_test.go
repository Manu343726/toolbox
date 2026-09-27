package knowledge_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// writeCorpus materialises a set of files in a temporary directory and returns
// its path. Every test in this file builds its corpus through it, so that a test
// states only what it is about.
func writeCorpus(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

func mustCorpus(t *testing.T, dir string) *knowledge.Corpus {
	t.Helper()
	c, err := knowledge.NewCorpus([]knowledge.CorpusRoot{{Path: dir, Name: "docs"}}, knowledge.CorpusOptions{})
	require.NoError(t, err)
	return c
}

// emptyRecord returns an ownership record bound to a throwaway destination.
func emptyRecord(t *testing.T) *knowledge.OwnershipRecord {
	t.Helper()
	return &knowledge.OwnershipRecord{
		Version: 1,
		Identity: knowledge.OwnershipIdentity{
			BackendOrigin: "http://127.0.0.1:8888",
			BaseID:        "test",
			CorpusRoot:    "/tmp/whatever",
			Namespace:     "docs",
		},
		Files: map[string]knowledge.OwnershipEntry{},
	}
}

func TestWalkFindsMarkdownAndSkipsEverythingElse(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{
		"index.md":                "# Index\n",
		"runbooks/restore.md":     "# Restore\n",
		"runbooks/deep/nested.md": "# Nested\n",
		"notes.txt":               "not corpus content",
		"diagram.png":             "\x89PNG\r\n",
		".hidden/secret.md":       "dot directory",
		".git/config.md":          "version control internals",
		".trash/deleted.md":       "gone",
	})

	res, err := mustCorpus(t, dir).Walk(t.Context(), knowledge.WalkOptions{})
	require.NoError(t, err)

	var paths []string
	for _, f := range res.Files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"index.md", "runbooks/deep/nested.md", "runbooks/restore.md"}, paths,
		"only markdown is corpus content, dot directories hold configuration and history, and a repository's object store is not documentation")
}

func TestWalkIsCaseInsensitiveAboutTheExtension(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"Notes/RESTORE.MD": "# Restore\n"})
	res, err := mustCorpus(t, dir).Walk(t.Context(), knowledge.WalkOptions{})
	require.NoError(t, err)
	require.Len(t, res.Files, 1,
		"a repository written on one operating system gets checked out on another, and .MD is the same document as .md")
	assert.Equal(t, "Notes/RESTORE.MD", res.Files[0].Path)
}

func TestWalkOrderIsStable(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{
		"z.md": "z\n", "a.md": "a\n", "m/b.md": "b\n", "m/a.md": "ba\n",
	})
	c := mustCorpus(t, dir)

	first, err := c.Walk(t.Context(), knowledge.WalkOptions{})
	require.NoError(t, err)
	for range 5 {
		again, err := c.Walk(t.Context(), knowledge.WalkOptions{})
		require.NoError(t, err)
		require.Equal(t, first.Files, again.Files,
			"a plan and a projection both have to be byte-identical between two runs over an unchanged directory")
	}
}

func TestParseFileSeparatesFrontmatterFromBody(t *testing.T) {
	t.Parallel()

	raw := []byte("---\nid: wiki:auth\ntitle: Authentication\nkind: architecture\nstatus: active\ntags:\n  - security\n  - oauth\n---\n\n# Authentication\n\nOur API uses OAuth 2.1.\n")
	parsed, err := knowledge.ParseFile(raw)
	require.NoError(t, err)

	assert.True(t, parsed.HadFrontmatter)
	assert.Equal(t, "wiki:auth", parsed.Front.ID)
	assert.Equal(t, "Authentication", parsed.Front.Title)
	assert.Equal(t, knowledge.KindArchitecture, parsed.Front.Kind)
	assert.Equal(t, knowledge.StatusActive, parsed.Front.Status)
	assert.Equal(t, []string{"security", "oauth"}, parsed.Front.Tags)
	assert.Equal(t, "# Authentication\n\nOur API uses OAuth 2.1.\n", parsed.Body,
		"the blank line separating frontmatter from the document is structure, not prose")
}

func TestParseFileWithoutFrontmatterIsAllBody(t *testing.T) {
	t.Parallel()

	parsed, err := knowledge.ParseFile([]byte("# Just a document\n\nWith text.\n"))
	require.NoError(t, err)
	assert.False(t, parsed.HadFrontmatter, "a file with no frontmatter is reconciled all the same, and the path alone scopes it")
	assert.Equal(t, "# Just a document\n\nWith text.\n", parsed.Body)
}

func TestParseFileTreatsUnterminatedFrontmatterAsBody(t *testing.T) {
	t.Parallel()

	// A file with a stray "---" under its title must not be reduced to nothing.
	raw := []byte("---\nid: never-closed\n\n# A document\n")
	parsed, err := knowledge.ParseFile(raw)
	require.NoError(t, err)
	assert.False(t, parsed.HadFrontmatter)
	assert.Equal(t, string(raw), parsed.Body,
		"losing a person's writing to a fence that was never closed is worse than ignoring the fence")
}

func TestParseFileRefusesAnUnusableDeclaredValue(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"unknown kind":      "---\nkind: architecure\n---\nbody\n",
		"unknown status":    "---\nstatus: active-ish\n---\nbody\n",
		"unknown authority": "---\nauthority: machine\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := knowledge.ParseFile([]byte(raw))
			require.Error(t, err,
				"these become retrieval filters, and a typo in a filter is a silently empty result nobody notices until they need it")
			assert.Contains(t, err.Error(), "frontmatter")
		})
	}
}

func TestParseFileKeepsUnknownKeysWithoutActingOnThem(t *testing.T) {
	t.Parallel()

	raw := []byte("---\nid: x\nreviewed_by: someone\ncustom: value\n---\nbody\n")
	parsed, err := knowledge.ParseFile(raw)
	require.NoError(t, err)
	assert.Equal(t, "x", parsed.Front.ID)
	assert.Contains(t, parsed.Front.Extra, "custom",
		"a corpus is a body of documentation people extend with their own vocabulary")
	assert.Equal(t, []string{"custom", "reviewed_by"}, parsed.Front.SortedExtraKeys())
}

func TestFrontmatterRoundTrips(t *testing.T) {
	t.Parallel()

	raw := []byte("---\nid: wiki:auth\ntitle: Authentication\nkind: policy\nstatus: deprecated\nowner: platform\nsupersedes:\n  - wiki:old\n---\n\nbody\n")
	parsed, err := knowledge.ParseFile(raw)
	require.NoError(t, err)

	rendered, err := parsed.Front.Render()
	require.NoError(t, err)
	reparsed, err := knowledge.ParseFile(append(rendered, []byte(parsed.Body)...))
	require.NoError(t, err)
	assert.Equal(t, parsed.Front.ID, reparsed.Front.ID)
	assert.Equal(t, parsed.Front.Kind, reparsed.Front.Kind)
	assert.Equal(t, parsed.Front.Supersedes, reparsed.Front.Supersedes)
	assert.Equal(t, parsed.Body, reparsed.Body)
}

func TestPlanReportsEveryFileExactlyOnce(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{
		"changed.md":   "# Changed, edited\n",
		"same.md":      "# Same\n",
		"to-delete.md": "# Going away\n",
	})
	corpus := mustCorpus(t, dir)

	// A first plan establishes the record.
	rec := emptyRecord(t)
	planner := knowledge.NewPlanner(corpus)
	first, err := planner.Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Commit: "c1"})
	require.NoError(t, err)
	require.Len(t, first.Created, 3)
	for _, e := range first.Created {
		rec.Files[e.SourceKey] = knowledge.OwnershipEntry{
			ID: e.ID, Root: e.Root, Path: e.Path, Digest: e.Digest,
			ModTime: e.ModTime, Size: e.Size, ReconciledAt: time.Now(),
		}
	}

	// One is edited, one is removed from disk, and a genuinely new one arrives.
	require.NoError(t, os.Remove(filepath.Join(dir, "to-delete.md")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "changed.md"), []byte("# Changed, edited twice\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "added.md"), []byte("# Added\n"), 0o644))

	second, err := planner.Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Commit: "c2", Prune: true})
	require.NoError(t, err)

	assert.Empty(t, second.Moved, "nothing moved here; the move cases have their own tests")
	assert.Len(t, second.Unchanged, 1, "an unchanged file costs nothing")
	assert.Len(t, second.Updated, 1, "a changed digest is an update")
	assert.Len(t, second.Created, 1, "a new file is a creation")
	assert.Len(t, second.Deleted, 1, "a recorded file that is gone is a deletion")

	total := len(second.Created) + len(second.Updated) + len(second.Unchanged) + len(second.Deleted)
	assert.Equal(t, 4, total, "between them the four lists account for every file; a file in none of them is a bug, not an unclassified state")
}

func TestDeclaredIdSurvivesAMove(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{
		"old/place.md": "---\nid: wiki:runbook\n---\n\n# Runbook\n\nThe steps.\n",
	})
	corpus := mustCorpus(t, dir)
	planner := knowledge.NewPlanner(corpus)

	rec := emptyRecord(t)
	first, err := planner.Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Commit: "c1"})
	require.NoError(t, err)
	require.Len(t, first.Created, 1)
	assert.Equal(t, "wiki:runbook", first.Created[0].ID, "a declared id is the identity, not the path")

	rec.Files[first.Created[0].SourceKey] = knowledge.OwnershipEntry{
		ID: first.Created[0].ID, DeclaredID: true, Root: "docs", Path: "old/place.md",
		Digest: first.Created[0].Digest, ModTime: first.Created[0].ModTime, Size: first.Created[0].Size,
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "new"), 0o755))
	require.NoError(t, os.Rename(filepath.Join(dir, "old/place.md"), filepath.Join(dir, "new/place.md")))

	second, err := planner.Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Commit: "c2", Prune: true})
	require.NoError(t, err)

	require.Len(t, second.Moved, 1, "a declared identity makes a move a move")
	assert.Equal(t, "old/place.md", second.Moved[0].From.Path)
	assert.Equal(t, "new/place.md", second.Moved[0].To.Path)
	assert.Equal(t, "wiki:runbook", second.Moved[0].To.ID, "the identity is unchanged, so the facts already extracted survive the move")
	assert.Empty(t, second.Deleted, "a move is not a delete")
	assert.Empty(t, second.Created, "a move is not a create")
}

func TestMovedFileWithoutADeclaredIdIsReportedNotSilentlyTreatedAsUnrelated(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# Runbook\n\nSteps.\n"})
	corpus := mustCorpus(t, dir)
	planner := knowledge.NewPlanner(corpus)

	rec := emptyRecord(t)
	first, err := planner.Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Commit: "c1"})
	require.NoError(t, err)
	require.Len(t, first.Created, 1)
	rec.Files[first.Created[0].SourceKey] = knowledge.OwnershipEntry{
		ID: first.Created[0].ID, Root: "docs", Path: "a.md",
		Digest: first.Created[0].Digest, ModTime: first.Created[0].ModTime, Size: first.Created[0].Size,
	}
	require.NoError(t, os.Rename(filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")))

	plan, err := planner.Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Commit: "c2", Prune: true})
	require.NoError(t, err)

	require.Len(t, plan.Moved, 1,
		"identical content digests do not coincide by accident, so this is a move and not an unrelated new document")
	assert.Equal(t, "a.md", plan.Moved[0].From.Path)
	assert.Equal(t, "b.md", plan.Moved[0].To.Path)
	assert.Empty(t, plan.Deleted)
	assert.Empty(t, plan.Created)
	require.Len(t, plan.Warnings, 1)
	assert.Contains(t, plan.Warnings[0], "declares no `id`",
		"a person who reorganised a tree needs to be told, because every fact referencing the old identifier is now orphaned")
}

func TestTwoFilesClaimingOneIdentityIsRefused(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{
		"one.md": "---\nid: wiki:same\n---\n\none\n",
		"two.md": "---\nid: wiki:same\n---\n\ntwo\n",
	})
	_, err := knowledge.NewPlanner(mustCorpus(t, dir)).Plan(t.Context(), emptyRecord(t),
		knowledge.PlanOptions{BaseID: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two files claim the identifier",
		"there is no correct answer, and picking one would leave the other's content unreconciled with nobody noticing")
}

func TestPlanDigestIsStableAcrossRunsAndChangesWithContent(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# A\n", "b.md": "# B\n"})
	planner := knowledge.NewPlanner(mustCorpus(t, dir))
	opts := knowledge.PlanOptions{BaseID: "test", Commit: "c1"}

	first, err := planner.Plan(t.Context(), emptyRecord(t), opts)
	require.NoError(t, err)
	second, err := planner.Plan(t.Context(), emptyRecord(t), opts)
	require.NoError(t, err)
	assert.Equal(t, first.PlanDigest, second.PlanDigest,
		"two plans over an unchanged corpus are the same plan, and a confirmation that expired because somebody ran the planner twice would fail for no reason")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("# B, edited\n"), 0o644))
	third, err := planner.Plan(t.Context(), emptyRecord(t), opts)
	require.NoError(t, err)
	assert.NotEqual(t, first.PlanDigest, third.PlanDigest)
}

func TestPruneIsOffUnlessAskedFor(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"kept.md": "# Kept\n"})
	corpus := mustCorpus(t, dir)
	rec := emptyRecord(t)
	rec.Files["docs:vanished.md"] = knowledge.OwnershipEntry{ID: "docs:vanished.md", Root: "docs", Path: "vanished.md", Digest: knowledge.Digest([]byte("x"))}

	planner := knowledge.NewPlanner(corpus)

	withoutPrune, err := planner.Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test"})
	require.NoError(t, err)
	assert.Empty(t, withoutPrune.Deleted, "a first reconcile over an existing base must not be able to remove anything by accident")

	withPrune, err := planner.Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Prune: true})
	require.NoError(t, err)
	assert.Len(t, withPrune.Deleted, 1)
}

func TestScopeNarrowingPrunesWhatItOwned(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"keep/a.md": "# A\n", "drop/b.md": "# B\n"})
	rec := emptyRecord(t)
	rec.Files["docs:drop/b.md"] = knowledge.OwnershipEntry{ID: "docs:drop/b.md", Root: "docs", Path: "drop/b.md", Digest: knowledge.Digest([]byte("gone"))}

	corpus, err := knowledge.NewCorpus(
		[]knowledge.CorpusRoot{{Path: dir, Name: "docs"}},
		knowledge.CorpusOptions{Include: []string{"keep"}},
	)
	require.NoError(t, err)

	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Prune: true})
	require.NoError(t, err)
	assert.Len(t, plan.Deleted, 1, "narrowing the scope of a reconcile over the same destination legitimately means these files are no longer mine")
}

func TestDriftIsReportedAndNeverResolved(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{
		"runbook.md": "# Runbook\n\nThe retry count is three.\n",
	})
	corpus := mustCorpus(t, dir)

	walk, err := corpus.Walk(t.Context(), knowledge.WalkOptions{})
	require.NoError(t, err)
	require.Len(t, walk.Files, 1)
	id := walk.Files[0].ID

	tests := map[string]struct {
		claim string
		want  int
	}{
		"claim still in the file":        {claim: "The retry count is three.", want: 0},
		"claim contradicted by the file": {claim: "The retry count is five.", want: 1},
		"claim worded differently":       {claim: "Retries are configured to three.", want: 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), emptyRecord(t), knowledge.PlanOptions{
				BaseID: "test",
				Derived: []knowledge.DerivedClaim{{
					ID: "obs-1", Text: tc.claim, Origin: knowledge.OriginDerived,
					SourceContentIDs: []string{id},
				}},
			})
			require.NoError(t, err)
			assert.Len(t, plan.Drift, tc.want)
			for _, d := range plan.Drift {
				assert.Equal(t, driftName, d.Detection,
					"a reader has to know how much weight to put on a heuristic")
			}
		})
	}
}

// driftName mirrors the unexported detection label, asserted here as the visible
// contract rather than the implementation's constant.
const driftName = "claim text is not contained in the authored content that produced it"

func TestDriftIgnoresClaimsNotDerivedAndClaimsWithNoAuthoredSource(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# A\n\ntext\n"})
	corpus := mustCorpus(t, dir)

	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), emptyRecord(t), knowledge.PlanOptions{
		BaseID: "test",
		Derived: []knowledge.DerivedClaim{
			{ID: "retained-1", Text: "anything", Origin: knowledge.OriginRetained, SourceContentIDs: []string{"x"}},
			{ID: "orphan-1", Text: "anything", Origin: knowledge.OriginDerived, SourceContentIDs: []string{"no-such-content"}},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, plan.Drift,
		"a claim contradicted by nothing this walk read is not a conflict between a person and the system")
}

// writeFileString and moveFile exist so that the apply tests can change a corpus
// between a plan and an apply without repeating the same three lines, and so
// that a missing intermediate directory is created rather than producing an
// error a reader has to interpret.
func writeFileString(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func moveFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
}
