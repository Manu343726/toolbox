# 0012 — Logging: `slog` itself, a fanout of providers, and a route for everyone

Date: 2026-09-26
Status: accepted (supersedes the first draft of this record, which proposed a logging facade
of the framework's own)

## Context

The framework has no logging. Go code inside a subsystem writes to standard `log`, which
goes to whatever the process's standard error happens to be, carries no deployment
identifier, and cannot be filtered by anything. An agent talking to a subsystem over
ConnectRPC has no way to record what it did at all, so the audit trail of an agent's actions
— which is the thing a deployment most wants to keep — is assembled from whatever each
process happened to print.

Four requirements pull in different directions, and the design has to satisfy all of them
at once.

**Go code should log the ordinary way, and the ecosystem should come with it.** The
standard library's answer is `slog`: a `*slog.Logger` that any Go programmer already knows
and that any library can emit through. More importantly, `slog` is an *interface* with a
large body of existing implementations — fanout, rotating files, syslog, Loki, colour,
test capture — written by people who had the same problem. Choosing `slog` is only worth
something if the framework then uses the ecosystem rather than rebuilding it. A logging
facade of the framework's own `Entry`, `Handler` and `Level` types would have thrown all of
that away to buy nothing: the facade would be a worse `slog`.

**Backends are plural and replaceable.** A developer wants standard error. A deployment wants
a rotating file. A deployment running elsewhere wants syslog or a log aggregator. These are
not variations of one thing: they differ in what they can lose, where they can be read, and
whether they block. Making the choice a build tag, or a compile-time import, would fix the set
of possible backends when the binary is built — the opposite of what a provider subsystem is
for everywhere else in this framework.

**The fanout differs per project.** A machine runs one installation and many projects. One
project's logs want to be findable — tagged, and written to a file inside that project rather
than mixed into a machine-wide log. Another project's want only errors. A configuration that
could state one fanout would make every project on a machine share one, and one that could
only be per-process would make the machine's own view impossible to change without editing
each project.

**A caller outside the process has to be able to log.** An agent is not in the daemon. It
cannot call `slog.New` against a router in another process, and it should not have to open
the deployment's log files to write into them.

## Decision

### The Go API is `slog`, and so is everything under it

Go code logs through `slog`:

```go
slog.Info("stored a source", "id", id, "bytes", len(content))
```

Nothing in the framework's own API is a logging type. An entry is a `slog.Record`, a sink is
a `slog.Handler`, severity is a `slog.Level`, the line format is `slog.NewTextHandler` or
`slog.NewJSONHandler`, and `slog.SetDefault` makes the deployment's fanout the process-wide
default — so a dependency that logs through `slog` lands in it without knowing the framework
exists.

The rest is the ecosystem's, deliberately:

- the fanout within a route is `slog.MultiHandler`;
- a route's tags are bound with `WithAttrs`;
- attributes a caller binds with `slog.With`, and groups it opens with `WithGroup`, are bound
  and nested by each sink's own `WithAttrs` and `WithGroup`;
- a sink's minimum is a `slog.HandlerOptions.Level`, so filtering is the sink's own `Enabled`
  and a caller deciding whether to format an expensive attribute learns the answer from the
  call `slog` makes;
- a rotating file is `lumberjack`, and a syslog or Loki sink is the handler written for it.

What the framework contributes is the one thing the standard library does not have: deciding
**which** handlers an entry reaches, and what it is tagged with on the way. `log.Router` is
the only type in `pkg/log` that implements `slog.Handler`, and it does that because the
routing decision has to be a handler to sit in front of one.

The first draft of this record proposed the framework's own `Entry`, `Handler` interface,
`Level` constants, line format and rotating file. That was a reimplementation of `slog` and
of its ecosystem, done while claiming to adopt it, and it is withdrawn. Three zero-value
errors in it were the tell: a `slog.Level` is not a sentinel, so a `Level` field had to
become a pointer or a route with no matcher silently dropped every debug line.

### Backends are providers, and the role is open

A logging backend is a `slog.Handler`, and a subsystem that produces one is a **provider**,
declaring itself the way every other provider subsystem does, with
`func Providers(endpoint string) []api.Provider` and the role `loghandler`. The role is an
open identifier by design, so this is an addition to a vocabulary rather than a special case
in it, and a deployment lists which logging backends it has through the same catalog that
lists its parsers and adapters.

The provider interface is one method returning a `slog.Handler`. Most are a few lines,
because a backend the standard library can already do over an `io.Writer` does not need a
subsystem. `text`, `json` and `null` are built into `pkg/log` rather than shipped as
subsystems, because a deployment must be able to log before any subsystem has started.

`subsystems/logfile` is the reference provider: lumberjack plus a mapping from a
configuration's options to a standard library handler over it. It has no command, no contract
and no binary, because a logging provider has no operations to address — a subsystem whose
only job is to be composed in process and declared in the catalog is a provider.

### The write path is in process, and that is deliberate

A log entry is handed to its handlers **directly**, not over ConnectRPC.

This is the one place where the framework's usual rule — a subsystem is addressable, and a
feature calls it over the wire — does not apply, and the reason is that the rule exists to
keep lifecycles separate. A log handler is not a peer: it is a sink the router holds, like a
file handle. Routing an entry over HTTP to a sibling in the same process would add a network
hop and a serialization step to every line, and would make logging able to fail — with a
connection error, a timeout, a partial write — for reasons that have nothing to do with the
log.

The providers are still *discoverable* over the catalog, which is what the role is for, and
their configuration still arrives through the deployment's configuration, so nothing is
hidden — only the bytes take a shorter path.

### An external caller logs over the wire, into the same fanout

`subsystems/logger` serves an RPC, because an agent or another microservice is not in this
process and cannot be. It takes an entry and hands it to **the same router** the in-process
handlers use.

That shared router is the point. If the RPC built its own fanout, a deployment would have two
configurations that could disagree, and an operator reading a log file would have no way to
tell which one wrote a line. One fanout, two doors. The host builds it once, before any
subsystem starts, and hands the same value to the logger subsystem.

`Log` answers with the sinks the entry reached, because a caller in another process is owed
an answer and "no error" does not say whether anything recorded it — an entry matching no
route is delivered successfully to nobody. That is the one logging failure the log itself
cannot report, so the deployment also counts it and `GetConfig` reports the count.

### The fanout is routes, and **every** matching route contributes

A configuration names **handlers** — each a provider, its options and a minimum — and
**routes**. A route is a matcher, a set of handler names, and a set of attributes to add to
whatever it matches.

Every route that matches contributes. The first draft of this record said the first match
wins, and a test proved that wrong: "everything to the file, and this project's problems also
to the pager" cannot be expressed by first-match-wins, and that is not a corner case, it is
the reason a fanout exists. A handler named by two routes receives an entry **once**,
attributed to the first route that claimed it, because a line duplicated because two routes
overlapped is a line nobody can count.

For the same reason a route that adds attributes and sends them nowhere is **refused** at
load time. Such a route is a route that does not do what it says, and the only sign would be
a project whose logs could not be found among the deployment's.

The attributes a route adds are what make a project's logs findable. A relative path in a
handler's options resolves against **the project the declaring file belongs to**, so
`path: logs/project.log` in a project's file is a file inside that project and not inside its
`.toolbox` directory.

Routing is by attribute rather than by handler name alone because the interesting question is
never "which backend" but "whose line is this, and where should it go".

### A project's name and a project's location are different things

An entry's workspace is a label: a route matches it and the line carries it. A project's
configuration *file* is a location on this machine. One field cannot be both, and the two are
decided apart:

- a workspace that **names a directory** is a location — that project's own configuration
  file, its own fanout, and its entries tagged with the directory's name, so a route written
  as `match: {workspace: acme}` matches whether the caller said `acme` or said where `acme`
  lives;
- a workspace that is **only a name** is a label — matched against the deployment's own
  routes, with no file looked up, because a name says which project a caller is serving and
  not where that project lives, and guessing a directory from a name would put one project's
  log in another's.

A line saying `workspace: acme` is a fact about the entry. A line saying
`workspace: /home/someone/src/acme` is a fact about the machine.

### The fanout is the deployment's, and it is not settable over RPC

The fanout lives in the configuration file, which ADR-0011 established as the deployment's
description. A project's file is found by walking up from a directory, so a project states
its own handlers and routes without a machine-wide file having to know the project exists.
The section is opaque to `pkg/config`: that package finds the file, refuses a key nobody
understands at the top level and hands the section over, and `pkg/log` reads it and refuses a
key *it* does not. A document validated twice is validated by whichever reader was stricter.

`SetConfig` on the logger subsystem **records the project a caller is acting for**. It does
not change where entries go. A caller that could widen the fanout by RPC would be a caller
that could send anything anywhere, and the routes are the deployment's rather than the
caller's.

The first draft of this record said `SetConfig` retunes a running deployment without a
restart. That is withdrawn: it is the wrong thing to hand out over a network, and the reason
it was tempting is that it is convenient, which is not a reason.

### Configuration is data, and one reader builds it

The file and the RPC both produce a `log.Config`, and the router is built from that value and
nothing else. `Router` keeps the configuration it was built from, so `GetConfig` reports the
fanout actually in use rather than a description that could differ from it. A configuration is
applied whole or not at all: a router half way through a new fanout would send some entries
by the old rules and some by the new, and where a line ended up would depend on when it was
written.

## Consequences

- Every subsystem that logs goes through `slog`, so a deployment's logs are one stream rather
  than one per process and per library. A dependency the framework does not control lands in
  the fanout, which is the reason for choosing `slog` rather than a facade.
- Adding a backend is a provider implementing one method. Nothing in the router, the
  configuration or the RPC changes, which is the property that makes the provider roles worth
  having.
- A configuration is refused rather than guessed at. An unknown key, a misspelled backend, a
  misspelled level and a bare number where text belongs are all errors naming what was wrong
  and what is accepted, because each of them is otherwise a fanout that is quietly not the one
  that was written.
- Logging cannot fail the thing it is logging about. A sink that errors is reported on standard
  error and counted; it does not propagate into the caller's result. That is a deliberate
  trade: a log line is not worth failing a request over, and a deployment that loses log lines
  under pressure is more operationally sound than one that loses requests.
- A sink that blocks blocks the caller. The router does not buffer, because a buffer that
  fills silently drops entries and a buffer that does not is a queue someone has to size. The
  rotating file provider is the one that must be cheap, and a deployment that needs a
  non-blocking path writes a provider for it.
- Describing a deployment must not deploy it. Composing a host to learn what operations it
  offers builds a fanout that discards and opens nothing, because the command tree is built
  before any command has parsed its flags and there is no deployment to configure yet.
- The launch modes of ADR-0011 and the fanout are orthogonal: a deployment that disables the
  daemon still logs, into whatever handlers it configured, because the router lives in the
  process that writes.
