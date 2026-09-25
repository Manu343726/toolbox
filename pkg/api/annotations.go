package api

import (
	"sort"
	"strings"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

// A contract states authorization-relevant facts in its documentation, because a
// descriptor knows which methods exist and nothing about what invoking one does.
// The keys below are the framework's vocabulary, and they are the protobuf spelling
// of the same facts an OpenAPI document states through x-toolbox-* extensions, so
// one policy written against side effects means the same thing in both formats.
//
// The vocabulary lives here rather than in the comment parser on purpose: the
// parser reads syntax and does not know what any of it means, and this package owns
// what a side effect is. An unrecognised key is reported rather than ignored, because
// a misspelt key silently leaves an operation unclassified, and an unclassified
// operation is one no policy can expose. The failure has to be visible.

// Annotation keys the framework recognises in a protobuf comment.
const (
	// AnnotationSideEffects declares what invoking the method does, as one or more
	// side effects. It is the only key that states an authorization-relevant fact,
	// and it is what makes a method of a ConnectRPC service classifiable at all:
	// every RPC is a POST, so nothing about the transport implies an effect.
	AnnotationSideEffects = "toolbox.side-effects"
)

// SideEffects reads the side effects a comment declared.
//
// A key it does not recognise, and a value outside the SideEffect vocabulary, are
// both returned as findings rather than dropped, so the caller can surface them.
// A method that declared nothing has no side effects, which means unknown and not
// read-only: a policy that permits unclassified operations has to say so.
func SideEffects(annotations shareddocs.AnnotationSet) (effects []SideEffect, unknown []string) {
	values, declared := annotations.Lookup(AnnotationSideEffects)
	if !declared {
		return nil, nil
	}
	for _, value := range values {
		effect := SideEffect(value)
		if !KnownSideEffect(effect) {
			unknown = append(unknown, value)
			continue
		}
		effects = append(effects, effect)
	}
	return deduplicateSideEffects(effects), unknown
}

// UnknownAnnotations returns the keys of a set the framework does not recognise.
func UnknownAnnotations(annotations shareddocs.AnnotationSet) []string {
	unknown := make([]string, 0)
	for _, key := range annotations.Keys() {
		if key != AnnotationSideEffects {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// KnownSideEffect reports whether an effect is one this framework documents.
//
// The vocabulary is open: a description may declare an effect nobody recognises,
// and a consumer must treat it as consequential. This reports whether the name is
// one the framework defines, which is what lets a reader reject a typo without
// rejecting a valid extension.
func KnownSideEffect(effect SideEffect) bool {
	switch effect {
	case SideEffectReadOnly, SideEffectCreate, SideEffectUpdate, SideEffectDelete,
		SideEffectExternal, SideEffectIrreversible, SideEffectFinancial:
		return true
	default:
		return false
	}
}

// ReadOnly reports whether every declared effect is a read.
//
// An operation with no declared effects is not read-only: unknown is not safe, and
// a policy must be told to trust an unclassified operation rather than infer it.
func ReadOnly(effects []SideEffect) bool {
	if len(effects) == 0 {
		return false
	}
	for _, effect := range effects {
		if effect != SideEffectReadOnly {
			return false
		}
	}
	return true
}

// EffectClass is the coarse class a policy filters on: a read, a write, or
// something nobody classified.
type EffectClass string

// The classes a policy can name.
const (
	// EffectClassRead covers operations whose every declared effect is a read.
	EffectClassRead EffectClass = "read"
	// EffectClassWrite covers operations declaring at least one effect that is
	// not a read.
	EffectClassWrite EffectClass = "write"
	// EffectClassUnclassified covers operations that declared no effect at all.
	// A policy has to name it explicitly, because an operation nobody classified
	// is not a read and must not become one by omission.
	EffectClassUnclassified EffectClass = "unclassified"
)

// EffectClasses returns the classes a set of declared effects falls into, sorted.
// An operation with no declared effects is in exactly one class, the unclassified
// one, so a policy that names read and write has still not covered it.
func EffectClasses(effects []SideEffect) []EffectClass {
	if len(effects) == 0 {
		return []EffectClass{EffectClassUnclassified}
	}
	if ReadOnly(effects) {
		return []EffectClass{EffectClassRead}
	}
	// A method that both reads and writes is a write. A policy that permits reads
	// must not thereby permit it, and one that permits writes has to permit it
	// whatever else it does.
	return []EffectClass{EffectClassWrite}
}

// ParseEffectClass reads a policy's class name.
func ParseEffectClass(value string) (EffectClass, bool) {
	switch EffectClass(strings.ToLower(strings.TrimSpace(value))) {
	case EffectClassRead:
		return EffectClassRead, true
	case EffectClassWrite:
		return EffectClassWrite, true
	case EffectClassUnclassified:
		return EffectClassUnclassified, true
	default:
		return "", false
	}
}

func deduplicateSideEffects(effects []SideEffect) []SideEffect {
	if len(effects) == 0 {
		return nil
	}
	seen := make(map[SideEffect]bool, len(effects))
	result := make([]SideEffect, 0, len(effects))
	for _, effect := range effects {
		if seen[effect] {
			continue
		}
		seen[effect] = true
		result = append(result, effect)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
