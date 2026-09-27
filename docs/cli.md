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

`toolbox skill find-skill --query x` and `skill find-skill --query x` are one command with a
different host around it, and a test compares their flags so they cannot drift apart.

## Reading the help

```
$ toolbox skill find-skill --help
toolbox.skill.v1.SkillService/FindSkill

FindSkill searches the integrated catalogs for skills matching what a caller wants.

The query is pushed down to each catalog, so a large catalog is not shipped here to be
filtered. A catalog that cannot search is reported as having returned nothing, rather than
returning everything and leaving the caller to notice.

Usage:
  toolbox skill find-skill [flags]

Flags:
      --catalog string      Optional catalog identifier, to search one catalog rather than all
                            of them.
  -h, --help                help for find-skill
      --page_size int       Optional page size. Zero means the server's own default.
      --page_token string   Optional page token from a previous response.
      --query string        What to look for, matched against a skill's name and description.
```

Three things in there are worth stating:

- **The help is the contract's own comment.** Not a restatement of it, and not the field's
  type. If a flag says `string`, the contract has no comment for that field — fix the
  `.proto`, not the generator. Note what is *not* in that block: no default for
  `page_size`, because the contract does not state one, and a generator that invented one
  would be claiming a promise the contract does not make.
- **The first line is the fully-qualified method.** It is how a reader who found a command
  by guessing finds the contract that defines it.
- **The type follows the field.** An `int32` contract field is an `int` flag, and it is sent
  at the field's own width. The *name* follows the field too, which is why it is
  `--page_size` and not `--page-size`.

## Nested messages

A message field is addressable as JSON *and* flattened into dotted flags, so setting one
field does not mean hand-writing a document:

```sh
toolbox prompt put-prompt --prompt.id greet --prompt.version 1 \
    --prompt.name Greeting --prompt.template 'Hello {{name}}' --prompt.variables name
toolbox prompt put-prompt --prompt '{"id":"greet","name":"Greeting","version":"1","template":"Hello {{name}}","variables":["name"]}'
```

Both work, and a dotted flag overrides the JSON for the same field, because the JSON is
applied first. A repeated message takes one JSON object per value rather than a JSON array,
and is not flattened: no flag addresses `--sources.0.id`, so a dotted flag there would be a
lie about which element it sets.

Nesting is bounded, and a message that contains itself — `ApiSchema` holds repeated
`ApiSchema` — stops rather than recursing.

## Where a call goes

An operation command resolves its service before calling, and **how it behaves when the core
does not answer is a deployment setting** — see
[`configuration.md`](configuration.md#daemonlaunch) for the three modes and what each one
does. In short:

1. **A core that answers is the target**, and nothing is started. A call answered anywhere
   else would write to a store nobody else can see, and report success.
2. **`auto`** (the default) starts one if none is answering, for an address on this machine.
3. **`explicit`** does not start one and does not fall back — the lifecycle belongs to an
   init system, and a client that quietly served a private copy would hide a stopped service
   behind a working command.
4. **`disabled`** never needs one, and refuses the `daemon` subcommand.

So a round trip across processes needs a core:

```sh
toolbox skill add-skill --ref local.greet --confirm
toolbox skill get-skill --ref local.greet     # a different process, same state
```

`--confirm` is the configuration-confirmation rule as a field, so the proposal and the
approval cannot be reordered. Without it the same command reports what *would* change and
changes nothing.

With `auto` that needs no setup at all: the first command starts a detached core, exactly as
a `tmux` server starts on the first `tmux` invocation.

Point at one explicitly with `--core`, which is the short way to name the daemon's address:

```sh
toolbox skill get-skill --core core.internal:9180 --ref local.greet
```

A standalone subsystem command takes the same flags and reaches the same state:

```sh
skill --core core.internal:9180 add-skill --ref local.greet --confirm
toolbox skill get-skill --core core.internal:9180 --ref local.greet
```

A call that cannot reach the core says so, and names it:

```
the core at 127.0.0.1:1 did not answer: …
```

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
subsystem command reads `skill find-skill` and not `skill skill find-skill`: the
contract is a package, the command is already that package's short name, and the level would
cost a word per invocation to say nothing.

## See also

- [`mcp.md`](mcp.md) — the same surface, for an agent.
- [`policy.md`](policy.md) — what an agent may call, which is a different question.
- [`architecture.md`](architecture.md) — how a core and its subsystems are arranged.
