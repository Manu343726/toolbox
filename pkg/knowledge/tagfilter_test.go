package knowledge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tag filter's rules, tested where the rules live.
//
// The algebra is the backend's, so these could have sat only in the adapter. They are here as well
// because the type is the framework's, and because a rule that is only checked through a wire
// translation is one failure removed from being noticed: if `Validate` ever stopped being called,
// the adapter's tests would still pass while the filter accepted a tree the backend cannot express.

// The validator's whole job is to refuse the two shapes that produce a page that stays empty with
// no visible error. "No filter" and "a filter that matches nothing" are different: the first takes
// every input, the second produces an empty document that looks correct.
func TestTheFilterRefusesAnythingThatWouldMatchNothing(t *testing.T) {
	t.Parallel()
	for name, filter := range map[string]TagFilter{
		"nothing at all":            {},
		"a group with no members":   {And: []TagFilter{}},
		"an or with no members":     {Or: []TagFilter{}},
		"a leaf with no tags":       {Leaf: &TagLeaf{}},
		"a leaf with an empty list": {Leaf: &TagLeaf{Tags: []string{}}},
		"a not with nothing in it":  {Not: &TagFilter{}},
	} {
		err := filter.Validate("tag_groups[0]")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "matches nothing", name)
	}
}

func TestTheFilterRefusesAMatchModeItCannotTranslate(t *testing.T) {
	t.Parallel()
	// The accepted values are named in the refusal, because "invalid filter" leaves a caller to
	// guess a vocabulary they did not choose.
	err := TagFilter{Leaf: &TagLeaf{Tags: []string{"a"}, Match: "sometimes"}}.Validate("tag_groups")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "any_strict")
	assert.Contains(t, err.Error(), "sometimes")
}

func TestTheFilterAcceptsEveryMatchModeTheBackendHas(t *testing.T) {
	t.Parallel()
	// A validator that only knew one of them would refuse a perfectly good filter, and a trigger
	// rejected at configuration time is a trigger somebody gives up on.
	for _, mode := range []string{"", "any", "all", "any_strict", "all_strict"} {
		assert.NoError(t, TagFilter{Leaf: &TagLeaf{Tags: []string{"a"}, Match: mode}}.Validate("t"),
			"match mode %q", mode)
	}
	// `resolve` is a second axis on a leaf and is not constrained beyond being a string, because
	// the backend's vocabulary for it is not enumerated anywhere this package could check against.
	assert.NoError(t, TagFilter{Leaf: &TagLeaf{Tags: []string{"a"}, Resolve: "exact"}}.Validate("t"))
}

func TestTheFilterAcceptsEveryShapeItCanCarry(t *testing.T) {
	t.Parallel()
	for name, filter := range map[string]TagFilter{
		"a bare leaf": {Leaf: &TagLeaf{Tags: []string{"a"}}},
		"an and": {And: []TagFilter{
			{Leaf: &TagLeaf{Tags: []string{"a"}}},
			{Leaf: &TagLeaf{Tags: []string{"b"}}},
		}},
		"an or": {Or: []TagFilter{
			{Leaf: &TagLeaf{Tags: []string{"a"}}},
			{And: []TagFilter{{Leaf: &TagLeaf{Tags: []string{"b"}}}}},
		}},
		"a not around a leaf":     {Not: &TagFilter{Leaf: &TagLeaf{Tags: []string{"draft"}}}},
		"a not around a compound": {Not: &TagFilter{Or: []TagFilter{{Leaf: &TagLeaf{Tags: []string{"a"}}}}}},
		"a not around a not":      {Not: &TagFilter{Not: &TagFilter{Leaf: &TagLeaf{Tags: []string{"a"}}}}},
		"nesting four deep": {Or: []TagFilter{
			{And: []TagFilter{
				{Or: []TagFilter{{Leaf: &TagLeaf{Tags: []string{"a"}}}}},
				{Not: &TagFilter{Leaf: &TagLeaf{Tags: []string{"b"}}}},
			}},
			{Leaf: &TagLeaf{Tags: []string{"c"}}},
		}},
	} {
		assert.NoError(t, filter.Validate("tag_groups[0]"), name)
	}
}

func TestTheFilterSaysWhereInTheTreeItWentWrong(t *testing.T) {
	t.Parallel()
	// A tree has no single obvious "the filter". A caller handed several levels needs to be told
	// which one, or the fix is a search through a structure they built to save writing one filter.
	for _, tc := range []struct {
		name   string
		filter TagFilter
		path   string
	}{
		{"at the root", TagFilter{Leaf: &TagLeaf{}}, "trigger.tag_groups[0]"},
		{"inside an and", TagFilter{And: []TagFilter{
			{Leaf: &TagLeaf{Tags: []string{"ok"}}},
			{Leaf: &TagLeaf{}},
		}}, "trigger.tag_groups[0].and[1]"},
		{"inside an or inside an and", TagFilter{And: []TagFilter{
			{Or: []TagFilter{{Leaf: &TagLeaf{Tags: []string{"ok"}}}, {Leaf: &TagLeaf{Match: "sometimes"}}}},
		}}, "trigger.tag_groups[0].and[0].or[1]"},
		{"inside a not", TagFilter{Not: &TagFilter{And: []TagFilter{}}}, "trigger.tag_groups[0].not"},
	} {
		err := tc.filter.Validate(tc.path)
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), tc.path, tc.name)
	}
}

func TestTheFiltersErrorIsReadableWithoutThePathItWasGiven(t *testing.T) {
	t.Parallel()
	// A caller constructing a filter programmatically has no index to name, and an error saying
	// "at . and[2]" is less useful than one that names the shape.
	err := TagFilter{And: []TagFilter{{}, {}}}.Validate("")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "at .")
	assert.Contains(t, err.Error(), "and[0]")
}

func TestALeafAndAnEmptyFilterAreDistinguishedWithoutParsing(t *testing.T) {
	t.Parallel()
	// Both are asked in the conversion — one to decide whether a single leaf should be reported
	// through the flat shorthand, the other to tell "no filter" from "a filter that matches
	// nothing". So both answers have to be reachable without looking at the struct.
	assert.True(t, TagFilter{Leaf: &TagLeaf{Tags: []string{"a"}}}.IsLeaf())
	assert.False(t, TagFilter{And: []TagFilter{{Leaf: &TagLeaf{Tags: []string{"a"}}}}}.IsLeaf())
	assert.False(t, TagFilter{}.IsLeaf(), "an empty filter is not a leaf: it is the absence of one")

	// A leaf with no tags *is* a leaf and is *not* empty, which is the pair that matters: it is a
	// filter that constrains something in shape and matches nothing in fact, and conflating the
	// two is how a "no filter" is sent where a refusal belonged.
	emptyLeaf := TagFilter{Leaf: &TagLeaf{}}
	assert.True(t, emptyLeaf.IsLeaf())
	assert.False(t, emptyLeaf.Empty(), "a leaf with no tags is present, which is why it is refused rather than ignored")

	assert.True(t, TagFilter{}.Empty())
	assert.False(t, TagFilter{Leaf: &TagLeaf{Tags: []string{"a"}}}.Empty())
	assert.False(t, TagFilter{And: []TagFilter{{Leaf: &TagLeaf{Tags: []string{"a"}}}}}.Empty())
	assert.False(t, TagFilter{Not: &TagFilter{Leaf: &TagLeaf{Tags: []string{"a"}}}}.Empty())
}

// --- narrowing a plan to one directory.

func planFixture() Plan {
	entry := func(path string) PlanEntry {
		return PlanEntry{ID: path, Path: path, SourceKey: "docs:" + path}
	}
	return Plan{
		BaseID: "docs", Commit: "abc123",
		Created:   []PlanEntry{entry("runbooks/restore.md")},
		Updated:   []PlanEntry{entry("runbooks/rollback.md")},
		Unchanged: []PlanEntry{entry("runbooks/index.md")},
		Deleted:   []PlanEntry{entry("architecture/overview.md")},
		Moved: []PlanMove{
			{From: entry("runbooks/old.md"), To: entry("runbooks/new.md")},
			{From: entry("architecture/old.md"), To: entry("architecture/new.md")},
		},
		Drift:       []Drift{{ClaimID: "page-1", ClaimText: "there is no overview file", Detection: "the corpus has no such file"}},
		Warnings:    []string{"architecture/overview.md is not a markdown file"},
		UnreadFiles: 1,
	}
}

func TestNarrowingAPlanKeepsOnlyWhatIsUnderTheDirectory(t *testing.T) {
	t.Parallel()
	narrowed := planFixture().RestrictedTo("runbooks")

	require.Len(t, narrowed.Created, 1)
	assert.Equal(t, "runbooks/restore.md", narrowed.Created[0].Path)
	require.Len(t, narrowed.Updated, 1)
	require.Len(t, narrowed.Unchanged, 1)
	assert.Empty(t, narrowed.Deleted, "a deletion elsewhere is not a fact about this directory")
	require.Len(t, narrowed.Moved, 1)
	// A move is judged by where it *lands*, not where it started: the effect on a directory is the
	// file arriving in it, and a file leaving is not an arrival.
	assert.Equal(t, "runbooks/new.md", narrowed.Moved[0].To.Path)
	assert.Equal(t, "docs", narrowed.BaseID)
	assert.Equal(t, "abc123", narrowed.Commit)
}

func TestNarrowingAPlanDropsDriftRatherThanReportingPartOfIt(t *testing.T) {
	t.Parallel()
	// This is the decision that is easy to get wrong in the other direction. A derived claim is
	// contradicted by the corpus *as a whole* — a page that says "there is no overview file" is
	// wrong because the overview was deleted, wherever the overview lived. Attributing that to
	// `runbooks/` would be a claim about runbooks that is not true, and a reader would go looking
	// for a problem in the wrong directory.
	assert.Empty(t, planFixture().RestrictedTo("runbooks").Drift,
		"a narrowed plan reports no drift rather than a partial answer that reads like a complete one")
}

func TestNarrowingAPlanKeepsTheWarningsWholesale(t *testing.T) {
	t.Parallel()
	// A warning about a file outside the preview is still true, and a reader who narrowed to one
	// directory still needs to know a file elsewhere is unreadable. Hiding it because it names a
	// path they did not ask for would be the plan being tidy at the expense of being honest.
	narrowed := planFixture().RestrictedTo("runbooks")
	assert.Equal(t, []string{"architecture/overview.md is not a markdown file"}, narrowed.Warnings)
	assert.Equal(t, 1, narrowed.UnreadFiles, "and the count it belongs to")
}

func TestNarrowingAPlanRecomputesTheDigestSoAConfirmationMatchesWhatWasPreviewed(t *testing.T) {
	t.Parallel()
	// The whole reason the digest is recomputed: a confirmation refers to the plan a person read.
	// If the narrowed plan kept the whole-corpus digest, then previewing one directory and
	// confirming would fail — or worse, succeed against a different document than the one that was
	// approved.
	whole := planFixture()
	whole.PlanDigest = whole.digest()
	narrowed := whole.RestrictedTo("runbooks")

	assert.NotEmpty(t, narrowed.PlanDigest)
	assert.NotEqual(t, whole.PlanDigest, narrowed.PlanDigest,
		"a narrowed plan is a different document, and confirming it must not accept the whole-corpus digest")
	// And it is the narrowed plan's own digest, so confirming a preview round-trips.
	assert.Equal(t, narrowed.digest(), narrowed.PlanDigest)
	// Narrowing to nothing is also a different document rather than the original.
	empty := whole.RestrictedTo("architecture/nowhere")
	assert.NotEqual(t, whole.PlanDigest, empty.PlanDigest)
}

func TestNarrowingAPlanToNothingIsAPlanRatherThanAFailure(t *testing.T) {
	t.Parallel()
	// A directory with nothing to do is an answer — "this is current" — and it is the answer a
	// reader asking about a quiet directory deserves. Refusing, or returning the whole plan, would
	// both be worse than the truth.
	narrowed := planFixture().RestrictedTo("architecture/nowhere")
	assert.Empty(t, narrowed.Created)
	assert.Empty(t, narrowed.Updated)
	assert.Empty(t, narrowed.Unchanged)
	assert.Empty(t, narrowed.Deleted)
	assert.Empty(t, narrowed.Moved)
	assert.Equal(t, "docs", narrowed.BaseID)
}

func TestNarrowingAPlanToNoDirectoryChangesNothing(t *testing.T) {
	t.Parallel()
	// No subtree means no narrowing, and the plan comes back as it was — including its digest, so
	// a caller that passes an empty filter has not silently invalidated a confirmation.
	whole := planFixture()
	whole.PlanDigest = whole.digest()
	for _, empty := range []string{"", "   ", "/", "./"} {
		narrowed := whole.RestrictedTo(empty)
		assert.Equal(t, whole.PlanDigest, narrowed.PlanDigest, "subtree %q", empty)
		assert.Len(t, narrowed.Created, len(whole.Created), "subtree %q", empty)
	}
}

func TestNarrowingAPlanDoesNotMatchASiblingWithTheSamePrefix(t *testing.T) {
	t.Parallel()
	// "runbooks" is a prefix of "runbooks-archive", and a `HasPrefix(path, "runbooks")` would put
	// every file in the archive into a preview of the runbooks directory. The separator is what
	// makes it a directory boundary rather than a string prefix.
	p := planFixture()
	p.Created = append(p.Created, PlanEntry{ID: "runbooks-archive/old.md",
		Path: "runbooks-archive/old.md", SourceKey: "docs:runbooks-archive/old.md"})
	narrowed := p.RestrictedTo("runbooks")
	require.Len(t, narrowed.Created, 1)
	assert.Equal(t, "runbooks/restore.md", narrowed.Created[0].Path)
}

// A create and a replace are told apart by `PriorDigest` alone, and the apply's ingest counts rest
// on that. The accessor that used to spell the test out was removed as dead code — its only caller
// was a clause that could never fire — so the invariant it encoded is asserted here directly,
// because a plan that disagrees with itself about which files changed is a plan whose counts are
// wrong in a way nothing else would notice.
func TestAPlanEntrySaysWhichItIsByHavingAPriorDigestOrNot(t *testing.T) {
	t.Parallel()
	created := PlanEntry{Path: "a.md", Digest: "sha256:aaaa"}
	updated := PlanEntry{Path: "b.md", Digest: "sha256:bbbb", PriorDigest: "sha256:cccc"}
	unchanged := PlanEntry{Path: "c.md", Digest: "sha256:dddd", PriorDigest: "sha256:dddd"}

	// This is what `Apply` reads, and it is the only thing it reads.
	assert.Empty(t, created.PriorDigest, "a create has no prior, and is counted as ingested")
	assert.NotEmpty(t, updated.PriorDigest, "a replace has one, and is counted as replaced")
	assert.NotEqual(t, updated.Digest, updated.PriorDigest, "and it differs, which is what makes it a change")
	// The third case is the one a batch never contains, and saying so is the point: the apply's
	// classification is unconditional precisely because an unchanged entry cannot reach it.
	assert.Equal(t, unchanged.Digest, unchanged.PriorDigest, "an unchanged entry has a prior equal to its own")
}
