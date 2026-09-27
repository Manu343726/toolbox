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
	if len(t.Tags) > 0 {
		trigger.TagGroups = []hs.MentalModelTriggerInputTagGroupsInner{{
			TagGroupLeaf: &hs.TagGroupLeaf{Tags: t.Tags},
		}}
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
	if len(t.TagGroups) == 1 {
		if leaf := t.TagGroups[0].TagGroupLeaf; leaf != nil {
			out.Tags = leaf.GetTags()
		}
	}
	if out.Mode == "" && out.TagsMatch == "" && len(out.FactTypes) == 0 && len(out.Tags) == 0 {
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
	if len(t.Tags) > 0 {
		out.TagGroups = []hs.MentalModelRefreshScopeTagGroupsInner{{
			TagGroupLeaf: &hs.TagGroupLeaf{Tags: t.Tags},
		}}
	}
	return out
}
