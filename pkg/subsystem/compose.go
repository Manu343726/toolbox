package subsystem

import (
	"context"
	"fmt"
)

// A composition is extensible by the subsystems that are part of it.
//
// Most subsystems expose features: they serve a contract, and something calls it. Some do not,
// and those are the ones this file is about. A subsystem that **contributes** rather than
// **serves** exists to configure the deployment it is part of — to add a subsystem, to decide
// what an existing one is pointed at — and to hold the material that decision needs without
// that material appearing anywhere the deployment describes itself.
//
// The concrete case this exists for: a team wants the assistant in their project to have their
// coding standards, which live in a private repository. The obvious answers are all worse.
// Writing the remote into the project's configuration puts a credential in the one file a
// person writes and reviews, and a private repository means a credential. Hard-coding it into
// the host's composition root forks that root. Copying the skills in gives up the pin, so
// drift is invisible. A contributor holds the credential, uses it once, and hands over a
// checkout path — so the configuration a person reviews says which catalog, the deployment's
// records say where it came from, and neither says how it was reached.
//
// # What a contribution is, and what it is not
//
// A contribution **adds a subsystem** to the composition. It does not rewrite the
// configuration of one already registered.
//
// That distinction is not a simplification, it is the whole design. Rewriting a registered
// subsystem's options means merging two sources of the same settings with a precedence rule
// between them, and "which wins, the file or the code" is a question with no good answer: a
// person editing a file that silently loses to code they cannot see is worse than either
// winning. Adding a subsystem has no such question, because a contribution brings its own
// subsystem rather than an opinion about somebody else's.
//
// A contributor that wants different settings for a subsystem the deployment already has
// composes its own under its own name. Two subsystems serving the same contract is what the
// provider model is for: resolution is by identifier, so both are reachable and the deployment
// decides which the project names.
//
// # Ordering, and why nothing is half-configured
//
// Contributions are applied in a **configure phase that completes before any subsystem in the
// composition is started**. A subsystem that is contributed is therefore already configured
// when anything first asks the deployment what it can reach, so there is no window in which a
// catalog exists in the configuration and not in the provider directory. A deployment whose
// contributor failed does not start at all, rather than starting and serving an incomplete
// set — because a project naming a reference that resolves to nothing is the one state a
// project's list must never be in, and a startup failure is how a person hears about it.
//
// Each contributor runs exactly once, and the phase iterates while contributors keep
// contributing. It terminates because a name can be composed once: a contributor is already
// registered under its name by the time it runs, so a cycle — A composing B while B composes A
// — is refused as a name taken rather than looped on.

// Compositor is what a contributing subsystem is given: a way to add to the composition it is
// part of.
//
// It is declared here and implemented by the composition package rather than the other way
// round, because a subsystem's configuration must be expressible without naming the thing that
// composes it. That is the same rule every contract in this framework follows: a provider
// implements a contract, and the thing that resolves to it depends on the contract rather than
// on the implementation.
type Compositor interface {
	// Compose adds a subsystem to the deployment under a name.
	//
	// The name is the deployment's, not the contributor's: it is what a registry entry, a
	// provider record, a log line and a command name it. It is refused when taken, so a
	// contribution may add a subsystem and never replace one — and the refusal says so,
	// because a contributor that silently overrode the deployment's own subsystem would be
	// the precedence rule this design refuses to have.
	//
	// The factory is called later, in the start phase, and a factory that fails is a
	// startup failure. It is not called here, so a contribution cannot start a subsystem
	// before the composition it is part of exists.
	Compose(name string, factory Factory) error
}

// Contributor is a subsystem that configures its composition rather than only serving one.
//
// The host asks a built subsystem whether it is one, so a subsystem declares this by
// implementing it rather than by being listed somewhere. `Server` implements it when its
// configuration supplied a `Configure` hook, which is how a subsystem written against this
// package gets it without writing a line of ceremony.
type Contributor interface {
	// Configure applies this subsystem's contribution to the composition.
	//
	// It runs before any subsystem is started, with a context bounded by the start. A
	// failure aborts the start: a deployment that could not be fully configured does not
	// come up partly configured.
	Configure(ctx context.Context, into Compositor) error
}

// Configure is the hook on Config that makes a subsystem a contributor.
//
// It is a field rather than a second registration because "does this subsystem contribute" is
// a property of the subsystem, not of the composition: the same built server contributes in
// one deployment and merely runs in another, and a composition that had to be told which of
// its subsystems were contributors would be a second list of everything.
func (s *Server) Configure(ctx context.Context, into Compositor) error {
	if !s.IsContributor() {
		return fmt.Errorf("subsystem %q contributes nothing to its composition", s.config.Name)
	}
	return s.config.Configure(ctx, into)
}

// IsContributor reports whether this subsystem contributes to its composition.
func (s *Server) IsContributor() bool { return s != nil && s.config.Configure != nil }

// Serves reports whether a subsystem has anything to serve: a contract, a mount, or work of
// its own that has to run.
//
// A subsystem with none of those is not started. It has no listener, so no port, no handshake
// and no registry entry; it contributed to the composition and is finished. That is what makes
// "may not expose features itself" true rather than aspirational — a contributor that still
// opened a port would be visible in every deployment listing, and a thing whose only job is
// to decide how the rest of the deployment is wired has nothing to be reached for.
func (s *Server) Serves() bool {
	return s != nil && (len(s.config.Services) > 0 || len(s.config.Mounts) > 0 ||
		s.config.Background != nil)
}
