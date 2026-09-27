package knowledge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The content constructors, and the invariant they hold.
//
// `pkg/knowledge.Content` is the type this design names as the place the behaviour lives, and these
// are the four entry points. The invariant is the reason they exist: a `Content` that has an origin
// but no location, or a location of the wrong kind for that origin, is a thing a reader can act on
// and be wrong about — "go and fix that file" with nowhere to fix it.
//
// While the mapping lived in the provider instead, `ListContent` and `GetContent` disagreed about
// the mutability of the same file. That survived every test in the suite, because no test asked
// both methods the same question. Constructing in one place is what makes the disagreement
// impossible rather than merely absent, and the mutability assertions here are that guarantee.

func sampleFile() File {
	when, _ := time.Parse(time.RFC3339, "2026-09-01T10:00:00Z")
	return File{
		Root:      "/srv/knowledge/docs",
		Path:      "runbooks/restore.md",
		ID:        "docs/runbooks/restore.md",
		Title:     "Restore a base",
		Body:      "# Restore\n\nDrop the base, keep the repository.\n",
		Digest:    "sha256:aaaa",
		ModTime:   when,
		Size:      46,
		Read:      true,
		SourceKey: "docs:runbooks/restore.md",
		FullPath:  "/srv/knowledge/docs/runbooks/restore.md",
	}
}

func options() ContentOptions {
	return ContentOptions{BaseID: "docs", IncludeBody: true,
		ReconciledAt: time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC), SourceCommit: "abc123"}
}

func TestACorpusFileBecomesContentThatIsNotEditableHere(t *testing.T) {
	t.Parallel()
	c, err := ContentFromFile(sampleFile(), []string{"toolbox:kind=procedure", "runbook"}, options())
	require.NoError(t, err)

	assert.Equal(t, "docs/runbooks/restore.md", c.ID)
	assert.Equal(t, "docs", c.BaseID)
	assert.Equal(t, OriginAuthored, c.Origin)
	assert.Equal(t, "Restore a base", c.Title)
	assert.Equal(t, OriginTag(OriginAuthored), c.Provenance.OriginTag)
	// The location is a corpus path because a person edits this in a repository, and saying
	// anything else would send them to the backend for a file the backend does not hold.
	assert.Equal(t, Location{Kind: LocationKindCorpusPath, Path: "runbooks/restore.md"}, c.Location)
	// The answer to "may a tool edit this", and the whole reason it is a field rather than a
	// convention: a runbook is edited in git, so `None` is what stops an assistant writing prose
	// that the next reconcile deletes.
	assert.Equal(t, MutabilityNone, c.Mutability)
	assert.Equal(t, "sha256:aaaa", c.Revision.Digest)
	assert.Equal(t, int64(46), c.Revision.Size)
	assert.Equal(t, "abc123", c.Revision.SourceCommit)
	assert.False(t, c.Revision.ReconciledAt.IsZero())
	assert.True(t, c.HasBody)
	assert.Equal(t, "# Restore\n\nDrop the base, keep the repository.\n", c.Body)
}

func TestACorpusFileIsStillACorpusFileWhenTheBodyWasNotAskedFor(t *testing.T) {
	t.Parallel()
	// A listing that does not want three thousand bodies. `HasBody` is what makes the difference
	// between "this deployment does not retain text" and "you did not ask", and a caller cannot
	// tell those apart from an empty string.
	opts := options()
	opts.IncludeBody = false
	c, err := ContentFromFile(sampleFile(), nil, opts)
	require.NoError(t, err)
	assert.False(t, c.HasBody)
	assert.Empty(t, c.Body)
	// Everything else is still there, because a listing is about *which* files exist.
	assert.Equal(t, "sha256:aaaa", c.Revision.Digest)
	assert.Equal(t, MutabilityNone, c.Mutability)
}

func TestACorpusFileTheWalkSkippedIsNotReportedAsAnEmptyDocument(t *testing.T) {
	t.Parallel()
	// The cheap filter skipped this file, so nothing read it. Reporting it as a document with no
	// text is a document that says nothing and looks empty; reporting it as having no body at all
	// is honest, and the two are told apart by `HasBody`.
	entry := OwnershipEntry{
		ID:      "docs/architecture/overview.md",
		Title:   "Overview",
		Digest:  "sha256:bbbb",
		Size:    62,
		ModTime: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
	}
	c, err := ContentFromRecord("/srv/knowledge/docs", "architecture/overview.md", entry.ID, entry,
		[]string{"architecture"}, options())
	require.NoError(t, err)
	assert.Equal(t, OriginAuthored, c.Origin)
	assert.False(t, c.HasBody, "nothing read it, so nothing claims to have read it")
	assert.Empty(t, c.Body)
	assert.Equal(t, "sha256:bbbb", c.Revision.Digest)
	assert.Equal(t, MutabilityNone, c.Mutability)
	assert.Equal(t, "architecture/overview.md", c.Location.Path)
}

func TestARecordWithNoTitleFallsBackToItsFrontmatter(t *testing.T) {
	t.Parallel()
	// A record can carry a title in either of two places depending on whether the file declared
	// one, and a listing that showed an empty title for half a corpus would look like a corpus of
	// untitled files rather than a parser that checked one field.
	entry := OwnershipEntry{ID: "x", Digest: "sha256:cccc",
		Front: Frontmatter{Title: "From frontmatter"}}
	c, err := ContentFromRecord("", "a.md", "x", entry, nil, options())
	require.NoError(t, err)
	assert.Equal(t, "From frontmatter", c.Title)

	entry.Title = "From the index"
	c, err = ContentFromRecord("", "a.md", "x", entry, nil, options())
	require.NoError(t, err)
	assert.Equal(t, "From the index", c.Title, "an explicit title wins")
}

func TestARetainedDocumentIsTheOneOriginThatIsCurated(t *testing.T) {
	t.Parallel()
	c, err := ContentFromRetained("docs", "fact-1", "One base, two halves",
		"one base, two halves", "fact-1", "the corpus half is load-bearing",
		[]string{"architecture"}, options())
	require.NoError(t, err)

	assert.Equal(t, OriginRetained, c.Origin)
	// Getting this one wrong in the direction of `None` is the expensive direction: an assistant
	// told a retained fact is not editable goes looking for a file to edit, and there is none.
	assert.Equal(t, MutabilityCurated, c.Mutability)
	assert.Equal(t, Location{Kind: LocationKindDocument, Path: "fact-1"}, c.Location)
	assert.True(t, c.HasBody)
	assert.Equal(t, OriginTag(OriginRetained), c.Provenance.OriginTag)
}

func TestARetainedDocumentWithNoTextIsNotAnEmptyDocument(t *testing.T) {
	t.Parallel()
	c, err := ContentFromRetained("docs", "fact-1", "One base", "", "fact-1", "", nil, options())
	require.NoError(t, err)
	assert.False(t, c.HasBody, "a fact the backend has not rendered text for is not a document saying nothing")
}

func TestAPageIsRegeneratedSoAnEditToItIsDiscarded(t *testing.T) {
	t.Parallel()
	c, err := ContentFromPage("docs", "page-1", "restore", "# Restore", "page-1",
		[]string{"runbook"}, options())
	require.NoError(t, err)
	assert.Equal(t, OriginDerived, c.Origin)
	// The distinction between a page and a document, and the reason it is stated rather than
	// inferred: a page's body is what the system currently believes, and the next refresh
	// overwrites whatever a caller put there.
	assert.Equal(t, MutabilityRegenerated, c.Mutability)
	assert.Equal(t, Location{Kind: LocationKindPage, PageID: "page-1"}, c.Location)
	assert.Equal(t, "restore", c.Title, "a page is named, and its name is its title")
	// A page's body exists by definition, so `HasBody` does not depend on the caller asking: a
	// caller that wanted the body did not get an answer about whether there is one.
	assert.True(t, c.HasBody)
}

func TestEveryOriginGetsItsOwnMutabilityAndNoCallerChooses(t *testing.T) {
	t.Parallel()
	// A table rather than three assertions, because the property is that the *map* is total and
	// distinct: one mutability per origin, and a fourth origin nobody has added yet cannot appear
	// in a contract without a decision about it.
	for origin, want := range map[Origin]Mutability{
		OriginAuthored:      MutabilityNone,
		OriginRetained:      MutabilityCurated,
		OriginDerived:       MutabilityRegenerated,
		OriginUnspecified:   MutabilityNone,
		Origin("something"): MutabilityNone,
	} {
		assert.Equal(t, want, MutabilityFor(origin), string(origin))
	}

	// And the constructors agree with the function, which is the only way they can: none of them
	// takes a mutability.
	retained, err := ContentFromRetained("docs", "f", "t", "b", "f", "", nil, options())
	require.NoError(t, err)
	page, err := ContentFromPage("docs", "p", "n", "b", "p", nil, options())
	require.NoError(t, err)
	assert.Equal(t, MutabilityFor(retained.Origin), retained.Mutability)
	assert.Equal(t, MutabilityFor(page.Origin), page.Mutability)
}

func TestTheInvariantRefusesContentThatCouldNotBeActedOn(t *testing.T) {
	t.Parallel()
	// Each case is a `Content` a reader would be misled by, and the refusal names the content so
	// the diagnosis does not have to be a search.
	for name, c := range map[string]Content{
		"no identifier": {Origin: OriginAuthored,
			Location: Location{Kind: LocationKindCorpusPath, Path: "a.md"}},
		"an origin nobody declared": {ID: "x", Origin: Origin("invented"),
			Location: Location{Kind: LocationKindCorpusPath, Path: "a.md"}},
		"claiming a body it does not have": {ID: "x", Origin: OriginAuthored, HasBody: true,
			Location: Location{Kind: LocationKindCorpusPath, Path: "a.md"}},
		"no location kind": {ID: "x", Origin: OriginAuthored},
		"an authored file with no path": {ID: "x", Origin: OriginAuthored,
			Location: Location{Kind: LocationKindCorpusPath}},
		"a retained fact with no document": {ID: "x", Origin: OriginRetained,
			Location: Location{Kind: LocationKindDocument}},
		"a page with no page id": {ID: "x", Origin: OriginDerived,
			Location: Location{Kind: LocationKindPage}},
	} {
		require.Error(t, ValidateContent(c), name)
	}
}

func TestTheConstructorsRefuseRatherThanReturnContentThatFailsTheInvariant(t *testing.T) {
	t.Parallel()
	// A constructor that returned a broken value with a nil error would put the failure somewhere
	// further from its cause, in whichever caller happened to use it first.
	_, err := ContentFromRetained("docs", "", "t", "b", "d", "", nil, options())
	require.Error(t, err, "a retained fact with no identifier")
	assert.Contains(t, err.Error(), "building content", "the refusal names the content it is about")

	_, err = ContentFromPage("docs", "page-1", "n", "b", "", nil, options())
	require.Error(t, err, "a page with no page id has nowhere a reader could go")
}

func TestValidateCanBeSkippedForTheOneCallerBuildingToBeRefused(t *testing.T) {
	t.Parallel()
	// The escape hatch exists because the invariant is worth having, not because it is worth
	// bypassing — and a caller that uses it has said so in the options rather than by omission.
	c, err := ContentFromRetained("docs", "", "t", "b", "d", "", nil,
		ContentOptions{BaseID: "docs", SkipValidation: true})
	require.NoError(t, err)
	assert.Equal(t, "", c.ID)
	assert.Error(t, ValidateContent(c), "it still does not pass; the flag only skips the check")
}

// --- the small value types, each of which encodes one decision.

func TestALocationNamesItselfTheWayAPersonWould(t *testing.T) {
	t.Parallel()
	// A reader is shown this in a refusal or a log line, so it has to read as a thing they could
	// open rather than as a struct dump.
	for _, tc := range []struct {
		loc  Location
		want string
	}{
		{Location{Kind: LocationKindCorpusPath, Path: "runbooks/restore.md"}, "runbooks/restore.md"},
		{Location{Kind: LocationKindDocument, Path: "fact-1"}, "fact-1"},
		{Location{Kind: LocationKindPage, TreePath: "runbooks", PageID: "page-1"}, "runbooks/page-1"},
		// A page the backend has not filed under a folder is named by its own identifier, since
		// that is the only thing there is to name it by.
		{Location{Kind: LocationKindPage, PageID: "page-1"}, "page-1"},
		// An unrecognised kind names nothing rather than something invented.
		{Location{Kind: LocationKind("elsewhere"), Path: "x"}, ""},
	} {
		assert.Equal(t, tc.want, tc.loc.String(), tc.loc.Kind)
	}
}

func TestTwoRevisionsAreTheSameWhenTheirDigestsAgreeAndOnlyThen(t *testing.T) {
	t.Parallel()
	// The digest decides and the timestamps are deliberately not consulted, because the timestamps
	// are the thing a filesystem is free to get wrong — and a revision compared by mtime would
	// report a content that never changed as changed.
	base := Revision{Digest: "sha256:aaaa", ModTime: time.Unix(1, 0), Size: 10}
	sameContentLater := Revision{Digest: "sha256:aaaa", ModTime: time.Unix(999, 0), Size: 999}
	assert.True(t, base.Equal(sameContentLater))

	assert.False(t, base.Equal(Revision{Digest: "sha256:bbbb"}), "a different digest is a different revision")
	// Two revisions with no digest are not known to be the same, and reporting them as equal is how
	// a reconcile decides nothing changed when it cannot tell.
	assert.False(t, base.Equal(Revision{}), "no digest is not evidence of sameness")
	assert.False(t, base.Equal(Revision{}))
}

func TestACorpusFileAnswersWhereItLivesAndWhatVersionItIs(t *testing.T) {
	t.Parallel()
	f := sampleFile()
	assert.Equal(t, Location{Kind: LocationKindCorpusPath, Path: f.Path}, f.Location())
	r := f.Revision()
	assert.Equal(t, f.Digest, r.Digest)
	assert.Equal(t, f.ModTime, r.ModTime)
	assert.Equal(t, f.Size, r.Size)
}

// --- the framework's own tag namespace.

func TestTheFrameworkTagNamesAnOrigin(t *testing.T) {
	t.Parallel()
	// Callers filter on this string, so it is part of the framework's vocabulary and not an
	// implementation detail of whoever writes it.
	assert.Equal(t, TagNamespaceFramework+":origin=authored", OriginTag(OriginAuthored))
	assert.Equal(t, TagNamespaceFramework+":origin=retained", OriginTag(OriginRetained))
}

func TestReadingATagBackGivesTheDimensionAndTheValue(t *testing.T) {
	t.Parallel()
	// A parser with no tests is a parser that is about to be wrong in a way only a deployment
	// notices, so this is table-driven over the shapes a tag can actually take.
	for _, tc := range []struct {
		name             string
		tag              string
		dimension, value string
		ok               bool
	}{
		{"the origin tag", OriginTag(OriginAuthored), "origin", "authored", true},
		{"a value containing an equals sign", TagNamespaceFramework + ":tag=a=b", "tag", "a=b", true},
		{"a person’s own tag", "runbook", "", "", false},
		{"another namespace’s tag", "vault:docs", "", "", false},
		{"a prefix that only looks right", "toolbox-k:origin=authored", "", "", false},
		{"no value at all", TagNamespaceFramework + ":origin", "", "", false},
		{"no dimension", TagNamespaceFramework + ":=authored", "", "", false},
		{"an empty value", TagNamespaceFramework + ":origin=", "", "", false},
		{"the empty string", "", "", "", false},
	} {
		dimension, value, ok := ParseTagGroup(tc.tag)
		assert.Equal(t, tc.ok, ok, tc.name)
		if tc.ok {
			assert.Equal(t, tc.dimension, dimension, tc.name)
			assert.Equal(t, tc.value, value, tc.name)
		} else {
			assert.Empty(t, dimension, tc.name)
			assert.Empty(t, value, tc.name)
		}
	}
}

// --- the revision line a status bar prints.

func TestARevisionReadsAsOneLineNamingWhatItIsAbout(t *testing.T) {
	t.Parallel()
	// This is what an operator sees when they ask whether a projection is current, so it has to
	// carry the base, the counts and the commit, and a digest short enough to read at a glance.
	got := DescribeRevision(ProjectionRevision{
		BaseID: "docs", Value: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		FileCount: 120, PageCount: 8, Commit: "0123456789abcdef0123", Which: "corpus",
	})
	assert.Equal(t,
		`base="docs" value=0123456789ab files=120 pages=8 commit=0123456789ab changed=corpus`, got)
}

func TestARevisionLineOmitsWhatIsNotKnown(t *testing.T) {
	t.Parallel()
	// A commit, a stale count and a changed-scope that were not established are not printed as
	// zero: a status line claiming "no stale pages" when staleness was never measured is a claim
	// nobody made.
	got := DescribeRevision(ProjectionRevision{BaseID: "docs", Value: "sha256:abc", FileCount: 3})
	assert.NotContains(t, got, "commit=")
	assert.NotContains(t, got, "stale=")
	assert.NotContains(t, got, "changed=")
	assert.Contains(t, got, "files=3")
}

func TestARevisionLineShowsAStaleCountOnlyWhenThereIsOne(t *testing.T) {
	t.Parallel()
	got := DescribeRevision(ProjectionRevision{BaseID: "docs", Value: "sha256:abc", StalePages: 2})
	assert.Contains(t, got, "stale=2", "a reader has to be able to tell a stale page from an unchecked one")
}

func TestARevisionLineShortensOnlyWhatItCanRecogniseAsADigest(t *testing.T) {
	t.Parallel()
	// A real digest is shortened to something readable at a glance. A value that is *not* one —
	// a different algorithm, an upstream revision name, a placeholder — is shown whole, because
	// shortening an unrecognised value means guessing where it ends, and two different values
	// truncated to the same twelve characters is the one thing a fingerprint must never do.
	const full = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assert.Contains(t, DescribeRevision(ProjectionRevision{Value: full}), "value=0123456789ab")
	for _, unrecognised := range []string{"not-a-digest", "sha256:tooshort", "main", "sha1:0123456789abcdef"} {
		assert.Contains(t, DescribeRevision(ProjectionRevision{Value: unrecognised}),
			"value="+unrecognised, unrecognised)
	}
	// A short commit is not padded or ellipsised into something longer than it is.
	assert.Contains(t, DescribeRevision(ProjectionRevision{Commit: "abc"}), "commit=abc")
}
