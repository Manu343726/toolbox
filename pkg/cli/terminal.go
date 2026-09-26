package cli

import (
	"os"

	"golang.org/x/term"
)

// terminalSize reports the reader's terminal width.
//
// It asks about the descriptors a help page is actually written to rather than assuming
// standard output, and it treats "not a terminal" as a normal answer rather than a failure: a
// command's help piped to a file or captured by a test has no width to honour, and in that case
// the caller gets the floor. Guessing a width from a pipe is how help output ends up
// wrapped for a reader who never sees it.
func terminalSize() (int, bool) {
	for _, file := range []*os.File{os.Stdout, os.Stderr} {
		if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 0 {
			return width, true
		}
	}
	// COLUMNS is the shell's own statement of the reader's intent, and it is set by
	// interactive shells that track window size. It is consulted after the descriptors so a
	// stale exported COLUMNS does not override a terminal that can be asked directly.
	if width, err := parseColumns(); err == nil {
		return width, true
	}
	return 0, false
}
