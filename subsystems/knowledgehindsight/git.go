package knowledgehindsight

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// gitTimeout bounds one `git` invocation.
//
// It is short because the calls here are local plumbing — resolving a ref is a file read — and a
// reconcile that hung on a git lock would look like a backend that had stopped answering.
const gitTimeout = 10 * time.Second

// resolvedHead returns the commit a corpus root is at, and whether it is a git checkout at all.
//
// It shells out rather than reading `.git` directly because the worktree layout varies — a linked
// worktree, a submodule, a `gitdir` pointer — and reimplementing that resolution is a way to
// report a plausible wrong commit. A directory that is not a checkout is not an error: a corpus is
// a directory of markdown, and requiring git would be requiring a version control system this
// framework has no opinion about.
func resolvedHead(ctx context.Context, roots []knowledge.CorpusRoot) (string, error) {
	for _, root := range roots {
		if dir := gitDir(root.Path); dir != "" {
			out, err := runGit(ctx, dir, "rev-parse", "HEAD")
			if err == nil && out != "" {
				return out, nil
			}
		}
	}
	return "", nil
}

// gitDir returns the `.git` directory for a path, or the empty string.
func gitDir(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ""
	}
	candidate := filepath.Join(path, ".git")
	if st, err := os.Stat(candidate); err == nil {
		if st.IsDir() {
			return candidate
		}
		// A `.git` file is a pointer, which is what a linked worktree and a submodule
		// have. Following it is a read of one line.
		if data, rerr := os.ReadFile(candidate); rerr == nil {
			line := strings.TrimSpace(string(data))
			if rest, ok := strings.CutPrefix(line, "gitdir:"); ok {
				target := strings.TrimSpace(rest)
				if !filepath.IsAbs(target) {
					target = filepath.Join(path, target)
				}
				return target
			}
		}
	}
	return ""
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Git must not take the user's pager, editor or credential helpers into a subprocess
	// a service started, and none of them help here.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
