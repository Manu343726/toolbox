# Skills

A skill is a body of instructions for carrying out a task, written once and served to
an agent that needs it. This document is the design record and the implementation
plan: what the format is, where skills come from, how they reach an agent, which parts
of the protocol the framework implements, and in what order the work gets done.

It is written as the specification is agreed, step by step, and it is amended as
implementation changes what the SDK actually does — a decision taken later is written
into the section it belongs to rather than left as a marker, and where the plan was
**wrong** the correction says so and says why, because a corrected plan nobody can see
is a plan that will be followed again.

**Status: the specification is complete, and every phase of it is built.** A project's
own skills are read, projected per client, pinned and served over the Model Context
Protocol skills extension; a git-backed catalog provider serves many catalogs, one per
registered remote; and the public directory is a registration form of that provider rather
than a subsystem of its own. What is built and what is not is stated per phase below
rather than here, so this line does not have to be rewritten every time.

## The plan

Nine phases. Each closes before the next opens.

| # | Phase | State | Produces |
|---|---|---|---|
| 1 | **Upgrade the MCP Go SDK to v1.8.0** | **done** | A dependency that negotiates `2026-07-28` and implements `server/discover` |
| 2 | **The catalog model** | **done** | A catalog role, aggregation, and fully-qualified references |
| 3 | **The catalog RPC contract** | **done** | Seven methods, in `pkg/skills/proto`, derived from what the design requires of a catalog |
| 4 | **The canonical skill and distillation** | **done** | The union of every client's features, projected per client |
| 5 | **The aggregate contract** | **done** | `SkillService`, plus a read of which pinned skills have gone out of date |
| 6 | **The reader: the format in a root package** | **done** | `pkg/skills`, one typed representation, two input dialects |
| 7 | **The project configuration** | **done** | `skills:` names qualified references; `local` is implicit |
| 7a | **Git-backed catalogs** | **done** | `subsystems/skillgit`: a provider host, one catalog per registered remote; register, create, sync, push, unregister, delete |
| 7b | **A remote catalog** | **done, as 7a** | `skills.sh` is a registry over git, not a second mechanism: `owner/repo` is a shorthand for a GitHub remote |
| 8 | **Security posture** | **done** | Authority never widened; the project's list is the permission; drift is reported |
| 9 | **Implement** | **partly done** | The extension is served; the catalogs it aggregates are not yet providers |

Every phase is now built. The one that changed shape is 7b: the research
described above found the public directory to be a registry over git rather
than a source of its own, so it is a registration form of the git provider
rather than a second subsystem.

**The specification is closed.** Nothing below is an open question; a decision taken
later is written into the section it belongs to rather than left as a marker, because a
settled thing that still reads as pending gets asked about again.

### Phase 1 — upgrade the SDK

**Decided.** The framework pins `modelcontextprotocol/go-sdk` v1.6.1 across 19
modules. It moves to v1.8.0.

Not optional, and not a convenience: the extension is specified against protocol
revision `2026-07-28` and declares its capabilities in the `extensions` field of the
`server/discover` response. v1.6.1's newest revision is `2025-11-25` and it has no
`server/discover` at all, so on v1.6.1 the declaration would go somewhere the
specification does not describe, and a client reading capabilities the spec-correct way
would not see it. v1.8.0 is a released version — not a pre-release — whose newest
revision is exactly `2026-07-28` and which implements `server/discover`.

It is its own commit because it is a change to a dependency every module shares and not
a change to this feature. Verified by the full suite, `make vet`, the independent-module
sweep with `GOWORK=off`, and the CI workflow.

### Phase 9 — implement

**Decided**, and in scope for this work. The deliverable is a deployment whose
aggregated MCP server serves `io.modelcontextprotocol/skills`, so that a skill written
in the standard format and included by a project is discoverable with `skills/list`,
retrievable with `skills/get`, and readable file by file with `resources/read` — from
any catalog the deployment has integrated.

It is built on the three SDK extension points in *How the framework implements it*
below, not on the unmerged `#1238`. What it must satisfy is the specification's own
requirements, which is what the tests in *Tests* are written against.

## Where this sits in the framework

A skill is a *domain resource*, like a workflow, an agent profile or a prompt
template: versioned, independently addressable, and stored by the subsystem that owns
it. What makes a skill different from the other three is that its content is not a
field in a message. A skill is a directory of files, and the framework has to know
which files, how large they are, and what they hash to before it can offer a skill to
an agent at all.

That is what the rest of this document is about.

## The format is not this framework's

A skill is a directory containing a `SKILL.md` and whatever files that skill refers
to. `SKILL.md` begins with YAML frontmatter naming the skill and describing it, and
the rest is the instructions themselves:

```text
code-review/
├── SKILL.md
├── references/
│   └── checklist.md
└── scripts/
    └── lint.sh
```

```markdown
---
name: code-review
description: Use this when reviewing a change for correctness, not style.
---

Read the change. For each hunk, ask what input would make it wrong…
```

This is the **Agent Skills** format. It is not defined by this framework, and a skill
written for one agent is a skill written for any of them. The framework reads it; it
does not extend it, and it does not define a vocabulary of its own for what may appear
in a skill.

The rules the format sets, which this framework enforces rather than interprets:

- A skill is a directory. Its **name** is the `name` field in its `SKILL.md` frontmatter.
- `SKILL.md` **must** begin with YAML frontmatter containing at minimum `name` and
  `description`.
- The **final segment of the parent directory's name must equal `name`**, so a skill is
  identifiable from its path alone and a client resolving a reference to
  `skill://<name>/` reaches the skill that reference meant.
- `name` is 1–64 characters of lowercase letters, digits and hyphens, not starting or
  ending with a hyphen and containing no consecutive hyphens.
- `description` is 1–1024 characters. It is the only part of a skill a host reads
  before deciding whether to load the rest, which is why a skill without one can never
  be chosen.
- A skill may contain further files and subdirectories. `scripts/`, `references/` and
  `assets/` are conventional, not required.

The format also allows `license`, `compatibility`, `metadata` and `allowed-tools`
fields. All are carried through to a client that reads them, and `allowed-tools` is the
one exception — see *Security*, and *A request for more authority is not a statement of
need* for the distinction that decides it.

## Toolbox skills are templates; what is served is distilled

A skill in a Toolbox catalog is a **template**. It is written in the standard format
and it is portable, and it is also allowed to carry Toolbox's own frontmatter
properties — under the reverse-domain prefix
`com.github.manu343726.toolbox/`, following the convention the MCP Skills extension
uses for its own reserved keys.

What the MCP skills feature returns is **not the template**. It is a distillation of
the template, produced for the client that is asking, from what that client is
actually able to do with it.

This is the difference between a file and a served artifact, and it is the reason
distillation exists at all: a standard skill is written for the union of all readers,
and a particular client may read only part of it. Serving the template unaltered hands
every client the whole thing and lets it decide what to ignore, which is the client
doing the framework's work.

The first targets are the common cases, and what each supports is taken from that
vendor's own documentation rather than assumed. **All four support skills**, so no
client needs a fallback for having none.

### The canonical Toolbox skill is the union

**A Toolbox skill supports every feature any supported client has.** It is the union
of the Agent Skills standard and the extensions the four clients define. An author
writes against the union and gets every one of them, because distillation is what
guarantees it: a feature no target client has is not a feature of the format.

This is the whole point of the arrangement. A standard skill is written for the union
of readers anyway, and a person writing one should not have to choose between Claude
Code's `when_to_use` and Copilot's `argument-hint`. The union is what makes "write once,
works in the client you are in" true rather than aspirational.

The union, by **what kind of thing a feature is**. The kind is what decides how
distillation treats it, which is why the table is organised this way rather than by
which client contributed it.

| Kind | Fields |
|---|---|
| **Identity** | `name` (standard, required, 1–64 chars, lowercase and digits with single hyphens, matching the directory), `description` (standard, required, 1–1024 chars, the field every client selects on) |
| **Classification** | `license`, `compatibility`, `metadata` (standard) |
| **Invocation** | `user-invocable` (Claude Code, Copilot), `disable-model-invocation` (Claude Code, Copilot), `allow_implicit_invocation` (Codex, `policy` block), `when_to_use` (Claude Code), `argument-hint` (Claude Code, Copilot), `arguments` (Claude Code), `default_prompt` (Codex, `interface`), `context` (Claude Code, Copilot) |
| **Execution** | `model`, `effort` (Claude Code) |
| **Permission request** | `allowed-tools` (standard) |
| **Permission restriction** | `disallowed-tools` (Claude Code) |
| **Requirement** | `dependencies.tools` (Codex) |
| **Presentation** | `interface.display_name`, `interface.short_description`, `interface.icon_small`, `interface.icon_large`, `interface.brand_color` (Codex) |
| **Toolbox** | `com.github.manu343726.toolbox/enabled` |

And as files, because a skill's features are not only its frontmatter:

| File | From | Notes |
|---|---|---|
| `SKILL.md` | standard | Required |
| `references/`, `scripts/`, `assets/` | standard | Conventional; any supporting file is permitted |
| `agents/openai.yaml` | Codex | **Not frontmatter** — a separate file, holding the `interface`, `policy` and `dependencies` blocks |

### A request for more authority is not a statement of need

**`allowed-tools` and `dependencies.tools` are two different features and are never
conflated**, because they are opposites in kind and the difference is a permission
boundary:

| | `allowed-tools` | `dependencies.tools` |
|---|---|---|
| Says | "**pre-approve** these tools for me" | "this skill **needs** a tool called X" |
| Kind | a request for authority | a statement of need |
| If the framework acts on it | the model's permissions widen | nothing widens; the skill either works or does not |
| This framework | **never acts on it** | **honours it** |

A skill declaring a dependency is telling the truth about what it needs to run, and in
a Toolbox deployment that is checkable: the deployment knows which MCP tools it exposes,
so the framework can say whether a skill's declared tool is actually there. A skill
declaring `allowed-tools` is asking to be trusted, and one client of four grants that
ask while three ignore it.

So the taxonomy carries a rule, and the rule is about direction:

- **A feature that asks for more authority is never acted on.**
- **A feature that asks for less is carried, and can only reduce.** `disallowed-tools`
  narrows the tool pool while a skill is active; if a client acts on it the model has
  fewer tools, and if it does not, nothing changed. Carrying it is safe in the one
  direction that matters, so distillation carries it.
- **A feature that states a need is checked rather than granted.** A dependency is
  satisfied or it is not, and telling the author which is which is more useful than
  pretending.

`agents/openai.yaml` being a file rather than a field is why manifest completeness is
not in tension with per-client behaviour. A template that includes one carries it for
every client; the three that do not read it ignore it, and it is listed either way.

### The distillation projection

Distillation takes the union and projects it for one client. What it does, per feature:

| Feature in the template | Distilled for a client that has it | Distilled for a client that does not |
|---|---|---|
| `name`, `description` | Carried unchanged | Carried unchanged |
| `license`, `compatibility`, `metadata` | Carried unchanged | Left in place; ignored |
| `user-invocable`, `disable-model-invocation`, `allow_implicit_invocation` | Carried unchanged — same names, same senses | Left in place; ignored |
| `argument-hint`, `arguments`, `when_to_use`, `default_prompt`, `context` | Carried unchanged | Left in place; ignored |
| `model`, `effort` | Carried unchanged | Left in place; ignored |
| `interface.display_name`, `short_description`, `icon_small`, `icon_large`, `brand_color` | Carried unchanged | Left in place; ignored |
| `disallowed-tools` | Carried unchanged, and honoured — it can only narrow | Left in place; ignored |
| `dependencies.tools` | Carried unchanged, and **checked** against the deployment's exposed tools | Carried unchanged; checked |
| `allowed-tools` **Never carried, to any client** | **Never carried** |
| `agents/openai.yaml` | Carried unchanged | Left in place; ignored |
| A dependency on executing `scripts/`, when the client cannot run them | — | **Instructions appended to the body** saying how to achieve the same thing otherwise |
| `toolbox/enabled: false` | Not served at all | Not served at all |

Four things fall out of that table, and they are the design:

- **Nothing is dropped and nothing is renamed.** Every client ignores a field it does
  not recognise, and three of the four say so in their documentation. The projection is
  therefore not a filter; it is the union with the body adapted.
- **The only content change is an appended passage.** A template that depends on running
  a script reaches a client that cannot run one as the same skill plus instructions for
  doing it by other means. The author's `description` and instructions are otherwise
  exactly as written.
- **`allowed-tools` is the one field never carried.** One client of four treats it as a
  request to elevate its own permissions and the other three ignore it, so carrying it
  would give the field two different meanings depending on who read it. This is the
  concrete reason, and it was a decision before it was evidence.
- **`disallowed-tools` and `dependencies` are carried, and for opposite reasons.** One
  narrows, so acting on it can only make a model less capable; the other states a need,
  and acting on it costs nothing. Neither is a request, which is precisely what separates
  them from `allowed-tools`.

**A consequence worth stating:** because distillation appends, a skill's distilled body
is a *superset* of the author's instructions, never a different skill. Two clients
differing means one of them was given extra guidance, not a different opinion about what
the skill says.

### What each client actually supports

The evidence the union is built from:

| | opencode | Claude Code | Codex | VS Code Copilot |
|---|---|---|---|---|
| `name` + `description` | required | `description` recommended, `name` defaults to the directory | required | required |
| `license` | yes | — | — | — |
| `compatibility` | yes | — | — | — |
| `metadata` | yes, string to string | — | — | — |
| `allowed-tools` | **no** | **yes** | via `openai.yaml` dependencies | — |
| Frontmatter beyond the standard | — | `when_to_use`, `argument-hint`, `arguments`, `disable-model-invocation`, `user-invocable`, `disallowed-tools`, `model`, `effort`, `context` | — | `argument-hint`, `user-invocable`, `disable-model-invocation`, `context` |
| Supporting files read on reference | yes | yes, "loaded when needed" | yes | yes, "only when referenced" |
| Runs `scripts/` | yes | yes | yes | yes |
| Slash-command invocation | — | yes, `/name` | yes, `/skills` or `$` | yes, `/name` |
| Progressive disclosure | yes, via the skill tool | yes | yes, with a 2%-of-context or 8000-character budget | yes, three levels |
| Unknown frontmatter fields | ignored | ignored, "without reporting an error" | — | — |
| Switching a skill off | `permission.skill` in `opencode.json` | `disable-model-invocation` | `[[skills.config]] enabled = false` in `config.toml` | `disable-model-invocation` |

**What this establishes:**

- **Only `name` and `description` are universal.** Every other field in the union is read
  by some clients and ignored by others.
- **The clients' `disable-model-invocation` is not our `enabled`, and the sense
  differs.** Claude Code and Copilot read it as *the model may not invoke this by
  itself*, on a skill the model still loads when relevant. opencode and Codex control
  availability from configuration instead — by permission pattern, and by path. A
  Toolbox `enabled` is a different fact: whether the skill is part of the project at
  all. It is not translated into any of these.
- **Progressive disclosure is universal, and three of the four budget the initial
  listing.** A very large skill set is a real condition rather than a hypothetical:
  Codex shortens descriptions and may omit skills entirely, and Claude Code truncates a
  skill's combined description text at 1,536 characters.

### What distillation does

**Distillation is the process of adapting a Toolbox skill to the skills features a
client supports.** Every skill goes through it before being served to anybody, which
is why **every skill is visible to every client** — a client never fails to see a skill
because of what it can do, only ever receives it in the form it can act on.

It changes **content, never the file set**:

- **The manifest is always complete.** Every file of the skill is listed, each exactly
  once, with the URI of `SKILL.md` among them. This is the specification's requirement
  and distillation does not come near it, because a file is never dropped from a skill
  to suit a client.
- **The instructions are adapted.** Where a skill depends on something the client cannot
  do, the distilled `SKILL.md` gains instructions appended to its body that say how to
  achieve the same thing another way. The skill the model reads is coherent and complete;
  it simply carries an extra passage explaining how to work here.
- **The author's `description` is never rewritten.** It reaches the model as written,
  because two clients reading different descriptions of one skill means the model is
  reasoning about something the author did not write.
- **Toolbox's own frontmatter extension is kept on the template and not carried into
  the output.** The served frontmatter is the standard's fields, so another reader gets
  a skill it recognises, and a property about how *this* deployment treats a skill stays
  on the template where it belongs.

Because the body can differ per client, so do the digests — and that is what a digest
is for. The entry describes what this server serves to this client, and a client that
verifies what it fetched against the entry it was given gets the right answer.

**The client says which it is.** The identity comes from the connection — what the
client reports when it initialises — and not from configuration, because a distillation
that guessed wrong hands a client content it cannot use and nothing in the result would
reveal the guess. A client this framework has never heard of gets the common
denominator: `name`, `description`, and the complete file set.

All four target clients support skills, so no client needs a fallback for having none.
A client this framework has never heard of gets the common denominator: `name`,
`description`, and the complete file set.

### The Toolbox feature set is typed, and complete

The framework's own frontmatter block carries **the whole feature set**, typed, under
`com.github.manu343726.toolbox/`. Not a bag of strings, and not only `enabled`: every
feature in the union has a name and a type, so the framework can validate it, reason
about which client has it, and tell a boolean from a string that looks like one.

```text
com.github.manu343726.toolbox/
  enabled: bool
  invocation:
    user_invocable: bool
    model_invocation: bool          # one field; see the dialects below
    when_to_use: string
    argument_hint: string
    arguments: [string]
    default_prompt: string
    context: inline | fork
  execution:
    model: string
    effort: low | medium | high | xhigh | max
  permissions:
    allowed_tools: [string]         # never acted on
    disallowed_tools: [string]
  requirements:
    tools: [ { type, name, description, transport, url } ]
  presentation:
    display_name: string
    short_description: string
    icon_small: string
    icon_large: string
    brand_color: string
  classification:
    license: string
    compatibility: string
    metadata: { string: string }
```

**One typed representation, two input dialects.** A skill written for Toolbox declares
its features in the block above. A skill written for a client directly uses that
client's own fields — `disable-model-invocation`, `allow_implicit_invocation`,
`interface.*`, `dependencies.*` — and the framework reads those too. Both are parsed
into the same typed form, and **a template that states the same feature twice with
different values is refused**, because two sources of one fact is the disagreement this
framework keeps refusing everywhere else.

**One field, several spellings.** The typed form has a single
`invocation.model_invocation`, and distillation spells it the way the target client
wants: Claude Code and Copilot get `disable-model-invocation`, which is its negation;
Codex gets `policy.allow_implicit_invocation`, which is the same fact stated positively.
Two clients, one field, and no possibility of the framework holding two opinions about
whether a skill may load itself.

**Type safety is stricter than YAML's.** Claude Code accepts `yes`, `no`, `on`, `off`,
`1` and `0` in any case as well as `true` and `false`, so a reader that took YAML's
word for a boolean would reject a skill that client is perfectly happy with. The
framework's reader accepts every spelling its targets accept, and reports a value that
is none of them rather than coercing it.

**A local skill is switched off through this block.** `enabled: false` on a local skill
means it is part of the project and is not served — the property lives in the same place
the person put the skill, rather than in a second list in the configuration file.

**Answered by checking rather than assuming:** the Agent Skills standard has **no**
`enabled` flag. Its frontmatter is exactly `name`, `description`, `license`,
`compatibility`, `metadata` and `allowed-tools`. It is also *silent* on unknown fields
— it defines what each field means and never declares another key invalid — and the MCP
Skills extension reserves only `io.modelcontextprotocol/`-prefixed keys *inside*
`metadata`. So an extension is permitted, and a namespaced key is the shape least
likely to upset a reader that is strict about its own.

## Catalogs

A **catalog** is a source of skills. It is a role a subsystem can play, in the same
way `parser`, `adapter` and `invoker` are roles in the API layer: a role has a
contract, a deployment registers implementations of it, and the framework resolves to
one at the point of use. A catalog is identified by name, and that name is part of how
a project refers to a skill.

A catalog is **read-only or read-write**. A read-only catalog is browsed and read; a
read-write one can also be written to, which is what makes "add this skill to my
project" an operation rather than a copy-paste. The distinction is a property the
catalog declares, so a write against a read-only catalog is refused with a reason
rather than silently ignored.

### The `local` catalog

Every project has one implicitly. It is the `skills/` directory under the project's
configuration directory, and it follows the standard skills directory structure:

```text
.toolbox/
├── config.yaml
└── skills/
    ├── code-review/
    │   ├── SKILL.md
    │   └── references/checklist.md
    └── release-notes/
        └── SKILL.md
```

It needs no configuration entry to exist. A project with no `skills/` directory has an
empty `local` catalog, and an empty catalog is a project that offers nothing, not a
project that failed to start.

**`local` is read-only.** The framework reads this directory and never writes to it. A
person owns these files and edits them as they would any file in their project.

### What a project includes

A project's `skills:` list in its configuration file names **fully-qualified skill
references**:

```yaml
skills:
  - skills.sh.pull-request-review
  - some-team.coding-standards
```

Skills in the `local` catalog are **included implicitly**: every skill under
`.toolbox/skills/` is part of the project without being named, because a person who put
a skill in their own project has already said they want it.

So the project's effective set is:

```text
  every skill in the local catalog          (implicit, never listed)
+ every reference named in skills:          (explicit, each qualified)
```

The distinction is what the two halves are for. A local skill needs no ceremony and
cannot be forgotten; a remote skill is named explicitly, so the configuration file
records that this project depends on somebody else's content — which is a fact worth
being able to read, diff, and review.

**A local skill is disabled by the template itself**, through
`com.github.manu343726.toolbox/enabled` in its own frontmatter. It is implicitly
included, so there is no entry in the project's configuration to remove, and a skill a
person put in their own project is switched off in the same place they put it.

### Fully-qualified references and the URI

A skill is named `<catalog>.<name>`. `local.code-review` is the `code-review` skill in
the `local` catalog; `skills.sh.pull-request-review` is a skill from the remote catalog.

The catalog prefix is not decoration. Two catalogs may both hold a skill called
`code-review`, and the specification is explicit that skill names are *not* unique and
that a host must resolve them per origin — so the thing that identifies a skill in this
framework is the pair, and dropping the catalog from a reference would reintroduce
exactly the collision the qualification exists to prevent.

**The catalog is always spelled in the URI**, including for a project skill:

```text
skill://local/code-review/SKILL.md
skill://skills.sh/pull-request-review/SKILL.md
```

The specification requires the final path segment to equal the skill name and allows
leading segments as a server-chosen prefix, so this satisfies it. One spelling per
skill means the string in a configuration file and the string in a URI are the same
fact, and there is no rule to learn about when the prefix disappears.

### Adding and removing a skill never writes a catalog

`local` is read-only, and an agent can add a skill to a project, enable one and disable
one. Those are consistent, and the reconciliation is the important part: **"adding a
skill to the project" means adding a reference to the project's configuration file, not
copying files into it.**

So the framework's write surface for skills is exactly one thing — the `skills:` list in
`pkg/config` — and the catalogs themselves are never written. A remote skill is fetched,
read and verified where the catalog keeps it, and a project gains access to it by naming
it. This is what keeps the read-only claim about every catalog true, and it is why a
read-only catalog is not a limitation here rather than a missing feature.

A change to a project's configuration file is confirmed by the user. See *Security*, and
AGENTS.md rule 17, which states it as a general rule rather than a skills one.

### The aggregate

A deployment integrates catalogs. The **skills subsystem** is the aggregator: it holds
the registered catalogs, resolves a qualified reference to the catalog that owns it, and
presents every catalog's skills as one surface, so a project can reach a skill from any
integrated catalog without knowing which subsystem provides it.

This is the same shape as the API layer's provider resolution, and for the same reason:
several implementations of one contract, selected at the point of use, with a
deployment deciding which are present. The consequence to carry through the design is
the one that applies to every provider in this framework — resolution is by *identifier*,
not by contract name, because several providers serve the same contract on purpose.

### The subsystem

`subsystems/skill` is **reshaped** into the aggregator. Its existing versioned in-memory
catalogue of skill metadata is not a catalog in this sense — no files, no digests,
nothing to aggregate — and it goes.

The reshaped subsystem does three things, which is why it is one subsystem rather than
three:

1. **Aggregates.** It holds the registered catalogs and resolves a qualified reference
   to the one that owns it.
2. **Serves over MCP.** It is the deployment's `io.modelcontextprotocol/skills`
   endpoint, so skills reach an agent through the same client that already sees the
   deployment's tools.
3. **Offers agent tools.** It is how an agent finds, adds, enables and disables skills.

### The agent tools

An agent needs to *discover* skills, not only be handed the ones a project already
names — otherwise a project cannot use the catalog it integrated until a human has read
its index. `SkillService` therefore serves:

| Method | Does |
|---|---|
| `FindSkill` | Search the integrated catalogs for skills matching something an agent wants |
| `AddSkill` | Add a qualified reference to the project's `skills:` list |
| `EnableSkill` | Add a reference that is present but not included |
| `DisableSkill` | Remove a reference from the project's `skills:` list |
| `CheckPins` | Report whether the catalog still holds what `skills.lock.yaml` pinned |
| `Validate` | Check the project's references against what its catalogs actually serve |

`CheckPins` and `Validate` answer two questions a project otherwise cannot ask at all.
`Validate` is what makes "a reference that resolves to nothing" a report rather than a
silent absence, and `CheckPins` is what makes drift visible: the lockfile records a digest
per pinned skill, and nothing re-reads those digests unless something asks.

These are **not** the same thing as the `skills/*` MCP methods, and the difference
matters. `skills/list` reports what the project may use; these change what that
is, and a method that changes state is governed by the framework's rules about side
effects: each declares what invoking it does, and a policy decides whether an agent may
invoke it. `AddSkill` is `create`-shaped, and `FindSkill` and `CheckPins` are `read_only`.

**`find` pushes the query to the catalogs.** A catalog is a provider and it answers its
own query, so a large catalog is not shipped to be filtered here. This is part of the
catalog contract in phase 3.

The write methods are subject to the configuration-confirmation rule, so an agent adding a
skill is asked before the file changes.

### The `skills.sh` catalog

One catalog is for [skills.sh](https://www.skills.sh/), the public directory of agent
skills. It is **read-only**: it indexes skills published by third parties and is browsed,
not written to.

This section listed four things as undecided — how it enumerates, how a skill's files are
obtained, how a version or a pin is expressed, and how it behaves with no network. They
were investigated rather than assumed, and the answers are that **it is not a second
mechanism**:

- **It is a directory over git repositories, not a host they are served from.** Its own
  installer, `npx skills add <owner/repo>`, shallow-clones `github.com/owner/repo` and
  reads skill directories out of the checkout. Verified by reading the shipped bundle and
  then by doing it: `vercel-labs/skills` cloned at depth 1 yields a standard-format
  `find-skills/SKILL.md` that this framework's reader reads and projects for all five
  client identities.
- **There is no JSON API to enumerate against.** The site is server-rendered, and the
  obvious endpoints return 404. So "every skill on skills.sh" is not a question this
  framework can put to it.
- **Discovery is a directory holding a `SKILL.md`**, under whichever convention the
  repository's own clients read — `.claude/skills`, `.agents/skills`, `.opencode/skills`
  and twenty-eight more, which the ecosystem's installer searches. That list is read from
  the installer rather than invented, and is in `pkg/skills` as
  `ConventionalSkillDirectories`.
- **There is no upstream version.** A shallow clone is whatever `HEAD` was, so the pin
  question answers itself: the manifest *is* the pin, which this document already decided
  for the framework's own reasons.

So a `skills.sh` catalog is a **git-backed catalog registered from a shorthand**, and the
shorthand resolves to **GitHub** — not to skills.sh, because skills.sh is an index and
`npx skills add owner/repo` is a name for a repository. `RemoteURL("vercel-labs/skills")`
returns `https://github.com/vercel-labs/skills.git`, and the two are the same thing
reached two ways.

What that costs the enumeration the section above wanted: a deployment cannot claim to
search all of skills.sh, because nothing supports it. It can search the repositories it
has registered, which is what the git provider's `FindSkills` already answers. Stating
that is better than a search that silently covers less than it appears to.

### Git-backed catalogs

The second catalog implementation is the **concept** of a catalog backed by a git
repository, and the subsystem implements that concept **many times over** — once per
registered repository. It is not one catalog with one remote hardcoded into it.

So the git subsystem is a **provider host**. Each registered remote is a separate
`SkillCatalogService` provider with its own identifier, its own storage, and its own
sync state, and the aggregator resolves `<catalog>.<name>` to whichever of them owns
the prefix. This is the same shape as the registry's: one subsystem, many registrations,
each resolved by identifier.

Its storage is the subsystem's own, and it holds one clone per registered catalog:

```text
<deployment data>/skillcatalogs/<name>/     a clone, kept by the catalog
```

The repository is always present as a clone in that storage. A modification is synced
by **committing and pushing**, and a catalog can also be **synced from its remote**,
which is a `git pull --rebase`.

**A conflict is reported, never resolved by guessing.** If a sync cannot be completed,
the error says so and names the path to the repository, because the person who has to
look at it is the person who can decide what it meant. A catalog that resolved its own
conflicts would be choosing a side in someone else's repository.

**Every modification is its own commit.** Not batched: a change is a change, and a
history that shows one commit per thing is a history somebody can read.

**The commit identity is configured, not assumed.** It is stated in the deployment's
configuration file, and a project may override it — because the deployment is a machine
and the project is where the work is, and the person whose repository it is should get
the last word on whose name is on the commit. One identity governs the subsystem's
commits across every catalog it holds, because it is the same person on the same
machine making them.

**A catalog this deployment found is read-only; one it created is not.** That is the
whole difference between them, and it is what makes pushing to somebody else's
repository a separate deliberate act rather than a side effect of storing a skill. A
write against a read-only one is refused *before* anything is written, and refused **with
the reason** — which is what makes `copy` and `move` answerable rather than appearing to
succeed.

**git is the binary, not a library.** Every operation shells out, because `git` already
knows the machine's ssh keys, its credential helpers, its proxy configuration and its
`insteadOf` rules — and "credentials are deployment configuration" means exactly that a
deployment should not have to hand a private repository's secret to a framework. A Go
library would need its own answer to all four, and a worse one. A deployment that cannot
find git is told at start rather than when a tool refuses to register a catalog.

**A skill is read by the same code whichever catalog it came from.** A clone is a
directory, so a skill in one is read by `pkg/skills.Directory` — the same reader a
project's own `.toolbox/skills/` uses. That is what stops a manifest meaning one thing for
a project's own skill and another for the same skill in a checkout, and a pin from being
two kinds of thing depending on where the skill came from.

#### Two services, because two different questions

The subsystem serves two contracts, and the split is not a convenience:

- **`SkillCatalogService`**, once per registered catalog, for *skills*. Which catalog a
  call is for is in the request's own `catalog` field, so a deployment with a dozen
  catalogs still runs one provider.
- **`SkillGitService`**, for deciding *what sources exist at all*. A catalog contract is
  about skills; register, create, sync, push, unregister and delete are about where skills
  come from. A contract that mixed them would make every catalog provider responsible for
  deciding what catalogs there are, which is not a catalog's business.

The management service's listing is `ListCheckouts` and its status is `CheckoutStatus`,
which was **not** what this document said — it said `ListCatalogs`, and that collided with
the catalog contract's new method. The rename is not cosmetic. Which catalogs are *served*
is the catalog contract's question and every provider answers it; which commit a checkout
is on, whether it is clean, and whether it can sync is a fact about a directory this
provider owns. Two methods with one name answering one question in two shapes would be one
fact stated twice with nothing to catch a disagreement.

### Registering and creating catalogs

The subsystem's methods manage the set of git-backed catalogs, not just the skills inside
one. `SkillGitService` is a separate service from the catalog contract, and the six
registration methods are:

| Method | Declares | What it does |
|---|---|---|
| `RegisterCatalog` | `create update` | Take a git remote and a name, clone it, and serve it as a catalog under that name |
| `CreateCatalog` | `create update` | `git init` a new repository, commit it, and serve it — pushing only if a remote was given |
| `SyncCatalog` | `update` | `git pull --rebase` its remote |
| `PushCatalog` | `create update` | Send a checkout's commits to its remote |
| `ListCheckouts` | `read_only` | Report which catalogs are registered and what commit each is on |
| `UnregisterCatalog` | `delete` | Stop serving a registered remote. The clone stays on disk |
| `DeleteCatalog` | `delete` | Unregister it **and** remove the clone. A separate operation, named for what it does |

`SyncCatalog` declares `update` and not `read_only` because `git pull` moves the checkout's
ref and rewrites its working tree: the catalog an agent reads afterwards is not the one it
read before. It was declared `read_only` and the declaration was never exercised, because
this subsystem did not embed its own contract and every method here therefore reached the
policy layer unclassified — which the default policy withholds, so the misdeclaration
changed nothing observable. See [What the default policy decides about
these](#what-the-default-policy-decides-about-these).

**Creating a catalog is local.** A new repository is made where the deployment runs and
served from there, and it is pushed only when a remote is named. No forge is contacted
and no token is needed to start a catalog — which means a deployment can offer
git-backed catalogs on a machine with no account anywhere, and publishing one is a
separate, deliberate act.

**The name is supplied when registering**, and it is refused if taken. Two checkouts of
one repository can therefore both be registered, which is occasionally what somebody
wants, and a name already in use is an error rather than two catalogs silently sharing
one.

**Unregistering and deleting are two operations.** Unregistering forgets the remote and
leaves the clone where it is: the skills are still readable on disk, and nothing is
removed without being asked for twice. Deleting removes the clone as well, and it is
named for that so the destructive one is never reached by accident.

**Registration is state that outlives the process.** A catalog registered yesterday is
still registered after a restart, because a deployment that forgot its catalogs on
reboot would serve a different set of skills each time it came up — and the project's
`skills:` list would then name references that resolve to nothing.

**Credentials are deployment configuration.** A clone may be private, and a project
says which catalogs it wants while the deployment is what can reach them.

### What the default policy decides about these

The default policy is one line — `allow * read` — and it is deliberately the narrower
half: *every read is granted, and nothing that changes state is*. Granting a write means
naming the deployments and actors that may change something, which is a decision somebody
has to make rather than a default somebody has to accept. So out of the two skills
services:

| | Granted by default | Withheld until a deployment opts in |
|---|---|---|
| `SkillService` | `ListSkills`, `GetSkill`, `FindSkill`, `ReadSkillFile`, `ListCatalogs`, `CheckPins`, `Validate` | `AddSkill`, `EnableSkill`, `DisableSkill` |
| `SkillGitService` | `ListCheckouts` | `RegisterCatalog`, `CreateCatalog`, `SyncCatalog`, `PushCatalog`, `UnregisterCatalog`, `DeleteCatalog` |

The split is not a matter of taste. Every withheld method writes **state that outlives the
process**: a project's `skills:` list, or the deployment's set of registered catalogs. A
deployment that forgot either on reboot would come up serving a different set of skills than
it did yesterday, and a project would find its references resolving to nothing. The
withheld methods are also the ones a person should agree to, and rule 17 is what makes them
ask first when an agent calls them.

**This was not a decision until now, and that is the part worth recording.** Every method in
`SkillGitService` arrived in the policy layer as **unclassified** — not one of the seven was
classified — because `subsystems/skillgit` had no `docs_embed.go`, and the
`@toolbox.side-effects` annotations live in the proto *source*, which nothing was reading.
An unclassified operation is refused, which is the safe direction, so the deployment looked
correct: everything withheld, nothing reported. `ListCheckouts` declares `read_only`,
changes nothing, and was still denied, because a deployment that could not be asked which
catalogs it has is withholding information rather than authority.

The annotations were right and the *reachability* was broken, which is why nothing
questioned it: the contract was annotated, the service worked, the operations were listed,
and the policy layer never saw any of it. The annotation above `SyncCatalog` was wrong in
the same gap — it said `read_only` for a method that runs `git pull` — and had never been
exercised, so correcting the reachability exposed a second defect underneath it.

Three things hold it shut now, and the third is the one that generalises:

1. `subsystems/skillgit/docs_embed.go` embeds the contract.
2. `cmd/toolbox`'s policy test asserts the default policy's promise over *every* operation
   in the deployment, in both directions — a read that is denied, and a write that is
   granted.
3. `internal/repocheck` requires every subsystem with a contract to embed its source, and
   every method in every contract to declare an effect the framework recognises. The first
   would have caught the missing file; the second is what caught `OperationExposure` in
   `apitools`, which declared nothing and so withheld the one operation an agent needs in
   order to find out what it is allowed to do.

### Copying and moving between catalogs

`SkillService` serves **`CopySkill`** and **`MoveSkill`**, and they are **two independent
operations** — neither is defined in terms of the other, so neither acquires the other's
failure cases. A copy that fails halfway has *duplicated* a skill: recoverable, and the
source is still there to remove the duplicate from. A move that fails halfway has neither
duplicated nor removed, which is its own state and its own reporting.

**They are on the aggregator, not on the catalog contract.** A copy and a move span two
catalogs, and two catalogs need not share a provider — the git-backed host holds a checkout
per remote, and a deployment may run two of them. The contract a catalog implements is about
*one source of skills*, and a provider asked to move a skill between catalogs would have to
resolve a catalog it does not hold, which is the coupling the provider pattern exists to
avoid. Composed here out of what a catalog already serves — `GetSkill` and `ReadSkillFile` to
read, `PutSkill` to write, `DeleteSkill` to remove — they work between catalogs held by
different providers, and **every provider gains them without being changed**.

**Both ends are checked before anything is fetched**, which is what `CatalogInfo.writable` is
for: writability is *declared* rather than discovered by attempting a write, so a move that
cannot happen costs a caller nothing and names the catalog that refused.

| Refused when | Because |
|---|---|
| The target is read-only | The skill has nowhere to go. A catalog registered from elsewhere rather than created here is one this deployment will not write to |
| The source is read-only | A move needs somewhere to remove the skill **from**. This is its own condition, not an inference from copying — a copy does not have it |

**A removal that fails after the write is reported, not raised.** The skill is then in both
catalogs, which is a duplicate and not a loss, and the response says so in
`source_removed` and `source_problem`. A caller handed an error would look in the source,
find the skill still there, and conclude something worse than a duplicate.

**A move never overwrites; a copy may be asked to.** `CopySkill` carries `fail_if_exists`,
because a caller choosing between two versions it can see is a decision it can make. A move
has no such choice to offer: replacing would destroy whatever the target held, and the one
thing a move is for is that the skill is **not** in two places. So `MoveSkill` has no such
field, and a target that already holds the name is refused.

**Both ends' names are required**, and the target's is not defaulted to the source's — a call
that collides must fail on a collision the caller asked for. The same catalog on both ends is
a **rename**, which is the move a deployment is most likely to want.

**Every file is carried, and each is checked against the manifest** it was listed in, using
the same `verifyFile` a served file goes through. A copy that carried bytes disagreeing with
the manifest would write a skill whose pin describes something else, into a catalog where the
mismatch is no longer visible. The **name** is stated to the target and the **manifest is
not**: the name is what the catalog is being asked to call it and cannot be inferred from the
files, while a manifest the target computes from the bytes it was given is the truth about
what it holds.

**Neither carries a `confirm` field**, which is deliberate. `AddSkill`, `EnableSkill` and
`DisableSkill` do, because they write the *project's* configuration file, which a person
wrote and reviews. A copy writes a *catalog*, which is a deployment's own storage and which
the deployment decides who may write; asking the person who owns the project about a change
to the deployment would be asking the wrong one.

## The catalog contract

A catalog is a provider role, so it has a contract, and the contract is derived from
what the design above says a catalog has to do. Every method here exists because
something above needs it; nothing here is speculative.

The contract is a provider contract in the same sense as `parser`, `adapter` and
`invoker`: a subsystem implements it, the deployment registers the implementation, and
the aggregator resolves to one by identifier. A deployment may run with no catalogs
beyond `local` and nothing else is affected.

### What the design requires of a catalog

| Requirement | Comes from |
|---|---|
| Say whether it can be written to | `copy` and `move` are refused against a read-only target, so the refusal needs a fact to refuse on |
| List its skills | the aggregate presents every catalog's skills as one surface |
| Answer a query itself | `find` pushes the query down, so a large catalog is not shipped to be filtered here |
| Return an entry with a complete manifest | the manifest is the pin, and the pin is how drift is detected |
| Read a file, with its digest and size | a client verifies what it fetched; the pin is only as good as the digests in it |
| Take a whole skill, and remove one | the git catalog is read-write, and `copy` and `move` are two independent operations |
| Report its own limits | a client has to know a skill is too large before it tries |
| Say which catalogs it holds | the git provider is a *provider host* holding one per registered remote, and a provider's own identifier is not a catalog's name |

### The service

```text
service SkillCatalogService {
  // @toolbox.side-effects read_only
  rpc DescribeCatalog(DescribeCatalogRequest) returns (DescribeCatalogResponse);

  // @toolbox.side-effects read_only
  rpc ListCatalogs(ListCatalogsRequest) returns (ListCatalogsResponse);

  // @toolbox.side-effects read_only
  rpc ListSkills(ListSkillsRequest) returns (ListSkillsResponse);

  // @toolbox.side-effects read_only
  rpc FindSkills(FindSkillsRequest) returns (FindSkillsResponse);

  // @toolbox.side-effects read_only
  rpc GetSkill(GetSkillRequest) returns (GetSkillResponse);

  // @toolbox.side-effects read_only
  rpc ReadSkillFile(ReadSkillFileRequest) returns (ReadSkillFileResponse);

  // @toolbox.side-effects create update
  rpc PutSkill(PutSkillRequest) returns (PutSkillResponse);

  // @toolbox.side-effects delete
  rpc DeleteSkill(DeleteSkillRequest) returns (DeleteSkillResponse);
}
```

`ListCatalogs` was **not** in the contract this document originally specified, and its
absence was a real gap rather than a simplification: the contract was written assuming
one provider serves one catalog, and the *provider host* this same document specifies
serves many. Without it, a directory that read each provider's own identifier as a
catalog's name resolved exactly one of a host's catalogs and left the rest unreachable —
which a project's `skills:` list would then name with no way to be served. A provider
holding one catalog answers with that one; a provider holding many answers with all of
them, and the aggregator learns which is which from that rather than from guessing.

So the eighth method exists because a provider is not a catalog, and the earlier seven
were not wrong — they were incomplete about a fact the design had already stated twice
(provider host, resolved by identifier) and the contract had not carried.

The side effects are the framework's own vocabulary, and they are the reason a policy
can govern this contract. Everything that reads is `read_only`; a write is `create
update` because a catalog write is neither purely one nor the other; and a delete says
`delete` because that is a different consequence and a deployment may reasonably want
to allow one without the other.

### The messages

```text
// CatalogInfo is what a catalog says about itself.
message CatalogInfo {
  // Catalog identifier, the first segment of every skill reference and URI.
  string id = 1;
  // Human-readable name.
  string name = 2;
  // What this catalog is for.
  string description = 3;
  // True when this catalog accepts PutSkill and DeleteSkill. A read-only catalog
  // refusing a write is what makes "copy and move" answerable.
  bool writable = 4;
  // Where the catalog keeps its skills, when that is a location a person can look at.
  // A git catalog names its checkout; a remote one names its origin.
  string location = 5;
}

// SkillRef identifies one skill within a catalog.
message SkillRef {
  // Catalog identifier.
  string catalog = 1;
  // Skill name, which is also its directory's name.
  string name = 2;
}

// SkillFile is one file in a skill's manifest.
message SkillFile {
  // Path within the skill, slash-separated, relative to the skill's directory.
  string path = 1;
  // Byte length of the content.
  int64 size = 2;
  // SHA-256 of the raw bytes, as "sha256:{64 lowercase hex}".
  string digest = 3;
  // What to serve the file as.
  string mime_type = 4;
}

// SkillEntry is a skill as a catalog holds it: the manifest, and the frontmatter
// rendered verbatim. This is the pin, and it is what drift is detected against.
message SkillEntry {
  // Reference, resolving to skill://<catalog>/<name>/SKILL.md.
  SkillRef ref = 1;
  // Resource URI of the SKILL.md.
  string uri = 2;
  // The SKILL.md frontmatter, verbatim, including fields this framework has no
  // opinion about. A host builds its registry from this alone.
  google.protobuf.Struct frontmatter = 3;
  // Every file of the skill, each exactly once, SKILL.md included. Complete by
  // construction: a catalog that cannot enumerate a skill does not serve it.
  repeated SkillFile resources = 4;
  // The limits that applied when this entry was produced, so a client knows a
  // skill was within them without knowing the framework's numbers.
  int32 max_files = 5;
  int64 max_bytes = 6;
}
```

`ListSkills` and `FindSkills` return `SkillRef` plus the two fields a client selects
on, rather than whole entries. A deployment with many skills would otherwise fetch
every manifest to decide which one to load — and three of the four target clients
already budget that initial listing, so the aggregator cannot be the thing that makes it
expensive.

`PutSkill` carries the whole skill rather than a file at a time. `copy` and `move` read
one skill and write another, and a whole-skill put is what both of them are, as well as
what a git-backed commit is: one change, one commit.

**A catalog that serves a skill can enumerate it.** A catalog that downloads a skill
has the files and can digest them, so the manifest is always available and the
specification's `"dynamic"` marker is never needed here. `skills.sh` is the case this was
asked about, and the answer is that its implementation downloads the skill from the web
and examines what arrived.

## Serving skills over MCP

Skills reach an agent through the **MCP Skills extension**
(`io.modelcontextprotocol/skills`). The framework serves that extension from its
aggregated MCP server, so a skill is discoverable and readable with the same client
that already sees the deployment's tools.

The extension defines three methods:

| Method | Purpose |
|---|---|
| `skills/list` | Enumerates the skills the server serves, paginated |
| `skills/get` | Returns the entry for one skill, named by the URI of its `SKILL.md` |
| `resources/directory/read` | Lists a directory resource's direct children (optional) |

A server that declares the extension **must** implement `skills/list` and
`skills/get`. `resources/directory/read` is gated behind a `directoryRead: true`
setting, and a client must not call it otherwise.

Each file in a skill is exposed as an ordinary MCP resource under a `skill://` URI and
read with the standard `resources/read`. The extension is therefore a way to publish
*instructions and supporting files* alongside the tools a server already serves — not a
second content channel.

### A skill entry

Both `skills/list` and `skills/get` return the same entry shape:

```json
{
  "uri": "skill://code-review/SKILL.md",
  "frontmatter": {
    "name": "code-review",
    "description": "Use this when reviewing a change for correctness, not style.",
    "license": "Apache-2.0"
  },
  "resources": [
    {
      "uri": "skill://code-review/SKILL.md",
      "digest": "sha256:b95a384300adeea2d902f7d19cd7c04b378ef58e09759107b9c7db4dcacbaa25",
      "size": 190
    }
  ]
}
```

`frontmatter` is the `SKILL.md` frontmatter rendered verbatim as JSON, carrying **every
field the author wrote** rather than a curated subset. A host builds its skill
registry from these entries alone, without fetching each `SKILL.md`, so a field this
framework dropped would be a field a client could never see.

`resources` is either a complete array of the skill's files — `SKILL.md` and every
supporting file, each with a SHA-256 digest and a byte length — or the string
`"dynamic"` for a skill whose content is generated such that no stable digest can be
published. A skill read from a directory always has a complete array: the files are
there, and their digests are computable.

### Integrity

Every `resources` entry carries the SHA-256 of the file's bytes as `sha256:{64 hex}`
and its length. A host **must** verify a file against the entry it was fetched under,
and a mismatch means the content is not what the entry described — corrupted,
tampered with, or stale. The host recovers by calling `skills/get` for the skill
again; because the `resources` set then differs, any approval bound to the old set is
revoked.

This is what the digests are for, and it is worth being precise about what they are
not: **a digest is not a trust anchor.** It comes from the same server as the content.
It confirms that the entry and the file agree, and cannot establish that the skill is
safe, cannot defend against the server itself, and cannot detect an intermediary that
rewrites both together.

### Limits

| Limit | Value | Counted over |
|---|---|---|
| Resources per skill | 512 entries | The entries of the skill's `resources` |
| Total size per skill | 16 MiB | The sum of `size` over those entries |

A host must accept a skill up to these limits, so a skill over either is one no
conforming host is obliged to load. A skill that exceeds them is **refused** rather
than served, with an error naming what the skill holds and what the limit is.

### Errors

| Condition | Code |
|---|---|
| `skills/get` for a URI that identifies no skill this server serves | `-32602` |
| `resources/directory/read` for a URI that is not a directory resource | `-32602` |
| `resources/read` for a skill file this server does not serve | `-32602` |
| Internal failure | `-32603` |

A server that has not declared `directoryRead: true` need not recognise
`resources/directory/read` and answers as the base protocol specifies for an unknown
method.

## How the framework implements it

### No released Go SDK implements this extension

Checked against the module proxy and the SDK's own sources, as of 2026-09-26:

| Version | Protocol revision | `skills` support |
|---|---|---|
| v1.6.1 (what this repository pins) | `2025-11-25` | none |
| **v1.8.0** (latest release) | **`2026-07-28`** | **none** |

The string `skill` does not appear anywhere in the v1.8.0 module — not in `mcp/`, not
in tests, not in the docs. Nor does any other SDK: the extension's own
implementations list records the Go, TypeScript, Python and C# SDKs all as *in
progress*. This extension is ahead of every SDK, not behind the Go one.

Support is being written, in the Go SDK, and is not available:

- **`modelcontextprotocol/go-sdk#1238`** — *skills: add SEP-2640 protocol support*.
  Open, not a draft, 15 commits, 27 files, last touched 2026-09-23. Adds a top-level
  `skills` package: `types.go`, `client.go`, `server.go`, `validation.go`,
  `verify.go`, `pagination.go`, and a conformance server. Its `AddHandlers` installs
  `ListSkillsHandler`, `GetSkillHandler` and `ReadDirectoryHandler` on an
  `*mcp.Server`, and its `Skill` type is the entry shape the specification defines.
  It is **protocol only** — no filesystem layer — so the part this framework would
  otherwise have to write is not in it either way.
- **`#1240`** — *skills: add filesystem provider helper*, still a **draft**, and
  stacked on #1238. This is the layer that would do the directory walking, manifest
  building and digesting. A draft carries no compatibility promise at all.

**Not adopted, and the reasoning is recorded so it is not relitigated:** pinning a
`pseudo-version` to #1238's head commit would make this framework's build depend on an
unmerged pull request in another repository. That commit can be rebased or force-
pushed, the pseudo-version then resolves to different code or stops resolving at all,
and the breakage would appear in *our* CI as a dependency failure in someone else's
change. #1238 is also recorded as `behind` main, so a rebase is expected before it
lands. We depend on released versions.

### The implementation therefore uses the SDK's extension points

Three, all of which are stable API rather than internals, and all present in v1.8.0:

- **`AddReceivingCustomMethod`** serves `skills/list` and `skills/get`. This
  **corrects** what this section originally said, which was `AddReceivingMiddleware`,
  and the correction is worth its own paragraph because the plan was wrong in a way
  only running the code would show.
- **`AddResourceTemplate`** with a `skill://` template routes `resources/read` for a
  skill's files, since the SDK's `lookupResourceHandler` matches URI templates as well
  as exact URIs. The path is a *reserved* expansion, `{+path}`, because a skill's
  files are nested and a plain `{path}` matches one segment only.
- **`ServerCapabilities.AddExtension`** declares
  `io.modelcontextprotocol/skills` with its `directoryRead` setting, alongside the
  `resources` capability the extension requires. Capabilities are fixed when the
  server is created, so the declaration happens there rather than afterwards.

#### Why middleware could not have done it

The plan was that receiving middleware would intercept the two methods, on the
reasoning that a method the SDK does not know falls through with
`ErrNotHandled` and a middleware handling it would never reach the dispatch. The
premise is right and the conclusion does not follow. The SDK checks a request
against its method table *before* the middleware chain runs:

```go
func handleReceive[S Session](ctx context.Context, session S, jreq *jsonrpc.Request) (Result, error) {
	info, err := checkRequest(jreq, session.receivingMethodInfos())
	if err != nil {
		return nil, err
	}
	...
	mh := session.receivingMethodHandler()
```

`checkRequest` answers `ErrNotHandled` for a method it does not know, and the
request never reaches a middleware. The middleware chain only ever runs for
methods already in the table — so a middleware cannot add one, and
`AddReceivingCustomMethod` is the SDK's supported way to do it. It is a better
point anyway: a custom method goes through the same middleware chain a standard
method does, so whatever the gateway already installs applies to it too.

#### Two things about the SDK that shaped the implementation

**Client identity is not in the session.** The design already said client identity
comes from the connection rather than from configuration, and at `2026-07-28`
that is true in a stronger sense than the design meant: the revision has no
`initialize` handshake, so the SDK leaves `ServerSession.InitializeParams` **nil**
— deliberately, because there is no single answer. Every request carries
`clientInfo` and `clientCapabilities` in its own `_meta`, and that is where the
identity is read from. So the design's conclusion held and its stated reason did
not: there is no connection to ask.

**Extensions are declared on the capabilities, not on the server.**
`AddExtension` is a method on `*ServerCapabilities`, and `ServerOptions.Capabilities`
is read when the server is constructed. Passing a non-nil `Capabilities` is also
required to declare anything at all: nil means the SDK's historical default of
the logging capability alone, which is not what a gateway serves.

### Why the SDK is upgraded to v1.8.0 for this

The extension is specified against protocol revision `2026-07-28` or later, and it
declares its capabilities in the `extensions` field of the `server/discover` response.
v1.6.1's newest revision is `2025-11-25` and it has no `server/discover` at all, so on
v1.6.1 the declaration would go somewhere the specification does not describe, and a
client that reads capabilities the spec-correct way would not see it.

v1.8.0 is a released version — not a pre-release, and not a pull request — whose
newest revision is exactly `2026-07-28`, and which implements `server/discover` with
`ServerCapabilities` as its result. On v1.8.0 the extension is declared where the
specification says to declare it, at the revision the specification says to declare it
at. That is the difference between implementing the extension and implementing a
workaround for the SDK's absence of one.

The upgrade is on its own, separately, because it is a change to a dependency every
module shares and not a change to this feature.

### The revision is reachable over HTTP only as a stateless server

The SDK's streamable HTTP transport serves `2026-07-28` **only** when configured
stateless. This is in the transport, not a preference:

```go
// The streamable HTTP transport supports every legacy SDK protocol version,
// but the SEP-2575 >= 2026-07-28 protocol is only supported when the
// transport is configured as stateless.
func (t *StreamableServerTransport) SupportsProtocolVersion(version string) bool {
	if version >= protocolVersion20260728 {
		return t.Stateless && slices.Contains(supportedProtocolVersions, version)
	}
	return slices.Contains(supportedProtocolVersions, version)
}
```

A stateful HTTP handler therefore cannot serve the extension at all: clients
negotiate down to `2025-11-25`, which is below the revision the extension
specifies. `HTTPHandler` is stateless, which is what makes the extension servable
over HTTP. See `docs/mcp.md` for the full table.

### What statelessness costs, and what it does not

A stateless endpoint cannot make a server-to-client request, and the SDK rejects
one immediately. **Elicitation is a server-to-client request**, so it is
unavailable over a stateless HTTP endpoint.

This costs the framework a capability it wants, and the wanting is worth stating
plainly: elicitation is the standard mechanism for a server to ask its client to
collect input from the user, and the framework has a rule that needs exactly
that — a change to a project's configuration file is the user's to confirm, and
adding a skill to a project writes `skills:`.

Three facts make this a division of labour rather than a shortfall:

- **The gateway is the same in every mode.** A deployment that needs the server to
  ask its user something runs the same gateway over **stdio**, which serves
  `2026-07-28` with sessions and can elicit. The transport is a launch-mode
  choice, not a property of the framework.
- **The framework holds no per-session state to lose.** The exposure footprint is
  process-wide by design — exposure belongs to a deployment, not a connection — so
  nothing is given up by answering each request on its own.
- **The rule does not require a prompt to hold.** A rule that must reach a user
  either runs where elicitation is available, or is implemented as a value the
  agent has to relay and the user has to answer. The second works on any
  transport, and is what this feature does: adding a skill to a project returns the
  change and requires the user to confirm it, which is a two-step interaction
  rather than a prompt.

So: **elicitation is a stdio capability**, and the skills extension is served over
both stdio and stateless HTTP. Neither is given up, and nothing needs to be
revisited when elicitation is implemented.

### One thing the sessionless revision changes about distillation

The design says client identity comes from the connection, not from configuration.
Under `2026-07-28` there is no connection to read it from — there is no
`initialize` handshake at all — so the client states itself **on every request**:

- `io.modelcontextprotocol/clientInfo` in the request's `params._meta`;
- `io.modelcontextprotocol/clientCapabilities` beside it.

The conclusion is the same and the mechanism is simpler: identity is what the
client asserts on the request being served, never what a deployment configured
about it. Distillation reads `clientInfo` from the request and has nothing in hand
but that request. An unknown client is still the common denominator, and an
assertion is still only an assertion — it selects a projection, it grants nothing.

## Security

Skill content is instructional text delivered to a model, which makes it a
prompt-injection surface in a way a tool call is not: a served skill can place
server-authored bytes in front of a model and direct it to act on them with the host's
own tools.

Decided:

- **A feature that asks for more authority is never acted on.** `allowed-tools` is the
  only one, and ignoring it is a decision rather than a gap. The specification is
  explicit that a remote server populating this field is *requesting elevated access on
  the host*, not describing its own environment — so a default of "honour it" is not
  available. A skill naming it is served, the field is not acted on, and the reason is
  recorded here so it can be revisited deliberately rather than rediscovered.
- **A feature that asks for less is carried.** `disallowed-tools` narrows the tool pool
  while a skill is active, so acting on it can only leave a model less capable than it
  would otherwise be. Refusing it would mean a model kept a capability the skill's
  author asked to have taken away.
- **A feature that states a need is checked, not granted.** `dependencies.tools` says
  what a skill requires, which in a Toolbox deployment is verifiable: the deployment
  knows which MCP tools it exposes. Reporting a dependency that is not there is useful
  to the author; silently satisfying it would not be.
- **The three are never treated as one thing.** A request for authority, a request for
  restriction, and a statement of need are three different claims about authority, and
  the union keeps them apart.

Also decided, and recorded here so a later change to one can be argued from the
reason rather than rediscovered:

- **A load asks the user nothing. The project's list is the permission.** A skill the
  project names is a skill the project exposes, and a person who put a name in their own
  configuration has already decided it. There is no second prompt between a project and
  a skill, and a tool that adds one is subject to the configuration-confirmation rule
  rather than to a separate approval of its own.
- **A named skill is pinned, and the pin is the manifest.** A pin records the skill's
  name *and* the manifest of its files with their digests, which is what makes a change
  detectable. A catalog is live — `skills.sh` publishes updates, a git catalog can be
  pulled — so the content behind a name can change under a project that never asked it
  to.

  **The pin lives in `skills.lock.yaml`, beside the configuration file, and not in it.**
  This **corrects** what the design originally said, which was that the `skills:`
  entry records the manifest inline, and the correction matters because of what the
  configuration file is. It is the one file in a project a person writes and reviews,
  and its value is that its list is readable at a glance: this project depends on
  `skills.sh.pull-request-review` and on nothing else. A machine-maintained manifest
  block inline would mean every content change rewrote the file whose whole purpose is
  that a person can read it, and it would put generated bytes inside the one file the
  configuration-confirmation rule protects.

  So it is a lockfile by every convention that matters: written on first use, compared
  rather than trusted, and read by the person who wants to know what changed rather
  than by whoever reviews the file. A bare reference in `skills:` therefore means
  "unpinned" — a valid entry, and one whose first use records the pin. The separation
  also keeps rule 17 about the file a person reviews: adding a skill changes the list,
  and the pin beside it is not a change to anything a person wrote.
- **A changed skill is marked outdated and the user is told, not blocked.** When the
  catalog's manifest for a pinned skill no longer matches the pin, the skill is marked
  outdated and the change reported, and the project chooses whether to pull the update
  or stay where it is. The specification's content-bound approval is the same idea — a
  changed set means the content someone agreed to is no longer the content in use —
  and this is where that lands: the pin is the agreement, drift is visible, and nothing
  is refused behind the user's back.
- **A skill's origin is visible in the URI and in the project's list, and that is
  enough.** The origin is the catalog, it is the first segment of every URI the skill
  is served under, and a project exposes a non-local skill only by naming it in
  `skills:`. The exposure *is* the record: there is nothing a model could read that the
  URI and the configuration do not already say, so no separate mechanism is wanted or
  needed.
- **A change to a project's configuration file is confirmed by the user.** Settled,
  and settled as a general framework rule rather than a skills one: a person writes and
  reviews their project's configuration, so an operation that would change it asks first
  and names the file and the change. It is AGENTS.md rule 17, and it applies to any
  feature whose job involves a project saying something new about itself — so the tools
  that add, enable and disable a skill go through it, and so does anything added later.

  "Asks first" is implemented as a value the agent has to relay and the user has to
  answer, not as an MCP prompt. That is not a lesser mechanism chosen for convenience:
  elicitation is unavailable on the stateless HTTP endpoint this feature must also be
  served over, and a rule that only held on one transport would not be a framework
  rule. The two-step interaction works everywhere. See *The revision is reachable over
  HTTP only as a stateless server*.
- **A skill is a skill.** The framework does not classify skills by origin, and a
  catalog prefix is not a trust tier. What a project does have is a place to record
  that it depends on somebody else's content: the `skills:` list names every non-local
  skill by qualified reference, so the dependency is visible in a file a person reads.

## What exists today

**Built:**

- **`pkg/skills`** — the format. A skill is read from a directory or from a document plus
  a manifest, into one typed form; two input dialects reach it and one stating a fact
  twice with two values is refused; a template is projected for a client; a manifest is
  computed from the bytes it describes.
- **`pkg/skills/proto`** — the catalog contract, seven methods, in a root package because
  a *provider* implements it.
- **`subsystems/skill`** — the aggregator. The implicit local catalog, resolution by
  qualified reference, `skills.lock.yaml`, and `SkillService`. It serves the project's own
  skill set and, through `CopySkill` and `MoveSkill`, transfers a skill between catalogs —
  including catalogs held by different providers, since a transfer is composed from reads and
  writes a catalog already serves.
- **`pkg/mcp`** — the extension. `skills/list`, `skills/get`, a `skill://` resource
  template, and the capability declaration. A deployment serving no skills declares
  nothing.

- **`subsystems/skillgit`** — the git-backed catalog provider. A provider *host*: a
  deployment registers as many catalogs as it likes, one per remote, each under its own
  identifier. It serves the catalog contract for every registered catalog and a second
  service for the registrations themselves.
- **`subsystems/skilldirectory`** — a **contributor**: it configures the catalog provider
  with the repositories the public directory indexes, and exposes nothing itself. It is
  the worked example of the composition-contribution mechanism, and the shape a user's own
  contributor takes when the repository is private and the credential is not written down.
  See [ADR-0013](decisions/0013-composition-contributions.md).
- **`RemoteURL`** — `owner/repo` resolved to a GitHub remote, which is what makes a
  public-directory skill a registration rather than a subsystem.

**Not built, and a deployment is complete without them:**

- **Nothing.** Every operation described above is served. The entry that used to sit here —
  `copy` and `move` between catalogs — has been built, and
  [that section](#copying-and-moving-between-catalogs) now describes what they do rather than
  what they would do.

**What was there before, and is gone:** a versioned in-memory catalogue of skill
*metadata* — an identifier, a version, a name, a description, instructions as a single
string, and declared capability and policy references. It had no notion of a catalog, of
a skill directory, of files, of digests, or of MCP, and a subsystem that cannot do any
of those is not a subsystem that could.

## Decisions taken, and their reasons

Recorded here so a later change to one can be argued from the reason rather than
rediscovered.

| Decision | Why |
|---|---|
| The SDK moves to v1.8.0 | It negotiates `2026-07-28` and implements `server/discover`, which is where the extension is declared. v1.6.1 does neither. |
| Skills live in catalogs, not one store | Several sources exist — a project's own directory, a public registry. A model that assumed one source would have no way to express the second. |
| A catalog is a provider role | A contract others can implement, resolved by identifier at the point of use — the same pattern as `parser`/`adapter`/`invoker`, for the same reason. |
| A catalog declares read-only or read-write | So a write against a catalog that cannot take one is refused with a reason rather than appearing to succeed. |
| References are `<catalog>.<name>` | Skill names are not unique across sources, and the MCP specification requires hosts to resolve them per origin. The prefix *is* the origin. |
| The `local` catalog is implicit | A project owns its own skills and should not have to declare that it has them. |
| The format reader is a root package | AGENTS.md rule 4: a subsystem makes an implementation addressable, it does not hold the logic. Two catalogs need the same reader. |
| `SkillService` is read-only plus validate | The catalogs own storage. A write RPC on the aggregator that did not write a catalog would misreport where a skill lives. `validate` earns its place by failing a deployment at start rather than at first use. |
| `directoryRead: false` | The manifest is already complete, and the specification says a directory read adds nothing for a host holding an entry. It is for dynamically generated skills, which a catalog of files is not. |
| `allowed-tools` is ignored, and documented as ignored | A server populating that field is requesting elevated access on the host, not describing its own environment. Ignoring it now is a decision, not an omission: a skill naming `allowed-tools` is served, the field is not acted on, and the reason is recorded so it can be revisited deliberately. |
| The HTTP MCP endpoint is stateless | The SDK's streamable HTTP transport serves `2026-07-28` only when stateless, and the Skills extension is specified against that revision. A stateful handler negotiates down to `2025-11-25` and cannot serve the extension at all. Nothing is lost: the gateway holds no per-session state, and exposure is process-wide by design. |
| Elicitation is a stdio capability | A stateless endpoint cannot make a server-to-client request, and elicitation is one. The same gateway over stdio serves the same revision with sessions and can elicit, so the transport is a launch-mode choice and no capability is given up by choosing either. |
| The extension is served through `AddReceivingCustomMethod`, not middleware | The SDK checks a request against its method table before the middleware chain runs, so a middleware cannot add a method the SDK does not know. The original plan assumed otherwise; the code says otherwise. |
| Client identity is read from the request's `_meta` | At `2026-07-28` there is no `initialize` handshake, so the SDK leaves the session's initialization parameters nil and the client states itself on every request. Identity is where the client wrote it, which is where the design already said it was — but for a stronger reason than it gave. |
| A pin is a lockfile beside the configuration, not a block inside it | The file a person writes and reviews should hold what the person decided. A machine-maintained manifest inline would rewrite that file on every content change and put generated bytes inside the one file rule 17 protects. |
| `ReadSkillFile` serves the skill's own document as the projection | The entry a client fetched it under describes the projection. Serving the template would give a client a file whose frontmatter disagrees with the entry in its hand, and the two digests would then describe different things. |
| A manifest is recomputed, never taken from a catalog | A digest in a manifest a server published is worth exactly as much as the server published it, so a check that reads the manifest and compares it to itself agrees with whatever it was handed. A manifest that cannot be complete is refused rather than completed. |
| `api.ConnectKind` reads a kind back off the wire | A gateway answering a protocol of its own has to decide which code *that* protocol wants: "no such skill" is `NotFound` inside ConnectRPC and `InvalidParams` inside the skills extension, because there the URI is the parameter. It is the inverse of `ConnectCode` and lives beside it, so the mapping is stated once in each direction. |

## Decisions this phase added

| Decision | Why |
|---|---|
| A provider is not a catalog, so the contract enumerates | The git provider holds a checkout per registered remote. A directory reading a provider's own identifier as a catalog's name resolved one catalog of a dozen and left the rest unreachable, which a project's `skills:` list would name with no way to be served. The contract gained `ListCatalogs` because the design had already stated "provider host" twice and the contract had not carried it. |
| The provider's management service lists *checkouts*, not catalogs | Which catalogs are served is the catalog contract's question and every provider answers it. Which commit a checkout is on is a fact about a directory one provider owns. Two methods with one name answering one question in two shapes would be one fact stated twice with nothing to catch a disagreement. |
| A catalog found is read-only; a catalog created is not | It is what makes pushing to somebody else's repository a separate deliberate act rather than a side effect of storing a skill, and it is why `copy` and `move` can be refused with a reason instead of appearing to succeed. |
| `PutSkill` carries the content, not only the manifest | A skill written file by file could be read back halfway through, and a manifest naming a file nobody had written is a pin nothing describes. A file neither sent nor listed is removed, so a skill replaced wholesale is exactly what was sent — a stale supporting file is content the author deleted. |
| git is the binary, not a library | `git` already knows the machine's ssh keys, credential helpers, proxy configuration and `insteadOf` rules, and "credentials are deployment configuration" means a deployment should not hand a private repository's secret to a framework. |
| The public directory is a registry over git, not a source | Its own installer shallow-clones the repository, and there is no JSON API to enumerate against. So it is a `owner/repo` shorthand resolving to GitHub, and a deployment claims only to search the repositories it registered rather than a directory it cannot query. |
| A `bin/` directory is build and tool output at any depth | The check that asks "is anything on disk that git will not track, and is it source" would otherwise report a `.yml` a browser tool wrote there. The directory is the signal, not the extension. |

## Tests

The checks phase 7 is written against. These are the specification's requirements,
not this framework's preferences:

- **Reader.** A skill that validates, and one refused with a reason naming what is
  wrong. The manifest's digests and sizes, recomputed and compared. Both limits, at
  and over the boundary. Deterministic ordering.
- **Handler.** Valid reads, malformed requests, not-found behaviour, and ConnectRPC
  error-code assertions — including `-32602` for a URI that identifies no skill.
- **Protocol.** `skills/list` and `skills/get` over a real transport, not a fake one.
  `resources/read` for a skill file. The declared extension and its `directoryRead`
  setting. `skills/list` empty for a deployment with no skills, rather than an error.
- **Boundary.** The directory-name rule. Refusal of any path that resolves outside a
  skill's own directory. Refusal of a symlink that leaves it.
- **Independence.** The subsystem passes with `GOWORK=off`, and the root package it
  mounts has no dependency on any subsystem.
