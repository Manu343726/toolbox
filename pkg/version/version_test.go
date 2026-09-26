package version_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/Manu343726/toolbox/pkg/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bug this package exists for: a store holding versions 1 through 10 served
// the ninth, because "10" sorts before "9" as text.
func TestNumbersOrderByValueNotAsText(t *testing.T) {
	for _, testCase := range []struct {
		earlier, later string
	}{
		{"1", "2"},
		{"2", "10"},
		{"9", "10"},
		{"10", "11"},
		{"99", "100"},
		{"1.2", "1.10"},
		{"1.9", "1.10"},
		{"2.0.0", "2.0.1"},
		{"2.9.9", "2.10.0"},
		{"0.0.1", "0.0.2"},
		{"0.9", "0.10"},
	} {
		t.Run(fmt.Sprintf("%s before %s", testCase.earlier, testCase.later), func(t *testing.T) {
			assert.Negative(t, version.Compare(testCase.earlier, testCase.later))
			assert.Positive(t, version.Compare(testCase.later, testCase.earlier))
			assert.True(t, version.Less(testCase.earlier, testCase.later))
			assert.Equal(t, testCase.later, version.Latest(testCase.earlier, testCase.later))
		})
	}
}

// A counter padded to a width is the same number written longer, so the two
// spellings are one version rather than two that must be told apart.
func TestLeadingZeroesDoNotChangeAVersionsPosition(t *testing.T) {
	assert.Zero(t, version.Compare("1", "01"))
	assert.Zero(t, version.Compare("1.02", "1.2"))
	assert.Zero(t, version.Compare("007", "7"))
	assert.Negative(t, version.Compare("007", "8"))

	// The same holds for a run that is nothing but zeroes: a version padded to a
	// width is the same number written longer, whatever width it was padded to.
	assert.Zero(t, version.Compare("0.1", "00.1"))
	assert.Zero(t, version.Compare("0", "0000"))
}

// A version that has never been revised is older than one that has, and a version
// that runs out of parts is the earlier of the two.
func TestAbsentAndShorterVersionsComeFirst(t *testing.T) {
	assert.Negative(t, version.Compare("", "1"))
	assert.Positive(t, version.Compare("1", ""))
	assert.Zero(t, version.Compare("", ""))
	assert.Negative(t, version.Compare("1", "1.0.1"))
	assert.Negative(t, version.Compare("1.0", "1.0.0"))
}

// A scheme may prefix its versions, and may use text between numbers.
func TestTextAroundNumbersComparesAsText(t *testing.T) {
	assert.Negative(t, version.Compare("v1", "v2"))
	assert.Negative(t, version.Compare("alpha", "beta"))
	assert.Negative(t, version.Compare("2026-01-02", "2026-02-01"))
	assert.Negative(t, version.Compare("release-1", "release-10"))
	assert.Negative(t, version.Compare("1.0.0-rc1", "1.0.0-rc2"))
}

// Surrounding whitespace is not part of a version, and a caller that stored one
// with a trailing space should not find it unreachable.
func TestWhitespaceIsNotPartOfAVersion(t *testing.T) {
	assert.Zero(t, version.Compare(" 1 ", "1"))
	assert.Zero(t, version.Compare("  ", ""))
	assert.Negative(t, version.Compare("  ", "1"))
}

// A sort is what a store does to decide what to serve, so the order it produces
// is the order the whole framework relies on.
func TestSortingProducesNumericOrder(t *testing.T) {
	versions := []string{"10", "2", "1", "9", "1.10", "1.2", "", "3"}
	sort.SliceStable(versions, func(i, j int) bool { return version.Less(versions[i], versions[j]) })
	assert.Equal(t, []string{"", "1", "1.2", "1.10", "2", "3", "9", "10"}, versions)
}

// The helpers must agree with the comparison they are defined in terms of, or a
// caller using the wrong one gets a different answer.
func TestTheHelpersAgreeWithCompare(t *testing.T) {
	versions := []string{"", "1", "2", "10", "1.10", "1.2", "v1", "v2"}
	for _, earlier := range versions {
		for _, later := range versions {
			order := version.Compare(earlier, later)
			t.Run(fmt.Sprintf("%s vs %s", earlier, later), func(t *testing.T) {
				require.Equal(t, order < 0, version.Less(earlier, later))
				// Compare is antisymmetric on its inputs, whichever way round they are
				// given, which is what makes it usable as a sort key.
				assert.Equal(t, -sign(order), sign(version.Compare(later, earlier)))
			})
		}
	}
}

func sign(order int) int {
	switch {
	case order < 0:
		return -1
	case order > 0:
		return 1
	default:
		return 0
	}
}
