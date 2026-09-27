package hindsight

import (
	"fmt"
	"strings"

	hs "github.com/vectorize-io/hindsight/hindsight-clients/go"

	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// The tag-group tree, translated.
//
// A tag group discriminates on its **JSON key** and carries no wrapper and no `type` field: a leaf
// is `{"tags": [...]}`, an `and` is `{"and": [...]}`, an `or` is `{"or": [...]}`, a `not` is
// `{"not": {...}}`. The generated client's oneof is serialised bare, in both directions, and that
// is asymmetric in an easy-to-miss way:
//
//   - **Outbound**, `&hs.TagGroupLeaf{Tags: …}` marshals to `{"tags": […]}` and marshals to the
//     same bare form it unmarshals from, so the outbound direction is correct as written and
//     needed no accommodation. A wrapped `{"leaf": {…}}` is *not* the wire form and is rejected on
//     the way back in with `data failed to match schemas in anyOf(...)`.
//   - **Inbound**, the four alternatives are tried in order and the first that decodes wins, so a
//     shape that matches two of them is read as the earlier one. Each has a distinct key, so a
//     well-formed node is unambiguous — but a node with *no* recognised key fails to decode rather
//     than decoding as an empty leaf, and that is worth stating rather than discovering.
//
// And the algebra is four keys with no further restriction: a `not` takes any of them, including a
// compound, because the generated type is itself a `oneof` over the same four. An earlier version
// of this file claimed a `not` could only take a leaf. That was wrong, and it was wrong from a
// shape I had invented rather than one the API description declared — which is the same mistake in
// miniature as trusting a spec, and the reason the claim is recorded here.

// filterInput converts this package's tree into the backend's.
//
// A flat `Tags` list is a single leaf, which is exact for everything the shorthand can say — so
// nothing is lost in the translation, only in what the shorthand can say. The tree is the way to say
// the rest.
func filterInput(groups []knowledge.TagFilter) []hs.MentalModelTriggerInputTagGroupsInner {
	if len(groups) == 0 {
		return nil
	}
	out := make([]hs.MentalModelTriggerInputTagGroupsInner, 0, len(groups))
	for _, g := range groups {
		if node, ok := filterNodeInput(g); ok {
			out = append(out, node)
		}
	}
	return out
}

func filterNodeInput(f knowledge.TagFilter) (hs.MentalModelTriggerInputTagGroupsInner, bool) {
	switch {
	case f.Leaf != nil:
		leaf := &hs.TagGroupLeaf{Tags: f.Leaf.Tags}
		if f.Leaf.Match != "" {
			leaf.Match = &f.Leaf.Match
		}
		if f.Leaf.Resolve != "" {
			leaf.Resolve = &f.Leaf.Resolve
		}
		return hs.MentalModelTriggerInputTagGroupsInner{TagGroupLeaf: leaf}, true
	case len(f.And) > 0:
		node := &hs.TagGroupAndInput{}
		for _, sub := range f.And {
			if child, ok := filterNodeInput(sub); ok {
				node.And = append(node.And, child)
			}
		}
		return hs.MentalModelTriggerInputTagGroupsInner{TagGroupAndInput: node}, len(node.And) > 0
	case len(f.Or) > 0:
		node := &hs.TagGroupOrInput{}
		for _, sub := range f.Or {
			if child, ok := filterNodeInput(sub); ok {
				node.Or = append(node.Or, child)
			}
		}
		return hs.MentalModelTriggerInputTagGroupsInner{TagGroupOrInput: node}, len(node.Or) > 0
	case f.Not != nil:
		// A `not` wraps any of the four, so the same node builder is reused. A `not` with
		// nothing usable inside is dropped rather than sent as an empty node, which would
		// decode as "no filter" and silently *widen* the trigger — the opposite of a
		// negation.
		inner, ok := filterNodeInput(*f.Not)
		if !ok {
			return hs.MentalModelTriggerInputTagGroupsInner{}, false
		}
		node := &hs.TagGroupNotInput{Not: hs.Not{
			TagGroupLeaf:     inner.TagGroupLeaf,
			TagGroupAndInput: inner.TagGroupAndInput,
			TagGroupOrInput:  inner.TagGroupOrInput,
			TagGroupNotInput: inner.TagGroupNotInput,
		}}
		return hs.MentalModelTriggerInputTagGroupsInner{TagGroupNotInput: node}, true
	}
	return hs.MentalModelTriggerInputTagGroupsInner{}, false
}

// filterFrom reads a tree back out of the backend's response shape.
func filterFrom(groups []hs.MentalModelRefreshScopeTagGroupsInner) []knowledge.TagFilter {
	if len(groups) == 0 {
		return nil
	}
	out := make([]knowledge.TagFilter, 0, len(groups))
	for _, g := range groups {
		if f, ok := filterNodeFrom(g); ok {
			out = append(out, f)
		}
	}
	return out
}

func filterNodeFrom(g hs.MentalModelRefreshScopeTagGroupsInner) (knowledge.TagFilter, bool) {
	switch {
	case g.TagGroupLeaf != nil:
		leaf := knowledge.TagFilter{Leaf: &knowledge.TagLeaf{Tags: g.TagGroupLeaf.GetTags()}}
		if m := g.TagGroupLeaf.GetMatch(); m != "" {
			leaf.Leaf.Match = m
		}
		if r := g.TagGroupLeaf.GetResolve(); r != "" {
			leaf.Leaf.Resolve = r
		}
		return leaf, true
	case g.TagGroupAndOutput != nil:
		f := knowledge.TagFilter{}
		for _, sub := range g.TagGroupAndOutput.GetAnd() {
			if child, ok := filterNodeFrom(sub); ok {
				f.And = append(f.And, child)
			}
		}
		return f, len(f.And) > 0
	case g.TagGroupOrOutput != nil:
		f := knowledge.TagFilter{}
		for _, sub := range g.TagGroupOrOutput.GetOr() {
			if child, ok := filterNodeFrom(sub); ok {
				f.Or = append(f.Or, child)
			}
		}
		return f, len(f.Or) > 0
	case g.TagGroupNotOutput != nil:
		inner := g.TagGroupNotOutput.GetNot()
		wrapped, ok := filterNodeFrom(hs.MentalModelRefreshScopeTagGroupsInner{
			TagGroupLeaf:      inner.TagGroupLeaf,
			TagGroupAndOutput: inner.TagGroupAndOutput,
			TagGroupOrOutput:  inner.TagGroupOrOutput,
			TagGroupNotOutput: inner.TagGroupNotOutput,
		})
		if !ok {
			return knowledge.TagFilter{}, false
		}
		return knowledge.TagFilter{Not: &wrapped}, true
	}
	return knowledge.TagFilter{}, false
}

// describeFilter renders a filter as a sentence, for a refusal that has to name what it saw.
//
// A tree has no single "the value", and a message saying `tags_match is "sometimes"` is more use
// than one saying the filter is invalid, because it says which part and what it was.
func describeFilter(f knowledge.TagFilter) string {
	switch {
	case f.Leaf != nil:
		parts := []string{"the tags " + strings.Join(quoteAll(f.Leaf.Tags), ", ")}
		if f.Leaf.Match != "" {
			parts = append(parts, fmt.Sprintf("matching %q", f.Leaf.Match))
		}
		return strings.Join(parts, " ")
	case len(f.And) > 0:
		return "an `and` of " + describeAll(f.And)
	case len(f.Or) > 0:
		return "an `or` of " + describeAll(f.Or)
	case f.Not != nil:
		return "a `not` of " + describeFilter(*f.Not)
	}
	return "an empty filter"
}

func describeAll(filters []knowledge.TagFilter) string {
	parts := make([]string, 0, len(filters))
	for _, f := range filters {
		parts = append(parts, describeFilter(f))
	}
	return strings.Join(parts, " and ")
}

// filterOutput converts this package's tree into the response shape.
//
// A separate function rather than a shared one because the generated input and output types are
// distinct, and the difference is a direction rather than a meaning. Reusing the input type would
// be a translation that compiles and would not round-trip — the two shapes have separately-declared
// nested types — so the conversion is done twice, deliberately, and the second one is tested.
func filterOutput(groups []knowledge.TagFilter) []hs.MentalModelRefreshScopeTagGroupsInner {
	if len(groups) == 0 {
		return nil
	}
	out := make([]hs.MentalModelRefreshScopeTagGroupsInner, 0, len(groups))
	for _, g := range groups {
		if node, ok := filterNodeOutput(g); ok {
			out = append(out, node)
		}
	}
	return out
}

func filterNodeOutput(f knowledge.TagFilter) (hs.MentalModelRefreshScopeTagGroupsInner, bool) {
	switch {
	case f.Leaf != nil:
		// The output oneof reuses the *same* leaf type as the input one, so the same builder
		// serves both directions. Only the three compounds have separate types, and the
		// asymmetry is the generator's rather than the API's.
		leaf := &hs.TagGroupLeaf{Tags: f.Leaf.Tags}
		if f.Leaf.Match != "" {
			leaf.Match = &f.Leaf.Match
		}
		if f.Leaf.Resolve != "" {
			leaf.Resolve = &f.Leaf.Resolve
		}
		return hs.MentalModelRefreshScopeTagGroupsInner{TagGroupLeaf: leaf}, true
	case len(f.And) > 0:
		node := &hs.TagGroupAndOutput{}
		for _, sub := range f.And {
			if child, ok := filterNodeOutput(sub); ok {
				node.And = append(node.And, child)
			}
		}
		return hs.MentalModelRefreshScopeTagGroupsInner{TagGroupAndOutput: node}, len(node.And) > 0
	case len(f.Or) > 0:
		node := &hs.TagGroupOrOutput{}
		for _, sub := range f.Or {
			if child, ok := filterNodeOutput(sub); ok {
				node.Or = append(node.Or, child)
			}
		}
		return hs.MentalModelRefreshScopeTagGroupsInner{TagGroupOrOutput: node}, len(node.Or) > 0
	case f.Not != nil:
		inner, ok := filterNodeOutput(*f.Not)
		if !ok {
			return hs.MentalModelRefreshScopeTagGroupsInner{}, false
		}
		node := &hs.TagGroupNotOutput{Not: hs.Not1{
			TagGroupLeaf:      inner.TagGroupLeaf,
			TagGroupAndOutput: inner.TagGroupAndOutput,
			TagGroupOrOutput:  inner.TagGroupOrOutput,
			TagGroupNotOutput: inner.TagGroupNotOutput,
		}}
		return hs.MentalModelRefreshScopeTagGroupsInner{TagGroupNotOutput: node}, true
	}
	return hs.MentalModelRefreshScopeTagGroupsInner{}, false
}
