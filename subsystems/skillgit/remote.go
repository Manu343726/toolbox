package skillgit

import (
	"strings"
)

// A remote is two things, and conflating them is how a credential ends up on disk.
//
// The **remote** is where a repository is: `https://github.com/owner/repo.git`, or
// `git@github.com:owner/repo.git`. It is not a secret, it is the thing a person needs to see
// in order to know what content they are being served, and it is worth writing down.
//
// The **credential** is what gets git past whatever the remote asks for: a token in the URL's
// userinfo, a password. It is a secret, it is held by the machine rather than by the
// deployment's configuration, and it is worth writing down nowhere.
//
// So a remote is recorded without its credential and used with it, and the two are separated
// before either is stored. The alternative — recording the remote as named and redacting it
// only when displayed — writes the token to a file that is readable, often committed, and
// frequently attached to a bug report, while *looking* like it does not. Redaction at display
// time hides the symptom and leaves the defect.

// Auth states how a catalog authenticates to its remote.
//
// It is recorded because "this catalog reaches a private repository" and "this one reaches a
// public one" are different facts a person needs before depending on the content, and because
// a remote recorded without its credential is otherwise indistinguishable from a public one.
//
// **What the credential-free record does and does not achieve** is worth being exact about,
// because a guarantee that is over-read is worse than none:
//
//   - **This framework's own records never hold a credential.** Not the registration file, not
//     a provider record, not a descriptor, not an MCP tool schema, not anything the
//     deployment serves. That is what this split is for.
//   - **Nothing authenticated crosses a wire this framework owns.** A clone happens once, in
//     the process that held the credential.
//   - **git records the remote it was cloned from, userinfo and all**, in the checkout's own
//     `.git/config`. That is git's file, in git's own format, and this framework neither
//     writes nor reads it. So a caller who puts a token in a URL has put it on the machine's
//     disk in git's record — the split here stops *this framework* from also writing it to a
//     file a person reads, may commit, and may paste into a bug report. It does not make a
//     token in a URL harmless, and it is not meant to.
//
// So the answer to "where should the credential be" is the machine's own mechanisms — an ssh
// agent, a credential helper, a `.netrc` — which is also `AuthMachine` below. A contributor
// holding a credential should hand over a remote that carries none, and if it must fetch with
// one, it should fetch and then hand over the path.
type Auth string

const (
	// AuthNone needs nothing: a public repository, or a local path.
	AuthNone Auth = ""
	// AuthMachine means the machine's own mechanisms reach the remote — an ssh agent, a
	// credential helper, a `.netrc`. The deployment neither holds nor records a credential,
	// and the remote is recorded exactly as named because there was nothing to remove.
	AuthMachine Auth = "machine"
	// AuthCheckout means the caller's remote carried a credential, and git recorded it in the
	// checkout when it cloned. The registration does not repeat it. A checkout can still sync,
	// because git holds the remote itself, so this is a fact about who now has the credential
	// rather than a failure waiting to happen.
	AuthCheckout Auth = "checkout"
)

// userinfoIsCredential says whether a scheme's userinfo is a secret or a username.
//
// This is the whole decision, and getting it wrong in either direction is bad in a way that
// shows up late. Treating a username as a credential strips it, and stripping the username
// from `ssh://git@host/path` or `git@host:path` breaks a remote that worked — so a "careful"
// redaction that hides nothing and breaks the thing. Treating a password as a username writes
// it to disk. So the answer is per scheme, and it is here rather than inlined twice.
//
// `http` and `https` carry `user:password` or a bare token, which is why only they are
// credentials. `ssh` authenticates by key and its userinfo is the account to use. `git` and
// `file` have no authentication in a URL at all, so userinfo there is either a mistake or
// something to strip rather than act on.
var userinfoIsCredential = map[string]bool{
	"http":  true,
	"https": true,
	"ssh":   false,
	"git":   true,
	"file":  true,
}

// parsed is a URL-shaped remote, taken apart without deciding anything about it.
type parsed struct {
	// scheme is the URL scheme, which decides what userinfo means.
	scheme string
	// userinfo is what followed the scheme before the host, or empty.
	userinfo string
	// host is the authority with any userinfo removed.
	host string
	// path is what followed the first slash of the authority, without its slash.
	path string
	// hasPath separates a remote with no path from one whose path is empty.
	hasPath bool
}

// parse separates a remote into its parts, and reports false for one that is not URL-shaped.
//
// A scp-style remote is not URL-shaped and is returned whole: `git@` in `git@host:path` is a
// username, and it is required for the remote to work, so there is nothing here to decide.
func parse(remote string) (parsed, bool) {
	trimmed := strings.TrimSpace(remote)
	// The longest scheme first, so `https` is not matched by a scheme that prefixes it.
	for _, scheme := range []string{"https", "http", "ssh", "git", "file"} {
		head, found := strings.CutPrefix(trimmed, scheme+"://")
		if !found {
			continue
		}
		authority, path, hasPath := strings.Cut(head, "/")
		userinfo, host, hasUser := strings.Cut(authority, "@")
		if !hasUser {
			userinfo, host = "", authority
		}
		return parsed{scheme: scheme, userinfo: userinfo, host: host, path: path, hasPath: hasPath}, true
	}
	return parsed{}, false
}

// rebuild assembles the parts back into a URL, which is the form both a person reads and git
// accepts.
func (p parsed) rebuild() string {
	rebuilt := p.scheme + "://" + p.host
	if p.hasPath {
		rebuilt += "/" + p.path
	}
	return rebuilt
}

// carriesCredential reports whether this remote's userinfo is a secret rather than a username.
func (p parsed) carriesCredential() bool {
	return p.userinfo != "" && userinfoIsCredential[p.scheme]
}

// HasCredential reports whether a named remote carries a credential in it.
//
// It is a question about the *name*, asked before the remote is stored, because that is the
// last moment the credential is still separable from the remote. After that the two have been
// split and the question no longer has an answer to give.
func HasCredential(remote string) bool {
	parsed, ok := parse(remote)
	return ok && parsed.carriesCredential()
}

// authFor states what a named remote's authentication will be, which is a question about the
// name asked before the remote is stored.
func authFor(remote string) Auth {
	if HasCredential(remote) {
		return AuthCheckout
	}
	return AuthMachine
}

// CredentialFreeRemote returns a remote with any credential in its URL removed.
//
// The result is both what is recorded and what is shown, because they are the same fact: a
// remote without a credential is what a person needs to read, and it is what git needs to be
// handed when the machine already holds the credential. Two functions differing only in how
// much they removed would be one function with a parameter nobody could justify.
//
// Only the credential goes. The host and path are what identify the repository, and dropping
// them would produce a remote naming no host at all — which reads as careful redaction and is
// a broken remote.
func CredentialFreeRemote(remote string) string {
	parsed, ok := parse(remote)
	if !ok || !parsed.carriesCredential() {
		return strings.TrimSpace(remote)
	}
	return parsed.rebuild()
}

// describeRemote renders a remote for a person, which is the credential-free form.
func describeRemote(remote string) string { return CredentialFreeRemote(remote) }
