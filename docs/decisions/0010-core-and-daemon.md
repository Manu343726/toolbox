# 10. The core, and running it as a daemon

Status: proposed<br>
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
host and speaks the same contract. Nothing discovers it, and nothing can.

**An AI client has to be a subprocess.** `toolbox mcp` serves stdio, so a client
launches a process per session, and the process starts every subsystem it needs on
loopback. That works and it costs a start-up per session, and it means the agent
cannot reach a subsystem the host was not launched with.

dotfilesd has solved the same shape: a central daemon that loads and instances
plugins, a registry the daemon owns, clients that resolve peers through that
registry, a fixed default address so a process can find the local daemon without
configuration, and a CLI that is a daemon client rather than a second implementation.

## What already exists

Most of the mechanism is built. This is a composition and wiring change, not a new
subsystem.

| Piece | Where | State |
|---|---|---|
| In-process composition of factories | `pkg/host.Host` | built, used by `cmd/toolbox` |
| Registry with leases and status | `subsystems/registry` | built, registered into by `cmd/toolbox` |
| Name/service → endpoint resolution | `subsystems/registry.Resolver` | **built, never constructed** |
| Typed resolve-then-bind | `core.Resolver`, `core.Bind` | built, used for providers |
| Reflection-based description of a live endpoint | `pkg/protocontract.FromEndpoint` | built |
| Catalog: descriptions, exposure, invocation | `subsystems/apitools` | built, in-process store |
| Aggregated MCP over reflection or a catalog | `pkg/mcp` | built |
| Streamable HTTP transport for MCP | `mcp.Server.HTTPHandler` | built |

The gap is that the registry and the catalog are in-process objects, and the MCP
builder is handed the object rather than a client. Everything a daemon needs is
behind an interface that already has a ConnectRPC implementation.

## Decision

**The core is the framework's infrastructure, and it has two run modes that share
one composition.**

- **In-process** (default). `toolbox` with no daemon runs the core in the CLI
  process, starts the selected subsystems, and spawns the MCP directly, stdio or
  remote according to flags. This is today's behaviour and it does not change.
- **Daemon.** `toolbox daemon` runs the same core as a long-lived process. It
  starts its subsystems, registers them in its own registry, and serves the
  aggregated MCP itself over Streamable HTTP, so an AI client connects to a network
  address instead of launching a subprocess per session.

A subsystem or an MCP **joins a core** by being told which one:

- in-process subsystems belong to the in-process core, by definition;
- a subsystem the daemon did not start registers itself with the daemon's registry;
- a client reads the daemon's catalog over ConnectRPC rather than reading an
  in-process store.

**Resolution tries the local core by default.** A resolver chain, in order: the
endpoints this process started, then a local daemon, then a configured address. A
subsystem that calls a peer and finds nothing says so, and distinguishes "not
deployed" from "deployed and not answering" — because a deployment may legitimately
run without a peer.

**`toolbox` with a daemon running is a daemon client.** It launches a stdio MCP
that serves the daemon's surface, so a stdio client gets the whole deployment
without the CLI reimplementing any of it. This is the `dotfilesctl` case: using
framework tools through a CLI, with the state living in one place.

## Decisions this needs

Four, and they are not all mine to make.

**1. How a process finds the local core.** dotfilesd uses a fixed default port
(`9105`, overridable by `DOTFILESD_PORT`) and nothing else. Toolbox's registry
currently binds `127.0.0.1:0` and reports its address through an in-process
descriptor, so there is no discoverable address at all. Options: a fixed default
port, a state file under `XDG_RUNTIME_DIR` written by the daemon, or a unix socket
in the same directory. A state file is the most robust and costs one write per
daemon start; a fixed port is the simplest and collides when two run. **Recommend a
state file, with the port as a fallback override.**

**2. Does a separately launched subsystem register with a running daemon?** This is
the "MCPs can state to which core they belong to" case, and it is the one that makes
membership dynamic rather than a literal. It needs a registration call the daemon
accepts, and a decision on whether the daemon may *start* a registered subsystem
(it cannot — it does not have the factory — so a registered subsystem is one the
user or a supervisor started).

**3. Where does a "core" stop and a "host" start?** `pkg/host` composes factories;
a core also owns a registry, a catalog, a policy, and a provider directory. The
honest answer is that `pkg/host` grows into the core, and `cmd/toolbox`'s literal
becomes the in-process composition *inside* it. That is a rename plus a
responsibility, not a rewrite, and it should be named rather than left implicit.

**4. Is the policy still a document, or does the daemon own it?** ADR-0008 put the
default in a file and said a centrally held document belongs to the policy
subsystem. A daemon has one long-lived policy; a stdio client connecting to it
should get that policy, not a re-read of its own file. So the daemon's policy is
authoritative for everything it serves, and `--policy` on a *client* is refused
rather than silently ignored.

## Consequences if accepted

- `cmd/toolbox/main.go`'s factory map stops being the only way in. It becomes the
  in-process composition inside the core, and a deployment gains a second way to
  populate one: subsystems that started themselves and registered.
- `registry.Resolver` gets constructed, by the core and by any subsystem that calls a
  peer. It is already written and tested; this is wiring.
- The MCP gateway gains a source that reads a remote catalog. `NewFromAPICatalog`
  takes `api.Catalog`; the ConnectRPC client the daemon serves already satisfies
  the reads it needs, so this is an adapter rather than a new path.
- A second toolbox process on the same host is no longer a second deployment. It is
  a client of the first, which is the whole point of the daemon mode.
- `toolbox mcp --stdio` and `toolbox mcp --http` become the same code path with a
  different transport, rather than two compositions.
