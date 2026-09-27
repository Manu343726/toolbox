package knowledge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A derived document's trigger is the whole of what it is given, and every field is a filter
// rather than a label. So the defaults are worth pinning: a page whose trigger matches nothing is
// a page that stays empty however many times it is refreshed, and an empty page has no visible
// error.

func TestDefaultTriggerIsStatedRatherThanInherited(t *testing.T) {
	t.Parallel()
	got := DefaultTrigger()

	// Incremental rather than a full regeneration, because a full generation has no previous
	// document to edit and is the slower way to change an existing one.
	assert.Equal(t, "delta", got.Mode)

	// Observations rather than raw facts. This is the load-bearing default: a trigger over
	// raw facts would read the same text the base was given, before consolidation had
	// deduplicated or synthesised it, so every document would restate the corpus.
	assert.Equal(t, []string{"observation"}, got.FactTypes)

	// Excluding mental models is what stops a projection from becoming an input to the next
	// projection. Without it, a page built from a model would feed the model, and the
	// derivation would stop being checkable.
	assert.True(t, got.ExcludeMentalModels, "a projection must not be an input to the next projection")

	assert.True(t, got.RefreshAfterConsolidation,
		"a document that does not refresh after consolidation is stale as soon as it is written")

	// `all_strict` rather than `all`: a strict match means the memory carries the tag, not
	// that it was resolved to something carrying it. A non-strict default would let a page
	// build from entities it shares with its subject.
	assert.Equal(t, "all_strict", got.TagsMatch)

	// No tags and no cron, because a default tag list would silently exclude everything not
	// in it, and a default cron would be a schedule nobody chose.
	assert.Empty(t, got.Tags)
	assert.Empty(t, got.Cron)
}

func TestADerivedTriggerIsAFilterAndNotALabel(t *testing.T) {
	t.Parallel()
	// The distinction is the property, so it is worth a test that says so: tags on a trigger
	// select memories, they do not describe the document. A page tagged with terms it was not
	// given matches nothing, and stays empty — which looks identical to a page that has not
	// been refreshed yet.
	trigger := DefaultTrigger()
	assert.Equal(t, "all_strict", trigger.TagsMatch,
		"the default match is strict, so a tag on the trigger selects rather than resolves")
	assert.Empty(t, trigger.Tags,
		"a default tag list would exclude every memory not in it, which is a filter nobody asked for")
}

// A trigger cannot both follow consolidation and run on a schedule. That is checked at the
// contract boundary with the message a caller sees, but the *reason* belongs with the type, and
// this is what keeps a future default from reintroducing the combination.
func TestTheTwoRefreshTriggersAreAlternatives(t *testing.T) {
	t.Parallel()
	// A document refreshes either when consolidation produces something or on a cron. Naming
	// both means it refreshes twice, for two reasons, and neither of them is visible in the
	// resulting document — so a reader comparing two revisions has no way to tell which of the
	// two caused the change.
	followsConsolidation := DefaultTrigger()
	assert.True(t, followsConsolidation.RefreshAfterConsolidation)
	assert.Empty(t, followsConsolidation.Cron)

	scheduled := Trigger{Mode: "delta", Cron: "0 * * * *"}
	assert.Empty(t, scheduled.RefreshAfterConsolidation)
	assert.Equal(t, "0 * * * *", scheduled.Cron)
}
