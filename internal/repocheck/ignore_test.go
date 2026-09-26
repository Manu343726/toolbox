package repocheck_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A .gitignore rule that hides source is worse than no rule at all, because nothing reports
// it: the file is written, the build sees it, the tests pass, and the commit that should have
// carried it does not. This happened here — a bare `toolbox` rule, meant to catch a build at
// the repository root, matched any path *component* of that name, and so silently ignored
// `cmd/toolbox/` and `pkg/*/proto/toolbox/`. A new host source file and a whole contract
// package were invisible to git.
//
// The check is the inverse of the binary check: where that one asks "is anything tracked here
// that is a build output", this asks "is anything on disk that git will not track, and is it
// source". Generated protobuf is legitimately ignored, so a directory of generated code is not
// a finding — but a `.proto`, a `.go` and a `.yaml` are all source, and one of those being
// invisible is the failure.
func TestNoSourceFileIsInvisibleToGit(t *testing.T) {
	root := repositoryRoot(t)
	ignored := ignoredSourceFiles(t, root)

	for _, path := range ignored {
		assert.NotEmpty(t, path, "a source file git will not track")
	}
	assert.Empty(t, ignored,
		"these source files are on disk and git will not track them, so a commit silently "+
			"leaves them out: %s", strings.Join(ignored, ", "))
}

// ignoredSourceFiles lists the tracked-eligible source files git is ignoring.
func ignoredSourceFiles(t *testing.T, root string) []string {
	t.Helper()
	// `git status --ignored` reports ignored paths as directories rather than as the files
	// inside them, so each is expanded before being judged. Without that, a directory of
	// generated protobuf and a file that happens to share its path look identical.
	output, err := runGit(root, "status", "--porcelain", "--ignored", "--untracked-files=all")
	require.NoError(t, err)

	var ignored []string
	for _, line := range strings.Split(output, "\n") {
		// "!! " marks an ignored path, and a status line is fixed width.
		if !strings.HasPrefix(line, "!! ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "!! "))
		if isSourceFile(filepath.Base(path)) {
			ignored = append(ignored, path)
		}
	}
	return ignored
}

// isSourceFile reports whether a file is source, as opposed to a build output or a generated
// artefact.
//
// The list is what makes this check possible: a rule hiding a `.pb.go` is doing its job, and a
// rule hiding a `.proto` is a bug. `.yaml` is here because a project's and a deployment's
// configuration are source — a person writes and reviews them — and because a lockfile written
// beside them on a run is the one `.yaml` that is not, which is why the caller reports the
// path rather than the verdict: a person reads it and says which it is.
func isSourceFile(name string) bool {
	switch filepath.Ext(name) {
	case ".go":
		// Generated protobuf and ConnectRPC code is ignored by design.
		return !strings.HasSuffix(name, ".pb.go") && !strings.HasSuffix(name, ".connect.go")
	case ".proto", ".mod", ".sum", ".md", ".yml":
		return true
	default:
		return false
	}
}

func runGit(root string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	return string(output), err
}
