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

## Where skills come from: catalogs

Skills do not live in one place. They live in **catalogs**, and a project says which
ones it wants.

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

**Not yet decided:** whether `local` is read-only or read-write. A person's answer is
that they edit the files; the framework's answer would be that installing a catalog's
skill into a project writes into this directory. Those are different claims about the
same directory and only one of them can be true.

### Fully-qualified references

A skill is named `<catalog>.<name>`. `local.code-review` is the `code-review` skill in
the `local` catalog; `skills.sh.some-owner-some-skill` is a skill from the remote
catalog.

The catalog prefix is not decoration. Two catalogs may both hold a skill called
`code-review`, and the specification is explicit that skill names are *not* unique and
that a host must resolve them per origin — so the thing that identifies a skill in this
framework is the pair, and dropping the catalog from a reference would reintroduce
exactly the collision the qualification exists to prevent.

**Not yet decided:** how the qualified reference maps onto the MCP URI. The
specification requires the *final* path segment to equal the skill name, and allows
leading segments as a server-chosen organisational prefix, so `skill://<catalog>/<name>/SKILL.md`
satisfies it. Whether the implicit `local` catalog is spelled out or elided is open —
and it should be one or the other, because two spellings of one skill is the ambiguity
the qualification was introduced to remove.

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
expressed, how it behaves offline, and what a skill from it is trusted to be. The last
of these is not a detail — see *Security*.

**Not yet decided:** whether the aggregator is a reshape of `subsystems/skill` or a new
subsystem beside it. The existing `subsystems/skill` holds a versioned in-memory
catalogue of skill metadata, which is not a catalog in this sense: it has no files, no
digests, and nothing to aggregate.

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

What this framework must therefore decide, step by step:

- **Not yet decided:** whether a skill's `allowed-tools` frontmatter is ignored
  outright, or ignored unless a deployment has explicitly approved that grant for
  that skill. The specification is explicit that a remote server populating
  `allowed-tools` is requesting elevated access on the host, not describing its own
  environment — so a default of "honour it" is not available.
- **Not yet decided:** what approval, if any, a skill load requires in this framework,
  and whether approval is bound to the entry's `resources` set.
- **Not yet decided:** how a skill's origin is made visible to a model that receives
  its content. A catalog prefix is not the same thing as an origin: a skill from
  `skills.sh` is third-party content, and a project that includes one should be able to
  see that before the skill's text reaches a model.

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
