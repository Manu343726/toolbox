package skills

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
)

// readBool reads a boolean in every spelling a target client accepts.
//
// Claude Code accepts `yes`, `no`, `on`, `off`, `1` and `0` in any case as well as `true`
// and `false`, so a reader that took YAML's word for a boolean would refuse a skill that
// client is perfectly happy with. The framework's reader accepts every spelling its targets
// accept, and reports a value that is none of them rather than coercing it — because a
// value coerced to `false` is a skill silently switched off.
func parseBool(value any, where string) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case int:
		return numericBool(int64(typed), where)
	case int64:
		return numericBool(typed, where)
	case float64:
		if typed != float64(int64(typed)) {
			break
		}
		return numericBool(int64(typed), where)
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "yes", "on", "1":
			return true, nil
		case "false", "no", "off", "0":
			return false, nil
		}
		return false, notABoolean(where, strconv.Quote(typed))
	}
	return false, notABoolean(where, describeValue(value))
}

// numericBool reads the numeric spellings.
//
// YAML resolves an unquoted `1` to a number rather than to text, so a reader that only
// handled strings would refuse the same value written quoted or bare. Both are accepted, and
// a number that is neither one nor zero is refused: `2` is not a boolean that happened to be
// mistyped, it is a value this package would have to guess about.
func numericBool(value int64, where string) (bool, error) {
	switch value {
	case 1:
		return true, nil
	case 0:
		return false, nil
	}
	return false, notABoolean(where, strconv.FormatInt(value, 10))
}

func notABoolean(where, was string) error {
	return api.Errorf(api.KindInvalid,
		"%s is %s, which is not a boolean; a boolean is one of: "+
			"true, false, yes, no, on, off, 1, 0", where, was)
}

// describeValue names what a value was, for a message about what it should have been.
func describeValue(value any) string {
	switch value.(type) {
	case nil:
		return "empty"
	case []any:
		return "a list"
	case map[string]any:
		return "a block"
	case int, int64, float64:
		return "a number"
	default:
		return fmt.Sprintf("a %T", value)
	}
}

// asString reads a text field.
//
// A value that is not text is read as its own rendering rather than refused, because the
// alternative is refusing a skill over a frontmatter value a client would have accepted —
// and the only fields where a non-string is a genuine mistake are the ones parsed strictly
// elsewhere, the closed sets and the booleans.
func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		// A whole number renders as itself rather than as 3.000000, so a version or a port
		// written unquoted reads the way its author meant it.
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}

// asStringList reads a list field.
//
// A bare value where a list belongs is read as a one-element list rather than refused,
// because that is what a client reading the same frontmatter does: `allowed-tools: Bash`
// means one tool, not a mistake. An empty list and an absent field are both "none", and
// they are not distinguished, because no client distinguishes them either.
func asStringList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		list := make([]string, 0, len(typed))
		for _, item := range typed {
			list = append(list, asString(item))
		}
		return list
	case []string:
		return append([]string(nil), typed...)
	default:
		return []string{asString(value)}
	}
}

// asStringMap reads a mapping of text to text.
func asStringMap(value any) map[string]string {
	table, ok := asMap(value)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(table))
	for key, item := range table {
		out[key] = asString(item)
	}
	return out
}

// asMap reads a mapping, reporting whether the value was one.
//
// YAML gives a nested block as map[string]any, and a block written with non-string keys —
// a `1: x` pair, or a `true: x` pair — arrives as map[any]any. Both are accepted, because
// refusing the second would refuse a document that is valid YAML and that its author meant.
func asMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[asString(key)] = item
		}
		return out, true
	default:
		return nil, false
	}
}

// asToolRequirements reads the tools a skill states it needs.
//
// A list of names is read as names with no other detail, because that is the shape a client
// reading the same document accepts, and a requirement is a statement of need: this
// framework reports it and does not grant it, so a sparse one is still useful to the author.
func asToolRequirements(value any) ([]ToolRequirement, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []any:
		requirements := make([]ToolRequirement, 0, len(typed))
		for _, item := range typed {
			if table, ok := asMap(item); ok {
				requirements = append(requirements, ToolRequirement{
					Type:        asString(table["type"]),
					Name:        asString(table["name"]),
					Description: asString(table["description"]),
					Transport:   asString(table["transport"]),
					URL:         asString(table["url"]),
				})
				continue
			}
			name := asString(item)
			if strings.TrimSpace(name) == "" {
				return nil, api.Errorf(api.KindInvalid,
					"a tool requirement is empty; a requirement is a name, or a block with a name")
			}
			requirements = append(requirements, ToolRequirement{Name: name})
		}
		return requirements, nil
	default:
		name := asString(typed)
		if strings.TrimSpace(name) == "" {
			return nil, api.Errorf(api.KindInvalid, "a tool requirement is empty")
		}
		return []ToolRequirement{{Name: name}}, nil
	}
}

// stringField reads a text field from a document, treating absent as empty.
func stringField(document map[string]any, key string) string {
	return asString(document[key])
}

// stringMapField reads a text mapping from a document, treating absent as none.
func stringMapField(document map[string]any, key string) map[string]string {
	return asStringMap(document[key])
}

// sortedKeysOf returns a mapping's keys in a stable order.
//
// A skill that is refused for an unknown property is refused for the first one in the
// author's own order, so a reader fixing it meets the same message twice rather than a
// different one each run — and a manifest is reproducible from the same directory on
// another machine, which is what makes a digest worth anything.
func sortedKeysOf(table map[string]any) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
