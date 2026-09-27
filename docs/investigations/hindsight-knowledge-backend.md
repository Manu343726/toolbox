# Investigation: using Hindsight as the knowledge subsystem's backend

> **Superseded.** This investigation fed into
> [`docs/knowledge.md`](../knowledge.md), which is the specification. Where they
> differ, `docs/knowledge.md` is right. It is kept because the API survey and
> the reasoning about the `fs mount` direction are the evidence behind it, and
> because §5.2's check of *why* a page cannot hold authored text is the reason
> the specification's ingest is a reconcile rather than a write.
>
> See also [`hindsight-human-wiki-integration.md`](hindsight-human-wiki-integration.md),
> the corpus design written after this one, and
> [`README.md`](README.md) for how the three documents relate.

**Status: investigation. Nothing here is a decision.** No ADR was proposed, no
code exists, and no contract was written. This document records what was found,
what the constraints were, and what the open questions were, so that a decision
could be made deliberately later.

**Read the repository claims against commit `25fc519`.** The old
`subsystems/knowledge` was deleted in `4e51265`, so §2, §3.3's Toolbox column,
§5.4's contract sketch, §6.2, §6.5 and §10 describe a repository that no longer
exists. They are kept unedited so the reasoning is auditable; §10 has a note
saying what actually happened instead. Every claim about Hindsight is unaffected
and still checkable against the API description.

## What Hindsight is

**Hindsight** (<https://hindsight.vectorize.io>, HTTP API **0.10.1**,
description at `/openapi.json`) is a memory backend written by Vectorize, not by
this project. You hand it documents; it extracts individual **facts** from them,
links them to **entities** and each other, consolidates related facts into
deduplicated **observations**, and answers questions by running four retrieval
strategies in parallel (vector, keyword, entity-graph, temporal), fusing and
reranking them. Given a question it will **reflect** — write a document
answering it and keep rewriting it as the bank changes. That document is a
**mental model**; a mental model in a folder tree with a trigger is a **knowledge
page**. The isolation unit is a **bank**.

Everything in this document that uses those words uses them in Hindsight's sense.
[`knowledge.md`](../knowledge.md) §2 is where the provider-neutral renaming
happens.

Start at [`/developer/knowledge-pages`](https://hindsight.vectorize.io/developer/knowledge-pages)
and [`/developer/observations`](https://hindsight.vectorize.io/developer/observations).
There is no `/developer` index; the site sidebar lists the paths.

---

**Scope of research.** Hindsight's public surface was read from the
documentation site and from the machine-readable OpenAPI description at
`https://hindsight.vectorize.io/openapi.json` (Hindsight HTTP API 0.10.1,
**77 paths, 99 operations, 195 schemas**). The Toolbox side was read from
this repository at commit `25fc519`. Every claim about Hindsight's API below is
traceable to that OpenAPI document or to a documentation page; the few places
where documentation and spec disagree are called out.

---

## 1. The short version

1. **Hindsight is a memory system, not a document store.** It stores four kinds
   of thing — raw facts, consolidated *observations*, synthesized *mental
   models*, and hand-authored-shaped *knowledge pages* — and it answers queries
   by running four retrieval strategies in parallel and fusing them. It also
   runs an LLM. This is much larger than the current `knowledge` subsystem and
   it is a genuinely good fit for what `docs/feature-spec.md` says the
   knowledge subsystem is *for*.

2. **Hindsight cannot be served by today's `toolbox.knowledge.v1`.** That
   contract has three RPCs (`PutSource`, `GetSource`, `Search`) and an in-memory
   reference store. Serving Hindsight through it would mean either discarding
   almost everything Hindsight does, or growing the contract substantially. The
   growth is additive and backwards compatible, so this is a sequencing
   question, not a blocker.

3. **The preload question has a clean answer, and it is not the obvious one.**
   `hindsight fs mount` projects *memory → disk*. Its inverse is **not**
   "create a knowledge page with the body I typed", because a knowledge page
   *is* a mental model: its content is always synthesized by an LLM from a
   `source_query`, there is no writable body field anywhere in the public API,
   and the default trigger rewrites the document after every consolidation.
   A hand-typed page is seed prose the first refresh will edit.

   The inverse is **ingestion**: authored files are retained as *documents*,
   which is what Hindsight's own Obsidian integration does, one way, with a
   content-hash reconcile. Authored text becomes the source of truth about
   *what was said*; the pages built from it become the reconciled truth about
   *what holds*. That is the same division Hindsight's own docs insist on.

4. **There is a second, different kind of preload** that is hand-authored
   *verbatim* and is never rewritten: **bank template import** — mission,
   directives, retain/reflect/observations configuration, consolidation
   strategies, entity vocabulary, and mental-model *definitions*, in one
   versioned JSON manifest with a published JSON Schema, a `dry_run` mode and a
   round-trip export. This is the artifact that belongs in a repository beside
   the deployment's configuration, and it maps closely onto this project's
   `docs/configuration.md` and `pkg/config`.

5. **A promising lead is a dead end.** `KnowledgeNode.managed` is documented as
   *"Client-set flag: true = system-owned, false = hand-authored"*, which sounds
   exactly like the escape hatch. It appears **once** in the entire OpenAPI
   document, in a **response-only** schema, and in **no** request schema. There
   is no HTTP way to set it. Do not design against it.

6. **Recommended placement: `pkg/knowledge` (provider-neutral) + a Hindsight
   client behind it, mounted by a new `subsystems/knowledgehindsight` serving a
   grown, provider-neutral `toolbox.knowledge.v1`.** This follows ADR-0005 and
   the `pkg/skills` → `subsystems/skillgit` precedent exactly, and it means a
   second backend later is a second provider rather than a second contract.

---

## 2. The knowledge subsystem as it was at `25fc519`

> **Deleted in `4e51265`.** There is no `subsystems/knowledge` any more, and
> nothing in this section is true of the tree. It is kept because the reason the
> contract had to grow rather than be reused is stated here, and because the
> reference implementation's limits are what made a real backend necessary in
> the first place.

### 2.1 What existed

`subsystems/knowledge` is 182 lines of implementation plus an 82-line contract.
It is a **reference implementation**, and the repository says so repeatedly:

- `docs/subsystems.md`: *"Store sources and perform deterministic metadata
  search — Reference metadata search."*
- `docs/feature-spec.md`: *"A knowledge source identifies a document collection
  or ingestion input. The reference implementation stores source metadata and
  performs deterministic metadata search. **Production ingestion, embeddings,
  vector indexes, and reranking are behind the same service contract.**"*
- `docs/status.md` known limitations: *"Knowledge search does not yet perform
  ingestion, embeddings, vector search, or reranking."*
- `docs/todos.md` P1: *"Knowledge ingestion — Add source adapters for files,
  URLs, and databases. Add chunking, metadata extraction, embeddings, indexes,
  and reranking. Add source ACLs and policy-aware retrieval."*

So the intent to put a real engine behind this contract is already recorded, and
Hindsight is an unusually complete answer to it.

### 2.2 The current contract

`toolbox.knowledge.v1.KnowledgeService`:

| RPC | Side effects | Notes |
|---|---|---|
| `PutSource` | `create update` | Stores a `KnowledgeSource{id,name,type,location,tags}` |
| `GetSource` | `read_only` | By id |
| `Search` | `read_only` | Deterministic case-insensitive substring over name/type/location/tags, score hardcoded to 1 |

The store is a `sync.RWMutex` map in memory. The search is a `strings.Contains`.
It is deliberately a placeholder.

### 2.3 Gaps that matter for Hindsight

- **No bank/tenant concept.** Every Hindsight call is scoped to a `bank_id`.
- **No document concept.** Hindsight's traceability, upsert, chunk listing and
  bulk delete all hang off a `document_id`.
- **No async operation concept.** Roughly a third of Hindsight's interesting
  operations return an `operation_id` and complete later.
- **No tag vocabulary.** Hindsight has `tags` with five match modes and compound
  `tag_groups`; the contract has a bare `repeated string tags`.
- **No derived-knowledge concept.** Observations, mental models and pages have no
  representation at all.
- **No provenance.** Hindsight can tell you which document and chunk a fact came
  from, with a `content_hash` per document. `SearchPassage` has only
  `source_id`, `text`, `score`.

Filling these means new services, not new fields on the existing ones.

---

## 3. Hindsight: the model, and the API surface

### 3.1 The mental model

Hindsight's pipeline, in its own terms:

```
Your agent  ──retain()──▶  Hindsight
                            │
     ┌──────────────────────┴───────────────────────┐
     │ LLM fact extraction: raw content → facts     │
     │ entity resolution, embedding, dedup          │
     │ temporal / semantic / entity links           │
     └──────────────────────┬───────────────────────┘
                            ▼
                    Memory units
              (world facts | experience facts)
                            │
                   consolidation (LLM)
                            ▼
                     Observations
              (deduplicated, evidence-grounded)
                            │
                   mental models (LLM)
                            ▼
                  Knowledge pages (LLM)
                   = a document in a folder tree
```

The four retrieval arms at query time, fused with RRF and then reranked:
**semantic** (vector), **keyword** (BM25), **graph** (entity traversal), and
**temporal** (dates). The name is TEMPR. This is a large qualitative jump from
substring matching, and it is the reason to use Hindsight rather than a plain
vector store.

A "bank" is the unit of isolation: its own config, its own memories, its own
pages, its own directives. Banks are the closest thing to a tenant.

### 3.2 The four kinds of knowledge, and who authors them

| Kind | What it stores | Authored by | Rewritten by a machine? |
|---|---|---|---|
| **World fact** | objective facts received | an LLM, from retained content | only on curate |
| **Experience fact** | the bank's own actions and interactions | an LLM, from retained content | only on curate |
| **Observation** | consolidated beliefs, deduplicated, with evidence and a proof count | consolidation LLM | continuously refined, never overwritten |
| **Mental model** | a synthesized document answering a `source_query` | reflect LLM | yes, on its trigger |
| **Knowledge page** | *is* a mental model, configured as a document | reflect LLM | yes, `mode: delta` after every consolidation |
| **Directive** | hand-written rules the reflect agent must follow | **a person** | **no** |
| **Bank config / missions** | mission, retain/reflect/observations missions, disposition traits, entity vocabulary, consolidation strategies | **a person** | **no** |

That table is the whole preload answer, and §5 is about it.

### 3.3 Full operation inventory

Grouped by the API's own areas. The totals — **77 paths, 99 methods** — are
exact; the per-group splits are not given as numbers, because several groups
share a templated path (`/directives/{id}` carries four methods) and any count
of them is a function of how the path is split rather than a fact about the
API.

**Monitoring** — `GET /health`, `/health/ready`, `/health/live`, `/version`,
`/metrics`. `/version` returns `api_version` and a `features` flag object
(`audit_log`, `bank_config_api`, `bank_llm_health`, `document_export_api`,
`document_import_api`, `file_upload_api`, `llm_trace`, `mcp`, `observations`,
`store_document_text`, `worker`) — **this is how a client should discover what a
given deployment can do**, rather than assuming.

**Memory** — retain, recall, reflect, dry-run extract, prompt preview, list
memories, get memory, curate memory, observation history, list tags, list
observation scopes, preview consolidation strategies, clear observations for a
memory, clear all memories, graph.

**Banks** — list, create, partial-update, delete, get/update/reset config,
stats, stats time-series, LLM health, profile (removed), background (removed),
aliases (list/add/set-primary/remove), clear all observations, recover failed
consolidation, trigger consolidation.

**Knowledge base** — tree, create folder, create page, get page, search
pages, update node, delete node, export markdown bundle.

**Mental models** — list, create, get, update, delete, history, refresh,
dry-run refresh, clear content.

**Directives** — list, create, get, update, delete.

**Documents** — list, list chunks, get, update tags, delete, reprocess,
get a chunk.

**Operations** — list, get, cancel, retry, delete. Every async workflow in
Hindsight is observable through these.

**Entities** — list, entity graph, entity detail (+ a deprecated
regenerate).

**Transfer** — import documents (async), export documents (async), download
a stored file, export a bank (async), import a bank (async), clone a bank
(async).

**Templates** — import a bank template, export a bank template, plus
`GET /v1/bank-template-schema`.

**Files** — `POST /files/retain` (multipart upload → memories, async),
`GET /files/download/{key}`.

**Webhooks** — register, list, delete, update, list deliveries.

**Audit / traces** — audit logs, audit stats, LLM request traces, LLM request
stats.

**Attachments** — `GET /banks/{bank}/attachments/{id}`, serving bytes
retained inline with a document.

### 3.4 MCP server (relevant because Toolbox generates MCP tools)

Hindsight ships its own MCP server at `/mcp/{bank_id}/`, **27 tools** in
single-bank mode and 30 in multi-bank mode. Single-bank mode puts `bank_id` in
the URL so tools do not carry it. Read-only tools are annotated
`readOnlyHint: true`; delete/clear/invalidate get `destructiveHint: true`.

This matters for §6: Hindsight's MCP tool names (`retain`, `sync_retain`,
`recall`, `reflect`, `create_knowledge_page`, …) are a good sanity check on what
a Toolbox contract *should* expose as agent tools, because Hindsight has already
made that judgement about the same feature set.

### 3.5 Client

An official Go client exists: `go get github.com/vectorize-io/hindsight/hindsight-clients/go`,
generated by OpenAPI Generator, namespaced as `client.MemoryAPI`,
`client.BanksAPI`, `client.DocumentsAPI`, etc. **Recommendation: do not depend
on it.** It is a large generated surface with `NullableString`/`NullableTime`
wrappers, it would drag the whole 195-schema model into a Toolbox module's
dependency graph, and §4 argues the Hindsight types must not appear in a Toolbox
contract anyway. A hand-written client over `net/http` + `encoding/json` for the
~40 endpoints actually used is smaller, has no transitive dependency, and is
testable against the real spec. The OpenAPI document itself is the contract to
hold it to.

---

## 4. Where this belongs in the architecture

### 4.1 The rules that bind this

From `AGENTS.md`:

- **Rule 4** — *"A format the framework implements itself lives in a root
  package; its provider subsystem mounts that package. Provider subsystems mount
  a package and convert messages — nothing else."*
- **Rule 8** — *"Core workflow, agent, skill, and knowledge contracts must not
  contain OpenAI, Anthropic, or other provider-specific request/response types."*
- **Rule 2** — every subsystem owns its own protobuf file and generated package.
- **Rule 3 / ADR-0009** — cross-subsystem calls are over ConnectRPC after
  `core.Bind`, never in-process.
- **Rule 5** — *"several providers serve the same contract on purpose, so a name
  cannot tell them apart"* — bind by identifier, not by contract name.
- **Rule 12** — `api.Errorf` / `api.WrapError` with an `api.ErrorKind`; never a
  bare `fmt.Errorf`, which collapses into "internal".
- **Rule 9** — MCP exposure is a policy boundary, not a reflection shortcut.
- **Rule 17** — a change to a project's configuration file is the user's to
  confirm, implemented as *a value the agent relays and the user answers*.

From **ADR-0005** (*reusable packages behind thin provider subsystems*):
*"Behaviour lives in reusable root packages. Subsystems are the addressable form
of a package, and nothing else. … A provider subsystem is the form of a reusable
package, and a package is the substance. Neither is allowed to grow a second copy
of the other."*

### 4.2 Three coherent placements

#### Option A — provider-neutral contract, grown additively *(recommended)*

```
pkg/knowledge/                     provider-neutral model + engine interfaces
pkg/knowledge/hindsight/           Hindsight HTTP client implementing them
subsystems/knowledge/              reference in-memory provider (unchanged 3 RPCs)
subsystems/knowledgehindsight/     mounts the client behind toolbox.knowledge.v1
```

- `toolbox.knowledge.v1` gains new services alongside the existing one
  (`MemoryBankService`, `MemoryService`, `DocumentService`,
  `KnowledgeBaseService`, `MentalModelService`, `DirectiveService`,
  `OperationService`, `TemplateService`, `ObservationService`,
  `EntityService`, `IngestService`).
- The existing `KnowledgeService` keeps its three RPCs and its in-memory
  reference provider. New RPCs are additive; nothing breaks.
- Vocabulary is provider-neutral. "Bank" becomes a first-class provider-neutral
  concept (a *knowledge base*, which is what the subsystem has always been
  called). "Observation", "mental model", "knowledge page", "directive",
  "operation", "tag" are all neutral already.
- A second backend later is a second provider, which is what rule 5 wants.

Costs: the contract grows large. Requires disciplined wording so it does not
become Hindsight-shaped by another name. Some Hindsight features either need
neutral modelling or must be declared out of scope (§7).

#### Option B — provider-owned contract, 1:1 mirror

`subsystems/knowledgehindsight` owns `toolbox.knowledgehindsight.v1` mirroring
the 77 endpoints.

- **For**: fastest; no neutrality argument to win; nothing is lost or softened.
- **Against**: every operation is Hindsight-shaped, so a second backend means a
  second contract, which is the outcome rule 5 exists to prevent. A future
  `pkg/sqliteknowledge` would expose a different vocabulary for the same
  concepts and an agent would have to know which it is talking to. The provider
  becomes a second framework rather than a provider of one.
- Note that a provider subsystem *may* own a provider-shaped contract — rule 8
  forbids provider types in **core** contracts, not in a provider's own. So
  Option B does not break a rule outright. It breaks the *direction* the rules
  are pointing.

Worth stating plainly because it is a real fallback: if the goal is "get
Hindsight into the deployment this week", B is legitimate and A is a
refactor-with-a-view. It is recorded here as a rejected option, not a forbidden
one.

#### Option C — register Hindsight's OpenAPI, write no Go *(the free option)*

Hindsight publishes an OpenAPI 3.1 document, and this repository already has
`pkg/openapi` (parse, render, serve, invoke) plus `subsystems/apiopenapi` to
mount it plus `subsystems/apitools` to turn a catalog into tools. So Hindsight can
be registered as a catalogued external API with **no new subsystem at all**.

- **For**: zero code, complete fidelity, 77 operations available immediately,
  and it is precisely what the provider machinery exists for.
- **Against**:
  - No Toolbox contract, so no `docs_embed.go`, no `@toolbox.side-effects`
    classification, and the generated CLI/MCP tool names come from OpenAPI
    `operationId`s rather than from a contract a person wrote.
  - Error classification is HTTP-status-based. Rule 12's `api.ErrorKind`
    vocabulary is not involved, so a caller's mistake and a misconfigured
    deployment are harder to tell apart at the boundary.
  - Tool surface is 77 operations wide, gated only by policy. That is a real
    exposure-footprint decision, not a technicality.
  - **No home for preload.** A preload operation is not a Hindsight endpoint —
    it is "walk a directory, diff against `content_hash`, retain, prune". That
    has to be Toolbox code.
  - Every Hindsight quirk (five tag-match modes, `min_scores` semantics,
    `all_strict` on tagged pages) becomes an agent-visible parameter with no
    contract comment explaining when it is right.

**Verdict: C is a legitimate first step** and is probably the fastest way to
evaluate Hindsight against real workloads, provided the preload gap is
acknowledged as *not* solved by it. If Hindsight is adopted, A is the destination
and C can be retired or kept as a second catalogued API alongside.

### 4.3 Compliance audit of Option A against the rules

| Rule | How Option A satisfies it |
|---|---|
| 1 subsystem independence | `subsystems/knowledgehindsight/` gets its own `proto/`, `go.mod`, `Makefile`, `cmd/`, `docs_embed.go`, and passes `GOWORK=off make -C … test` |
| 2 own contract | it serves the *existing* `toolbox.knowledge.v1`; it adds none of its own |
| 3 no in-process import | Hindsight is a separate process with its own database. The provider reaches it over HTTP. The *reference* provider and the *Hindsight* provider never import each other |
| 4 behaviour in a root package | the engine model and the Hindsight client are `pkg/knowledge/**`; the subsystem converts messages and nothing more, exactly like `subsystems/skillgit` over `pkg/skills` |
| 5 registry resolves, providers by identity | `Providers(endpoint)` exports one `api.Provider{ID: "knowledgehindsight", Role: "knowledge"}`; a host that later runs two backends distinguishes them by ID |
| 6 typed clients | internal Hindsight calls are hand-written typed methods; the Toolbox contract is generated and typed for callers |
| 7 policy grants, reflection describes | `@toolbox.side-effects` on every RPC; `pkg/mcp` decides exposure, never the annotation alone |
| 8 no provider types in core contracts | the contract says "knowledge base", "passage", "observation" — never "bank_id from Hindsight", never "TEMPR", never `min_scores` |
| 9 MCP is a policy boundary | unchanged; the Hindsight provider's operations are discovered and gated like any other |
| 10 MCP commands generated | `pkg/cliapp` gives the new command its `mcp` subcommand for free |
| 11 one translation, one implementation | the contract↔engine conversion lives in `pkg/knowledge`; the MCP gateway and the CLI both read the same contract. A test asserts they agree |
| 12 classified failures | the client returns `api.Errorf(api.KindUnavailable, …)` for a Hindsight 5xx, `KindInvalid` for a 4xx, and the transport maps kinds to codes in one place |
| 17 config changes confirmed | §5.3's template import and ingest are proposed-then-confirmed, in the same shape `AddSkill` already uses |

---

## 5. The preload problem, investigated

This is the question the whole investigation turns on, so it gets the most
space.

### 5.1 What `hindsight fs mount` actually does

From the Hindsight knowledge-pages documentation:

> `hindsight fs mount --bank my-bank` keeps a local folder in sync with this
> bundle via a background refresh loop, so `ls`, `grep`, `rg`, and your editor
> work against real files.

The bundle comes from `GET /v1/{tenant}/banks/{bank}/knowledge-base/export` and
is a **flat** set of files: a nested `index.md`, one `<page-id>.md` per page, and
a `<page-id>.log.md` refresh history for pages that have been rebuilt.

The direction is unambiguous. It is:

```
        bank memory  ──export──▶  markdown bundle  ──mount──▶  local folder
                        (one way)                              (kept current)
```

And the documentation is emphatic about why:

> A knowledge page is a **projected view** over processed memory, the way a
> database view is not a table. Before a page is written, Hindsight has already
> done the work files can't do for themselves: extracted facts … deduplicated
> them; and reconciled their contradictions through consolidation. … **Your raw
> documents remain the source of truth about *what was said*. The pages are the
> reconciled truth about *what holds*.**

> Left alone, a hand-maintained wiki becomes a beautifully formatted lie — not
> because anyone lied, but because keeping it true is a chore, and chores lose.

So the inverse operation, whatever it is, has to preserve that division rather
than collapse it. That is the constraint, and it is a good one: it is the reason
Hindsight is better than a folder of markdown.

### 5.2 Why "create a page with the body I typed" cannot work

Checked directly against the spec, because it is the first thing anyone would try:

| Where a body could go | What the spec has |
|---|---|
| `CreatePageRequest` | `name`, `source_query` (both **required**), `parent_id`, `tags`, `max_tokens`, `trigger` — **no body, no content** |
| `UpdateNodeRequest` | `name`, `parent_id`, `source_query`, `tags`, `max_tokens`, `trigger` — **no body, no content** |
| `CreateMentalModelRequest` | `id`, `name`, `source_query` (required), `tags`, `max_tokens`, `trigger` — **no content** |
| `UpdateMentalModelRequest` | `name`, `source_query`, `max_tokens`, `tags`, `trigger` — **no content** |

A mechanical sweep confirms it: **no schema anywhere in the OpenAPI document has
a writable field that sets a mental model's or a page's body.**

Worse, the default trigger for a page is actively hostile to authored text:

```json
{ "mode": "delta", "fact_types": ["observation"],
  "exclude_mental_models": true, "refresh_after_consolidation": true }
```

`refresh_after_consolidation: true` means the page rewrites itself every time
consolidation produces anything in its scope. `mode: delta` *edits* the existing
document rather than regenerating it — the documentation says this is so that
"hand-tuned structure and wording survive" — but survival is preservation
*across a rewrite*, not authorship. The first build, with no prior document to
edit, is a full generation from `source_query`. Whatever you typed is gone
before the first refresh even fires, because there is nowhere to put it.

**Conclusion: a human-authored document is not a knowledge page in Hindsight.
They are different objects, and the design has to keep them different.**

### 5.3 The four places authored material can legitimately land

#### (1) Retain as a document — the right default for prose

`POST /v1/{tenant}/banks/{bank}/memories` with a `MemoryItem`:

| Field | Relevance to preloading |
|---|---|
| `content` | string **or** an ordered list of `text`/`image`/`file` blocks, so an attachment sits inline where it appears |
| `document_id` | items sharing one are grouped into one document; re-retaining replaces it (`update_mode: replace`, the default) or appends (`append`) |
| `metadata` | `map[string]string`, free-form |
| `document_metadata` | free-form object (`additionalProperties: true`) — the place for the file path, git commit, and digest |
| `tags` | visibility scope, filtered at recall |
| `timestamp` | ISO 8601, or **`"unset"`** — documented explicitly for *"timeless content such as fictional documents or static reference material"*, which is exactly what an ADR or a spec is |
| `entities` + `resolve_entities` | `false` takes names literally: an existing entity is reused only on a case-insensitive name match. **This is the documented setting for hand-authored corrections** |
| `observation_scopes` | `per_tag` / `combined` (default) / `all_combinations` / `shared` |
| `operation_id` | client-supplied idempotency key for async retain; re-submitting returns the original operation, a mismatched reuse is `409` |

This is **exactly** what Hindsight's own Obsidian integration does, and the
integration's stated rule is the design:

> **Hindsight never becomes a second source of truth.** Sync is one-way
> (Obsidian → Hindsight), every answer cites the note it came from so you can
> **fix things at the source**, and chat conversations are **not** stored by
> default. Edit a note, and Hindsight reconverges on the next sync.

Its mechanics, which a Toolbox implementation should copy:

- every note is retained as a Hindsight **document**;
- **edits upsert, deletes remove**;
- **a content hash means unchanged notes are skipped** —
  `DocumentListItem.content_hash`, documented as *"Hash of the document text,
  for idempotent retain"*;
- auto-tagging on ingest with vault, folder (and sub-folders), and
  created/updated dates, so scope is a tag filter rather than a folder path
  the API has to understand;
- the sync index lives *outside* the synced tree
  (`~/.hindsight/obsidian/<vault>.json`), and **each ingester prunes only what
  its own index tracks** — so two ingesters cannot delete each other's
  documents.

**Costs, stated honestly.** LLM extraction is lossy and non-deterministic. The
authored prose is not retrievable verbatim; what comes back is facts, in the
authored document's own words but not its whole content. And the bank config
`store_document_text: false` discards the raw text entirely — so a deployment
that wants the authored words back byte-for-byte must either leave that flag on
or serve the verbatim text from the files rather than from Hindsight. This is a
real trade-off, and it is the strongest argument for keeping the authored corpus
on disk as the source of truth, which is what the design below does.

#### (2) Bank template import — the right place for authored *configuration*

`POST /v1/{tenant}/banks/{bank}/import`, with `?dry_run=true` for validation.
The manifest is versioned (`"version": "1"`), described by a published JSON
Schema at `GET /v1/bank-template-schema`, and round-trips through
`GET /banks/{bank}/export`.

```jsonc
{
  "version": "1",
  "bank":   { /* ~60 config fields, see below */ },
  "mental_models": [ { "id": "...", "name": "...", "source_query": "...",
                       "tags": [], "max_tokens": 2048, "trigger": {} } ],
  "directives":   [ { "name": "...", "content": "...",
                       "priority": 0, "is_active": true, "tags": [] } ]
}
```

All three sections are optional; omit one to leave that part unchanged. Mental
models match by `id`, directives by `name`, so re-import is an update rather than
a duplicate. The response separates `*_created` from `*_updated` and returns
`operation_ids` for the async content generation. Forward compatibility is
handled: older manifests upgrade automatically, newer-than-server is rejected
with a message suggesting an upgrade.

`bank` carries, among ~60 fields: `reflect_mission`, `retain_mission`,
`retain_extraction_mode` (`concise`/`verbose`/`custom`/`verbatim`/`chunks`),
`retain_custom_instructions`, `retain_chunk_size`, `enable_observations`,
`observations_mission`, `enable_text_search` / `enable_temporal_retrieval` /
`enable_graph_retrieval` / `enable_reranking`, `disposition_{skepticism,
literalism,empathy}` (1–5), `entity_labels` (controlled vocabulary),
`entities_allow_free_form`, `retain_default_strategy` + `retain_strategies`,
`consolidation_strategies` (per-scope missions and limits),
`knowledge_page_default_trigger`, `recall_*` and `mcp_enabled_tools`,
`store_document_text`, `enable_auto_consolidation`, `memory_defense`.

**This is the artifact that belongs in a repository.** It is hand-authored, it
is stored verbatim, nothing rewrites it, it is diffable, it has a schema, it has
a dry run, and it round-trips. It is also, notably, what the Bank Templates Hub
(`/templates`) distributes — *"Sharing — distribute recommended setups as
portable JSON files"*.

Note what it does **not** carry: document *content*. A template configures a
bank and defines what its mental models will be asked; it does not preload prose
into memories.

#### (3) A mental model with no trigger — generated once, then frozen

`MentalModelTrigger-Input` defaults to `mode: full`,
`refresh_after_consolidation: false`, and no cron. A model created with no
trigger is therefore generated once and never automatically rewritten.

This gets close to "an authored document that stays put", and `mode: delta`
preserves unchanged sections byte-for-byte, which is the mechanism designed for
hand-tuned wording. But the *content* is still LLM output from `source_query`.
You can write a `source_query` like "Reproduce this document verbatim, preserving
its section structure" and get close. You do not get your bytes. This is a
workaround, not a mechanism, and it costs an LLM call to produce a document you
already had.

#### (4) `KnowledgeNode.managed` — reserved, and a trap

The spec's own description: **"Client-set flag: true = system-owned, false =
hand-authored."** Default `false`.

Verified by sweeping the whole OpenAPI document:

- the string `managed` occurs **once** as a property name, in `KnowledgeNode`;
- `KnowledgeNode` is a **response** schema (it appears in `KnowledgeTreeResponse`
  and in the export bundle's schema description);
- **no request schema anywhere contains it** — not `CreatePageRequest`, not
  `UpdateNodeRequest`, not any mental-model or document schema;
- the other two textual occurrences are both in the `KnowledgePageBundleResponse`
  description, restating the same sentence.

And nothing in the documentation says a refresh skips a `managed: false` page.
Since `false` is the default, the flag as it stands cannot distinguish anything
from the default case. **Do not design against it.** It reads like the escape
hatch this problem needs, which makes it the most dangerous thing in the API to
build on.

### 5.4 The design that follows

Preload is **two distinct operations**, because Hindsight has two distinct kinds
of authored material, and merging them is the mistake:

#### (A) Corpus ingest — authored prose → documents. A *reconcile*, not a write.

Proposed shape, in the provider-neutral vocabulary of §6:

```proto
// IngestService — reconciles a directory of authored documents into a
// knowledge base.
//
// It is a reconcile and not a write, because the authored files are the source
// of truth about what was said and the bank's derived knowledge is the source
// of truth about what holds. A write would make the second one authoritative
// over the first, and every edit would then be a migration.
service IngestService {
  // @toolbox.side-effects create update delete
  // PlanIngest reports what reconciling a corpus would do, and changes nothing.
  rpc PlanIngest(PlanIngestRequest) returns (PlanIngestResponse);

  // @toolbox.side-effects create update delete
  // ApplyIngest reconciles a corpus. It reports what it did and refuses to
  // delete a document it did not write.
  rpc ApplyIngest(ApplyIngestRequest) returns (ApplyIngestResponse);
}
```

The mechanics, each of which is a decision rather than a detail:

- **Document id is a pure function of (base, relative path).** Then an edit is
  an upsert, a rename is a delete plus a create, and neither requires state
  outside the bank. Hindsight's own `document_id` is a free string
  (`meeting-2024-03-15`, and the Obsidian plugin uses `--prefix-doc-id`), so a
  stable scheme is ours to choose.
- **Diff on `content_hash`,** not on timestamps. `DocumentListItem.content_hash`
  is documented for exactly this: *"Hash of the document text, for idempotent
  retain."* An unchanged file costs zero LLM calls.
- **Ownership marker in tags or `document_metadata`.** Prune only documents whose
  recorded path lies under the reconciled root *and* carries this ingester's
  marker. Copy the Obsidian plugin's rule verbatim: each ingester prunes only
  what its own index tracks, so two ingesters over one bank cannot delete each
  other's work. Without this, `ApplyIngest` on a second directory silently
  destroys the first.
- **Frontmatter → tags and metadata.** The Obsidian plugin auto-tags with
  `vault:`, `folder:` and `created:`/`updated:` and carries the user's own
  frontmatter `tags`/`aliases` through. A Toolbox equivalent should do the same:
  tags are what recall filters on, so a path convention that becomes tags gives
  scope without teaching the API about directories.
- **`resolve_entities: false` for authored text,** following Hindsight's own
  documented guidance: *"Use False for hand-authored corrections, where the name
  you sent is the answer rather than a guess."*
- **`timestamp: "unset"` for reference material** that has no event time —
  specs, ADRs, glossaries — following the documented `"unset"` support.
- **Plan, then confirm, then apply** — `PlanIngest` returns
  `created[] / updated[] / unchanged[] / deleted[]` with digests, and
  `ApplyIngest` only runs what the user approved. This is AGENTS.md rule 17
  applied to a corpus: *"a framework operation that would write a project's
  configuration asks first, and its answer names the file and the change rather
  than making it quietly."* The same shape `subsystems/skill`'s `AddSkill`
  already uses, and it is a *value the agent relays*, so it works over any
  transport — which rule 18 requires for any rule that must reach a user.
- **`files/retain` for binaries.** `POST /banks/{bank}/files/retain` is
  multipart, takes real files, and returns `operation_ids`. A PDF or a
  screenshot cannot go through a text `content` field. The provider should use
  this path when the bytes are not text, and `retain` with a `text` block when
  they are.
- **Async throughout.** Ingest is `async: true` with a client-supplied
  `operation_id` per batch, so a lost acknowledgement is retried without
  duplicating work — that is precisely what Hindsight's `operation_id` is
  documented for. A reconcile over 500 files is 500 LLM extractions; it must not
  be a synchronous unary RPC.

#### (B) Configuration preload — authored manifest → bank definition. A *template*.

```proto
// TemplateService — a bank template is a versioned, hand-authored manifest
// that configures a bank and defines its directives and mental models.
//
// It is the only authored knowledge Hindsight stores verbatim and never
// rewrites, which is why it is separate from corpus ingest rather than another
// field on it: prose becomes facts and gets reconciled, configuration stays as
// written.
service TemplateService {
  // @toolbox.side-effects read_only
  // GetTemplateSchema returns the manifest's JSON Schema.
  rpc GetTemplateSchema(GetTemplateSchemaRequest) returns (GetTemplateSchemaResponse);

  // @toolbox.side-effects read_only
  // ExportTemplate renders a bank as a manifest of its explicit overrides.
  rpc ExportTemplate(ExportTemplateRequest) returns (ExportTemplateResponse);

  // @toolbox.side-effects create update
  // ImportTemplate applies a manifest. With dry_run it reports what it would do.
  rpc ImportTemplate(ImportTemplateRequest) returns (ImportTemplateResponse);
}
```

`ImportTemplate` is proposed-then-confirmed for the same rule-17 reason, and
`dry_run` is a first-class field rather than a query parameter so the
confirmation cannot be reordered out from under the write. Manifest
`version: "1"` maps onto Toolbox's own `pkg/api` API-version discipline.

### 5.5 What this means for `fs mount`

With ingest in place, the two directions compose into a loop, and the
composition is the useful part:

```
   authored files ──ApplyIngest──▶ bank memories ──▶ observations ──▶ pages
        ▲                                                                   │
        └──────────────── hindsight fs mount / export bundle ◀───────────────┘
```

A person edits a file; the ingest reconcile upserts the document; consolidation
extracts and reconciles facts; the pages refresh incrementally; the mounted
folder on someone's disk shows the reconciled result. The person fixes
contradictions **at the source**, which is the entire argument Hindsight makes
for existing. And because the pages are a projection, deleting one loses nothing
— it re-projects.

The `export` bundle deserves a Toolbox RPC too (`ExportKnowledgeBase`, returning
`repeated BundleFile{path, content, media_type}`), because it is the portable
form of the whole knowledge base and it is the thing a deployment would commit
or hand to a colleague. Note the bundle is **flat** (`index.md` +
  `<page-id>.md` + `<page-id>.log.md`), not the nested folder tree — the nesting
lives inside `index.md`. That is worth stating in the contract, because
"export" reads like it preserves the tree and it does not.

---

## 6. RPC interface design

### 6.1 Vocabulary mapping

The left column is what the contract says. The right is what Hindsight calls it,
and it never appears in the contract.

| Provider-neutral (contract) | Hindsight | Note |
|---|---|---|
| knowledge base | memory bank (`bank_id`) | the tenant/isolation unit; "base" is what this subsystem has always been called |
| base alias | bank alias | so a deployment can address a base by a friendly name |
| document | document | the container for retained content; id is caller-chosen |
| chunk | chunk | the text segment a document was split into |
| fact | memory unit (`world`/`experience`) | |
| fact type | `fact_type` | enum, not a string (rule: enums for fixed option sets) |
| observation | observation | consolidated, evidence-grounded belief |
| mental model | mental model | a synthesized document answering a query |
| knowledge page | knowledge page | a mental model configured as a document; the contract may collapse the two or keep both — see §7 |
| directive | directive | hand-authored rule, never rewritten |
| ingest | retain with a `document_id` | |
| reconcile | *(no Hindsight equivalent — ours)* | §5.4(A) |
| tag scope | `tags` + `tags_match` + `tag_groups` | |
| budget | `budget` (`low`/`mid`/`high`) | a token/scale enum, not a magic int |
| operation | async operation | `operation_id`, status, progress, retry |
| base template | bank template | the versioned manifest |

### 6.2 Proposed services

Grouped by concern, mirroring Hindsight's own API groups but named neutrally.
Every RPC carries `@toolbox.side-effects`; side effects are the whole basis for
MCP exposure and policy, so an unannotated RPC is unusable.

| Service | RPCs | Side-effect profile |
|---|---|---|
| `KnowledgeBaseService` | `ListBases`, `GetBase`, `CreateBase`, `UpdateBase`, `DeleteBase`, `GetBaseConfig`, `UpdateBaseConfig`, `ResetBaseConfig`, `GetBaseStats`, `ListBaseAliases`, `AddBaseAlias`, `SetPrimaryBaseAlias`, `RemoveBaseAlias`, `ClearObservations` | mostly `create update`; `List*`/`Get*`/`Export*` `read_only`; `DeleteBase`/`ClearObservations` `delete` |
| `MemoryService` | `Retain`, `Recall`, `Reflect`, `ListMemories`, `GetMemory`, `CurateMemory`, `GetMemoryHistory`, `GetMemoryGraph`, `ListTags`, `PreviewExtraction`, `PreviewPrompts` | `Retain`/`CurateMemory` `create update`; the rest `read_only` |
| `DocumentService` | `ListDocuments`, `GetDocument`, `UpdateDocument`, `DeleteDocument`, `ListDocumentChunks`, `GetChunk`, `ReprocessDocument` | `read_only` except `UpdateDocument` (`update`), `DeleteDocument` (`delete`), `ReprocessDocument` (`update`) |
| `KnowledgePageService` | `GetPageTree`, `CreatePageFolder`, `CreatePage`, `GetPage`, `SearchPages`, `UpdatePageNode`, `DeletePageNode`, `ExportPageBundle` | `read_only` for the four getters; `create update` for create/update; `delete` for delete |
| `MentalModelService` | `ListMentalModels`, `CreateMentalModel`, `GetMentalModel`, `UpdateMentalModel`, `DeleteMentalModel`, `GetMentalModelHistory`, `RefreshMentalModel`, `PreviewMentalModelRefresh`, `ClearMentalModel` | 4 `read_only`, 3 `create update`, `Delete` `delete`, refreshes `update` (they change the document) |
| `DirectiveService` | `ListDirectives`, `CreateDirective`, `GetDirective`, `UpdateDirective`, `DeleteDirective` | standard CRUD |
| `OperationService` | `ListOperations`, `GetOperation`, `CancelOperation`, `RetryOperation`, `DeleteOperation` | 2 `read_only`, `Cancel`/`Delete` `delete`, `Retry` `create` |
| `ObservationService` | `ListObservationScopes`, `ClearObservationsForFact`, `TriggerConsolidation`, `RecoverConsolidation`, `PreviewConsolidationStrategies` | 2 `read_only`, rest `create update delete` |
| `EntityService` | `ListEntities`, `GetEntity`, `GetEntityGraph` | all `read_only` |
| `TemplateService` | `GetTemplateSchema`, `ExportTemplate`, `ImportTemplate` | see §5.4(B) |
| `IngestService` | `PlanIngest`, `ApplyIngest` | `create update delete` — and it is the *only* `delete`-classed operation in the whole contract that a policy will plausibly want to allow during a normal agent session, which is exactly why it needs the confirm field |

Roughly 70 RPCs, plus the three the existing `KnowledgeService` already has.
That is large, and it should be said plainly: **it is a large contract**. Three
things keep it defensible.

1. It is *additive*. `KnowledgeService` keeps its three RPCs and its reference
   provider; nothing breaks, and the new services are opt-in per deployment.
2. Every RPC is a real capability with a real caller. Hindsight's own MCP server
   exposes 27 of them to an agent with no complaint, which is decent evidence
   that this is a usable feature surface rather than an API-shaped accident; the
   remainder is management surface an operator uses, not an agent.
3. `pkg/cliapp` generates the CLI and `pkg/mcp` generates the tools from the same
   contract, so the size costs nothing in hand-written code.

### 6.3 Message-shape notes

Following the repository's own rules, and the places where Hindsight's shape
would otherwise be copied verbatim:

- **Enums for fixed option sets.** `FactType{world, experience, observation}`,
  `TagMatchMode{any, all, any_strict, all_strict, exact}`,
  `ExtractionMode{concise, verbose, custom, verbatim, chunks}`,
  `Budget{low, mid, high}`, `RefreshMode{full, delta}`,
  `ObservationScopeMode{per_tag, combined, all_combinations, shared}`,
  `UpdateMode{replace, append}`. Hindsight itself uses enums for
  `observation_scopes` and `update_mode` and bare strings for `tags_match` and
  `budget`; the contract should not inherit the inconsistency.
- **`repeated`, never comma-separated.** `tag_groups` is a recursive boolean
  expression (`leaf / and / or / not`, each leaf with its own `match` and an
  optional fuzzy `resolve`). Model it as a recursive message with a `oneof`, not
  as a JSON blob and not as a string.
- **Sub-messages for grouping.** `RecallRequest` is genuinely a union of a dozen
  independent knobs; grouping them (`Scope{ tags, match, groups }`,
  `Window{ start, end }`, `ScoreFloors{ semantic, keyword, reranker, final }`)
  keeps the generated CLI and the JSON Schema legible.
- **`optional` for "absent means unchanged".** Hindsight uses three-valued
  semantics throughout — "omitted means leave unchanged, `''` means clear". Proto3
  `optional` expresses exactly that and a bare field does not. Getting this wrong
  on a `PATCH` is how a `UpdateDocument` silently clears a field.
- **`int32`/`int64` for numbers, `google.protobuf.Timestamp` for times.** Hindsight
  returns ISO-8601 strings; the conversion belongs in the provider, and the
  contract's field comments should say the input is ISO-8601 and the output is
  not.
- **Paging is `page_size` + `page_token` + `next_page_token`,** not
  `limit`/`offset` — matching `subsystems/skill`'s already-established
  convention rather than Hindsight's offsets.
- **Streaming: none.** Every Hindsight operation is unary. `pkg/core` has no
  streaming client and `docs/status.md` says so; introducing a streaming RPC
  here would be the only streaming contract in the tree.

### 6.4 Error mapping

Per rule 12, the client classifies and the transport maps. Hindsight returns
FastAPI-shaped errors (`422` with a `detail` array) and `404` for a missing
bank.

| Hindsight response | `api.ErrorKind` | ConnectRPC code |
|---|---|---|
| `400` / `422` validation | `KindInvalid` | `InvalidArgument` |
| `404` on a base, document, page, operation | `KindNotFound` | `NotFound` |
| `409` (duplicate page name in folder, `operation_id` reuse) | `KindAlreadyExists` | `AlreadyExists` |
| `404` on the *attachment* endpoint, which deliberately does not distinguish missing from invisible | `KindNotFound` | `NotFound` — and the contract comment must not imply existence |
| `5xx`, connection refused, timeout | `KindUnavailable` | `Unavailable` |
| a capability the deployment's `/version` reports as disabled | `KindUnsupported` | `Unimplemented` |
| anything else | `KindInternal` | `Internal` |

Two details worth carrying into the contract comments:

- **The attachment endpoint is a deliberate anti-probe.** Hindsight documents
  that *"a missing attachment and an invisible bank both return `404`, so the
  endpoint cannot be used to probe what a bank holds."* A provider must not turn
  that into a distinguishable error, or it has reintroduced the probe.
- **`/version` and its `features` flags should gate capability**, not version
  strings. `store_document_text`, `observations`, `document_import_api`,
  `file_upload_api`, `bank_config_api` and `mcp` are all optional. A provider
  that calls an endpoint a deployment has disabled should return
  `KindUnsupported` naming the flag, which is a far better error than a `404`
  from a route that was never mounted.

### 6.5 Provider declaration

Following the `skillgit` precedent, one record — not one per base:

```go
// Providers describes this subsystem to a host's provider directory.
//
// It contributes one record, not one per base, because a deployment's bases are
// runtime state and this record is what a host reads before anything is running.
func Providers(endpoint string) []api.Provider {
    return []api.Provider{{
        ID: Name, Subsystem: Name, Role: ProviderRole, // "knowledgebase"
        Endpoint: endpoint,
        ServiceNames: []string{ /* the mounted services */ },
        Status: api.ServerStatusServing, ImplementationVersion: Version,
    }}
}
```

A host running both the reference provider and the Hindsight provider
distinguishes them by `ID` (`knowledge`, `knowledgehindsight`) — which is
precisely what rule 5 asks for, since two providers serving one contract is the
normal case, not an accident.

---

## 7. MCP exposure and policy

Hindsight's 27-tool MCP surface is a useful check on what should be an *agent*
tool rather than an *operator* tool, because Hindsight has already drawn that
line for the same feature set.

- **Exposed by default to an agent:** `Retain`, `Recall`, `Reflect`,
  `ListMemories`, `GetMemory`, `GetPageTree`, `SearchPages`, `GetPage`,
  `ListDocuments`, `GetDocument`, `ListTags`, `ListOperations`, `GetOperation`,
  `ListMentalModels`, `GetMentalModel`, `ListDirectives`, `IngestService`.
- **Operator-only, even if a policy allows it:** `DeleteBase`,
  `ClearObservations`, `DeleteDocument`, `DeletePageNode`, `DeleteMentalModel`,
  `DeleteDirective`, `ImportTemplate`, `ApplyIngest`, `CloneBank`,
  `RecoverConsolidation`, webhook and audit administration.

Two rules from `AGENTS.md` bear directly on this:

- **Rule 7 / 9** — an operation is exposed because a *policy* permits it, never
  because reflection found it and never because the contract annotated it.
  Registering this provider must not expose anything by itself.
- **Rule 18** — `ApplyIngest` and `ImportTemplate` are operations that must reach
  a human. The stateless HTTP MCP endpoint cannot elicit (rule 18's own
  conclusion), so the confirm must be **a value the agent relays and the user
  answers** — exactly the `bool confirm` field `subsystems/skill`'s `AddSkill`
  already uses, chosen over a two-call handshake specifically so the two cannot
  be reordered. `PlanIngest` returning a digest-carrying plan, which the user
  reads, which the agent relays back as `confirm`, is that mechanism.

One further thought worth recording: `Recall` and `Reflect` are the two
operations whose *output shape* matters most to an agent and whose parameters
are most likely to be set wrongly (`tags_match: all_strict` on a tagged page
generates an empty page; `min_scores.reranker` is uncalibrated across queries).
Those two deserve contract comments that state the failure mode, because the
generated MCP tool description is the comment, and an agent reading it is the
only audience that will act on it.

---

## 8. Deliberately out of scope

Stated so that leaving them out is a decision rather than an oversight.

- **Knowledge pages versus mental models as separate contract concepts.** They
  are the same object with different defaults. Exposing both costs a service
  and a concept; collapsing them costs fidelity to a real difference. *Open
  question.*
- **`managed` / hand-authored pages.** Reserved upstream, unwritable today
  (§5.3(4)). If it ever becomes writable it changes the preload story
  materially, and that should be re-investigated then rather than designed for
  now.
- **Webhooks, audit logs, LLM traces, Prometheus metrics, bank transfer,
  cloning, entity regeneration.** Operator and infrastructure surface. They can
  be reached through the raw API in the meantime.
- **Memory defense** (`memory_defense` in the bank config) — a real and
  interesting feature with its own policy implications, but it deserves its own
  investigation.
- **Writing prose into a knowledge page.** Shown in §5.2 to be impossible today
  and, more importantly, to be the wrong shape.
- **A streaming RPC.** Nothing to stream.

---

## 9. Open questions

These are the decisions I could not make from the code and the spec alone.

1. **Provider-neutral contract, or provider-owned mirror?** Option A is what the
   architecture points at; Option B is faster. Worth deciding explicitly rather
   than by drift.
2. **Is the reference in-memory `knowledge` provider kept?** Keeping it means two
   implementations of a 70-RPC contract, and the second one is currently a
   substring search over a map. A narrower contract that both can honestly
   implement might be better than a wide one only one can.
3. **Knowledge pages: separate service, or one `DocumentService` covering mental
   models and pages?** The two differ only in trigger defaults, and the
   distinction is invisible in the API.
4. **Is ingest in scope for a knowledge provider at all**, or is it a
   `knowledgefiles` sibling subsystem that serves the corpus contract and calls
   the Hindsight provider over ConnectRPC? The second obeys rule 3 more
   literally and makes the reconcile testable without a bank; the first is one
   fewer module.
5. **Does the authored corpus stay in the bank or on disk?** If Hindsight is the
   only copy, `store_document_text` must stay `true` and lossy extraction is
   accepted. If the files stay authoritative (which §5.4 assumes), the
   deployment is running a cache and should be told so.
6. **What is a "base" in Toolbox's vocabulary?** `knowledge base` reads well, but
   `docs/` already uses "knowledge base" for the *concept*. A base *instance* may
   need a distinct word.
7. **Tag namespace convention.** Hindsight's own integrations use `vault:`,
   `folder:`, `user:`, `session:`, `type:`. Toolbox should pick a reserved prefix
   for framework-assigned tags so a corpus ingest cannot collide with
   hand-authored ones.
8. **Is `Reflect` a knowledge operation or an agent operation?** It runs an LLM
   agent loop. The `subsystems/agent` boundary is a real question, and getting
   it wrong puts a second LLM-calling path in the framework.

---

## 10. What implementing this would touch

For sizing only — **nothing below has been done.** One line has since gone the
other way: `subsystems/knowledge/proto/knowledge.proto` and
`subsystems/knowledge/knowledge.go` are no longer "changed", because the module
they would have been changed in was deleted. The new contract is a new module,
not a grown one — which is the decision
[`knowledge.md`](../knowledge.md) §6.1 records and this document got wrong.

**New**

- `pkg/knowledge/` — provider-neutral model, engine interfaces, and the
  contract↔engine conversion (this is where the behaviour lives, per ADR-0005).
- `pkg/knowledge/hindsight/` — hand-written HTTP/JSON client for the ~40
  endpoints used, returning `api.Errorf`-classified errors.
- `subsystems/knowledgehindsight/` — `proto/knowledgehindsight.proto`,
  `knowledgehindsight.go`, `docs_embed.go`, `Makefile`, `go.mod`, `*_test.go`,
  `cmd/knowledgehindsight/main.go`.
- A row in `.github/workflows/ci.yml`'s matrix. *This is not optional and
  nothing fails if it is forgotten* — the matrix is hand-written, and a new
  subsystem that is not in it is silently never tested on its own.
  `make check-repo` reports a matrix row naming no module; it does not report a
  module absent from the matrix.
- An ADR, once 1–4 above are decided.

**Changed**

- ~~`subsystems/knowledge/proto/knowledge.proto` — new services, additively. The
  existing `KnowledgeService` is untouched.~~ **Not what happened.** The module
  was deleted rather than grown, so there is nothing to be additive about. Open
  question 1 below ("provider-neutral contract, or provider-owned mirror?") is
  what this got wrong: it assumed the answer was "grow the existing one".
- `docs/subsystems.md` — the module count (already stale: it says sixteen, the
  tree has nineteen) and the new provider.
- `docs/status.md` — the "Knowledge search does not yet perform ingestion,
  embeddings, vector search, or reranking" limitation, at least for deployments
  that adopt the provider.
- `docs/architecture.md`, `README.md` — new public package `pkg/knowledge`.
- `docs/todos.md` — the P1 "Knowledge ingestion" section is partly delivered by
  this and should say which parts.
- `docs/decisions/` — a new ADR.
- `AGENTS.md` — nothing, unless a new non-negotiable rule falls out. Rule 8's
  existing text already covers the provider-neutrality requirement.

**Phasing**

1. **Evaluate with no new code.** Register Hindsight's `openapi.json` as a
   catalogued API (Option C), expose a small policy-allowed subset, and use it
   on real work. This answers "is Hindsight actually good for our workloads"
   in an afternoon, and it is worth doing before committing to that contract.
2. **Decide the contract** (open questions 1–4), write the ADR.
3. **`pkg/knowledge` + the Hindsight client**, with tests against the real
   OpenAPI document rather than a live server, so the suite stays deterministic
   and offline.
4. **The provider subsystem**, mounting the package.
5. **Ingest and template import last** (§5.4), because they are the parts that
   interact with a human and they are the parts most likely to change shape
   after the first real use.

---

## Appendix: sources

- `https://hindsight.vectorize.io/openapi.json` — API 0.10.1, 77 paths,
  195 schemas. The authoritative source for every structural claim above.
- `/` (overview), `/developer/knowledge-pages`, `/developer/api/knowledge-pages`,
  `/developer/api/bank-templates`, `/developer/api/documents`,
  `/developer/mcp-server`, `/sdks/go`, `/sdks/integrations/obsidian`.
- `https://arxiv.org/abs/2512.12818` — the paper.
- `https://github.com/vectorize-io/hindsight` — source.
- This repository at `25fc519`: `subsystems/knowledge/**`,
  `subsystems/skill/**`, `subsystems/skillgit/**`, `pkg/knowledge` (absent),
  `pkg/skills`, `pkg/api`, `pkg/mcp`, `pkg/cliapp`, `pkg/core`, `pkg/host`,
  `pkg/config`, `docs/architecture.md`, `docs/feature-spec.md`,
  `docs/subsystems.md`, `docs/status.md`, `docs/todos.md`,
  `docs/decisions/000{4,5,9}`, `AGENTS.md`, `.github/workflows/ci.yml`.
