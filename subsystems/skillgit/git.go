package skillgit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
)

// Git is the git binary's operations a catalog needs.
//
// It shells out to `git` rather than embedding a library, and that is the decision everything
// else here follows from: `git` already knows the machine's ssh keys, its credential helpers,
// its proxy configuration and its insteadOf rules, and "credentials are deployment
// configuration" means exactly that a deployment should not have to hand a private repository's
// secret to a framework. A library would need its own answer to all four, and a worse one.
//
// Nothing is resolved automatically. A merge that cannot be completed is reported with the
// repository's path, because a catalog that resolved its own conflicts would be choosing a
// side in somebody else's repository.
type Git struct {
	// binary is the git executable, so a test can name a real one and a deployment can name
	// an absolute path.
	binary string
	// timeout bounds one git invocation. It is not the caller's deadline — a clone of a large
	// repository is legitimately slow — but a git that has wedged is a deployment that has
	// wedged, and a catalog must not hold a request open forever on it.
	timeout time.Duration
}

// NewGit returns the git operations, refusing a binary that is not there.
//
// The check is at construction rather than at first use so that a deployment learns at start
// that it cannot offer git-backed catalogs, instead of a user discovering it when a tool
// refuses to register a catalog.
func NewGit(binary string) (*Git, error) {
	if strings.TrimSpace(binary) == "" {
		binary = "git"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, api.WrapError(api.KindFailedPrecondition, err,
			"git-backed skill catalogs need the %s binary, and it is not on this machine's "+
				"PATH. A deployment serving only its own project's skills is unaffected", binary)
	}
	return &Git{binary: resolved, timeout: 10 * time.Minute}, nil
}

// Clone makes a shallow copy of a remote at a directory.
//
// Shallow, because a catalog serves the current content of a repository and a full history of
// every repository it has ever been asked about is disk nobody asked for. The depth is one
// commit rather than a branch's whole history, so what is pinned is what is served.
func (g *Git) Clone(ctx context.Context, remote, directory string) error {
	if err := ensureAbsent(directory); err != nil {
		return err
	}
	// The directory itself, not only its parent: git is invoked *inside* it, so a
	// directory that does not exist yet is a chdir failure rather than a clone.
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return api.WrapError(api.KindInternal, err, "creating %s", directory)
	}
	_, err := g.run(ctx, directory,
		"clone", "--depth=1", "--single-branch", "--no-tags", remote, ".")
	return err
}

// Init makes a new repository at a directory, and commits whatever is in it.
//
// This is what "create a catalog" means: a repository made where the deployment runs and
// served from there. Pushing is a separate act, so a deployment can offer a git-backed catalog
// on a machine with no account anywhere.
//
// A directory that already holds a repository is refused, and that is the precise thing to
// refuse rather than "a directory that is not empty": a populated directory is a normal place
// to start a repository, and the case that must never happen silently is a **second** catalog
// initialised over a checkout a previous one left behind — which would commit one catalog's
// content into another catalog's history. Two catalogs never share one repository.
func (g *Git) Init(ctx context.Context, directory string, identity Identity) error {
	if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
		return api.Errorf(api.KindAlreadyExists,
			"%s is already a git repository, so a catalog cannot be created there without "+
				"reusing its history. Remove it, or choose another name", directory)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return api.WrapError(api.KindInternal, err, "creating %s", directory)
	}
	for _, arguments := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.name", identity.Name},
		{"config", "user.email", identity.Email},
		{"add", "--all"},
	} {
		if _, err := g.run(ctx, directory, arguments...); err != nil {
			return err
		}
	}
	// The first commit is skipped when the directory was empty, because git has nothing to
	// record and says so by refusing. A repository with no commit is a valid empty catalog.
	return g.Commit(ctx, directory, "The first commit of a new skill catalog.", identity)
}

// Remote names the remote a directory pushes to and pulls from, and is set once at creation.
//
// It is not configurable afterwards, because a catalog whose remote could change silently is a
// catalog whose content could come from somewhere nobody chose. Changing it is a new catalog
// under a new name, which is a decision rather than a flag.
func (g *Git) Remote(ctx context.Context, directory, remote string) error {
	_, err := g.run(ctx, directory, "remote", "add", "origin", remote)
	return err
}

// HasRemote reports whether a directory has an origin, so a catalog created locally is
// distinguishable from one that can sync.
func (g *Git) HasRemote(ctx context.Context, directory string) (bool, error) {
	remotes, err := g.run(ctx, directory, "remote")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(remotes, "\n") {
		if strings.TrimSpace(line) == "origin" {
			return true, nil
		}
	}
	return false, nil
}

// Commit records one change, as one commit.
//
// Not batched: a change is a change, and a history showing one commit per thing is a history
// somebody can read. A directory with nothing staged is not an error — it means the content
// was already what the repository holds, and committing nothing is the honest outcome.
func (g *Git) Commit(ctx context.Context, directory, message string, identity Identity) error {
	pending, err := g.run(ctx, directory, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(pending) == "" {
		return nil
	}
	for _, arguments := range [][]string{
		{"config", "user.name", identity.Name},
		{"config", "user.email", identity.Email},
		{"add", "--all"},
		{"commit", "--message", message},
	} {
		if _, err := g.run(ctx, directory, arguments...); err != nil {
			return err
		}
	}
	return nil
}

// Push sends a directory's commits to its remote.
func (g *Git) Push(ctx context.Context, directory string) error {
	if err := ensureClean(ctx, g, directory); err != nil {
		return err
	}
	_, err := g.run(ctx, directory, "push", "--set-upstream", "origin", "HEAD")
	return err
}

// Pull updates a directory from its remote, rebasing what is local on top.
//
// A rebase rather than a merge, because a catalog's commits are each one change and a linear
// history is one somebody can read. A conflict is **not** resolved here: it is reported with
// the repository's path, because the person who can decide what a conflict meant is the person
// who owns the repository, and a catalog that chose a side would be choosing it for them.
func (g *Git) Pull(ctx context.Context, directory string) error {
	hasRemote, err := g.HasRemote(ctx, directory)
	if err != nil {
		return err
	}
	if !hasRemote {
		return api.Errorf(api.KindFailedPrecondition,
			"the catalog at %s was created locally and has no remote to sync from. It is "+
				"served from this machine's copy", directory)
	}
	_, err = g.run(ctx, directory, "pull", "--rebase", "origin")
	if err != nil {
		return api.WrapError(api.KindFailedPrecondition, err,
			"the catalog at %s could not be synced. Nothing has been resolved: look there, "+
				"because deciding what a conflict meant is a decision about somebody else's "+
				"repository", directory)
	}
	return nil
}

// Head is a repository's current commit, which is what a catalog's location reports so a
// person can tell what they are being served.
func (g *Git) Head(ctx context.Context, directory string) (string, error) {
	head, err := g.run(ctx, directory, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head), nil
}

// Clean reports whether a repository has nothing uncommitted.
func (g *Git) Clean(ctx context.Context, directory string) (bool, error) {
	pending, err := g.run(ctx, directory, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(pending) == "", nil
}

func ensureClean(ctx context.Context, g *Git, directory string) error {
	clean, err := g.Clean(ctx, directory)
	if err != nil {
		return err
	}
	if clean {
		return nil
	}
	pending, _ := g.run(ctx, directory, "status", "--porcelain")
	return api.Errorf(api.KindFailedPrecondition,
		"the catalog at %s has changes that are not committed:\n%s\nA modification is one "+
			"commit, so commit it before pushing", directory, pending)
}

// ensureAbsent refuses a directory that already holds something.
//
// It is the check that makes a name mean one catalog: registering a name that is taken is
// refused rather than sharing a directory with whoever has it.
func ensureAbsent(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return api.WrapError(api.KindInternal, err, "reading %s", directory)
	}
	if len(entries) == 0 {
		return nil
	}
	return api.Errorf(api.KindAlreadyExists,
		"%s already holds %d entries, so it is not an empty directory to create a catalog in",
		directory, len(entries))
}

// run invokes git and returns its output, with a failure reported as this framework's own.
//
// A failure that reached a person through git's own stderr is more use than a summary of it,
// so the output is carried in the error rather than discarded. The classification comes first
// though: a caller that asked to do something impossible should not have to read a paragraph
// of git output to find out that it is impossible.
func (g *Git) run(ctx context.Context, directory string, arguments ...string) (string, error) {
	bounded, cancel := withTimeout(ctx, g.timeout)
	defer cancel()
	command := exec.CommandContext(bounded, g.binary, arguments...)
	command.Dir = directory
	// Git is chatty on stderr even when it succeeds, and a warning is not a failure; the
	// output is captured so it can be attached to a failure instead of printed.
	var stderr strings.Builder
	command.Stderr = &stderr
	command.Env = append(os.Environ(),
		// Nothing in a catalog's history should depend on the terminal width, and a pager
		// would make an invocation that is not a terminal wait for input that never comes.
		"GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0",
	)

	stdout, err := command.Output()
	if err == nil {
		return string(stdout), nil
	}
	// A cancelled context is the caller stopping, not the repository refusing, and reporting
	// it as a conflict would send a person to look at a repository that is fine.
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		detail = err.Error()
	}
	return "", api.WrapError(api.KindInternal, nil, "git %s failed: %s",
		strings.Join(arguments, " "), detail)
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		// The caller already bounds this, and a second deadline would only make the tighter
		// of the two apply for reasons nobody reading the call can see.
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// Identity is whose name goes on a commit.
//
// It is configured rather than assumed, because the deployment is a machine and the project is
// where the work is: the person whose repository it is should get the last word on whose name
// is on the commit. One identity governs a deployment's commits across every catalog it holds,
// because it is the same person on the same machine making them.
type Identity struct {
	// Name is the committer name.
	Name string
	// Email is the committer email.
	Email string
}

// DefaultIdentity is used when a deployment configured none.
//
// It is a real identity rather than a placeholder, because a placeholder is what ends up in
// somebody's repository's history: `Toolbox <toolbox@localhost>` says plainly that a machine
// made the commit, which is the fact a reader of that history wants.
func DefaultIdentity() Identity {
	return Identity{Name: "Toolbox", Email: "toolbox@localhost"}
}

// Valid reports whether an identity can be committed with, refusing one git would reject.
func (i Identity) Valid() bool {
	return strings.TrimSpace(i.Name) != "" && strings.Contains(i.Email, "@")
}

// RemoteURL resolves what a caller named into a URL git can use.
//
// A caller may name a remote as a URL, an scp-style `git@host:owner/repo`, or a local path —
// all of which are handed to git untouched — or as the `owner/repo` shorthand, which is
// resolved to GitHub.
//
// The shorthand resolves to **GitHub**, not to skills.sh, and the distinction is worth being
// precise about: skills.sh is a *directory* over skills that live in people's repositories, not
// a host they are served from. Its own installer clones the repository. So a catalog registered
// this way is a git-backed catalog whose content is a repository the public directory also
// indexes, and pretending there were a skills.sh endpoint to fetch from would be a claim that
// does not hold.
func RemoteURL(named string) string {
	trimmed := strings.TrimSpace(named)
	if trimmed == "" {
		return ""
	}
	if looksLikeRemote(trimmed) {
		return trimmed
	}
	return "https://github.com/" + strings.TrimSuffix(trimmed, "/") + ".git"
}

// looksLikeRemote reports whether a name is already something git can use.
//
// The test is deliberately about *shape* rather than about which hosts exist: a deployment may
// clone from a host this framework has never heard of, and a check against a list of known hosts
// would refuse it. What matters is that it is not a bare `owner/repo` shorthand.
func looksLikeRemote(named string) bool {
	if strings.HasPrefix(named, "/") || strings.HasPrefix(named, ".") {
		// An absolute or relative path, which is how a deployment serves a catalog from a
		// directory it already has.
		return true
	}
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(named, scheme) {
			return true
		}
	}
	// scp-style: user@host:path
	if at, colon, found := strings.Cut(named, "@"); found && colon > at {
		return true
	}
	// Anything with a host in it is a URL as far as git is concerned, and a shorthand is
	// exactly `owner/repo`: one segment, a slash, another segment.
	host, _, found := strings.Cut(named, "/")
	return !found || strings.Contains(host, ".")
}
