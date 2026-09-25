# 10. The core, and running it as a daemon

Status: accepted<br>
Date: 2026-09-25
Depends on [ADR-0009](0009-cross-subsystem-calls.md): a peer call is only
useful once there is something to resolve it against.

## Context

Three things the framework does not currently do, all of which the same mechanism
would provide.

**A peer call has nowhere to resolve to.** `registry.Resolver` resolves a subsystem
name or a fully-qualified service name to an endpoint. It has a test and nothing
else: nothing in `pkg/` or `cmd/` constructs it. Every in-process path uses
`core.NewStaticResolver` built from the subsystems the host happened to start, so
"resolve a callee" and "the host started it" are the same statement.

**Membership is a compile-time literal.** `cmd/toolbox/main.go` holds a map of 15
factory functions. That is a defensible composition root, and it is also the only
way a subsystem can join a deployment. A subsystem the binary was not built with
cannot participate at any address, in any mode, even if it is running on the same
host and speaks the same contract.

**An AI client has to be a subprocess.** `toolbox mcp` serves stdio, so a client
launches a process per session, and the process starts every subsystem it needs on
loopback. That works, it costs a start-up per session, and it means the agent
cannot reach a subsystem the host was not launched with.

dotfilesd has solved the same shape: a central daemon that loads and instances
plugins, a registry the daemon owns, clients that resolve peers through that
registry, a fixed default address so a process finds the local daemon without
configuration, and a CLI that is a daemon client rather than a second
implementation.

## What already exists

Most of the mechanism is built. This is composition and wiring, not a new subsystem.

| Piece | Where | State |
|---|---|---|
| In-process composition of factories | `pkg/host.Host` | built, used by `cmd/toolbox` |
| Registry with leases and status | `subsystems/registry` | built, registered into by `cmd/toolbox` |
| Name/service → endpoint resolution | `subsystems/registry.Resolver` | **built, never constructed** |
| Typed resolve-then-bind | `core.Resolver`, `core.Bind` | built, used for providers |
| Reflection description of a live endpoint | `pkg/protocontract.FromEndpoint` | built |
| Catalog: descriptions, exposure, invocation | `subsystems/apitools` | built, in-process store |
| Aggregated MCP over reflection or a catalog | `pkg/mcp` | built |
| Streamable HTTP for MCP | `mcp.Server.HTTPHandler` | built |

The gap is that the registry and the catalog are in-process objects, and the MCP
builder is handed the object rather than a client. Everything a daemon needs is
behind an interface that already has a ConnectRPC implementation.

## Decision

**The core is the host's own infrastructure. Subsystems are aggregated into it
either by in-process launch or by external registration. The core runs in-process
by default and as a daemon on request.**

### The core

The core is what the host already owns plus what a daemon adds: the registry, the
API catalog, the provider directory, the policy, and the aggregated MCP. `pkg/host`
is the core; the 15-entry factory map becomes the in-process composition *inside* it
rather than a list of things the framework knows about.

Two run modes, one composition:

- **In-process** (default). `toolbox` with no daemon runs the core in the CLI
  process, starts the selected subsystems, and serves the MCP directly, stdio or
  remote according to flags. This is today's behaviour.
- **Daemon.** `toolbox daemon` runs the same core as a long-lived process. It
  starts its subsystems, registers them, and serves the aggregated MCP itself over
  Streamable HTTP, so an AI client connects to a network address instead of
  launching a subprocess per session.

`toolbox` with a daemon running is a **daemon client**: it launches a stdio MCP
that serves the daemon's surface, so a stdio client gets the whole deployment
without the CLI reimplementing any of it. That is the `dotfilesctl` case — using
framework tools through a CLI with the state living in one place.

### Aggregation: launch or registration

A subsystem joins a core by one of two routes, and the core treats them alike:

- **in-process launch**, from a factory the core holds. The default, and the only
  route for a built-in.
- **external registration**, by a subsystem the user or a supervisor started, which
  connects to the configured daemon and registers its endpoint. A core cannot
  *start* a registered subsystem — it holds no factory for it — so a registered
  subsystem is one something else decided to run.

By definition an in-process subsystem belongs to the in-process core. A subsystem
that did not start inside this process tries the configured daemon, and that is the
whole of its discovery: an address, a port, and a connection attempt.

### Finding the core: a fixed port, in three places

A process finds the local core by **connecting to a configured address and port**.
The address may be a hostname, an IP, or a DNS name. There is one resolved value,
used identically by the daemon, by the CLI acting as a client, and by every
subsystem command — so all three agree without coordinating.

Resolution order, most specific first:

1. **CLI flag** — `--core <host:port>`, or `--port` for the port alone.
2. **Environment** — `TOOLBOX_CORE`, `TOOLBOX_PORT`.
3. **Configuration file** — a project file first, then the user's, so a project can
   pin its own address and a user can have a default.
4. **Built-in default** — `127.0.0.1` and a fixed Toolbox port.

There is no discovery protocol and no rendezvous: a process that wants the core
knows where it is, and a core that is not there is a connection failure rather than
a timeout waiting for something to appear.

### Policy is centralized, for now

The policy lives in the core. A client that supplies `--policy` to a daemon-backed
command has it **refused**, not silently ignored: the daemon's policy is
authoritative for everything it serves, and two answers to "what may an agent call"
is the failure mode ADR-0008 exists to prevent. The in-process core still reads its
own policy document, because it is the core.

### The direction this points in

**The framework provides functionality; behaviour is configurable per client and
per project.** The goal is a single core instance managing many projects, each with
its own config, policy, agents, skills, prompts, and knowledge — not one core per
project.

That goal is not in scope now, and this record does not pretend otherwise. What it
does is fix the shape so that reaching it is a matter of filling in scope rather
than re-plumbing:

- Scoping is by **`core.Metadata.WorkspaceID`**, which already exists and already
  propagates as `X-Toolbox-Workspace-Id` on service-to-service calls. `PolicyID`
  propagates beside it as `X-Toolbox-Policy-Id`. The two fields a multi-tenant core
  needs are already on the wire.
- Configuration is per scope: the project file is the natural scope boundary, and a
  client names the scope it wants rather than the core it wants.
- The catalog, the registry, the knowledge store, and the agent store are all keyed
  by something that can become a scope key. A single-tenant core uses one implicit
  scope, so nothing has to change for the first deployment.

The discipline this imposes *now* is narrow and worth stating: **nothing in the core
may be a global that a second client would have to overwrite.** A single unkeyed
policy, store, or registry would be the thing that has to be unwound later, and it
is much cheaper not to build.

## Consequences

- `cmd/toolbox/main.go`'s factory map stops being the only way in. It becomes the
  in-process composition inside the core, and a deployment gains a second way to
  populate one.
- `registry.Resolver` gets constructed, by the core and by every subsystem command.
  It is written and tested; this is wiring.
- The MCP gateway gains a source that reads a *remote* catalog. `NewFromAPICatalog`
  takes `api.Catalog`, and the catalog's own ConnectRPC service already serves the
  reads it needs, so this is an adapter rather than a new path.
- A second toolbox process on the same host is no longer a second deployment. It is
  a client of the first.
- `toolbox mcp --stdio` and `toolbox mcp --http` become one code path with two
  transports.
- The registry's ephemeral `127.0.0.1:0` bind stays for an in-process core, because
  a process that owns its subsystems needs no address to be found at. A daemon binds
  the configured port.
