// Package repocheck holds the checks that run against the repository's own shape
// rather than against any package's behaviour.
//
// The one that matters here is a Go file that the toolchain never compiles. A
// directory holds one package, and a file in it that declares a different one is
// not an error: Go lists it under IgnoredGoFiles and carries on. The build
// passes, the tests pass, the file sits there looking maintained, and whatever it
// was written to do has never happened. The whole framework registers its
// contracts and their documentation from files of exactly that kind, so a file
// silently skipped is a subsystem documenting nothing and nobody finding out
// until a help page has no descriptions on it.
package repocheck

import (
	"bytes"
	"fmt"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Mismatch is one Go file the toolchain would not compile.
type Mismatch struct {
	// Path is the file, relative to the tree that was checked.
	Path string
	// Dir is the directory holding it.
	Dir string
	// Package is the package that directory holds.
	Package string
	// Declared is the package the file's clause names.
	Declared string
}

// Error names the file, the package its directory holds, and the one it declares,
// because "the build passes" is not a reason anyone would otherwise look.
func (m Mismatch) Error() string {
	return fmt.Sprintf(
		"%s declares package %q, and its directory is package %q. Go does not compile it: "+
			"it lists the file under IgnoredGoFiles and carries on, so whatever it registers "+
			"has never run. Rename the package to %q.",
		m.Path, m.Declared, m.Package, m.Package,
	)
}

// Packages reports every Go file in a tree whose package clause does not match the
// package its directory holds.
//
// It reads files rather than asking the toolchain, because the toolchain's answer
// is the thing being checked: a build-constrained file and a misnamed one are
// reported the same way by go list, and only one of them is a mistake. A file
// carrying an explicit build constraint is therefore skipped — that is how a
// repository keeps a helper for a build it does not perform, and there is nothing
// to fix about it.
func Packages(root string) ([]Mismatch, error) {
	byDir := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		byDir[filepath.Dir(path)] = append(byDir[filepath.Dir(path)], path)
		return nil
	})
	if err != nil {
		return nil, err
	}

	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	mismatches := make([]Mismatch, 0)
	for _, dir := range dirs {
		found, err := mismatchesIn(root, dir, byDir[dir])
		if err != nil {
			return nil, err
		}
		mismatches = append(mismatches, found...)
	}
	return mismatches, nil
}

// mismatchesIn reports the files of one directory that do not match its package.
func mismatchesIn(root, dir string, files []string) ([]Mismatch, error) {
	type parsed struct {
		path     string
		pkg      string
		isTest   bool
		excluded bool
	}
	entries := make([]parsed, 0, len(files))
	checked := 0
	for _, path := range files {
		clause, isTest, excluded, err := readPackageClause(path)
		if err != nil {
			return nil, err
		}
		if excluded {
			continue
		}
		entries = append(entries, parsed{path: path, pkg: clause, isTest: isTest})
		checked++
	}
	if checked == 0 {
		return nil, nil
	}

	// The directory's package is the one most of its files hold, because that is
	// what the directory is about and a file that disagrees is the mistake. Test
	// files vote too — foo and foo_test both name foo — which is what settles it
	// when a stray file is a minority rather than a pair.
	//
	// Where the files disagree equally there is no right answer to report against,
	// and the report picks one by name so that two runs of this check say the same
	// thing. A directory in that state is broken in more than one way, and the
	// message for each file names what the directory holds rather than claiming
	// more certainty than there is.
	counts := map[string]int{}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.pkg
		if entry.isTest {
			name = strings.TrimSuffix(name, "_test")
		}
		if _, seen := counts[name]; !seen {
			names = append(names, name)
		}
		counts[name]++
	}
	sort.Strings(names)
	pkg := ""
	for _, name := range names {
		if pkg == "" || counts[name] > counts[pkg] {
			pkg = name
		}
	}

	mismatches := make([]Mismatch, 0)
	for _, entry := range entries {
		// An external test package — foo_test beside foo — is how the standard
		// library's own tests are written, and the toolchain expects it.
		allowed := entry.pkg == pkg
		if entry.isTest && entry.pkg == pkg+"_test" {
			allowed = true
		}
		if allowed {
			continue
		}
		relative, relErr := filepath.Rel(root, entry.path)
		if relErr != nil {
			relative = entry.path
		}
		mismatches = append(mismatches, Mismatch{
			Path:     relative,
			Dir:      relativeDir(relative),
			Package:  pkg,
			Declared: entry.pkg,
		})
	}
	return mismatches, nil
}

// readPackageClause returns the package a file declares, whether it is a test, and
// whether an explicit build constraint marks it as a file this build does not
// perform.
func readPackageClause(path string) (clause string, isTest, excluded bool, err error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return "", false, false, err
	}
	// The parser reads the clause, and reports a file that is not Go at all — which
	// is the case this check exists to notice, so a parse failure is an error
	// rather than a file to skip.
	parsed, err := parser.ParseFile(token.NewFileSet(), path, source, parser.PackageClauseOnly)
	if err != nil {
		return "", false, false, fmt.Errorf("read the package clause of %s: %w", path, err)
	}
	// Only the comment block before the clause counts: a //go:build line further
	// down is prose, and a helper that mentions one is not constrained by it.
	head := source
	if offset := bytes.Index(source, []byte("package ")); offset >= 0 {
		head = source[:offset]
	}
	for _, line := range strings.Split(string(head), "\n") {
		if constraint.IsGoBuild(line) {
			excluded = true
			break
		}
	}
	return parsed.Name.Name, strings.HasSuffix(path, "_test.go"), excluded, nil
}

// relativeDir is the directory of a path relative to the tree root, for the
// message rather than for the search.
func relativeDir(relative string) string {
	directory := filepath.Dir(relative)
	if directory == "." {
		return "."
	}
	return directory
}
