package knowledge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are the helpers a caller reaches for when it holds content rather than when it is
// building one. They are small and their behaviour is a decision each: which origins exist, which
// path is a file's folder, what an unset budget means. Small and unstated is how a caller ends up
// writing its own version and getting a slightly different answer.

func TestOriginHasExactlyThreeValuesAndTheRestAreInvalid(t *testing.T) {
	t.Parallel()
	// Three, and the fourth is not a default to be extended: a contract that grows an origin
	// has to decide what a deployment with the old binary does with a message carrying the new
	// one, and an origin this package calls invalid is one the provider refuses rather than one
	// it quietly treats as authored.
	assert.True(t, OriginAuthored.Valid())
	assert.True(t, OriginRetained.Valid())
	assert.True(t, OriginDerived.Valid())
	assert.False(t, Origin("").Valid(), "an absent origin is not a fourth origin")
	assert.False(t, Origin("unknown").Valid())
}

func TestOnlyAuthoredContentIsAuthored(t *testing.T) {
	t.Parallel()
	// The distinction the whole content model turns on: somebody wrote one of these and
	// something derived two. A derived document that answered "was this authored?" wrongly
	// would let a caller offer to edit a file the next refresh overwrites.
	assert.True(t, Content{Origin: OriginAuthored}.IsAuthored())
	assert.False(t, Content{Origin: OriginRetained}.IsAuthored())
	assert.False(t, Content{Origin: OriginDerived}.IsAuthored())
}

func TestPathFolderIsTheParentAndNotAPrefix(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "runbooks", PathFolder("runbooks/restore.md"))
	assert.Equal(t, "runbooks/disaster", PathFolder("runbooks/disaster/restore.md"))
	// A file at the root has no folder, and returning "" rather than the file's own name is
	// what lets a caller join `<folder>/<name>` without a doubled path segment.
	assert.Equal(t, "", PathFolder("index.md"))
	// A backslash separator normalizes rather than becoming part of a folder name, so a corpus
	// walked on one platform and read on another produces the same folder. Without this, a
	// Windows checkout of the same repository would have a different tree.
	assert.Equal(t, "runbooks", PathFolder(`runbooks\restore.md`))
}

func TestBudgetDefaultsDependOnWhetherTheCallIsReasoning(t *testing.T) {
	t.Parallel()
	// Reflecting spends a model call to synthesise an answer, so it gets the cheaper budget;
	// a recall is a search and gets the balanced one. Choosing between them is a preference
	// rather than a tuning mistake, and the default is a stated one rather than the zero value
	// — a zero budget would be the cheapest possible answer, silently.
	assert.Equal(t, BudgetLow, DefaultFor(true))
	assert.Equal(t, BudgetMid, DefaultFor(false))
}

func TestAnUnsetBudgetTakesTheCallersDefaultAndASetOneIsLeftAlone(t *testing.T) {
	t.Parallel()
	// Resolve substitutes only for `Unspecified`. A caller that asked for the thorough budget
	// gets it even when the call is a reflection, because a preference expressed once is not
	// re-decided per call.
	assert.Equal(t, BudgetLow, BudgetUnspecified.Resolve(true))
	assert.Equal(t, BudgetMid, BudgetUnspecified.Resolve(false))
	assert.Equal(t, BudgetHigh, BudgetHigh.Resolve(false))
	assert.Equal(t, BudgetLow, BudgetLow.Resolve(false))
}

func TestCountByTagCountsEntriesNotOccurrences(t *testing.T) {
	t.Parallel()
	entries := []PlanEntry{
		{ID: "a", Tags: []string{"architecture", "runbook"}},
		{ID: "b", Tags: []string{"architecture"}},
		{ID: "c", Tags: []string{"architecture", "architecture"}},
		{ID: "d", Tags: nil},
	}
	// A tag repeated within one entry is one entry carrying it, not two. The number this feeds
	// is "how many files does this tag reach", and counting it twice would report coverage the
	// corpus does not have.
	assert.Equal(t, 3, CountByTag(entries, "architecture"))
	assert.Equal(t, 1, CountByTag(entries, "runbook"))
	assert.Equal(t, 0, CountByTag(entries, "absent"))
	assert.Equal(t, 0, CountByTag(nil, "architecture"))
}

func TestAnIdempotencyKeyDependsOnTheContentAndNotTheOrderItIsListed(t *testing.T) {
	t.Parallel()
	entries := []PlanEntry{
		{ID: "a", Digest: "1111"},
		{ID: "b", Digest: "2222"},
	}
	// A key over the same set in a different order is the same work, so it must be the same
	// key. Otherwise a reconcile that read a directory in a different order would re-ingest
	// everything it had already done, which is the one thing an idempotency key exists to
	// prevent.
	first := IdempotencyKeyForBatch("docs", "abc123", entries)
	second := IdempotencyKeyForBatch("docs", "abc123", []PlanEntry{entries[1], entries[0]})
	assert.Equal(t, first, second)

	// A different commit is different work, and a different base is a different base. Both have
	// to change the key or the backend would treat a second base's ingest as a repeat of the
	// first.
	assert.NotEqual(t, first, IdempotencyKeyForBatch("docs", "def456", entries))
	assert.NotEqual(t, first, IdempotencyKeyForBatch("other", "abc123", entries))

	// A changed digest is changed content. Without this the key would be a function of the
	// commit alone, and an edited file reconciled at the same commit would be skipped.
	assert.NotEqual(t, first, IdempotencyKeyForBatch("docs", "abc123", []PlanEntry{
		{ID: "a", Digest: "9999"}, {ID: "b", Digest: "2222"},
	}))
}

func TestTheContentInvariantNamesTheFieldItIsAbout(t *testing.T) {
	t.Parallel()
	// `validateContent` is unexported and the invariant it applies is the one a handler relies
	// on without checking, so it is tested here rather than through a public wrapper. What is
	// asserted is the *message*, not the refusal: "invalid content" leaves a caller with a list
	// of twenty and nothing to fix.
	require.NoError(t, validateContent(Content{
		ID: "wiki:restore", Origin: OriginAuthored, Body: "# Restore",
		Location: Location{Kind: LocationKindCorpusPath, Path: "runbooks/restore.md"},
	}), "the shape every authored document has must pass")

	cases := map[string]struct {
		content  Content
		contains string
	}{
		"no id": {
			content:  Content{Origin: OriginAuthored, Location: Location{Kind: LocationKindCorpusPath, Path: "a.md"}},
			contains: "no id",
		},
		"an origin outside the three": {
			content:  Content{ID: "x", Origin: Origin("nonsense"), Location: Location{Kind: LocationKindCorpusPath, Path: "a.md"}},
			contains: "is not one of authored, retained, derived",
		},
		"claims a body it does not have": {
			content:  Content{ID: "x", Origin: OriginAuthored, HasBody: true, Location: Location{Kind: LocationKindCorpusPath, Path: "a.md"}},
			contains: "claims to have a body",
		},
		"authored with no corpus path": {
			content:  Content{ID: "x", Origin: OriginAuthored, Location: Location{Kind: LocationKindCorpusPath}},
			contains: "no corpus path",
		},
		"retained with no document id": {
			content:  Content{ID: "x", Origin: OriginRetained, Location: Location{Kind: LocationKindDocument}},
			contains: "no document id",
		},
		"derived with no page id": {
			content:  Content{ID: "x", Origin: OriginDerived, Location: Location{Kind: LocationKindPage}},
			contains: "no page id",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validateContent(tc.content)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.contains)
		})
	}
}
