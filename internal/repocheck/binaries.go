package repocheck

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The magic numbers a compiler writes at the start of a binary. A file beginning
// with one of these is a build output whatever it is called and wherever it sits,
// which is the point: a list of paths and names goes stale with every new
// subsystem, and the thing being kept out of the repository does not care what it
// is called.
var (
	elfMagic = []byte{0x7f, 'E', 'L', 'F'}
	peMagic  = []byte{'M', 'Z'}
	// A Mach-O fat binary, either byte order, and a thin one, either width and
	// byte order. 32-bit little-endian is ce fa ed fe and 64-bit is cf fa ed fe;
	// the two big-endian forms start with the same bytes reversed.
	machoFatBe = []byte{0xca, 0xfe, 0xba, 0xbe}
	machoFatLe = []byte{0xbe, 0xba, 0xfe, 0xca}
	machoLe32  = []byte{0xce, 0xfa, 0xed, 0xfe}
	machoLe64  = []byte{0xcf, 0xfa, 0xed, 0xfe}
	machoBe32  = []byte{0xfe, 0xed, 0xfa, 0xce}
	machoBe64  = []byte{0xfe, 0xed, 0xfa, 0xcf}
	// A Java class file begins with the same four bytes as a fat Mach-O, so it is
	// deliberately not listed: the two cannot be told apart from their first four
	// bytes, and a file is rejected either way.
	wasmMagic    = []byte{0x00, 'a', 's', 'm'}
	archiveMagic = []byte{'!', '<', 'a', 'r', 'c', 'h', '>', '\n'}
)

// Binary is one file in the tree that is a compiled artefact.
type Binary struct {
	// Path is the file, relative to the tree that was checked.
	Path string
	// Kind is what the file's first bytes identify it as.
	Kind string
}

// Error names the file and what it is, because "a binary is in the repository"
// is only actionable if the reader knows which one and that removing it is safe.
func (b Binary) Error() string {
	return fmt.Sprintf("%s is a %s, which is a build output rather than source", b.Path, b.Kind)
}

// Binaries reports every file in a tree that begins with a compiler's magic
// number, other than the ones inside a directory named "bin".
//
// The exception is a fact about this repository rather than about binaries: every
// Makefile builds into its module's bin/ directory, .gitignore ignores bin/, and a
// binary there is a local build that was asked for. A binary anywhere else is
// either a stray "go build ." that landed beside the source or a build that was
// committed, and those are the two this is looking for.
//
// It reads four bytes rather than shelling out to `file`, so the answer does not
// depend on what is installed, and it does not need the file to be readable as Go
// — which is the case for the files it is looking for. A file it cannot open is
// reported as an error rather than skipped, because a file nobody can read is a
// file nobody can account for.
func Binaries(root string) ([]Binary, error) {
	found := make([]Binary, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules":
				return fs.SkipDir
			case "bin":
				// Where a Makefile in this repository is told to put a build.
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		kind, err := identify(path)
		if err != nil {
			return err
		}
		if kind == "" {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}
		found = append(found, Binary{Path: relative, Kind: kind})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	return found, nil
}

// identify returns what a file's first bytes say it is, or an empty string when it
// is not a build output.
func identify(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	head := make([]byte, 8)
	read, err := file.Read(head)
	if err != nil && read == 0 {
		// An empty file, or one that could not be read at all. An empty file is
		// not a binary; anything else is the caller's problem to hear about.
		if isEmpty(err) {
			return "", nil
		}
		return "", err
	}
	head = head[:read]
	switch {
	case bytes.HasPrefix(head, elfMagic):
		return "ELF binary", nil
	case bytes.HasPrefix(head, peMagic):
		return "PE binary", nil
	case bytes.HasPrefix(head, machoFatBe), bytes.HasPrefix(head, machoFatLe),
		bytes.HasPrefix(head, machoLe32), bytes.HasPrefix(head, machoLe64),
		bytes.HasPrefix(head, machoBe32), bytes.HasPrefix(head, machoBe64):
		return "Mach-O binary", nil
	case bytes.HasPrefix(head, wasmMagic):
		return "WebAssembly module", nil
	case bytes.HasPrefix(head, archiveMagic):
		return "ar archive", nil
	}
	return "", nil
}

// isEmpty reports whether a read ended because the file had nothing in it.
func isEmpty(err error) bool {
	return err != nil && strings.Contains(err.Error(), "EOF")
}
