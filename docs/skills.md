# Skills

A skill is a body of instructions for carrying out a task, written once and served to
an agent that needs it. This document is the design record and the implementation
plan: what the format is, where skills come from, how they reach an agent, which parts
of the protocol the framework implements, and in what order the work gets done.

It is written as the specification is agreed, step by step. Sections marked *not yet
decided* are open questions, and a section that is marked as such is not yet
implemented — this document records intent as well as fact, and the two are labelled
separately for that reason.

**Status: the specification is being agreed. Phases 2 to 6 of the plan below are open,
and nothing is implemented.**

## The plan

Eight phases. Phases 1 and 8 are settled; the specification is phases 2 through 7, and
each closes before the next opens.

| # | Phase | State | Produces |
|---|---|---|---|
| 1 | **Upgrade the MCP Go SDK to v1.8.0** | **decided** | A dependency that negotiates `2026-07-28` and implements `server/discover` |
| 2 | **The catalog model** | **decided** | A catalog role, aggregation, and fully-qualified references |
| 3 | **The catalog RPC contract** | open | The provider contract a catalog subsystem implements |
| 4 | **The aggregate contract** | **decided** | `SkillService`: list, get, read file, validate |
| 5 | **The reader: the format in a root package** | **decided** | `pkg/skills`, mounted by whichever subsystem serves a catalog |
| 6 | **The project configuration** | open | What the `skills:` list names, and how it resolves |
| 7 | **Security posture** | partly decided | `allowed-tools` ignored; the rest open |
| 8 | **Implement** | **decided** | The feature, end to end, against the specification's requirements |

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

### Phase 8 — implement

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
fields. **Not yet decided:** which of these the framework carries through, and
`allowed-tools` in particular has a security question attached to it — see *Security*.

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

The first targets are the common cases:

| Client | Known shape |
|---|---|
| **opencode** | Reads `SKILL.md` directories (`.agents/skills/`) natively; over MCP it models resources with `uri`/`mimeType`/`description` and surfaces a skill as a command with `source: "skill"` |
| **Claude Code** | `SKILL.md` directories; frontmatter `name` and `description` are what it selects on |
| **Codex** | *Not verified — see below* |
| **VS Code Copilot** | *Not verified — see below* |

**The client says which it is.** The identity comes from the connection — what the
client reports when it initialises — and not from configuration, because a distillation
that guessed wrong hands a client content it cannot use, and nothing in the result
would reveal the guess.

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

**Not yet decided:** what the "skills features" are that distillation adapts to, and what
a client that has no skills support at all is given. See below.

### What a Toolbox extension may add

A template may carry Toolbox properties. `enabled` is one: a local skill whose
frontmatter says it is not enabled is part of the project and is not served. This is
how a skill a person put in their own project gets switched off, and it is a property
of the template rather than of the project's configuration, which is why a project does
not need a second list to hold exclusions.

**Not yet decided:** the exact property names and their types, and whether the
distilled output carries them. They are namespaced under the prefix above precisely so
a client that does not know them ignores them, and so a future Toolbox can add more
without a second naming scheme.

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

**Not yet decided:** how a local skill is *disabled*. It is implicitly included, so
there is no entry to remove, and a skill a person put in their own project is not
obviously one they want switched off at runtime. Either there is a way to exclude one,
or the honest answer is that disabling applies only to skills that were named.

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

**Not yet decided:** whether the framework may rewrite a person's configuration file at
all when an agent asks. That is a write to a file a human owns and edits, and it is
treated below under *Security* rather than assumed.

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
its index. The subsystem therefore exposes tools for:

| Tool | Does |
|---|---|
| find a skill | Search the integrated catalogs for skills matching something an agent wants |
| add a skill to the project | Add a qualified reference to the project's `skills:` list |
| enable a skill | Add a reference that is present but not included |
| disable a skill | Remove a reference from the project's `skills:` list |

These are **not** the same thing as the `skills/*` MCP methods, and the difference
matters. `skills/list` reports what the project may use; these tools change what that
is, and a tool that changes state is governed by the framework's rules about side
effects: each declares what invoking it does, and a policy decides whether an agent may
invoke it. A tool that adds a remote skill to a project is `create`-shaped, and one that
lists or finds is `read_only`.

**Not yet decided:** whether `find` searches through the catalogs or searches only
their local index. A catalog is a provider; whether it can answer a query, or must be
enumerated and filtered here, changes the contract every catalog has to satisfy.

**Not yet decided:** whether the write tools are available at all without an explicit
opt-in, and if so, what they are allowed to write. See *Security*.

### The `skills.sh` catalog

One catalog implementation is for [skills.sh](https://www.skills.sh/), the public
directory of agent skills. It is a **read-only** catalog: it indexes skills published by
third parties and is browsed, not written to.

It is a separate subsystem from the aggregator, for the reason every provider is: it is
separately deployable, separately versioned, and a deployment may run without it.

What it is not yet pinned down, because it is a provider rather than part of the model:
how it enumerates (`skills.sh` presents a browsable directory and a
`npx skills add <owner/repo>` install path, and no public JSON API was found at the
obvious endpoints), how a skill's files are obtained, how a version or a pin is
expressed, and how it behaves with no network.

### The git catalog

The second catalog implementation is backed by a **git repository**, and it is
**read-write**: adding a skill to it is a change to a repository.

Its storage is the subsystem's own, not a checkout wherever the project happens to be:

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

**Not yet decided:** the identity a commit is made with, and whether the catalog commits
on every modification or batches them. Both are visible facts in somebody's history,
and neither should be a default nobody chose.

### Copying and moving between catalogs

The skills subsystem offers **two independent operations** — `copy` and `move` — and
neither is defined in terms of the other, so neither acquires the other's failure cases.

Both are refused when the **target** cannot be written: a read-only catalog does not
accept a skill, and saying so is the point of declaring one. A `move` also needs a
source it can remove from, which is its own condition rather than an inference from
copying.

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

- **`AddReceivingMiddleware`** intercepts `skills/list` and `skills/get`. The SDK's
  default receiving handler answers `jsonrpc2.ErrNotHandled` for a method it does not
  know, so a middleware that handles these never falls through to the SDK's dispatch.
  This behaviour is identical in v1.6.1 and v1.8.0.
- **`AddResourceTemplate`** with a `skill://` template routes `resources/read` for a
  skill's files, since the SDK's `lookupResourceHandler` matches URI templates as well
  as exact URIs.
- **`ServerCapabilities.AddExtension`** declares
  `io.modelcontextprotocol/skills` with its `directoryRead` setting, alongside the
  `resources` capability the extension requires.

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

## Security

Skill content is instructional text delivered to a model, which makes it a
prompt-injection surface in a way a tool call is not: a served skill can place
server-authored bytes in front of a model and direct it to act on them with the host's
own tools.

Decided:

- **`allowed-tools` is ignored**, and that is a decision rather than a gap. The
  specification is explicit that a remote server populating this field is *requesting
  elevated access on the host*, not describing its own environment — so a default of
  "honour it" is not available. A skill naming `allowed-tools` is served, the field is
  not acted on, and the reason is recorded here so it can be revisited deliberately
  rather than rediscovered.

Open:

- **Not yet decided:** what approval, if any, a skill load requires, and whether an
  approval is bound to the entry's `resources` set. The specification requires that
  binding wherever a host persists approvals, because a changed set means the content a
  person agreed to is no longer the content being loaded.
- **Not yet decided:** how a skill's origin is made visible to a model that receives
  its content. A catalog prefix is not the same thing as an origin: a skill from
  `skills.sh` is third-party content, and a project that includes one should be able to
  see that before the skill's text reaches a model.
- **A change to a project's configuration file is confirmed by the user.** Settled,
  and settled as a general framework rule rather than a skills one: a person writes and
  reviews their project's configuration, so an operation that would change it asks first
  and names the file and the change. It is AGENTS.md rule 17, and it applies to any
  feature whose job involves a project saying something new about itself — so the tools
  that add, enable and disable a skill go through it, and so does anything added later.
- **Not yet decided:** what a skill from a third-party catalog is trusted to be,
  including whether including one is a decision a project must make by name — which the
  qualified-reference model already gives a place to record.

## What exists today

The existing `subsystems/skill` holds a versioned in-memory catalogue of skill
*metadata* — an identifier, a version, a name, a description, instructions as a single
string, and declared capability and policy references. It has no notion of a catalog, of
a skill directory, of files, of digests, or of MCP.

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
