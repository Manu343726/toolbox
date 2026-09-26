package repocheck_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Manu343726/toolbox/internal/repocheck"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tree of Go files, written as a map of relative path to contents, so each case
// states the layout it is about instead of describing it.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, contents := range files {
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(contents), 0o644))
	}
	return root
}

func TestAFileMatchingItsDirectoryIsNotReported(t *testing.T) {
	root := write(t, map[string]string{
		"one/one.go":      "package one\n",
		"one/one_test.go": "package one\n",
		"two/two.go":      "package two\n",
	})
	found, err := repocheck.Packages(root)
	require.NoError(t, err)
	assert.Empty(t, found)
}

// This is the bug. The build passes, the tests pass, and the file has never run.
func TestAFileDeclaringAnotherPackageIsReported(t *testing.T) {
	root := write(t, map[string]string{
		"one/one.go":        "package one\n",
		"one/one_test.go":   "package one\n",
		"one/docs_embed.go": "package documentation\n",
	})
	found, err := repocheck.Packages(root)
	require.NoError(t, err)
	require.Len(t, found, 1)

	assert.Equal(t, filepath.Join("one", "docs_embed.go"), found[0].Path)
	assert.Equal(t, "one", found[0].Package)
	assert.Equal(t, "documentation", found[0].Declared)
	// The message has to name all three things, because "the build passes" is not
	// a reason anyone would otherwise go looking.
	assert.Contains(t, found[0].Error(), "docs_embed.go")
	assert.Contains(t, found[0].Error(), "documentation")
	assert.Contains(t, found[0].Error(), "one")
}

// The standard library's own tests are written this way, and the toolchain
// expects it, so flagging it would be a check that cries wolf.
func TestAnExternalTestPackageIsAllowed(t *testing.T) {
	root := write(t, map[string]string{
		"one/one.go":           "package one\n",
		"one/one_test.go":      "package one\n",
		"one/blackbox_test.go": "package one_test\n",
	})
	found, err := repocheck.Packages(root)
	require.NoError(t, err)
	assert.Empty(t, found)
}

// One stray file among several correct ones is the shape this bug takes, and the
// directory's package is decided by the majority rather than by whichever file
// happened to sort first.
func TestAMisnamedFileIsReportedAgainstTheDirectoriesPackage(t *testing.T) {
	root := write(t, map[string]string{
		"one/one.go":        "package one\n",
		"one/other.go":      "package one\n",
		"one/third.go":      "package one\n",
		"one/embed.go":      "package embed\n",
		"one/stray_test.go": "package two_test\n",
	})
	found, err := repocheck.Packages(root)
	require.NoError(t, err)

	reported := make(map[string]string, len(found))
	for _, mismatch := range found {
		reported[mismatch.Declared] = mismatch.Package
	}
	assert.Equal(t, map[string]string{"embed": "one", "two_test": "one"}, reported,
		"one report per file, each against the package the directory holds")
}

// A repository keeps a helper for a build it does not perform, and that file says
// so with a build constraint. Its package clause is its own business, so a check
// that reported it would send someone to fix something that is not broken.
func TestAFileCarryingABuildConstraintIsNotReported(t *testing.T) {
	root := write(t, map[string]string{
		"one/one.go": "package one\n",
		"tools.go":   "//go:build tools\n\npackage main\n",
		"helper.go":  "//go:build ignore\n\npackage main\n",
	})
	found, err := repocheck.Packages(root)
	require.NoError(t, err)
	assert.Empty(t, found)
}

// A //go:build line below the package clause is prose. A helper that mentions one
// in a comment is not constrained by it.
func TestAMentionOfABuildConstraintIsNotAConstraint(t *testing.T) {
	root := write(t, map[string]string{
		"one/one.go":   "package one\n",
		"one/notes.go": "package one\n\n// The tools.go file carries a //go:build line.\n",
	})
	found, err := repocheck.Packages(root)
	require.NoError(t, err)
	assert.Empty(t, found)
}

// A directory holding only tests has no non-test file to name the package, so the
// name comes from the tests and a test that disagrees with them is still reported.
func TestADirectoryOfOnlyTests(t *testing.T) {
	agreeing := write(t, map[string]string{"one/one_test.go": "package one\n"})
	found, err := repocheck.Packages(agreeing)
	require.NoError(t, err)
	assert.Empty(t, found)

	disagreeing := write(t, map[string]string{
		"one/one_test.go":   "package one\n",
		"one/stray_test.go": "package two\n",
	})
	found, err = repocheck.Packages(disagreeing)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "two", found[0].Declared)
}

// A file that is not Go at all is a file nobody can compile, so it is an error
// rather than something to step over.
func TestAFileThatIsNotGoIsAnError(t *testing.T) {
	root := write(t, map[string]string{"one/broken.go": "this is not Go\n"})
	_, err := repocheck.Packages(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken.go")
}

// Directories a check should not walk: a vendored copy and the repository's own
// metadata are not this repository's source.
func TestVendoredAndMetadataDirectoriesAreSkipped(t *testing.T) {
	root := write(t, map[string]string{
		"vendor/example/example.go": "package example\n",
		"vendor/example/wrong.go":   "package wrong\n",
		".git/hooks/pre-commit.go":  "package precommit\n",
		"one/one.go":                "package one\n",
	})
	found, err := repocheck.Packages(root)
	require.NoError(t, err)
	assert.Empty(t, found)
}

func TestATreeWithNoGoFilesIsEmpty(t *testing.T) {
	root := write(t, map[string]string{"README.md": "# nothing here\n"})
	found, err := repocheck.Packages(root)
	require.NoError(t, err)
	assert.Empty(t, found)
}

func TestAnAbsentTreeIsAnError(t *testing.T) {
	_, err := repocheck.Packages(filepath.Join(t.TempDir(), "nowhere"))
	require.Error(t, err)
}

// The check above is about the mechanism. This one is about this repository, and
// it is the assertion that would have caught the file whose package clause did not
// match its directory's: the documentation subsystem's docs_embed.go said
// "package documentation" in a package called "docs", Go listed it under
// IgnoredGoFiles, and the subsystem that exists to serve documentation served
// nothing at all.
func TestNoGoFileInThisRepositoryDeclaresAnotherPackage(t *testing.T) {
	root := repositoryRoot(t)
	found, err := repocheck.Packages(root)
	require.NoError(t, err)

	if len(found) > 0 {
		reported := make([]string, 0, len(found))
		for _, mismatch := range found {
			reported = append(reported, mismatch.Error())
		}
		t.Errorf("these files are not compiled, because their package clause does not match their directory:\n%s",
			joinLines(reported))
	}
}
