package knowledge_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Manu343726/toolbox/pkg/knowledge"
)

func authored(path, content string) knowledge.ProjectedFile {
	return knowledge.ProjectedFile{
		Path: path, Content: []byte(content), SourcePath: path, ContentID: path,
	}
}

func TestProjectionPutsBothHalvesInOneTree(t *testing.T) {
	t.Parallel()

	wiki, err := knowledge.ProjectWiki(knowledge.WikiInput{
		BaseID:      "project",
		Commit:      "abc123",
		Authored:    []knowledge.ProjectedFile{authored("runbooks/restore.md", "# Restore\n\nSteps.\n")},
		Pages:       []knowledge.PageFile{{Path: "operations/deploy.md", Content: []byte("# Deploy\n\nThe system believes...\n"), PageID: "page-1"}},
		GeneratedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	var paths []string
	for _, f := range wiki.Files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"file/runbooks/restore.md", "generated/operations/deploy.md"}, paths,
		"a reader should not have to know which half produced a file to read it")
}

func TestProjectedFileStatesItsOriginInFrontmatter(t *testing.T) {
	t.Parallel()

	wiki, err := knowledge.ProjectWiki(knowledge.WikiInput{
		BaseID:   "project",
		Authored: []knowledge.ProjectedFile{authored("a.md", "# A\n\nbody\n")},
		Pages:    []knowledge.PageFile{{Path: "b.md", Content: []byte("# B\n"), PageID: "p"}},
	})
	require.NoError(t, err)
	require.Len(t, wiki.Files, 2)

	for _, f := range wiki.Files {
		assert.NotEmpty(t, f.Origin)
		text := string(f.Content)
		assert.True(t, strings.HasPrefix(text, "---\n"),
			"was this written by a person or generated should be answered by opening the file, not by knowing where it came from: %s", f.Path)
		assert.Contains(t, text, "origin: "+f.Origin)
		assert.Contains(t, text, "authoritative: false")
	}
}

func TestAuthoredProseIsProjectedVerbatim(t *testing.T) {
	t.Parallel()

	// Deliberately awkward prose: trailing spaces, a tab, odd line endings mixed
	// in, and a trailing blank line. A projection that reflows any of it has
	// produced a different document from the one that was reviewed.
	body := "# Restore\n\nStep one.  \n\tindented\n\n\nTrailing blank lines above.\n"
	original := "---\nid: wiki:restore\ntitle: Restore\n---\n" + body

	wiki, err := knowledge.ProjectWiki(knowledge.WikiInput{
		BaseID:   "project",
		Authored: []knowledge.ProjectedFile{authored("restore.md", original)},
	})
	require.NoError(t, err)
	require.Len(t, wiki.Files, 1)

	projected := string(wiki.Files[0].Content)
	assert.Contains(t, projected, body,
		"a re-rendered authored file would differ from the reviewed original, and a diff between them is a question nobody asked")

	// The prose survives a parse/re-render of the frontmatter unchanged.
	parsed, err := knowledge.ParseFile([]byte(projected))
	require.NoError(t, err)
	assert.Equal(t, body, parsed.Body)
	assert.Equal(t, "wiki:restore", parsed.Front.ID, "the author's own frontmatter is carried through, not replaced")
	assert.Equal(t, "Restore", parsed.Front.Title)
	assert.Equal(t, knowledge.ProjectionOriginFile, parsed.Front.Extra["origin"])
}

func TestAuthoredFileWithNoFrontmatterGetsAHeaderAndKeepsItsText(t *testing.T) {
	t.Parallel()

	wiki, err := knowledge.ProjectWiki(knowledge.WikiInput{
		BaseID:   "project",
		Authored: []knowledge.ProjectedFile{authored("plain.md", "# Plain\n\nJust text.\n")},
	})
	require.NoError(t, err)
	require.Len(t, wiki.Files, 1)

	parsed, err := knowledge.ParseFile(wiki.Files[0].Content)
	require.NoError(t, err)
	assert.True(t, parsed.HadFrontmatter)
	assert.Equal(t, "# Plain\n\nJust text.\n", parsed.Body)
}

func TestTheBackendsOwnIndexIsNotUsedAndItsLogsAreKept(t *testing.T) {
	t.Parallel()

	wiki, err := knowledge.ProjectWiki(knowledge.WikiInput{
		BaseID:   "project",
		Authored: []knowledge.ProjectedFile{authored("a.md", "# A\n")},
		Pages: []knowledge.PageFile{
			{Path: "index.md", Content: []byte("# Backend index\n"), IsIndex: true},
			{Path: "p.md", Content: []byte("# Page\n"), PageID: "p"},
			{Path: "p.log.md", Content: []byte("---\ntype: log\n---\nhistory\n"), PageID: "p", IsLog: true},
		},
	})
	require.NoError(t, err)

	var paths []string
	for _, f := range wiki.Files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"file/a.md", "generated/p.log.md", "generated/p.md"}, paths,
		"there is one index for the whole view and it is generated here, because it has to list authored files too; a page's own history is part of the page")
	assert.NotContains(t, string(wiki.Index), "Backend index")
}

func TestProjectionIndexLinksEverythingAndSaysWhatItIs(t *testing.T) {
	t.Parallel()

	wiki, err := knowledge.ProjectWiki(knowledge.WikiInput{
		BaseID:      "project",
		Commit:      "deadbeef",
		GeneratedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		StalePages:  2,
		Authored:    []knowledge.ProjectedFile{authored("runbooks/restore.md", "---\ntitle: Restore a base\n---\n# Restore\n")},
		Pages:       []knowledge.PageFile{{Path: "deploy.md", Content: []byte("# Deploy\n"), PageID: "p1"}},
	})
	require.NoError(t, err)

	index := string(wiki.Index)
	assert.Contains(t, index, "not a corpus and not authority")
	assert.Contains(t, index, "file/runbooks/restore.md")
	assert.Contains(t, index, "generated/deploy.md")
	assert.Contains(t, index, "Restore a base", "the index shows a declared title where there is one")
	assert.Contains(t, index, "deadbeef")
	assert.Contains(t, index, "2 derived page(s) the backend reports as not yet refreshed",
		"a reader can tell a current view from an out-of-date one")
}

func TestProjectionIsFilterableByOriginAndSubtree(t *testing.T) {
	t.Parallel()

	in := knowledge.WikiInput{
		BaseID:   "project",
		Authored: []knowledge.ProjectedFile{authored("runbooks/a.md", "# A\n"), authored("notes/b.md", "# B\n")},
		Pages:    []knowledge.PageFile{{Path: "ops/c.md", Content: []byte("# C\n"), PageID: "p"}},
	}

	onlyAuthored, err := knowledge.ProjectWiki(withFilter(in, knowledge.OriginAuthored, ""))
	require.NoError(t, err)
	assert.Len(t, onlyAuthored.Files, 2, "a base with three thousand files must be readable one origin at a time")
	for _, f := range onlyAuthored.Files {
		assert.Equal(t, knowledge.ProjectionOriginFile, f.Origin)
	}

	subtree, err := knowledge.ProjectWiki(withFilter(in, knowledge.OriginUnspecified, "runbooks"))
	require.NoError(t, err)
	require.Len(t, subtree.Files, 1)
	assert.Equal(t, "file/runbooks/a.md", subtree.Files[0].Path)
}

func withFilter(in knowledge.WikiInput, origin knowledge.Origin, subtree string) knowledge.WikiInput {
	in.OriginFilter = origin
	in.Subtree = subtree
	return in
}

func TestProjectionIsRefusedAsACorpusRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wiki, err := knowledge.ProjectWiki(knowledge.WikiInput{
		BaseID:   "project",
		Authored: []knowledge.ProjectedFile{authored("a.md", "# A\n")},
	})
	require.NoError(t, err)
	writeProjection(t, dir, wiki)

	_, err = knowledge.NewCorpus([]knowledge.CorpusRoot{{Path: dir, Name: "docs"}}, knowledge.CorpusOptions{})
	require.Error(t, err, "ingesting a projection would make one generation of the system reason from another's own output")
	assert.Contains(t, err.Error(), "generated projection")
	assert.Contains(t, err.Error(), "reconciling it would ingest the system's own output")

	// The marker is also readable on its own, so a caller can check without
	// attempting a walk.
	assert.True(t, knowledge.IsProjectionRoot(dir))
}

func TestACorpusRootIsAcceptedAndAProjectionMarkerIsTheOnlyRefusal(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# A\n"})
	_, err := knowledge.NewCorpus([]knowledge.CorpusRoot{{Path: dir, Name: "docs"}}, knowledge.CorpusOptions{})
	require.NoError(t, err)
	assert.False(t, knowledge.IsProjectionRoot(dir))
}

func writeProjection(t *testing.T, dir string, wiki knowledge.Wiki) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, knowledge.MarkerName()), wiki.Marker, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.md"), wiki.Index, 0o644))
	for _, f := range wiki.Files {
		full := filepath.Join(dir, filepath.FromSlash(f.Path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, f.Content, 0o644))
	}
}

func TestMarkerRecordsWhatTheViewWasTakenFromAndSaysItIsNotAuthoritative(t *testing.T) {
	t.Parallel()

	marker := string(knowledge.MarkerFor("project", "abc123", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)))
	assert.Contains(t, marker, `"kind": "toolbox.knowledge.projection"`)
	assert.Contains(t, marker, `"commit": "abc123"`)
	assert.Contains(t, marker, `"authoritative": false`)
	assert.Contains(t, marker, "Do not edit it and do not reconcile it")
}

func TestProjectionIsDeterministic(t *testing.T) {
	t.Parallel()

	in := knowledge.WikiInput{
		BaseID:      "project",
		GeneratedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Authored:    []knowledge.ProjectedFile{authored("z.md", "# Z\n"), authored("a.md", "# A\n"), authored("m/b.md", "# B\n")},
		Pages: []knowledge.PageFile{
			{Path: "y.md", Content: []byte("# Y\n"), PageID: "y"},
			{Path: "x.md", Content: []byte("# X\n"), PageID: "x"},
		},
	}
	first, err := knowledge.ProjectWiki(in)
	require.NoError(t, err)
	for range 5 {
		again, err := knowledge.ProjectWiki(in)
		require.NoError(t, err)
		require.Equal(t, first.Files, again.Files, "two projections of an unchanged base are the same projection")
		require.Equal(t, first.Index, again.Index)
	}
}

func TestRevisionMovesWhenEitherHalfMoves(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# A\n"})
	corpus := mustCorpus(t, dir)

	tree := []knowledge.PageNode{{ID: "p1", TreePath: "ops", IsStale: false}}
	src := knowledge.NewRevisionSource(corpus, func(context.Context, string) ([]knowledge.PageNode, error) {
		return tree, nil
	}).WithClock(func() time.Time { return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) })

	first, err := src.Revision(t.Context(), "project", "c1")
	require.NoError(t, err)
	assert.Equal(t, 1, first.FileCount)
	assert.Equal(t, 1, first.PageCount)
	assert.Equal(t, 0, first.StalePages)
	assert.Equal(t, "both", first.Compare(knowledge.ProjectionRevision{}),
		"a first revision has nothing to compare against, so everything is new")

	same, err := src.Revision(t.Context(), "project", "c1")
	require.NoError(t, err)
	assert.Equal(t, first.Value, same.Value)
	assert.Empty(t, same.Compare(first))

	// The corpus moves.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("# B\n"), 0o644))
	authoredMoved, err := src.Revision(t.Context(), "project", "c1")
	require.NoError(t, err)
	assert.Equal(t, "authored", authoredMoved.Compare(same))
	assert.NotEqual(t, same.Authored, authoredMoved.Authored)
	assert.Equal(t, same.Derived, authoredMoved.Derived, "the other half did not move")

	// The engine moves.
	tree = []knowledge.PageNode{{ID: "p1", TreePath: "ops", IsStale: true}}
	derivedMoved, err := src.Revision(t.Context(), "project", "c1")
	require.NoError(t, err)
	assert.Equal(t, "derived", derivedMoved.Compare(authoredMoved))
	assert.Equal(t, 1, derivedMoved.StalePages,
		"a refresh that has not happened yet is visible without re-exporting anything")
}

func TestRevisionReportsAnUnavailableEngineHalfWithoutFailing(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# A\n"})
	corpus := mustCorpus(t, dir)

	failing := knowledge.NewRevisionSource(corpus, func(context.Context, string) ([]knowledge.PageNode, error) {
		return nil, assert.AnError
	})
	rev, err := failing.Revision(t.Context(), "project", "c1")
	require.NoError(t, err, "a mount whose corpus changed should still notice when the engine call is failing")
	assert.Equal(t, "unavailable", rev.Derived)
	assert.NotEmpty(t, rev.Authored, "the half that did answer still reports")

	// And it differs from the last known value, so a client re-fetches — which is
	// the right outcome, because the pages may well have changed.
	healthy := knowledge.NewRevisionSource(corpus, func(context.Context, string) ([]knowledge.PageNode, error) {
		return []knowledge.PageNode{{ID: "p1"}}, nil
	})
	known, err := healthy.Revision(t.Context(), "project", "c1")
	require.NoError(t, err)
	assert.Equal(t, "derived", rev.Compare(known))
}

func TestRevisionProbeDoesNotReadFileContents(t *testing.T) {
	t.Parallel()

	// A large file is cheap to stat and expensive to read, and the probe must be
	// the cheap one: at three thousand files a digest per file is a full read of
	// the corpus on every poll.
	dir := writeCorpus(t, map[string]string{"big.md": strings.Repeat("x", 1<<20)})
	corpus := mustCorpus(t, dir)
	src := knowledge.NewRevisionSource(corpus, func(context.Context, string) ([]knowledge.PageNode, error) {
		return nil, nil
	})

	rev, err := src.Revision(t.Context(), "project", "c1")
	require.NoError(t, err)
	assert.Equal(t, 1, rev.FileCount)

	// Touching without editing does not move the revision, because the probe
	// compares modification time and size and both are unchanged.
	before := rev.Authored
	info, err := os.Stat(filepath.Join(dir, "big.md"))
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "big.md"), info.ModTime(), info.ModTime()))
	after, err := src.Revision(t.Context(), "project", "c1")
	require.NoError(t, err)
	assert.Equal(t, before, after.Authored)
}
