# 0011 — Deployment configuration

Date: 2026-09-26
Status: accepted

## Context

`pkg/config` resolves one value — the core address — from a flag, the environment, a
configuration file, and a built-in default. That was enough to make the daemon bindable at
a known address and to make `toolbox mcp` and every standalone subsystem command agree on
where the core is.

Three things have since outgrown it.

**The file has no shape.** The search path is conventional and the parser accepts anything
it recognises, but nothing rejects a key it does not recognise. A misspelled `prot` is
indistinguishable from `port: 0`, and the deployment runs on a default somebody did not
choose.

**The file has no stated subject.** It holds a core address, a policy path, and a scope. A
core address and a policy are deployment: they describe how this installation is wired and
what it permits. A scope is a workspace selector — which project's agents, skills and
knowledge a client works in — and it does not belong in a file that describes the
installation. One file describing both means a machine-wide file carries per-project choices
and a per-project file carries machine-wide ones, and neither can be edited without
surprising the other.

**The daemon's lifecycle is unstated.** ADR-0010 decided that subsystems join by connecting
to a configured daemon, and that a subsystem missing its peer fails loudly. It did not say
who starts that daemon. Every client currently falls back to a private instance when the
core does not answer, which is right for a developer and wrong for a machine whose core is
a systemd unit that has stopped: the fallback hides a stopped service behind a working
command, and a write goes somewhere nobody is managing.

## Decision

### The file configures the deployment, and only the deployment

A configuration file describes how an installation is wired. It holds the address the
daemon binds and clients dial, the address the Model Context Protocol endpoint binds, the
policy the deployment authorises with, and how the daemon's lifecycle is managed.

It does not hold workspace state — agent definitions, skills, prompts, knowledge sources —
and it does not hold the workspace selector. Those are a client's choice at the point of
use, carried on the command line or in the environment, because they differ per invocation
rather than per installation.

This is a boundary on where a fact lives, not a restriction on the file's syntax. A key
that is not recognised is **refused with its name**, because a configuration file that
silently ignores what it does not understand is a file that cannot be trusted to state what
it does.

### Locations are conventional, and the project file wins

In order:

1. the file named by `--config`, or by `TOOLBOX_CONFIG`;
2. `<project>/.toolbox/config.yaml`, found by walking up from the working directory;
3. `$XDG_CONFIG_HOME/toolbox/config.yaml`, falling back to `~/.config/toolbox/config.yaml`;
4. nothing, which is valid and means every default.

The project file wins over the user's, because a project that pins its own address must not
be silently overridden by a machine-wide default. The reverse is the annoyance people
actually hit, which is what the explicit flag and environment variable are for.

Walking up from the working directory is what makes `.toolbox/config.yaml` work the same in
a subdirectory of a project as at its root, which is the convention every other tool in this
ecosystem follows and the reason it does not surprise anyone.

### Precedence is file, then environment, then flags

A flag that was given wins. An environment variable wins over a file. A file wins over a
built-in default. Every resolved value carries where it came from, so `toolbox` can say so
on one line rather than leaving a reader to guess which of three sources a port came from.

The implementation is in `pkg/config` rather than in the command. The standard way to get
cobra to read a configuration file is to pair it with viper — cobra itself has no
configuration API, and its documentation uses viper for this. Viper was not adopted here
because `pkg/config` already resolves two of the three layers, because the standalone
subsystem commands need the same resolution and have no command tree to hang it from, and
because a framework whose subsystems are meant to be independently buildable should not
take a thirty-dependency configuration library to learn which port to dial.

Cobra still does the part it is good at: the command binds each resolved value into its flag
**only where the caller did not set the flag**, so a command line always wins and nothing has
to know which layer supplied a value that the caller supplied themselves.

### How the daemon is launched is an enum, and the value changes behaviour

`daemon.launch` is one of three values, and the value is not documentation — each changes
what a client does when the core does not answer.

**`auto`** — the default, and the tmux model. A client that finds no core answering starts
one and waits for the address to come up. The spawned daemon is detached, so it outlives
the command that started it, and the client waits for the *address* rather than for its own
child, so two clients starting at once do not have to coordinate: one binds, the other's
daemon exits, and both proceed. A core that is not being listened for locally is never
started — an address on another machine is somebody else's daemon, and this one has no
business launching anything for it.

**`explicit`** — the client never starts a daemon, and never falls back to a private
instance when the configured core does not answer. It says the core did not answer and stops.
This is the mode for a machine whose core is a systemd unit: the lifecycle belongs to the
init system, and a client that quietly served a private copy would report a working command
while the service that owns the state is down. A write under this mode must reach the
deployment or fail.

**`disabled`** — the client never starts a daemon and never requires one to be running: it
serves from the instance in this process. Two things follow, and both are enforced rather
than documented. The `daemon` subcommand is **refused**, because a core that this
installation does not run is not something it can start. And the configuration must **name
the address to connect to**, because peers that are not in this process are found through a
core, and a deployment that has disabled the daemon and named no core has told us nothing
about where anything is.

The mode is a Go enum with three constants and a parser that refuses anything else. It is
not a protobuf enum because it configures a client and is never served over the wire; a
contract nobody serves would be a contract with no reader.

### The daemon and the Model Context Protocol endpoint bind separately

The daemon binds the address clients use to reach it, and the Model Context Protocol
endpoint binds an address of its own. They may be the same address, and are by default, so
a small deployment is one port.

They are configured separately because they answer different clients on different terms: one
is a machine-to-machine ConnectRPC directory, the other is a network surface for an agent.
A deployment that exposes its agent tools to a network wants those on an address it can
firewall and authenticate separately from an internal directory, and a single address cannot
be given two policies.

When the two addresses are the same, the Model Context Protocol endpoint is mounted on the
daemon's own server, which is what keeps it a single port. When they differ, the daemon
serves it on a second listener. The mount is a mount either way: it is not a protobuf
service, so putting it in reflection would be a lie.

## Consequences

- A configuration file is now a thing a person writes, so it is validated, refuses keys it
  does not recognise, and reports the source of every value it resolved.
- The workspace selector leaves the file. `pkg/config` still carries it, resolved from a flag
  or the environment, because a client still propagates it; it simply is not a thing an
  installation states.
- A client under `explicit` can fail where it used to succeed. That is the point: the
  fallback was hiding a stopped service.
- `daemon.launch: disabled` makes the configuration incomplete unless an address is named,
  and that is reported as an incomplete configuration rather than as a runtime failure
  somewhere later.
- The daemon gains a second listener in the two-address case, and the lifecycle of two
  listeners is one more thing a signal has to shut down.
