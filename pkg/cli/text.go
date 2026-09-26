package cli

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// A `.proto` comment is wrapped to the width of the source file, and everything the comment
// describes inherits that wrapping unless it is undone. These functions are the undoing.
//
// The two defects it causes are worth stating, because they are both "the help is wrong" in a
// way that is easy to miss and hard to report:
//
//   - A command's one-line summary was the comment's first *source line*. A sentence the author
//     wrapped across two lines became a summary ending mid-clause, so the parent's command list
//     read "ApiParserService is implemented by a parser subsystem. A parser subsystem" — which
//     says less than the comment and looks like the generator ran out of text.
//   - A flag's description kept the comment's line breaks, so whether a flag's help wrapped onto
//     a second line depended on where the author happened to break the line in the `.proto`.
//     Descriptions in the same block were ragged against each other for no reason a reader
//     could see.

// unwrap turns a source comment into text laid out for a terminal.
//
// Lines within a paragraph are joined with spaces, because a break in the source is where the
// author put it, not a break in the thought. Structure is kept, because it is not the author's
// line length that carries it: a blank line is a paragraph break, and a list item or a fenced
// block starts its own line.
func unwrap(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var paragraphs []string
	var paragraph strings.Builder
	// standalone records that the line being built began on its own — a list item, a code fence
	// — and so must not be joined to the line after it. It is tracked rather than inferred from
	// a trailing newline, because the first line of every paragraph is written the same way
	// and inferring it from a newline joins the second line of a sentence to nothing.
	standalone := false
	flush := func() {
		if paragraph.Len() == 0 {
			return
		}
		paragraphs = append(paragraphs, strings.TrimRight(paragraph.String(), " "))
		paragraph.Reset()
		standalone = false
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
		case ownLine(trimmed):
			// A list item or a code fence begins its own line, so the prose before it does not
			// absorb it. Without this a bulleted list renders as one paragraph of dashes.
			if paragraph.Len() > 0 {
				paragraph.WriteString("\n")
			}
			paragraph.WriteString(trimmed)
			standalone = true
		case paragraph.Len() == 0:
			paragraph.WriteString(trimmed)
		case standalone:
			paragraph.WriteString("\n")
			paragraph.WriteString(trimmed)
		default:
			paragraph.WriteString(" ")
			paragraph.WriteString(trimmed)
		}
	}
	flush()
	return strings.Join(paragraphs, "\n\n")
}

// flatten reduces a comment to a single line of text.
//
// It is what a flag's help wants and what a paragraph-preserving layout does not: a flag
// description sits in one column beside its name, and a paragraph break in the middle of it
// renders as a blank line and a fresh indent, which reads as three separate flags.
func flatten(text string) string {
	return strings.Join(strings.Fields(unwrap(text)), " ")
}

// ownLine reports whether a line must start its own line rather than continue a paragraph.
func ownLine(trimmed string) bool {
	switch {
	case strings.HasPrefix(trimmed, "-"), strings.HasPrefix(trimmed, "*"),
		strings.HasPrefix(trimmed, "#"), strings.HasPrefix(trimmed, "```"):
		return true
	default:
		// "1." and "2." start an ordered list.
		digits := 0
		for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
			digits++
		}
		return digits > 0 && digits+1 < len(trimmed) &&
			trimmed[digits] == '.' && trimmed[digits+1] == ' '
	}
}

// firstSentence is a command's one-line summary.
//
// It is the first sentence rather than the first line or the first paragraph, because that is
// what a one-line summary is: a whole thought. Taking the paragraph would put a paragraph in a
// column, and taking the line puts half a sentence there.
func firstSentence(text string) string {
	flattened := unwrap(text)
	// Only the first paragraph: a summary that ran into the second would be a summary of two
	// things.
	if index := strings.Index(flattened, "\n\n"); index >= 0 {
		flattened = flattened[:index]
	}
	for i := 0; i < len(flattened); i++ {
		switch flattened[i] {
		case '.', '!', '?':
			// A period between two digits is a version, not a sentence: "toolbox.v1" and
			// "0.1.0" are both entirely versions.
			if i+1 < len(flattened) && flattened[i+1] >= '0' && flattened[i+1] <= '9' {
				continue
			}
			if initialismBefore(flattened, i) {
				continue
			}
			if i+1 == len(flattened) || flattened[i+1] == ' ' || flattened[i+1] == '\t' {
				return strings.TrimSpace(flattened[:i+1])
			}
		}
	}
	return strings.TrimSpace(flattened)
}

// initialismBefore reports whether the period at an index closes an initialism — "e.g.",
// "i.e.", "U.S." — rather than a sentence.
//
// The word before the period is a single letter, which is what makes those three different from
// a word that happens to end in a period. Cutting "Accepts a format, e.g." at the abbreviation
// would leave a summary that names no format and stops mid-thought.
func initialismBefore(text string, index int) bool {
	run := 0
	for i := index - 1; i >= 0 && isWordByte(text[i]); i-- {
		run++
	}
	return run == 1
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// InstallHelpLayout makes a command's help read the way the reader's terminal can show it.
//
// Two things in cobra's default layout pick a width the reader did not. Flag help goes through
// `FlagUsages`, which never wraps, so a long description is one line however narrow the terminal
// is. And a command list is one line per command, so a summary longer than the terminal is a
// line that runs off the edge — or, in the case this replaced, one whose continuation is
// written at column zero and reads as a separate entry.
//
// It is applied to the generated root and to the host root from the same place, because two
// copies of a usage template would drift and the drift would be invisible until somebody
// compared two `--help` outputs.
func InstallHelpLayout(root *cobra.Command) {
	registered.Do(func() {
		cobra.AddTemplateFunc("flagUsages", func(flags *pflag.FlagSet) string {
			return flags.FlagUsagesWrapped(terminalWidth())
		})
		cobra.AddTemplateFunc("commandSummary", func(name string, padding int, short string) string {
			return commandSummary(name, padding, short)
		})
	})
	root.SetUsageTemplate(usageTemplate())
}

// registered keeps the template functions from being added twice, which cobra's global registry
// would otherwise treat as a redeclaration. The host root and every generated root install the
// layout, so this runs many times in one process.
var registered sync.Once

// commandSummary renders one entry of a command list: the name in its column, then the summary
// wrapped beneath itself.
//
// The two-space indent belongs to cobra's template, which puts it outside this function's reach,
// so it is not emitted here — the generator fills in one cell of the list, it does not draw the
// list. `padding` is cobra's own name column width, so `column` is the column the description
// starts in, exactly as cobra lays it out.
func commandSummary(name string, padding int, short string) string {
	const lead = 2
	column := lead + padding
	indent := strings.Repeat(" ", column+1)
	budget := max(24, terminalWidth()-column-1)
	return name + strings.Repeat(" ", max(0, padding-len(name))) +
		" " + wrapText(short, budget, indent, column+1)
}

// wrapText greedily wraps text to a width, indenting every line after the first.
//
// used is how many columns of the first line are already spoken for by the prefix the caller
// puts in front of it. Without it the first line is the one line that overflows, which is the
// line a reader notices least and a diff shows most.
//
// A word longer than the width is left intact rather than cut: a flag name or a contract path
// that cannot be broken is still readable when it overflows, and is not when it is cut.
func wrapText(text string, width int, indent string, used int) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	var out strings.Builder
	line := used
	for _, word := range words {
		switch {
		case out.Len() == 0:
			out.WriteString(word)
			line += len(word)
		case line+1+len(word) <= width:
			out.WriteString(" ")
			out.WriteString(word)
			line += 1 + len(word)
		default:
			out.WriteString("\n")
			out.WriteString(indent)
			out.WriteString(word)
			line = len(indent) + len(word)
		}
	}
	return out.String()
}

// terminalWidth is the width to wrap to, with a floor and a ceiling.
//
// The floor matters more than the detection: a description is unreadable at any width, and a
// reader whose terminal cannot be measured should get a readable width rather than none. The
// ceiling matters for the other reason — a description that is never wrapped is a description
// that needs scrolling, and on a very wide terminal the reader is as badly served as on a
// narrow one.
func terminalWidth() int {
	const (
		floor   = 60
		ceiling = 120
	)
	if width, ok := terminalSize(); ok && width >= floor {
		return min(width, ceiling)
	}
	return 100
}

// parseColumns reads the width from the COLUMNS environment variable.
func parseColumns() (int, error) {
	return strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS")))
}

// defaultUsageTemplate is cobra's own default, taken from a throwaway command so the copy
// follows cobra rather than a transcription of it.
func defaultUsageTemplate() string {
	var command cobra.Command
	return command.UsageTemplate()
}

// usageTemplate is cobra's own, with two things replaced: the flag blocks, so a long
// description wraps to the reader's terminal, and the command list, so a summary longer than
// the terminal is wrapped under its own column instead of running off the edge.
//
// It is a copy with replacements rather than a transcription, so it follows cobra when cobra
// changes; and a replacement that stops matching would leave cobra's own layout in place, which
// is a degraded help page rather than a broken one.
func usageTemplate() string {
	return strings.NewReplacer(
		"{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}",
		"{{flagUsages .LocalFlags}}",
		"{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}",
		"{{flagUsages .InheritedFlags}}",
		"{{rpad .Name .NamePadding }} {{.Short}}",
		"{{commandSummary .Name .NamePadding .Short}}",
		"{{rpad .CommandPath .CommandPathPadding}} {{.Short}}",
		"{{commandSummary .CommandPath .CommandPathPadding .Short}}",
	).Replace(defaultUsageTemplate())
}
