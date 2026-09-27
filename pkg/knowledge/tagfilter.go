package knowledge

import "fmt"

// TagFilter is a filter over the tag namespace, as a boolean expression rather than a list.
//
// A list of tag sets is the shape almost every filter API has, and it cannot express "these two
// tags, or all three of those" without the caller running two queries and merging them. So a filter
// is a tree: a leaf is a set of tags and how they combine, and `And`, `Or` and `Not` nest to any
// depth.
//
// The backend's own trigger filter is exactly this shape, which is why a trigger can carry one
// rather than a flat list: `Trigger.TagGroups` is this type, and `Trigger.Tags` is the shorthand for
// a single leaf. The two coexist because a flat list is what a caller writes ninety-nine times out
// of a hundred, and a tree it has to build for the hundredth.
type TagFilter struct {
	// Leaf is a set of tags and how they combine. Set, this is a leaf.
	Leaf *TagLeaf
	// And, Or and Not are the compounds, and they nest to any depth. `Not` takes any of them,
	// including a compound: the wire form is the same four keys at every level, so "not this
	// whole group" is expressible.
	And []TagFilter
	Or  []TagFilter
	Not *TagFilter
}

// TagLeaf is a set of tags and how they combine.
type TagLeaf struct {
	Tags []string
	// Match is `any`, `all`, `any_strict` or `all_strict`. Empty means the backend's own.
	Match string
	// Resolve is `exact` or `entities`. Empty means the backend's own.
	Resolve string
}

// IsLeaf reports whether this filter is a single tag set rather than a compound.
func (f TagFilter) IsLeaf() bool { return f.Leaf != nil }

// Empty reports whether the filter constrains nothing.
//
// It matters because "no filter" and "a filter that matches nothing" are different: a trigger with
// an empty filter takes every input, and a trigger whose filter is a group with no children matches
// none, which produces an empty document that looks correct.
func (f TagFilter) Empty() bool {
	return f.Leaf == nil && len(f.And) == 0 && len(f.Or) == 0 && f.Not == nil
}

// Validate reports whether the filter is one the backend can express.
//
// The rules are the backend's, checked here rather than discovered: a `not` may only wrap a leaf,
// and a group may not be empty. A filter that passes is one the wire form can carry, so a caller
// finds out at configuration time rather than when a page stays empty for want of a match.
func (f TagFilter) Validate(path string) error {
	switch {
	case f.Leaf != nil:
		if len(f.Leaf.Tags) == 0 {
			return &FilterError{Path: path, Reason: "a leaf with no tags matches nothing, and a filter that matches nothing is not a filter — it is a page that will stay empty however many times it is refreshed"}
		}
		if f.Leaf.Match != "" {
			switch f.Leaf.Match {
			case "any", "all", "any_strict", "all_strict":
			default:
				return &FilterError{Path: path, Reason: "the tag match mode is " + fmt.Sprintf("%q", f.Leaf.Match) + ", not one of any, all, any_strict or all_strict"}
			}
		}
		return nil
	case f.Not != nil:
		return f.Not.Validate(path + ".not")
	case len(f.And) > 0:
		for i, sub := range f.And {
			if err := sub.Validate(path + ".and[" + itoa(i) + "]"); err != nil {
				return err
			}
		}
		return nil
	case len(f.Or) > 0:
		for i, sub := range f.Or {
			if err := sub.Validate(path + ".or[" + itoa(i) + "]"); err != nil {
				return err
			}
		}
		return nil
	}
	return &FilterError{Path: path, Reason: "a group with no members constrains nothing, which is the same as leaving it out; a filter that looks present and matches nothing is the one shape a reader cannot diagnose"}
}

// FilterError is a filter that cannot be expressed, and where in the tree it went wrong.
//
// The path is there because a tree has no single obvious "the filter": a caller handed six levels
// needs to be told which level, or the fix is a search.
type FilterError struct {
	Path   string
	Reason string
}

func (e *FilterError) Error() string {
	if e.Path == "" {
		return "the tag filter " + e.Reason
	}
	return "the tag filter at " + e.Path + " " + e.Reason
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
