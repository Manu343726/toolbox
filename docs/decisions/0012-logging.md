# 0012 — Logging: a slog handler, a fanout of providers, and a route for everyone

Date: 2026-09-26
Status: accepted

## Context

The framework has no logging. Go code inside a subsystem writes to standard `log`, which
goes to whatever the process's standard error happens to be, carries no deployment
identifier, and cannot be filtered by anything. An agent talking to a subsystem over
ConnectRPC has no way to record what it did at all, so the audit trail of an agent's actions
— which is the thing a deployment most wants to keep — is assembled from whatever each
process happened to print.

Three requirements pull in different directions, and the design has to satisfy all of them
at once.

**Go code should log the ordinary way.** The standard library's answer is `slog`: a
`*slog.Logger` that any Go programmer already knows, that any library can emit through, and
that any test can assert on with a captured handler. Inventing a logging facade instead would
mean every dependency in the ecosystem had to be taught a new interface, and the framework
would be the only thing in the process that could not read its own logs.

**Backends are plural and replaceable.** A developer wants standard error. A deployment wants
a rotating file. A deployment running elsewhere wants syslog or a log aggregator. These are
not variations of one thing: they differ in what they can lose, where they can be read, and
whether they block. Making the choice a build tag, or a compile-time import, would mean the
set of possible backends is fixed when the binary is built — which is the opposite of what a
provider subsystem is for everywhere else in this framework.

**The fanout differs per project.** A machine runs one installation and many projects. One
project's logs want to be findable — tagged, and written to a file inside that project rather
than mixed into a machine-wide log. Another project's want only errors. A configuration that
could state one fanout would make every project on a machine share one, and a configuration
that could only be per-process would make the machine's own view impossible to change
without editing each project.

## Decision

### The Go API is `slog`, and the handler is ours

`pkg/log` provides an `slog.Handler` that hands entries to a router. Go code logs through
`slog`:

```go
logger := pkglog.Logger("knowledge")
logger.Info("stored a source", "id", id, "bytes", len(content))
```

Nothing in the framework's own API is a logging interface. `slog.SetDefault` makes the
framework's handler the process-wide default, so a dependency that logs through `slog` lands
in the deployment's fanout without knowing the framework exists. That is the whole point of
choosing `slog`: the entries a deployment wants to keep are the ones it did not have to ask
for.

### Backends are providers, and the role is open

A logging backend is a `log.Handler` — a named sink that accepts entries — and a subsystem
that produces one is a **provider**, declaring itself the way every other provider subsystem
in this framework does, with `func Providers(endpoint string) []api.Provider` and the role
`loghandler`. The role is an open identifier by design, so this is an addition to a
vocabulary rather than a special case in it, and a deployment can list which logging
backends it has through the same catalog that lists its parsers and adapters.

`pkg/log` also carries `stderr` and `null` as built-in providers. They are not subsystems
because a deployment must be able to log before any subsystem has started, and because a
provider that does nothing is not worth a module.

### The write path is in process, and that is deliberate

A log entry is handed to its handlers **directly**, not over ConnectRPC.

This is the one place where the framework's usual rule — a subsystem is addressable, and a
feature calls it over the wire — does not apply, and the reason is that the rule exists to
keep lifecycles separate. A log handler is not a peer: it is a sink the router holds, like a
file handle. Routing an entry over HTTP to a sibling in the same process would add a network
hop and a serialization step to every line, and would make logging able to fail — with a
connection error, a timeout, a partial write — for reasons that have nothing to do with the
log. A deployment whose logging can deadlock against its own logging is worse off than one
whose logging is a write to a file.

The providers are still *discoverable* over the catalog, which is what the role is for, and
their configuration still arrives through the deployment's configuration, so nothing is
hidden — only the bytes take a shorter path.

### An external caller logs over the wire, into the same fanout

`subsystems/logger` serves an RPC, because an agent or another microservice is not in this
process and cannot be. It takes an entry and hands it to **the same router** the in-process
`slog` handler uses.

That shared router is the point. If the RPC built its own fanout, a deployment would have two
configurations that could disagree, and an operator reading a log file would have no way to
tell which one wrote a line. One fanout, two doors.

### The fanout is routes, and a route is a matcher

A configuration names **handlers** — each a provider plus its options and a minimum level —
and **routes**. A route is a matcher, a set of handler names, and a set of attributes to add
to whatever it matches. Routes are evaluated in order and the first match wins; a route with
no matcher is the default and must therefore come last.

The attributes a route adds are what make a project's logs findable: a route matching
`workspace: acme` and adding `project: acme` tags every line that project produced, whether
the line came from Go code in the daemon or from an agent through the RPC. And a route
pointing at a file handler whose path is relative puts that project's output in that project,
because **a relative path in a handler's options resolves against the directory holding the
configuration file that declared it.**

Routing is by attribute rather than by handler name alone because the interesting question is
never "which backend" but "whose line is this, and where should it go". A fanout that can only
send everything everywhere is not a fanout; and one that can only send by logger name cannot
distinguish two projects' calls to the same subsystem, because both are logged by
`knowledge`.

### The same fanout is configuration, and configuration is per installation and per project

The fanout lives in the configuration file, which ADR-0011 established as the deployment's
description. Project files override user files by the same rule as every other key, so a
project states its own handlers and routes without a machine-wide file having to know the
project exists. The logger subsystem's `SetConfig` changes the same structure at run time, so
an operator or an agent can retune a running deployment without restarting it.

The project-file override is not an accident of the search order — it is what makes the
feature possible. A machine-wide file is the fallback; a project's own file is the more
specific statement, and the more specific one wins.

### Configuration is data, and the router is the only thing that reads it

The file, the environment and the RPC all produce the same `log.Config` value, and the router
is built from that value and nothing else. One structure, one reader, so a fanout set over
RPC and a fanout read from a file cannot differ in a way nobody can see.

## Consequences

- Every subsystem that logs goes through `slog`, so a deployment's logs are one stream rather
  than one per process and per library.
- Adding a backend is a new provider subsystem. Nothing in the router, the configuration, or
  the RPC changes, which is the property that makes the provider roles worth having.
- Logging cannot fail the thing it is logging about. A handler that errors is reported to
  stderr and skipped; it does not propagate into the caller's result. That is a deliberate
  trade: a log line is not worth failing a request over, and a deployment that loses log lines
  under pressure is more operationally sound than one that loses requests.
- A handler that blocks blocks the caller. The router does not buffer, because a buffer that
  fills silently drops entries and a buffer that does not is a queue someone has to size. The
  rotating file provider is the one that must be cheap, and a deployment that needs a
  non-blocking path writes a provider for it.
- The launch modes of ADR-0011 and the fanout are orthogonal: a deployment that disables the
  daemon still logs, into whatever handlers it configured, because the router lives in the
  process that writes.
