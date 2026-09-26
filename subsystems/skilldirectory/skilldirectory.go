// Package skilldirectory contributes the public directory of agent skills to a deployment.
//
// It is a **contributor** rather than a service: it exposes no contract of its own, and its
// whole job is to decide what the git-backed catalog provider should be serving. It has no
// listener, no port, no registry entry and no tool — a thing whose only output is a decision
// about how the rest of the deployment is wired has nothing to be reached for.
//
// # What it is for
//
// [skills.sh](https://www.skills.sh/) is a directory of agent skills that live in people's
// git repositories, addressed `owner/repo`. It is a registry over repositories rather than a
// host they are served from: its own installer shallow-clones the repository and reads the
// skill directories out of the checkout. So a skill from it is a git-backed catalog with a
// name, and giving a deployment that directory is a matter of registering the repositories —
// which is what this subsystem contributes.
//
// # Why it is a contributor and not a configuration file
//
// Because the same shape serves the case that a configuration file cannot. A team's coding
// standards live in a **private** repository, so reaching them needs a credential, and the one
// file a person writes and reviews in their project is the worst place for a secret. A
// contributor holds the credential, fetches once, and hands the provider a checkout path. What
// the project records is which catalog it depends on; what the deployment records is where the
// content came from; and neither records how it was reached. The public directory needs no
// credential, so this one holds none — but it is the same three lines, and a user's own
// contributor with a credential is a copy of this with the fetch changed.
//
// The repositories are read from the environment rather than from the project's
// configuration, for the same reason: what a deployment draws skills from is the deployment's
// answer, not the project's, and two projects on one machine should not have to state the
// same list twice.
package skilldirectory

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/skillgit"
)

const (
	// Name is the stable subsystem name.
	Name = "skilldirectory"
	// Version is the reference implementation version.
	Version = "0.1.0"
	// Description is what the deployment reports this subsystem as.
	Description = "Contributes the public directory of agent skills to a deployment."
	// ProviderName is the name this subsystem contributes the catalog provider under.
	//
	// It is its own, and deliberately not this subsystem's own name: a name is the
	// deployment's word for one thing, and a provider is not the contributor that composed
	// it. It is also not `skillgit`, so a deployment can run the framework's provider
	// alongside this one without either replacing the other — which is the whole difference
	// between a contribution and an override.
	//
	// It names the *provider*, not the public directory. The catalogs it serves are named
	// after the repositories they came from, so a project writes `skills: [standards]` and
	// not `skills: [public.standards]`: the provider is how the deployment reached the
	// content and the catalog is what the content is.
	ProviderName = "public"

	// RepositoriesVariable names the environment variable holding the repositories to
	// serve, as a comma-separated list of `owner/repo`.
	RepositoriesVariable = "TOOLBOX_SKILL_REPOSITORIES"
	// CheckoutVariable names the environment variable holding a directory to fetch into.
	//
	// It is a variable rather than a fixed path because a deployment's storage is its own
	// decision, and a contributor that chose the path would be a second thing to configure
	// for no benefit.
	CheckoutVariable = "TOOLBOX_SKILL_CHECKOUTS"
)

// Options configures the contributor.
type Options struct {
	// Repositories are the `owner/repo` names to serve, as a catalog each. Empty takes
	// them from RepositoriesVariable.
	Repositories []string
	// CheckoutDir is where the repositories are fetched to. Empty takes it from
	// CheckoutVariable, and then from the git provider's own data directory.
	CheckoutDir string
	// DataDir is the git provider's data directory, needed when CheckoutDir is empty.
	DataDir string
	// Git is the git operations, or nil to resolve them from the machine. It is here so a
	// deployment with a particular git can say so.
	Git *skillgit.Git
	// Identity is whose name goes on a commit this subsystem makes. Fetching does not
	// commit, so it is only used if a checkout is created rather than cloned into.
	Identity skillgit.Identity
	// Version overrides the implementation version.
	Version string
}

// New returns the contributor.
//
// It is a subsystem, so it goes through the usual declaration; the difference is that it
// contributes rather than serves, and the consequence of that is in `Configure`.
func New(options Options) (*subsystem.Server, error) {
	return subsystem.NewServer(subsystem.Config{
		Name:        Name,
		Version:     versionOr(options.Version, Version),
		Description: Description,
		Configure: func(ctx context.Context, into subsystem.Compositor) error {
			return contribute(ctx, options, into)
		},
	})
}

// contribute fetches the repositories and adds a catalog provider serving them.
//
// The fetch happens here, in the configure phase, with the start's context — so it happens
// before anything is started, and it happens with whatever credential this code holds. The
// provider is handed paths, which is the whole point: a path cannot carry a credential, so
// nothing that reaches the provider can put one into its records.
func contribute(ctx context.Context, options Options, into subsystem.Compositor) error {
	repositories := options.Repositories
	if len(repositories) == 0 {
		repositories = splitList(os.Getenv(RepositoriesVariable))
	}
	if len(repositories) == 0 {
		// Nothing to contribute is not a failure. A deployment that has not said which
		// repositories it wants is a deployment serving only its project's own skills, which
		// is a working deployment — and a contributor that refused to start would make an
		// optional thing a requirement.
		return nil
	}

	git := options.Git
	if git == nil {
		resolved, err := skillgit.NewGit("")
		if err != nil {
			return err
		}
		git = resolved
	}
	identity := options.Identity
	if !identity.Valid() {
		identity = skillgit.DefaultIdentity()
	}

	checkouts := options.CheckoutDir
	if checkouts == "" {
		checkouts = os.Getenv(CheckoutVariable)
	}
	if checkouts == "" {
		// The provider's own storage, so the fetch and the read are in one place and a
		// person deleting a catalog deletes its checkout.
		checkouts = filepath.Join(options.DataDir, "directory")
	}
	if err := os.MkdirAll(checkouts, 0o700); err != nil {
		return api.WrapError(api.KindInternal, err, "creating %s", checkouts)
	}

	registrations := make([]skillgit.Registration, 0, len(repositories))
	for _, named := range repositories {
		reference, err := catalogName(named)
		if err != nil {
			return err
		}
		directory := filepath.Join(checkouts, reference)
		if err := fetch(ctx, git, named, directory, identity); err != nil {
			return err
		}
		registrations = append(registrations, skillgit.Registration{
			ID:        reference,
			Directory: directory,
			// The public directory is browsed, not written to, so a catalog from it is
			// read-only — and this contributor will not commit to somebody's repository
			// because it happens to have been cloned.
			ReadOnly: true,
		})
	}

	return into.Compose(ProviderName, func() (*subsystem.Server, error) {
		return skillgit.New(skillgit.Options{
			// The name the composition gave it, so the deployment can say which provider
			// answered. A subsystem registered under its package's name instead is one
			// contribution silently colliding with the next.
			Name:          ProviderName,
			DataDir:       options.DataDir,
			Git:           git,
			Identity:      identity,
			Registrations: registrations,
		})
	})
}

// fetch puts one repository on disk, cloning it if it is not already there.
//
// It is this subsystem's whole reason to exist, and it is the one place a credential would
// be used if one were held. Cloning into a path that is already there is refused rather than
// refreshed, because a contributor that silently updated a checkout would invalidate every
// pin taken against it, and a pin is the agreement — not something a contribution may move.
func fetch(
	ctx context.Context, git *skillgit.Git, named, directory string, identity skillgit.Identity,
) error {
	if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
		return nil
	}
	return git.Clone(ctx, skillgit.RemoteURL(named), directory)
}

// catalogName is the identifier a repository is served under: its own name.
//
// Not the whole path, because a catalog's identifier is a word a project writes in `skills:`
// and `vercel-labs/skills` is not a word — it has a slash in it, and a catalog is one segment
// of a skill's URI. The owner is dropped for the same reason two organizations may both
// publish a `coding-standards`, and because the deployment's list of repositories is where a
// person resolves which one they meant.
//
// The rule for what a name may be is the provider's, applied here rather than restated: a
// second, looser copy would let a name through that the provider then refuses, which is a
// contributor's mistake reported as a provider's.
func catalogName(named string) (string, error) {
	trimmed := strings.TrimSpace(named)
	if trimmed == "" {
		return "", api.Errorf(api.KindInvalid,
			"a repository name is empty; the public directory addresses skills as owner/repo")
	}
	// The last segment, for a shorthand and for a path alike: `vercel-labs/skills` and
	// `/srv/mirrors/skills` are both served as `skills`. A split at the *first* separator
	// would read a path's leading slash as its first segment and take everything after it,
	// which is a name nobody could write in a configuration file.
	name := filepath.Base(filepath.ToSlash(strings.TrimSuffix(trimmed, "/")))
	if err := skillgit.ValidCatalogID(name); err != nil {
		return "", api.WrapError(api.KindInvalid, err,
			"%q is not a repository this catalog could be served under", named)
	}
	return name, nil
}

func splitList(value string) []string {
	var entries []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			entries = append(entries, trimmed)
		}
	}
	return entries
}

func versionOr(version, fallback string) string {
	if version == "" {
		return fallback
	}
	return version
}
