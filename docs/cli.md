# The command line

Every subsystem contract becomes a command. The framework generates the tree from the
protobuf descriptors, so a contract cannot change without its commands changing, and there
is no hand-written flag to drift from the field it sets.

There are two commands that offer the same surface, and they are the same generator over the
same contracts:

| Command | What it is |
| ------- | ---------- |
| `bin/<subsystem>` | One subsystem, started in this process, described over reflection |
| `bin/toolbox` | The whole deployment, described from the contracts linked into the binary |

`toolbox knowledge search --query x` and `knowledge search --query x` are one command with a
different host around it, and a test compares their flags so they cannot drift apart.

## Reading the help

```
$ toolbox knowledge search --help
toolbox.knowledge.v1.KnowledgeService/Search

Search retrieves relevant passages from the knowledge base.

Usage:
  toolbox knowledge search [flags]

Flags:
  -h, --help           help for search
      --limit int      Maximum number of passages to return.
      --query string   Search text.
      --tags strings   Optional source or tag filters. (repeated)
```

Three things in there are worth stating:

- **The help is the contract's own comment.** Not a restatement of it, and not the field's
  type. If a flag says `string`, the contract has no comment for that field — fix the
  `.proto`, not the generator.
- **The first line is the fully-qualified method.** It is how a reader who found a command
  by guessing finds the contract that defines it.
- **The type follows the field.** An `int32` contract field is an `int` flag, and it is sent
  at the field's own width.

## Nested messages

A message field is addressable as JSON *and* flattened into dotted flags, so setting one
field does not mean hand-writing a document:

```
$ toolbox knowledge put-source --source.id notes --source.location mem://notes
$ toolbox knowledge put-source --source '{"id":"notes","tags":["a","b"]}'
```

Both work, and a dotted flag overrides the JSON for the same field, because the JSON is
applied first. A repeated message takes one JSON object per value rather than a JSON array,
and is not flattened: no flag addresses `--sources.0.id`, so a dotted flag there would be a
lie about which element it sets.

Nesting is bounded, and a message that contains itself — `ApiSchema` holds repeated
`ApiSchema` — stops rather than recursing.

## Where a call goes

An operation command resolves its service before calling:

1. **A core that answers is the target**, and nothing is started. A core holds the state a
   deployment accumulates, so a call answered anywhere else would write to a store nobody
   else can see — and report success.
2. **Otherwise this process serves it**, starting the subsystems `--component` selected.

A core that is configured but not running falls back to this process *and says so* on
stderr. Refusing would make a configured-but-absent core indistinguishable from a broken
deployment; falling back silently would make a write look durable when it is not.

```
$ toolbox knowledge get-source --core 127.0.0.1:9180 --id notes
toolbox: core at 127.0.0.1:9180
toolbox: the core at 127.0.0.1:9180 did not answer (…); serving this call from this process instead
```

So a round trip across processes needs a core:

```sh
toolbox daemon --all &                        # one address, state that outlives a command
toolbox knowledge put-source --core 127.0.0.1:9180 --source.id notes --source.location mem://notes
toolbox knowledge get-source --core 127.0.0.1:9180 --id notes     # a different process
```

A standalone subsystem command takes the same flag, and reaches the same state:

```sh
knowledge --core 127.0.0.1:9180 put-source --source.id notes --source.location mem://notes
toolbox knowledge get-source --core 127.0.0.1:9180 --id notes
```

A core's address is its **registry's**, not the address of every service it hosts — so a
command pointed at a core asks the core where the peer is, and a core that hosts nothing
says so. A command given a core and a way to reach it never starts a subsystem of its own.

Two commands only make sense with a subsystem in this process: `serve` and `mcp`. Pointed at
a core they refuse, and say why:

```
$ knowledge --core 127.0.0.1:9180 serve
serving the subsystem needs a subsystem in this process, and this command is pointed at
the core at http://127.0.0.1:9180: the core is already serving. Run it without --core to
serve knowledge here
```

They are not hidden. A hidden command is one whose absence a caller has to guess at, and
"can this serve anything?" is a question worth answering — it is answered with the reason
rather than with silence.

## The policy does not apply here

A policy states what an *agent* may call. An operator at a shell is the deployment's own
operator, and a policy gate on the command line would stop the person who writes the policy
from using it. `--policy` is read and reported by the command line so nobody is surprised by
which one applied, and it is not consulted when an operation is called.

## What is not offered

**A streaming method is a command and is refused.** It takes its input flags, so its help
documents what the method takes, and calling it says which method cannot be invoked:

```
$ toolbox registry watch-services
streaming method toolbox.registry.v1.RegistryService/WatchServices is not supported by the unary CLI generator
```

**The reflection services are not commands by default.** A caller that already knows a
contract has no use for being told what it is. `--include-reflection` on the gateway adds
them.

## Command names

A command name is a service's last name segment, kebab-cased. A name one service claims is
not lengthened. When several claim it — `v1.ServerReflection` and `v1alpha.ServerReflection`
both reduce to `server-reflection`, and one would silently win — the name is qualified by the
package segment that tells them apart, and by the whole path if even that collides. This is
the same ladder the gateway uses for tool names, for the same reason.

A service whose name repeats the command's own gets no level of its own. That is why a
subsystem command reads `knowledge search` and not `knowledge knowledge search`: the
contract is a package, the command is already that package's short name, and the level would
cost a word per invocation to say nothing.

## See also

- [`mcp.md`](mcp.md) — the same surface, for an agent.
- [`policy.md`](policy.md) — what an agent may call, which is a different question.
- [`architecture.md`](architecture.md) — how a core and its subsystems are arranged.
