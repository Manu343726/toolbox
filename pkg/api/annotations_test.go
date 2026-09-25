package api

import (
	"strings"
	"testing"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAnnotationsSeparatesProseFromFacts(t *testing.T) {
	comment := "Fetch one pet.\n\n" +
		"@toolbox.side-effects read_only\n" +
		"Returns a pet, or NotFound.\n" +
		"@toolbox.deprecated\n"

	prose, annotations := shareddocs.ParseAnnotations(comment)
	assert.Equal(t, "Fetch one pet.\n\nReturns a pet, or NotFound.", prose,
		"an annotation is not prose, and removing it does not leave a hole in the text around it")
	require.Len(t, annotations, 2)

	values, declared := annotations.Lookup(AnnotationSideEffects)
	assert.True(t, declared)
	assert.Equal(t, []string{"read_only"}, values)

	flag, declared := annotations.Lookup("toolbox.deprecated")
	assert.True(t, declared, "a key with no value is a flag, not an absent key")
	assert.Empty(t, flag)

	assert.Equal(t, []string{"toolbox.deprecated", "toolbox.side-effects"}, annotations.Keys(),
		"keys are sorted, so a report about an unrecognised one is deterministic")
}

func TestParseAnnotationsLeavesOrdinaryProseAlone(t *testing.T) {
	// A line that merely starts with an at-sign is not an instruction, and a key
	// without a namespace is not one either: a contract is allowed to talk about
	// annotations without being interpreted as declaring them.
	for _, comment := range []string{
		"Use @toolbox.side-effects to declare an effect.",
		"@param is a javadoc tag, not a toolbox annotation.",
		"@notnamespaced value",
		"",
	} {
		prose, annotations := shareddocs.ParseAnnotations(comment)
		assert.Empty(t, annotations, "comment %q declares no annotation", comment)
		assert.Equal(t, comment, strings.TrimSpace(prose))
	}
}

func TestSideEffectsReadsTheDeclaredVocabulary(t *testing.T) {
	_, annotations := shareddocs.ParseAnnotations("@toolbox.side-effects read_only")
	effects, unknown := SideEffects(annotations)
	assert.Equal(t, []SideEffect{SideEffectReadOnly}, effects)
	assert.Empty(t, unknown)
}

func TestSideEffectsReportsWhatItCouldNotRead(t *testing.T) {
	// A misspelt value is the failure that matters: the operation stays
	// unclassified, and an unclassified operation is one no policy can expose. That
	// has to be reported rather than silently dropped.
	_, annotations := shareddocs.ParseAnnotations("@toolbox.side-effects read_only readonl")
	effects, unknown := SideEffects(annotations)
	assert.Equal(t, []SideEffect{SideEffectReadOnly}, effects)
	assert.Equal(t, []string{"readonl"}, unknown)

	_, annotations = shareddocs.ParseAnnotations("@toolbox.sideeffect read_only")
	effects, unknown = SideEffects(annotations)
	assert.Empty(t, effects, "an unrecognised key declares nothing")
	assert.Empty(t, unknown, "there is nothing to read, so no value is unreadable")
	assert.Equal(t, []string{"toolbox.sideeffect"}, UnknownAnnotations(annotations),
		"an unrecognised key is reported, because it may be a misspelt one")
}

func TestEffectClassesTreatUnknownAsItsOwnClass(t *testing.T) {
	assert.Equal(t, []EffectClass{EffectClassRead}, EffectClasses([]SideEffect{SideEffectReadOnly}))
	assert.Equal(t, []EffectClass{EffectClassWrite}, EffectClasses([]SideEffect{SideEffectCreate}))
	assert.Equal(t, []EffectClass{EffectClassWrite},
		EffectClasses([]SideEffect{SideEffectReadOnly, SideEffectIrreversible}),
		"a method that reads and writes is a write, so permitting reads does not permit it")
	assert.Equal(t, []EffectClass{EffectClassUnclassified}, EffectClasses(nil),
		"an operation nobody classified is not a read")
	assert.False(t, ReadOnly(nil))
}

func TestParseEffectClass(t *testing.T) {
	for _, name := range []string{"read", "READ", " write "} {
		_, ok := ParseEffectClass(name)
		assert.True(t, ok, "class %q is understood", name)
	}
	_, ok := ParseEffectClass("maybe")
	assert.False(t, ok)
}
