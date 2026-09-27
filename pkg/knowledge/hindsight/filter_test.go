package hindsight

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hs "github.com/vectorize-io/hindsight/hindsight-clients/go"

	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// The tag-group tree, in both directions.
//
// The wire form was established by probing the generated client rather than by reading the API
// description, and it is the kind of thing a reader would get wrong: the `oneof` is serialised
// **bare**, so a leaf is `{"tags": [...]}` and not `{"leaf": {"tags": [...]}}`. These tests pin the
// form, because a wrong guess here fails as a decode error naming a generated type rather than as
// anything a reader could act on.

func TestALeafGoesOutAsABareTagSetAndNotAsAWrappedOne(t *testing.T) {
	t.Parallel()
	sent := triggerInput(knowledge.Trigger{Mode: "delta", Tags: []string{"runbook", "safety"}})
	raw, err := json.Marshal(sent)
	require.NoError(t, err)
	body := string(raw)

	// The match rides on the leaf as well as on the trigger, so the assertion is about the
	// *shape* — a bare object carrying `tags` — and not about the exact bytes.
	assert.Contains(t, body, `"tag_groups":[{"`)
	assert.Contains(t, body, `"tags":["runbook","safety"]`,
		"a leaf is a bare tag set, because the oneof has no wrapper on the wire")
	// The keys we sent, checked against the four the backend can decode. A `leaf` key here is
	// the mistake this test exists to catch: the generator's oneof is bare, and a wrapped node
	// matches none of the four alternatives, so the backend would answer with
	// `data failed to match schemas in anyOf(...)` — a generated type's name, and nothing
	// about the field that was wrong.
	var sentShape struct {
		TagGroups []map[string]json.RawMessage `json:"tag_groups"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &sentShape))
	require.NotEmpty(t, sentShape.TagGroups)
	for _, node := range sentShape.TagGroups {
		require.Contains(t, node, "tags", "a leaf carries its tags directly")
		for key := range node {
			assert.NotEqual(t, "leaf", key,
				"`leaf` is not on the wire; the oneof is bare, so a wrapped node matches none of the four alternatives")
		}
	}

	// And the wrapped form really is undecodable, so the test is asserting a fact about the
	// wire rather than about this file's own guess.
	var wrapped hs.MentalModelRefreshScopeTagGroupsInner
	err = json.Unmarshal([]byte(`{"leaf":{"tags":["runbook"]}}`), &wrapped)
	require.Error(t, err, "a wrapped node decodes as nothing at all")
	assert.Contains(t, err.Error(), "anyOf", err.Error())
}

func TestTheFlatShorthandIsSentAsOneLeafAndReadsBackAsTheShorthand(t *testing.T) {
	t.Parallel()
	sent := triggerInput(knowledge.Trigger{Mode: "delta", Tags: []string{"runbook"}, TagsMatch: "all_strict"})
	require.Len(t, sent.TagGroups, 1)
	leaf := sent.TagGroups[0].TagGroupLeaf
	require.NotNil(t, leaf, "the shorthand is one leaf, which is exact for everything a flat list can say")
	assert.Equal(t, []string{"runbook"}, leaf.GetTags())
	assert.Equal(t, "all_strict", leaf.GetMatch())

	// And it reads back as the shorthand rather than only as a tree, because a caller that set
	// `tags` reads `tags` back and a caller that set a tree reads `tag_groups` back.
	out := triggerFrom(responseTrigger(t, sent.TagGroups))
	assert.Equal(t, []string{"runbook"}, out.Tags)
	assert.Equal(t, "all_strict", out.TagsMatch)
	// Exactly one of the two forms, because a request carrying both is refused — so a trigger
	// read back has to be sendable again.
	assert.Empty(t, out.TagGroups, "one leaf reads back through the shorthand, not both")
}

func TestACompoundTriggerRoundTripsThroughTheWire(t *testing.T) {
	t.Parallel()
	// "these two tags, or all three of those" — the case a flat list cannot express and the
	// reason `TagGroups` exists at all.
	filter := knowledge.TagFilter{
		Or: []knowledge.TagFilter{
			{Leaf: &knowledge.TagLeaf{Tags: []string{"a", "b"}, Match: "all"}},
			{And: []knowledge.TagFilter{
				{Leaf: &knowledge.TagLeaf{Tags: []string{"c"}}},
				{Leaf: &knowledge.TagLeaf{Tags: []string{"d"}}},
			}},
		},
	}
	require.NoError(t, filter.Validate("test"))

	sent := triggerInput(knowledge.Trigger{Mode: "delta", TagGroups: []knowledge.TagFilter{filter}})
	raw, err := json.Marshal(sent.TagGroups)
	require.NoError(t, err)
	body := string(raw)
	// n-ary, and the four keys are the discriminator. An `and` of two is one node with both
	// members, not a chain of pairs.
	assert.Contains(t, body, `"or":[`)
	assert.Contains(t, body, `"and":[{"tags":["c"]},{"tags":["d"]}]`)

	back := triggerFrom(responseTrigger(t, sent.TagGroups))
	require.Len(t, back.TagGroups, 1, "a compound reads back as a tree, not as the shorthand")
	require.Len(t, back.TagGroups[0].Or, 2)
	assert.Equal(t, []string{"a", "b"}, back.TagGroups[0].Or[0].Leaf.Tags)
	assert.Equal(t, "all", back.TagGroups[0].Or[0].Leaf.Match)
	require.Len(t, back.TagGroups[0].Or[1].And, 2, "an n-ary and keeps its arity")
	assert.Equal(t, []string{"c"}, back.TagGroups[0].Or[1].And[0].Leaf.Tags)
	assert.Equal(t, []string{"d"}, back.TagGroups[0].Or[1].And[1].Leaf.Tags)
}

func TestANotWrapsACompoundAndSaysSoOnTheWire(t *testing.T) {
	t.Parallel()
	// An earlier version of this file claimed a `not` could only take a leaf, on the strength of a
	// shape the test had invented. The generated type is itself a `oneof` over the same four
	// alternatives, so a `not` takes any of them — and the test that "established" the
	// restriction is corrected here rather than left to be believed.
	filter := knowledge.TagFilter{Not: &knowledge.TagFilter{
		Or: []knowledge.TagFilter{
			{Leaf: &knowledge.TagLeaf{Tags: []string{"draft"}}},
			{Leaf: &knowledge.TagLeaf{Tags: []string{"deprecated"}}},
		},
	}}
	require.NoError(t, filter.Validate("test"), "a not around a compound is expressible")

	sent := triggerInput(knowledge.Trigger{Mode: "delta", TagGroups: []knowledge.TagFilter{filter}})
	raw, err := json.Marshal(sent.TagGroups)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"not":{"or":[`)

	back := triggerFrom(responseTrigger(t, sent.TagGroups))
	require.Len(t, back.TagGroups, 1)
	require.NotNil(t, back.TagGroups[0].Not, "the not survived the round trip")
	assert.Len(t, back.TagGroups[0].Not.Or, 2)
}

func TestAnEmptyFilterIsRefusedRatherThanSentAsSomethingThatMatchesNothing(t *testing.T) {
	t.Parallel()
	// "No filter" and "a filter that matches nothing" are different: the first takes every input
	// and the second produces an empty document that looks correct. A trigger whose filter is a
	// group with no members is the second, and it is the one shape a reader cannot diagnose.
	err := knowledge.TagFilter{}.Validate("tag_groups[0]")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches nothing")
}

func TestALeafWithNoTagsIsRefusedForTheSameReason(t *testing.T) {
	t.Parallel()
	err := knowledge.TagFilter{Leaf: &knowledge.TagLeaf{}}.Validate("tag_groups[0].leaf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tags")
}

func TestAnUnknownMatchModeIsRefusedAndNamed(t *testing.T) {
	t.Parallel()
	err := knowledge.TagFilter{Leaf: &knowledge.TagLeaf{Tags: []string{"a"}, Match: "sometimes"}}.Validate("tag_groups[0]")
	require.Error(t, err)
	// The accepted values are named, because "invalid filter" leaves a caller to guess the
	// vocabulary — and the vocabulary is in a field they did not set themselves.
	assert.Contains(t, err.Error(), "any_strict")
	assert.Contains(t, err.Error(), "sometimes")
}

func TestAFilterErrorNamesWhereInTheTreeItWentWrong(t *testing.T) {
	t.Parallel()
	// A tree has no single obvious "the filter". A caller handed six levels needs to be told
	// which, or the fix is a search.
	err := knowledge.TagFilter{
		Or: []knowledge.TagFilter{
			{Leaf: &knowledge.TagLeaf{Tags: []string{"a"}}},
			{And: []knowledge.TagFilter{{Leaf: &knowledge.TagLeaf{Tags: []string{"b"}, Match: "nope"}}}},
		},
	}.Validate("tag_groups")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tag_groups.or[1].and[0]", err.Error())
}

// responseTrigger is the backend's response shape, built from the input nodes.
//
// The input and output tag-group types are separately declared in the generated client — the leaf
// is shared, the three compounds are not — so a round trip has to cross that boundary rather than
// reuse the value. Building it this way means a test that passes here has actually been through
// both conversions.
func responseTrigger(t *testing.T, nodes []hs.MentalModelTriggerInputTagGroupsInner) hs.MentalModelTriggerOutput {
	t.Helper()
	out := hs.MentalModelTriggerOutput{TagGroups: make([]hs.MentalModelRefreshScopeTagGroupsInner, 0, len(nodes))}
	for _, n := range nodes {
		node := hs.MentalModelRefreshScopeTagGroupsInner{}
		if n.TagGroupLeaf != nil {
			node.TagGroupLeaf = n.TagGroupLeaf
		}
		if n.TagGroupAndInput != nil {
			group := &hs.TagGroupAndOutput{}
			for _, sub := range n.TagGroupAndInput.GetAnd() {
				group.And = append(group.And, responseNode(t, sub))
			}
			node.TagGroupAndOutput = group
		}
		if n.TagGroupOrInput != nil {
			group := &hs.TagGroupOrOutput{}
			for _, sub := range n.TagGroupOrInput.GetOr() {
				group.Or = append(group.Or, responseNode(t, sub))
			}
			node.TagGroupOrOutput = group
		}
		if n.TagGroupNotInput != nil {
			node.TagGroupNotOutput = &hs.TagGroupNotOutput{Not: responseNot(t, n.TagGroupNotInput.GetNot())}
		}
		out.TagGroups = append(out.TagGroups, node)
	}
	return out
}

func responseNode(t *testing.T, n hs.MentalModelTriggerInputTagGroupsInner) hs.MentalModelRefreshScopeTagGroupsInner {
	t.Helper()
	node := hs.MentalModelRefreshScopeTagGroupsInner{}
	if n.TagGroupLeaf != nil {
		node.TagGroupLeaf = n.TagGroupLeaf
	}
	if n.TagGroupAndInput != nil {
		group := &hs.TagGroupAndOutput{}
		for _, sub := range n.TagGroupAndInput.GetAnd() {
			group.And = append(group.And, responseNode(t, sub))
		}
		node.TagGroupAndOutput = group
	}
	if n.TagGroupOrInput != nil {
		group := &hs.TagGroupOrOutput{}
		for _, sub := range n.TagGroupOrInput.GetOr() {
			group.Or = append(group.Or, responseNode(t, sub))
		}
		node.TagGroupOrOutput = group
	}
	if n.TagGroupNotInput != nil {
		node.TagGroupNotOutput = &hs.TagGroupNotOutput{Not: responseNot(t, n.TagGroupNotInput.GetNot())}
	}
	return node
}

func responseNot(t *testing.T, n hs.Not) hs.Not1 {
	t.Helper()
	out := hs.Not1{}
	if n.TagGroupLeaf != nil {
		out.TagGroupLeaf = n.TagGroupLeaf
	}
	if n.TagGroupAndInput != nil {
		group := &hs.TagGroupAndOutput{}
		for _, sub := range n.TagGroupAndInput.GetAnd() {
			group.And = append(group.And, responseNode(t, sub))
		}
		out.TagGroupAndOutput = group
	}
	if n.TagGroupOrInput != nil {
		group := &hs.TagGroupOrOutput{}
		for _, sub := range n.TagGroupOrInput.GetOr() {
			group.Or = append(group.Or, responseNode(t, sub))
		}
		out.TagGroupOrOutput = group
	}
	if n.TagGroupNotInput != nil {
		out.TagGroupNotOutput = &hs.TagGroupNotOutput{Not: responseNot(t, n.TagGroupNotInput.GetNot())}
	}
	return out
}
