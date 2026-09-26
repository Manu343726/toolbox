package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A `.proto` comment is wrapped to the width of its source file, and everything derived from a
// comment inherited that wrapping. These tests are about the undoing of it: a summary that ends
// mid-clause is a help page that says less than the contract it describes, and it is very easy
// to introduce by moving a comment's line break.

func TestASummaryIsAWholeSentenceNotASourceLine(t *testing.T) {
	// The comment an author wrote, wrapped in the source file at the width they happened to
	// wrap at. The first source line ends mid-clause.
	const comment = "ApiParserService is implemented by a parser subsystem. A parser subsystem\n" +
		"reads one description document and renders it into the canonical form."

	assert.Equal(t,
		"ApiParserService is implemented by a parser subsystem.",
		firstSentence(comment))
}

func TestASummaryIsTheWholeParagraphWhenThereIsNoFullStop(t *testing.T) {
	// Better a complete thought than a half one: a comment with no full stop is one thought.
	assert.Equal(t, "Store sources and expose retrieval and nothing else at all",
		firstSentence("Store sources and expose retrieval\nand nothing else at all"))
}

func TestASummaryStopsAtTheFirstSentenceNotTheFirstParagraph(t *testing.T) {
	const comment = "One thing. Another thing, which is longer and would not fit in a column.\n\n" +
		"A second paragraph that is not part of the summary at all."
	assert.Equal(t, "One thing.", firstSentence(comment))
}

func TestASummaryIsNotCutInsideAVersion(t *testing.T) {
	// Every identifier in this framework has a version in it, and a period between two digits
	// is a version rather than the end of a sentence.
	for _, comment := range []string{
		"Reads a toolbox.v1 contract and renders it.",
		"Carries the implementation at 0.1.0 today.",
		"Exposes version 1.2 of the catalog.",
	} {
		summary := firstSentence(comment)
		assert.True(t, strings.HasSuffix(summary, "."), "%q should end at the real sentence end", summary)
		assert.NotContains(t, summary, "v1 reads", "cut inside a version")
	}
}

func TestASummaryIsNotCutAtAnAbbreviation(t *testing.T) {
	const comment = "Accepts a format, e.g. openapi, and renders it.\nAnd more that follows."
	summary := firstSentence(comment)
	assert.Contains(t, summary, "openapi", "the abbreviation is not the end of the thought")
	assert.Contains(t, summary, "renders it.")
}

func TestASummaryHandlesAParagraphThatIsOnlyABulletList(t *testing.T) {
	assert.Equal(t, "- one\n- two", firstSentence("- one\n- two"))
}

func TestUnwrapJoinsASentenceTheSourceBrokeInTwo(t *testing.T) {
	assert.Equal(t,
		"The component that produced the entry, which is the attribute routes most often match on.",
		unwrap("The component that produced the entry, which is the attribute routes\n"+
			"most often match on."))
}

func TestUnwrapKeepsParagraphsAndListItems(t *testing.T) {
	const comment = "First paragraph, wrapped\nacross two lines.\n" +
		"\n" +
		"Second paragraph.\n" +
		"- first item\n" +
		"- second item\n" +
		"\n" +
		"1. one\n" +
		"2. two"
	assert.Equal(t,
		"First paragraph, wrapped across two lines.\n\n"+
			"Second paragraph.\n- first item\n- second item\n\n"+
			"1. one\n2. two",
		unwrap(comment))
}

func TestUnwrapOfNothingIsNothing(t *testing.T) {
	assert.Empty(t, unwrap(""))
	assert.Empty(t, unwrap("   \n  \n"))
}

func TestFlattenIsOneLineBecauseAFlagIsOneColumn(t *testing.T) {
	// The defect this fixes: a flag description with a paragraph break renders as a blank line
	// and a fresh indent, which reads as three separate flags.
	const comment = "The sinks for this workspace's entries, in the order they are\n" +
		"declared.\n\nA backend the deployment does not have is refused."
	got := flatten(comment)
	assert.NotContains(t, got, "\n")
	assert.Contains(t, got, "declared. A backend the deployment does not have is refused.")
}

func TestWrapTextHangsTheContinuationUnderTheFirstLine(t *testing.T) {
	got := wrapText("one two three four five six", 11, "....", 0)
	assert.Equal(t, "one two\n....three\n....four\n....five\n....six", got)
}

func TestWrapTextCountsThePrefixAgainstTheFirstLine(t *testing.T) {
	// Without this the first line is the one that overflows, which is the line a reader notices
	// least and a diff shows most.
	got := wrapText("alpha beta gamma", 12, "    ", 8)
	for _, line := range strings.Split(got, "\n") {
		assert.LessOrEqual(t, len(line), 12, "line %q overflows the width", line)
	}
}

func TestWrapTextLeavesAnUnbreakableWordIntact(t *testing.T) {
	// A contract path cannot be broken, and cutting it produces something that is not a path.
	const path = "toolbox.registry.v1.RegistryService/WatchServices"
	got := wrapText("see "+path+" for the list", 8, "..", 0)
	assert.Contains(t, got, path)
}

func TestWrapTextOfNothingIsNothing(t *testing.T) {
	assert.Empty(t, wrapText("   ", 20, "  ", 0))
}

func TestCommandSummaryPadsTheNameAndHangsTheSummary(t *testing.T) {
	got := commandSummary("logger", 13, "A short summary.")
	assert.Equal(t, "logger        A short summary.", got,
		"the name is padded to the column cobra reserves, then one space")

	long := commandSummary("logger", 13,
		"A summary long enough that it cannot possibly fit in the space left over beside the name.")
	for _, line := range strings.Split(long, "\n") {
		assert.LessOrEqual(t, len(line), terminalWidth())
	}
	// Every line after the first lines up under the description column, so the list reads as a
	// column of descriptions rather than a column with stray text below it.
	lines := strings.Split(long, "\n")
	for _, line := range lines[1:] {
		assert.Equal(t, strings.Repeat(" ", 16), line[:16])
	}
}

// A command's help is rendered through this template, and these are the two places cobra
// chooses a width the reader did not: flag help that never wraps, and a command list whose
// continuation is written at column zero.

func TestHelpLayoutWrapsFlagHelpToTheTerminal(t *testing.T) {
	t.Setenv("COLUMNS", "70")
	command := &cobra.Command{Use: "root"}
	InstallHelpLayout(command)
	command.Flags().String("name", "", strings.Repeat("a description that is far too long ", 6))

	usage := command.UsageString()
	longest := 0
	for _, line := range strings.Split(usage, "\n") {
		if len(line) > longest {
			longest = len(line)
		}
	}
	assert.LessOrEqual(t, longest, 70, "no line should be wider than the reader's terminal")
	assert.Greater(t, len(strings.Split(usage, "\n")), 6, "a long description should take several lines")
}

func TestHelpLayoutIsInstalledWithoutRedeclaringTheTemplateFunctions(t *testing.T) {
	// The host root and every generated root install the layout, so this runs many times in
	// one process and cobra's global registry must not see a redeclaration.
	roots := make([]*cobra.Command, 0, 5)
	for i := 0; i < 5; i++ {
		command := &cobra.Command{Use: "root"}
		InstallHelpLayout(command)
		roots = append(roots, command)
	}
	for _, command := range roots {
		require.NotEmpty(t, command.UsageTemplate())
	}
}

func TestTheUsageTemplateChangesNothingButTheReplacements(t *testing.T) {
	// A replacement that stopped matching would leave cobra's layout in place, which is a
	// degraded help page rather than a broken one — and so would pass a test that only checked
	// the output. This is the test that notices.
	original := defaultUsageTemplate()
	patched := usageTemplate()

	// What each replacement takes out of cobra's template, and what it puts in its place.
	replacements := [][2]string{
		{"{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}", "{{flagUsages .LocalFlags}}"},
		{"{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}", "{{flagUsages .InheritedFlags}}"},
		{"{{rpad .Name .NamePadding }} {{.Short}}", "{{commandSummary .Name .NamePadding .Short}}"},
		{"{{rpad .CommandPath .CommandPathPadding}} {{.Short}}", "{{commandSummary .CommandPath .CommandPathPadding .Short}}"},
	}
	restore := make([]string, 0, 2*len(replacements))
	for _, pair := range replacements {
		assert.Contains(t, original, pair[0], "cobra's template no longer contains %q", pair[0])
		assert.Contains(t, patched, pair[1], "%q was not installed", pair[1])
		assert.NotContains(t, patched, pair[0], "%q was not replaced", pair[0])
		restore = append(restore, pair[1], pair[0])
	}

	// Putting cobra's own text back must reproduce cobra's own template exactly. That is the
	// statement that a cobra upgrade cannot silently change the shape of the help, and it does
	// not depend on how many template actions either version happens to have.
	restored := strings.NewReplacer(restore...).Replace(patched)
	assert.Equal(t, original, restored, "the only changes are the four replacements")
}

func TestTerminalWidthHasAFloorAndACeiling(t *testing.T) {
	t.Setenv("COLUMNS", "20")
	// Too narrow to be usable, so the floor applies rather than a width that renders nothing.
	assert.GreaterOrEqual(t, terminalWidth(), 60)

	t.Setenv("COLUMNS", "400")
	// Very wide, and a description that is never wrapped is a description that needs scrolling.
	assert.LessOrEqual(t, terminalWidth(), 120)

	t.Setenv("COLUMNS", "80")
	assert.Equal(t, 80, terminalWidth())
}

func TestFlagUsagesAreProducedForBothFlagBlocks(t *testing.T) {
	// Both blocks go through the same function, so a flag and an inherited flag are laid out
	// by one implementation.
	t.Setenv("COLUMNS", "80")
	command := &cobra.Command{Use: "child"}
	InstallHelpLayout(command)
	command.Flags().String("local", "", strings.Repeat("local description ", 8))
	parent := &cobra.Command{Use: "root"}
	parent.PersistentFlags().String("global", "", strings.Repeat("global description ", 8))
	parent.AddCommand(command)

	usage := command.UsageString()
	assert.Contains(t, usage, "local description")
	assert.Contains(t, usage, "global description")
	for _, line := range strings.Split(usage, "\n") {
		assert.LessOrEqual(t, len(line), 80, "line %q overflows", line)
	}
	_ = pflag.CommandLine
}
