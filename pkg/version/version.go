// Package version orders version strings.
//
// A domain resource in this framework is versioned by a string, and a store that
// holds several versions of one resource has to decide which of them a caller
// asking for "the latest" means. Comparing those strings as text gets that
// wrong in the one case everybody meets: "10" is less than "9", because the text
// is compared character by character and '1' precedes '9'. A store that has been
// written to eleven times would then serve the ninth.
//
// There is no version scheme the framework imposes, because a resource's versions
// are its own. So this does not parse a scheme. It compares the parts of a
// version as numbers where they are numbers and as text where they are not, which
// is what makes "1.2" less than "1.10" and "v2" greater than "v1" without needing
// to know whether the resource means semver, a date, or a counter.
package version

import "strings"

// Compare returns a negative number when a precedes b, zero when they are
// equivalent, and a positive number when a follows b.
//
// The comparison walks both versions in runs — a maximal stretch of digits, or a
// maximal stretch of anything else — and compares run against run. Two runs of
// digits are compared by value, so "9" precedes "10" and "1.2" precedes "1.10";
// any other pair is compared as text. A version that runs out of parts while the
// other still has some is the earlier of the two, so "1" precedes "1.0.1".
//
// Leading zeroes do not change a version's position, because a counter padded to
// a width is the same number written longer. Two spellings of one version
// therefore compare equal, which is what lets a store treat them as two versions
// rather than one.
func Compare(a, b string) int {
	left, right := strings.TrimSpace(a), strings.TrimSpace(b)
	if left == right {
		return 0
	}
	// An absent version precedes a present one, so a resource that has never been
	// revised is older than one that has.
	if left == "" {
		return -1
	}
	if right == "" {
		return 1
	}
	for left != "" && right != "" {
		leftRun, leftDigits := leadingRun(left)
		rightRun, rightDigits := leadingRun(right)
		if leftDigits && rightDigits {
			if order := compareNumbers(leftRun, rightRun); order != 0 {
				return order
			}
		} else if order := strings.Compare(leftRun, rightRun); order != 0 {
			return order
		}
		left, right = left[len(leftRun):], right[len(rightRun):]
	}
	switch {
	case left == "" && right == "":
		return 0
	case left == "":
		return -1
	default:
		return 1
	}
}

// Latest returns whichever of the two versions is later.
func Latest(a, b string) string {
	if Compare(a, b) >= 0 {
		return a
	}
	return b
}

// Less reports whether a precedes b.
func Less(a, b string) bool { return Compare(a, b) < 0 }

// leadingRun returns the next run of a version and whether that run is all digits.
func leadingRun(value string) (string, bool) {
	end := 0
	digits := value[0] >= '0' && value[0] <= '9'
	for end < len(value) {
		current := value[end] >= '0' && value[end] <= '9'
		if current != digits {
			break
		}
		end++
	}
	return value[:end], digits
}

// compareNumbers orders two runs of digits by value.
//
// The comparison is by length once leading zeroes are dropped, because a longer
// run of digits is the larger number unless the extra digits are leading zeroes.
// Comparing the text instead would reintroduce the bug this package exists to fix.
func compareNumbers(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}
