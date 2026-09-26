# 0013 — A composition is extensible by the subsystems in it

Date: 2026-09-27
Status: accepted (written after the implementation, which changed two things in it — see
*What the implementation changed*)

## Context

Most subsystems expose features: they serve a contract, and something calls it. Some do not,
and those had no way to exist at all.

`cmd/toolbox` is a 702-line composition root with a hand-written map of twenty subsystem
factories. Everything a deployment runs is named there, and a deployment that wanted one more
subsystem — written by the person who wanted it — had exactly two options. Fork the
composition root, or reimplement its fanout, policy, provider directory, catalog seeding and
gateway wiring in a private binary. Neither is "add a subsystem"; both are "replace the
deployment".

A capability check confirmed what the options were worth. A user *can* already write a private
binary that composes its own deployment with whatever it likes held in Go: `host.New()`,
`Register`, `Start` all accept a factory closure, and a value in that closure never leaves the
process. So the capability was largely there. What was missing was **additivity** — a way to
add to a composition rather than replace it.

The case that made it worth having is a team whose coding standards live in a private git
repository, and who want the assistant in their project to have them.

| Option | What it costs |
|---|---|
| Put the remote in the project's configuration | A credential in the one file a person writes and reviews, and one they paste into bug reports |
| Hard-code the remote in the composition root | A fork of the composition root, forever |
| Copy the skills into the project | The pin is gone, so drift under the project is invisible |
| Register the remote by hand on each machine | Every developer runs the same command, and nobody can see what a deployment has |
| A contributor that holds the credential, fetches once, hands over a path | The three lines below |

## The decision

**A subsystem may contribute to the composition it is part of, rather than only serve one.**

The mechanism is small: an interface declared in `pkg/subsystem`, a hook on `subsystem.Config`,
and a phase in `pkg/host.Start` that runs before anything starts.

```go
// pkg/subsystem
type Compositor interface {
    Compose(name string, factory Factory) error
}

type Contributor interface {
    Configure(ctx context.Context, into Compositor) error
}

// pkg/host
func (h *Host) Start(ctx context.Context) error
```

`Compositor` is declared in `pkg/subsystem` and implemented by `pkg/host`, not the other way
round, so a subsystem's configuration is expressible without naming the thing that composes
it. That is the same rule every contract here follows: a provider implements a contract, and
the thing that resolves to it depends on the contract rather than on the implementation.

### A contribution adds; it does not replace

A contribution **adds a subsystem**. It does not rewrite the configuration of one already
registered, and that distinction is the design rather than a simplification.

Rewriting a registered subsystem's options means merging two sources of the same settings with
a precedence rule between them, and *"which wins, the file or the code"* has no good answer. A
person editing a file that silently loses to code they cannot see is worse than either winning
— the file looks like it is in charge and is not. Adding a subsystem has no such question,
because a contribution brings its own subsystem rather than an opinion about somebody else's.

A contributor that wants different settings for a subsystem the deployment already has composes
its own under its own name. Two subsystems serving the same contract is what the provider model
is for: resolution is by identifier, so both are reachable and the deployment decides which the
project names.

### The phases are separate, and the order is fixed

`build`, then `configure`, then `start`.

The alternative — letting a contributing subsystem start and then register what it provides —
leaves a window in which a subsystem is up and the thing it contributes is not in the provider
directory. Anything resolving during that window would see a deployment that looks
misconfigured, and the cause would be a race rather than a mistake.

A contributor that fails aborts the start. A deployment that could not be fully configured
serving an incomplete set is the state a project's `skills:` list cannot survive, and a startup
failure is how a person hears about it.

### A contributor that serves nothing is not started

No listener, so no port, no handshake, no registry entry, and no mention in any listing of
what the deployment runs.

This is what makes *"may not expose features itself"* true rather than aspirational. A
contributor that still opened a port would be visible everywhere, and a thing whose only output
is a decision about how the rest of the deployment is wired has nothing to be reached for. The
rule is a property of the server — no services, no mounts, no background work — rather than a
flag, because whether a subsystem has anything to serve is already known.

## What the guarantee is, and what it is not

A secret cannot be hidden from the code that uses it. What a contribution buys is that it is
never **serialised**, and for the fetch case never crosses a wire the framework owns.

| Surface | With a contributor |
|---|---|
| Project configuration file | never written |
| `subsystem.Descriptor` | not a field |
| `api.Provider` record | not a field |
| The provider's registration file | a path, not a remote |
| MCP tool schema, `describe_feature` | no operation exists |
| The skills lockfile | digests only |
| The wire | nothing authenticated crosses it |

Two things bound it, and both are stated rather than left implied.

**git records the remote it was cloned from, userinfo and all**, in the checkout's own
`.git/config`. That is git's file, in git's own format, and this framework neither writes nor
reads it. So a caller who puts a token in a URL has put it on the machine's disk in git's
record. The split stops *this framework* from also writing it somewhere a person reads — and
`catalogs.yaml` was doing exactly that before, which is why the redaction exists at all.

**A literal in a contributor's source is in the source.** So the material should be a
reference — an ssh agent, a credential helper, a `.netrc`, or a value read from the environment
at start — and a contributor holding a credential should fetch with it and hand over the path.

The guarantee is tested in `cmd/toolbox`: a contributor holding a credential-shaped value
configures a catalog, the deployment serves it, and the value is asserted absent from every
artefact in the table while the project reaches the skill. A list of excluded fields is what
this guarantee looks like when it is not tested, and it is wrong the first time a subsystem adds
a field.

## Why the provider gained a construction-time configuration

Contributions are applied before anything is started, so a provider that could only be told its
catalogs over its own service would hold none when the first request arrived. A contributed
provider has to be complete at construction.

`skillgit.Options.Registrations` adopts a set the deployment has already decided — and a
supplied registration must **already be on disk**, because a path is the one thing that cannot
carry a credential. That is not a limitation. It means the only field a contribution fills
cannot hold a secret, which is the mechanism's guarantee expressed as a type.

A deployment that would rather the provider fetched for it registers a remote over the
service, which is the existing path and which needs a person to have done it.

## What the implementation changed

This record was written after the code, which is the practice `docs/skills.md` states: a
decision taken later goes into the section it belongs to, and where the plan was wrong the
correction says so. Two things changed.

**The name of a contributed subsystem had to be checked, and it caught this work's own test.**
A contributed subsystem registered under the name in its own `Config`, not the name it was
composed under — which defeated the refusal `Compose` makes. The deployment would hold two
subsystems under one name with nothing to say so, and the collision would happen in the
registry rather than in the composition, where nothing could see it. It is checked now, and
the check failed the first version of the leak test, which is the only reason it is known to
work.

**The phase loop did not terminate, and the failure was unbounded allocation.** An earlier
version carried one wave's contributions into the next, which satisfied the cross-wave
duplicate check and defeated termination: it rebuilt the same subsystems for ever, taking a
0.07s test to 525 seconds and the machine's memory with it. The fix keeps the per-wave and
across-wave state in two fields with the lifetimes they actually have, and the invariant that a
name is built **once** is checked rather than reasoned about — a mistake in the loop is now a
startup failure with a message. The nested-contributor test waits with a bound for the same
reason.

That is the argument for checking invariants rather than only arguing for them: a termination
proof is correct right up until the code changes, and a wrong one here was not a hang but an
allocation loop.

## Rejected alternatives

**A plugin interface the host loads.** A `.so`, or a subprocess the host discovers. It would
put a whole loading mechanism, a version compatibility problem and a failure mode — a plugin
that will not load — into the framework, for a case that needs one interface and one hook. And
it would make the guarantee worse: a plugin's configuration would cross a process boundary,
where a value in a factory closure does not.

**Configuration keys a deployment reads for contributors.** `contributors: [...]` in the
configuration file, with the credential alongside. That is the option this record exists to
avoid: it puts the secret in the file a person reviews, and it makes the repository list a
person's decision rather than the deployment's.

**Letting a contributor start subsystems itself.** It is the same capability with a worse
lifecycle — the contributor would own a composition, and the host would have to detect that
and merge two. A contribution is a decision about what should be there; whether it starts is
the composition's business.

**A `Vends` method for provider records.** A contributor that wants to supply a provider
composes the subsystem that provides it, and the host derives the record from its descriptor —
which is what `providerRecords` already does. A separate `Vends` would be one fact in two
places, and a disagreement between them would be invisible.

## Related documents

- `pkg/subsystem/compose.go` — the interface, the hook, and the rule about serving nothing.
- `pkg/host/compose.go` — the phase, the shared and per-wave state, and the invariant.
- `docs/skills.md` — the feature this was needed for, and why a project's list must never
  name a reference that resolves to nothing.
- [0011 — deployment configuration](0011-deployment-configuration.md) — why a configuration
  file is refused rather than guessed at, and why the file a person reviews is protected.
