# Skills

A skill is a body of instructions for carrying out a task, written once and served to
an agent that needs it. This document is the design record: what the format is, where
skills come from, how they reach an agent, and which parts of the protocol the
framework implements.

It is written as the specification is agreed, step by step. Sections marked *not yet
decided* are open questions, and a section that is marked as such is not yet
implemented — this document records intent as well as fact, and the two are labelled
separately for that reason.

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

## Where skills come from

A skill lives with the project it belongs to, under the project's configuration
directory:

```text
.toolbox/
└── skills/
    ├── code-review/
    │   ├── SKILL.md
    │   └── references/checklist.md
    └── release-notes/
        └── SKILL.md
```

A deployment with no skills has no `skills/` directory, and that is not an error: an
empty catalogue is a deployment that offers nothing, not a deployment that failed to
start.

**Not yet decided:** whether skills are also stored in the subsystem's own catalogue,
as the versioned in-memory store does today, or whether the directory becomes the
only source. The two answer different questions and the specification step covering
storage will settle it.

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

The MCP Go SDK the framework uses (v1.6.1) has **no** support for this extension: no
`skills` primitive, and a protocol revision list whose latest entry predates the
extension. The extension is therefore implemented in `pkg/mcp` on the three extension
points the SDK does offer:

- `AddReceivingMiddleware` intercepts `skills/list` and `skills/get`. The SDK's
  default receiving handler answers `jsonrpc2.ErrNotHandled` for a method it does not
  know, so a middleware that handles these never falls through to the SDK's dispatch.
- `AddResourceTemplate` with a `skill://` template routes `resources/read` for a
  skill's files, since the SDK's `lookupResourceHandler` matches templates as well as
  exact URIs.
- The server's `ServerCapabilities.Extensions` carries
  `io.modelcontextprotocol/skills` with its `directoryRead` setting, and the
  `resources` capability is declared alongside it as the extension requires.

The one thing the SDK does decide for us is the negotiated protocol version: a client
asking for a revision the SDK does not know is answered with the SDK's latest
(`2025-11-25`), and the extension is specified against `2026-07-28` or later. **Not yet
decided:** whether to declare the newer revision ourselves in `initialize`, and what a
client that then sees is required to do. The extension's methods work regardless,
because they are dispatched by name.

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
  its content, given that skill names are not unique across origins and a served skill
  must not silently shadow a same-named local one.

## What is implemented

*Not yet.* This document currently records the format, the protocol binding, and the
open questions. No part of the skills feature is implemented in this repository.

The existing `subsystems/skill` holds a versioned in-memory catalogue of skill
*metadata* — an identifier, a version, a name, a description, instructions as a single
string, and declared capability and policy references. It has no notion of a skill
directory, of files, of digests, or of MCP. What happens to it is one of the open
questions above.

## Tests

*Not yet decided.* For reference, the checks that will apply:

- Store and reader: a skill that validates, one that is refused with a reason, the
  manifest's digests and sizes, the two limits, and ordering.
- Handler: valid reads, malformed requests, not-found behaviour, and ConnectRPC
  error-code assertions — including `-32602` for a URI naming no skill.
- Protocol: `skills/list` and `skills/get` over a real transport, `resources/read` for
  a skill file, and the declared extension and its `directoryRead` setting.
- Boundary: the package-clause and directory-name rules, and refusal of any path that
  resolves outside a skill's own directory.
