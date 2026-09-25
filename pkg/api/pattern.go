package api

import (
	"fmt"
	"strings"
)

// A policy names operations by their identifier, so it needs a pattern language.
// The rules are narrow on purpose:
//
//   - "/" separates segments, and a segment is matched whole. "*" matches exactly
//     one segment, so a pattern cannot accidentally span from one API into another
//     service, and a service name — which contains dots — is one segment rather
//     than four.
//   - "**" matches the rest of the identifier, and only as the last segment. An
//     operation identifier is "<api>/<service>/<method>", so without it every rule
//     would have to know that arity, and "everything in this API" would be spelled
//     "knowledge/*/*" — a shape easy to get subtly wrong.
//   - There is no bare "*" segment shorthand that crosses a separator, because the
//     two mistakes that language invites are granting one API's operations to
//     another and granting a whole service when a person meant one method.
//   - Anything else is literal and compared case-sensitively, because an
//     identifier is a name the catalog assigned and matching it loosely would make
//     a policy say something other than it appears to.
//
// Specificity is measured in segments named exactly, because a pattern that names
// more of the identifier matches fewer operations and must therefore be consulted
// first: that is what makes a broad grant and a narrow refusal coexist without
// either depending on the order they were written.

// wildcard matches one segment, and rest matches every segment after it.
const (
	wildcard = "*"
	rest     = "**"
)

// Pattern is a compiled operation-identifier pattern.
type Pattern struct {
	source   string
	segments []patternSegment
	literal  int
	total    int
}

// patternSegment is one segment of a pattern: a literal, the single-segment
// wildcard, or the trailing rest.
type patternSegment struct {
	literal  string
	wildcard bool
	rest     bool
}

// CompilePattern parses and measures a pattern.
func CompilePattern(source string) (Pattern, error) {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return Pattern{}, fmt.Errorf("a rule needs a pattern")
	}
	parts := strings.Split(trimmed, "/")
	pattern := Pattern{source: trimmed, total: len(parts), segments: make([]patternSegment, 0, len(parts))}
	for index, part := range parts {
		if part == "" {
			return Pattern{}, fmt.Errorf("pattern %q has an empty segment", source)
		}
		if part == rest {
			if index != len(parts)-1 {
				return Pattern{}, fmt.Errorf(
					"pattern %q puts %q before its last segment; it matches the rest of the identifier",
					source, rest,
				)
			}
			pattern.segments = append(pattern.segments, patternSegment{rest: true})
			continue
		}
		if part == wildcard {
			// A pattern that is nothing but "*" means the whole surface, the way a
			// shell glob does. Treated as one segment it would match a single-segment
			// identifier, and an operation identifier is never one, so the rule that
			// grants everything would quietly grant nothing.
			segment := patternSegment{wildcard: true}
			if len(parts) == 1 {
				segment = patternSegment{rest: true}
			}
			pattern.segments = append(pattern.segments, segment)
			continue
		}
		if strings.Contains(part, wildcard) {
			return Pattern{}, fmt.Errorf(
				"pattern %q mixes a wildcard with other text in one segment; a wildcard matches a whole segment",
				source,
			)
		}
		pattern.literal++
		pattern.segments = append(pattern.segments, patternSegment{literal: part})
	}
	return pattern, nil
}

// String returns the pattern as it was written.
func (p Pattern) String() string { return p.source }

// Literal reports how many segments the pattern names exactly, which is what
// orders it against other patterns.
func (p Pattern) Literal() int { return p.literal }

// Total reports how many segments the pattern has.
func (p Pattern) Total() int { return p.total }

// Match reports whether an operation identifier matches the pattern.
//
// A pattern of only single segments must have exactly as many segments as the
// identifier, which is what makes a trailing "*" mean "every method of this
// service" rather than "this service, and anything under it". A trailing "**"
// relaxes that to "this and everything below", which is how a rule says
// "everything in this API" without naming the arity.
func (p Pattern) Match(identifier string) bool {
	parts := strings.Split(strings.TrimSpace(identifier), "/")
	trailing := len(p.segments) > 0 && p.segments[len(p.segments)-1].rest
	if trailing {
		if len(parts) < len(p.segments) {
			return false
		}
	} else if len(parts) != len(p.segments) {
		return false
	}
	for index, segment := range p.segments {
		if segment.wildcard || segment.rest {
			continue
		}
		if segment.literal != parts[index] {
			return false
		}
	}
	return true
}
