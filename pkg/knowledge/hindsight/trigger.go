package hindsight

import (
	hs "github.com/vectorize-io/hindsight/hindsight-clients/go"

	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// The trigger conversion, and one thing about it worth stating plainly.
//
// A trigger does not filter by a flat tag list. It filters by a **tag-group tree** — the same
// recursive `leaf` / `and` / `or` / `not` shape a query filter uses — which is what makes a trigger
// able to say "these two tags, or all three of those" at all. This framework's `Trigger` carries a
// flat list and a match mode, so a flat list here is a single leaf of that tree.
//
// That is a real narrowing, and it is stated rather than hidden: a caller wanting a compound
// trigger cannot express it through this contract, and `TriggerGroup` on the contract is where that
// would be added. Mapping the flat shape to one leaf is exact for everything the contract can say,
// so nothing is lost in the translation — only in what can be said.
func triggerInput(t knowledge.Trigger) *hs.MentalModelTriggerInput {
	resolved := knowledge.DefaultTrigger()
	if t.Mode != "" {
		resolved.Mode = t.Mode
	}
	if len(t.FactTypes) > 0 {
		resolved.FactTypes = t.FactTypes
	}
	if t.TagsMatch != "" {
		resolved.TagsMatch = t.TagsMatch
	}
	if t.Cron != "" {
		resolved.Cron = t.Cron
	}
	resolved.ExcludeMentalModels = t.ExcludeMentalModels
	resolved.RefreshAfterConsolidation = t.RefreshAfterConsolidation

	trigger := &hs.MentalModelTriggerInput{
		Mode:                      &resolved.Mode,
		FactTypes:                 resolved.FactTypes,
		ExcludeMentalModels:       &resolved.ExcludeMentalModels,
		RefreshAfterConsolidation: &resolved.RefreshAfterConsolidation,
	}
	if resolved.TagsMatch != "" {
		trigger.TagsMatch = *hs.NewNullableString(&resolved.TagsMatch)
	}
	if resolved.Cron != "" {
		trigger.RefreshCron = *hs.NewNullableString(&resolved.Cron)
	}
	if len(t.TagGroups) > 0 {
		// The tree, when there is one. The contract carries both forms because a flat list is
		// what a caller writes almost always, and refusing the tree because the shorthand
		// exists would make the backend's own capability unreachable.
		if nodes := filterInput(t.TagGroups); len(nodes) > 0 {
			trigger.TagGroups = nodes
		}
	} else if len(t.Tags) > 0 {
		// The shorthand, as one leaf — exact for everything a flat list can say.
		leaf := &hs.TagGroupLeaf{Tags: t.Tags}
		if resolved.TagsMatch != "" {
			leaf.Match = &resolved.TagsMatch
		}
		trigger.TagGroups = []hs.MentalModelTriggerInputTagGroupsInner{{TagGroupLeaf: leaf}}
	}
	return trigger
}

// triggerFrom reads a trigger back out of the backend's response shape.
//
// It reads the first leaf's tags rather than the whole tree, which is the inverse of triggerInput
// and is correct for anything this contract can have written. A trigger the backend created with a
// compound group is reported with its mode, match mode and fact types and without its tags, because
// flattening a tree into a list would report a filter this contract did not ask for.
func triggerFrom(t hs.MentalModelTriggerOutput) knowledge.Trigger {
	out := knowledge.Trigger{
		FactTypes:                 t.GetFactTypes(),
		TagsMatch:                 t.GetTagsMatch(),
		Cron:                      t.GetRefreshCron(),
		ExcludeMentalModels:       t.GetExcludeMentalModels(),
		RefreshAfterConsolidation: t.GetRefreshAfterConsolidation(),
	}
	if t.Mode != nil {
		out.Mode = *t.Mode
	}
	if groups := filterFrom(t.TagGroups); len(groups) > 0 {
		// A single leaf reads back through the *shorthand*, not through both.
		//
		// Filling both would describe a configuration this package's own contract refuses —
		// `tags` and `tag_groups` together is an error — so a trigger read back could not be
		// sent again. A round trip that produces a value the API rejects is a round trip that
		// has lost the one property worth having, which is that it is idempotent.
		if len(groups) == 1 && groups[0].IsLeaf() {
			out.Tags = groups[0].Leaf.Tags
			if groups[0].Leaf.Match != "" {
				out.TagsMatch = groups[0].Leaf.Match
			}
		} else {
			out.TagGroups = groups
		}
	}
	// The "nothing was reported" check has to consider the tree, and did not when it was
	// written: a trigger whose *only* filter is a compound has no mode, no fact types and no
	// flat tags, so it matched every clause and was reported as no trigger at all — a
	// configuration that silently became "use the defaults".
	if out.Mode == "" && out.TagsMatch == "" && len(out.FactTypes) == 0 &&
		len(out.Tags) == 0 && len(out.TagGroups) == 0 {
		// A trigger with nothing set is a trigger the backend did not report, and reporting the
		// defaults as if it had would be claiming a configuration nobody chose.
		return knowledge.Trigger{}
	}
	return out
}

// triggerOutput converts a trigger into the shape a manifest carries, which is the *output* form
// rather than the input one.
//
// The two are different types in the generated client and the difference is a direction, not a
// meaning: a manifest is read from a base and written back to one, so a trigger travelling inside
// one is in the output shape even though it is on its way to an input. Converting rather than
// reusing is what keeps a future divergence in the two shapes from silently dropping a field.
func triggerOutput(t knowledge.Trigger) *hs.MentalModelTriggerOutput {
	out := &hs.MentalModelTriggerOutput{
		FactTypes:                 t.FactTypes,
		ExcludeMentalModels:       &t.ExcludeMentalModels,
		RefreshAfterConsolidation: &t.RefreshAfterConsolidation,
	}
	if t.Mode != "" {
		mode := t.Mode
		out.Mode = &mode
	}
	if t.TagsMatch != "" {
		out.TagsMatch = *hs.NewNullableString(&t.TagsMatch)
	}
	if t.Cron != "" {
		out.RefreshCron = *hs.NewNullableString(&t.Cron)
	}
	if len(t.TagGroups) > 0 {
		out.TagGroups = filterOutput(t.TagGroups)
	} else if len(t.Tags) > 0 {
		leaf := &hs.TagGroupLeaf{Tags: t.Tags}
		if t.TagsMatch != "" {
			leaf.Match = &t.TagsMatch
		}
		out.TagGroups = []hs.MentalModelRefreshScopeTagGroupsInner{{TagGroupLeaf: leaf}}
	}
	return out
}
