# Deployment configuration

A configuration file describes **how an installation is wired**: the address the daemon
binds and clients dial, the address the Model Context Protocol endpoint binds, the policy
the deployment authorises with, and how the daemon's lifecycle is managed.

It does not describe a workspace. Agent definitions, skills, prompt templates and knowledge
sources are not deployment, and neither is the workspace selector — that differs per
invocation rather than per installation, so it is a flag and never a file key. A file that
tries to state it is refused.

## The file

```yaml
# .toolbox/config.yaml
daemon:
  host: 127.0.0.1     # the address the daemon binds and clients dial
  port: 9180
  launch: auto         # auto | explicit | disabled
mcp:
  host: 127.0.0.1     # empty means the daemon's host
  port: 9181          # 0 means the daemon's port
policy: ./ops.policy
```

Those six keys are all of them. **A key nobody understands is refused, by name**, with the
list of keys that are allowed — because a file that silently ignores what it does not
understand cannot be trusted to state what it does, and a misspelled `prot` is otherwise
indistinguishable from a deliberate default.

## Where it lives

In order, and the first one found wins:

1. the file named by `--config`, or by `TOOLBOX_CONFIG`;
2. `.toolbox/config.yaml` in the working directory **or any directory above it**, so a
   project's configuration works the same in a subdirectory as at its root;
3. `$XDG_CONFIG_HOME/toolbox/config.yaml`, falling back to `~/.config/toolbox/config.yaml`;
4. nothing — which is valid, and means every default.

**The project's file wins over the user's.** A project that pins its own address must not be
silently overridden by a machine-wide default. That is the direction people do not expect,
which is what the explicit flag and environment variable are for.

## What wins

A file, then the environment, then a flag, then a built-in default. **A flag you gave wins**,
because the person at the terminal is the one who knows what they meant.

| Key | Environment variable | Flag |
| --- | --- | --- |
| `daemon.host` | `TOOLBOX_DAEMON_HOST` | `--daemon-host` |
| `daemon.port` | `TOOLBOX_DAEMON_PORT` | `--daemon-port` |
| `daemon.launch` | `TOOLBOX_DAEMON_LAUNCH` | `--launch` |
| `mcp.host` | `TOOLBOX_MCP_HOST` | `--mcp-host` |
| `mcp.port` | `TOOLBOX_MCP_PORT` | `--mcp-port` |
| `policy` | `TOOLBOX_POLICY` | `--policy` |
| — | `TOOLBOX_SCOPE` | `--scope` |

Two conveniences exist for the common case of pointing at a core, and they yield to the
specific names above, so a stale one in a shell profile cannot override a deployment that has
adopted them:

- `TOOLBOX_CORE=host:port` and `--core host:port` — the daemon's address as one value.
- `TOOLBOX_PORT` and `--port` — the daemon's port. `--port` is deprecated.

Every command reports what it resolved and **where each value came from**, because "which of
four places did this port come from" is otherwise a question only the resolving process can
answer:

```
toolbox: daemon at 127.0.0.1:9180 (default host, config file port)
toolbox: Model Context Protocol at 127.0.0.1:9180
toolbox: daemon launch is auto (default)
toolbox: configuration from /home/you/project/.toolbox/config.yaml
```

## `daemon.launch`

How the daemon's lifecycle is managed. The value is not documentation — **each changes what a
client does when the core does not answer**, which is the only moment it matters.

### `auto` — the default, and the tmux model

A client that finds no core answering **starts one** and waits for the address to come up.
The daemon is detached, so it outlives the command that started it.

```
$ toolbox knowledge put-source --source.id notes --source.location mem://notes
toolbox: daemon at 127.0.0.1:9180 (default)
toolbox: no core at 127.0.0.1:9180; starting one
{ … }
```

Two clients starting at once do not have to coordinate: one binds the port, the other's
daemon exits because it cannot, and both proceed against the daemon that won — because each
waits for the *address*, not for its own child.

**A daemon is started only for an address on this machine.** An address elsewhere is
somebody else's daemon, and starting one here would put a second core on a network
pretending to be the first. Such a deployment gets the fallback and an explanation instead.

The spawned daemon writes to `~/.cache/toolbox/daemon.log`.

### `explicit` — the lifecycle belongs to an init system

The client **never starts a daemon and never falls back** to a private instance when the
configured core does not answer. It says the core did not answer and stops.

```
$ toolbox knowledge search --query notes
the core at 127.0.0.1:9421 did not answer, and daemon.launch is "explicit" so this command
will not start one and will not serve the call from this process instead: connection refused
```

This is the mode for a machine whose core is a systemd unit. A client that quietly served a
private copy would report a working command while the service that owns the state was
stopped — and a write would go somewhere nobody is managing. **This is the one behaviour that
changed most** from before the launch mode existed, and it is the point of having the mode.

Start it yourself:

```ini
# /etc/systemd/user/toolbox.service
[Service]
ExecStart=%h/go/bin/toolbox daemon --all --launch explicit
Restart=on-failure
```

### `disabled` — this machine runs no daemon

The client never starts one and never requires one: it serves from the instance in this
process. Two things follow, and both are enforced.

**The `daemon` subcommand is refused**, because a core this installation does not run is not
something it can start:

```
$ toolbox daemon
daemon.launch is "disabled", so this installation does not run a daemon: set it to "auto"
or "explicit" to have one
```

**The configuration must name the core to connect to.** Peers that are not in this process
are found through a core, and a deployment that disabled the daemon and named no core has
said nothing about where anything is. That is reported as an incomplete configuration, not as
a resolution failure on some later call:

```
daemon.launch is "disabled", so the configuration must state which core to connect to:
set daemon.host and daemon.port, or pass --daemon-host and --daemon-port
```

## Two addresses

The daemon and the Model Context Protocol endpoint bind separately. **They are the same
address by default**, so a small deployment is one port.

They are configured apart because they answer different clients on different terms: one is a
machine-to-machine ConnectRPC directory, the other is a network surface for an agent. A
deployment that exposes its agent tools to a network wants that on an address it can firewall
and authorise separately from an internal directory, and one address cannot be given two
policies.

```
$ toolbox daemon --daemon-port 9441 --mcp-port 9442
toolbox: core listening on http://127.0.0.1:9441
toolbox: Model Context Protocol on http://127.0.0.1:9442/mcp
```

When the addresses are the same the endpoint is mounted on the daemon's own server, which is
what keeps it one port. When they differ it gets a second listener. Either way it is a
*mount* rather than a service: it is not a protobuf contract, and putting it in reflection
would be a lie.

## The workspace selector

`--scope` and `TOOLBOX_SCOPE` exist, and a configuration file may not carry them. A
workspace is which project's agents, skills and knowledge a client works in; it differs per
invocation, so a file that described the installation would make every project on a machine
share one.

## See also

- [`cli.md`](cli.md) — the operations, and where a call goes.
- [`mcp.md`](mcp.md) — the same surface, for an agent.
- [`policy.md`](policy.md) — the `policy:` key, and what an agent may call.
- [`decisions/0011-deployment-configuration.md`](decisions/0011-deployment-configuration.md) —
  why the file holds what it holds.
