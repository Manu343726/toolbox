package docs

import (
	"sort"
	"strings"
)

// A protobuf contract states authorization-relevant facts in its comments, because
// a descriptor knows which methods exist and nothing else. The syntax below is the
// one place that decides how a comment says so, and it is deliberately narrow: a
// line, a dotted key, and space-separated values.
//
//	// Fetch one pet.
//
//	// @toolbox.side-effects read_only
//	rpc GetPet(GetPetRequest) returns (Pet);
//
// Two rules keep it honest. An annotation line is removed from the prose, because
// the prose is what generated help and an agent's tool description are built from —
// a machine-readable line left in a description is noise the reader pays for. And
// a key this package does not recognise is still returned, because the vocabulary
// belongs to the package that owns the model, not to the parser: deciding that
// `side-effects` is meaningful and `sideeffect` is a typo is not this file's
// call.

// AnnotationPrefix introduces an annotation line.
const AnnotationPrefix = "@"

// Annotation is one parsed annotation line: a namespaced key and its values.
type Annotation struct {
	// Key is the full dotted key, such as "toolbox.side-effects".
	Key string
	// Values are the space-separated words after the key. It is empty for a
	// valueless flag such as "toolbox.deprecated".
	Values []string
}

// AnnotationSet is every annotation parsed from one comment.
type AnnotationSet []Annotation

// Lookup returns the values declared for a key, and whether the key was present.
func (s AnnotationSet) Lookup(key string) ([]string, bool) {
	key = strings.TrimSpace(key)
	for _, annotation := range s {
		if annotation.Key == key {
			return annotation.Values, true
		}
	}
	return nil, false
}

// Keys returns the distinct keys the set declares, in sorted order, so a caller
// that validates a vocabulary can report what it did not recognise deterministically.
func (s AnnotationSet) Keys() []string {
	seen := make(map[string]bool, len(s))
	keys := make([]string, 0, len(s))
	for _, annotation := range s {
		if seen[annotation.Key] {
			continue
		}
		seen[annotation.Key] = true
		keys = append(keys, annotation.Key)
	}
	sort.Strings(keys)
	return keys
}

// Merge returns the union of two sets, later values winning for a repeated key.
// A method's own annotations override the ones its service declared, which is the
// same rule a nearer declaration follows everywhere else in the framework.
func (s AnnotationSet) Merge(over AnnotationSet) AnnotationSet {
	merged := make(AnnotationSet, 0, len(s)+len(over))
	merged = append(merged, s...)
	merged = append(merged, over...)
	return merged
}

// ParseAnnotations splits a comment into its prose and its annotations.
//
// A line is an annotation when it starts with "@" followed by a namespaced key of
// lower-case letters, digits, dots, and dashes. Anything else is prose, including a
// line that merely mentions an at-sign, so a contract can talk about annotations
// without being interpreted as one.
func ParseAnnotations(comment string) (string, AnnotationSet) {
	lines := strings.Split(comment, "\n")
	prose := make([]string, 0, len(lines))
	annotations := make(AnnotationSet, 0, 4)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if key, values, ok := parseAnnotationLine(trimmed); ok {
			annotations = append(annotations, Annotation{Key: key, Values: values})
			continue
		}
		prose = append(prose, trimmed)
	}
	return collapseBlankRuns(prose), annotations
}

// parseAnnotationLine reads one annotation line, if it is one.
func parseAnnotationLine(line string) (string, []string, bool) {
	if !strings.HasPrefix(line, AnnotationPrefix) {
		return "", nil, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, AnnotationPrefix))
	key, values, _ := strings.Cut(rest, " ")
	key = strings.TrimSpace(key)
	if !validAnnotationKey(key) {
		return "", nil, false
	}
	return key, strings.Fields(values), true
}

// validAnnotationKey reports whether a key is namespaced and well formed. A bare
// word is not an annotation: without a namespace, any comment that begins a line
// with an at-sign would be reinterpreted as a machine instruction.
func validAnnotationKey(key string) bool {
	namespace, name, ok := strings.Cut(key, ".")
	if !ok || namespace == "" || name == "" {
		return false
	}
	return validAnnotationSegment(namespace) && validAnnotationName(name)
}

func validAnnotationSegment(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
		default:
			return false
		}
	}
	return value != ""
}

func validAnnotationName(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return value != ""
}

// collapseBlankRuns removes the blank lines an extracted annotation left behind,
// and trims the ends, so removing a machine-readable line does not change the
// shape of the prose around it.
func collapseBlankRuns(lines []string) string {
	kept := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		if line == "" {
			blanks++
			continue
		}
		if blanks > 0 && len(kept) > 0 {
			kept = append(kept, "")
		}
		blanks = 0
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
