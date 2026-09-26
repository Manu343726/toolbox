package skills_test

import (
	"strings"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A project's `skills:` list names fully-qualified references, and the catalog is part of the
// identity rather than decoration on it.
func TestReferencesAreReadAtTheirLastDot(t *testing.T) {
	cases := []struct {
		written     string
		wantCatalog string
		wantName    string
		wantIsLocal bool
		wantURI     string
	}{
		{"local.code-review", "local", "code-review", true, "skill://local/code-review/SKILL.md"},
		// A catalog identifier may itself contain a dot, so the reference splits at the last
		// one. Splitting at the first would read this as a skill called `sh`.
		{"skills.sh.pull-request-review", "skills.sh", "pull-request-review", false,
			"skill://skills.sh/pull-request-review/SKILL.md"},
		{"some-team.coding-standards", "some-team", "coding-standards", false,
			"skill://some-team/coding-standards/SKILL.md"},
		// A padded entry is trimmed rather than refused: whitespace around a value in a
		// configuration file is not a claim about anything, and the canonical form is what
		// gets written back.
		{"  spaced.skill  ", "spaced", "skill", false, "skill://spaced/skill/SKILL.md"},
	}
	for _, testCase := range cases {
		t.Run(testCase.written, func(t *testing.T) {
			reference, err := skills.ParseReference(testCase.written)
			require.NoError(t, err)
			assert.Equal(t, testCase.wantCatalog, reference.Catalog)
			assert.Equal(t, testCase.wantName, reference.Name)
			assert.Equal(t, testCase.wantIsLocal, reference.IsLocal())
			assert.Equal(t, testCase.wantURI, reference.URI())
			assert.Equal(t, strings.TrimSpace(testCase.written), reference.String(),
				"the string in a configuration file and the string in a URI are the same fact")
		})
	}
}

// A reference that is not one is refused by name, because a list that quietly dropped an
// entry would be a project asking for a skill it would never be served.
func TestAReferenceThatIsNotOneIsRefused(t *testing.T) {
	cases := []struct{ name, written, mentions string }{
		{"unqualified", "code-review", "catalog"},
		{"empty", "   ", "empty"},
		{"no name", "local.", "both halves"},
		{"no catalog", ".code-review", "both halves"},
		{"a name that is not a path segment", "local.Code Review", "directory name"},
		{"a name with two hyphens in a row", "local.code--review", "single hyphens"},
		{"a catalog with whitespace", "some team.skill", "whitespace"},
		{"a catalog with a slash", "some/team.skill", "whitespace"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := skills.ParseReference(testCase.written)
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
			assert.Contains(t, err.Error(), testCase.mentions)
		})
	}
}

// A list is reported whole, so a person fixing their configuration sees everything that is
// wrong in one go rather than one line at a time against a list that reappears.
func TestAWholeListIsReportedAtOnce(t *testing.T) {
	_, err := skills.ParseReferences([]string{"good.one", "unqualified", "local."})
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "2 entries that are not a reference")
	assert.Contains(t, err.Error(), "unqualified")
	assert.Contains(t, err.Error(), "local.")
}
