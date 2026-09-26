package repocheck_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// EnvRoot names the directory a check should be run against, relative to the
// repository root or absolute.
//
// It exists because the checks are useful against one module as well as against
// the whole workspace. The CI matrix tests every subsystem on its own, and a
// subsystem that has a file the toolchain never compiles fails its own build in
// exactly the same silent way it fails the workspace build — so the job that
// asserts a module stands alone should assert its shape too.
const EnvRoot = "REPOCHECK_ROOT"

// repositoryRoot returns the tree to check.
//
// With EnvRoot set it is that directory, resolved against the repository root, so
// a step can name a module the way a person would. Without it, the repository
// root, found by walking up to the workspace file so the check does not depend on
// where in the tree it was invoked from.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	root := workspaceRoot(t)
	if named := os.Getenv(EnvRoot); named != "" {
		resolved := named
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(root, named)
		}
		info, err := os.Stat(resolved)
		require.NoError(t, err, "%s names a directory that does not exist", EnvRoot)
		require.True(t, info.IsDir(), "%s must name a directory", EnvRoot)
		return resolved
	}
	return root
}

// workspaceRoot walks up to the go.work file.
func workspaceRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.work")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("no go.work above this test, so the repository root cannot be found")
		}
		directory = parent
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += "\n  - " + line
	}
	return out
}
