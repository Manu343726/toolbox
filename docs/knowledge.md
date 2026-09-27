# Knowledge

**Status: specification. Nothing here is implemented.** This document specifies a
knowledge base subsystem that does not exist yet, replacing the placeholder that
was removed in the same change. Where it states a decision it is a decision this
document makes and a later change is expected to follow; where it states an open
question it is listed in §14 rather than answered.

The investigation that led here — the Hindsight API survey, the reasoning about
preloading, and the three placement options — is kept in
[`investigations/hindsight-knowledge-backend.md`](investigations/hindsight-knowledge-backend.md).
This document supersedes it where they differ.

---

## 1. What this subsystem is for

An assistant that forgets everything between sessions is limited to whatever
context the caller pastes in. The alternative is a **knowledge base**: a
governed body of knowledge an assistant can search, cite and reason over, and
which outlives any one conversation.

Three things make that hard, and this subsystem exists because of all three.

1. **Recall has to be selective.** A base holds more than fits in a context
   window, so retrieval has to find the relevant part and cite it. Substring
   matching does not do this; a reader looking for a specific term and a reader
   asking "why do we do it this way" need different searches.
2. **The corpus outlives the tool.** Reference material is written and reviewed
   by people, in files, over months. A base that only knows what an LLM
   extracted cannot show a person the sentence they wrote, and a base that
   rewrites its own prose cannot be reviewed at all.
3. **Derived knowledge is worth having.** What an assistant learns across
   sessions — a preference, a decision, a correction — is knowledge no file
   contains. Keeping it in the same base as the reference material is what makes
   the base *the* place an assistant looks.

The design that answers all three is the one in §4: a markdown wiki a person
owns is the source of truth about what was written, and a memory base is the
reconciled, retrievable, accumulating projection of it.

## 2. Terminology

Vocabulary is load-bearing here, because the backend and the file corpus are
different kinds of thing and the difference is the whole design.

| Term | Meaning |
|---|---|
| **knowledge base** | A named, isolated body of knowledge. The unit of separation between subjects, projects or tenants. |
| **base** | Short form. The contract's word, not the backend's. |
| **document** | A container for retained content and the unit of provenance. A fact traces to the document and chunk it came from. |
| **chunk** | The segment a document was split into before extraction. Holds the text that produced a fact. |
| **fact** | One extracted statement. A world fact (objective) or an experience fact (the base's own actions). |
| **observation** | A consolidated, deduplicated, evidence-bearing belief synthesised from facts. A base's settled view. |
| **mental model** | A synthesized document answering one question, rebuilt when its scope changes. |
| **page** | A mental model configured as a living document in a folder tree. The unit an agent browses. |
| **directive** | A hand-authored rule the reasoning step must follow. Never rewritten. |
| **wiki** | The directory of markdown a project owns. The authoritative corpus. |
| **ingest** | Reconciling a wiki into a base. |
| **reconcile** | An ingest that reports differences and applies them, rather than writing unconditionally. |
| **base template** | A versioned manifest configuring a base and defining its directives and mental models. |
| **operation** | A unit of asynchronous work the backend reports on: extraction, consolidation, a refresh. |

Terms deliberately **not** used, because they would import one implementation's
vocabulary into a provider-neutral contract: *bank* (a base is a base),
*observation scope*, *TEMPR*, *reranker*, *consolidation strategy*, *disposition
trait*, *entity*, *proof count*, *premise*, *dedup*.

## 3. Requirements

Numbered so a change can be traced to what it satisfied. "H" marks a capability
the Hindsight backend provides directly; "W" marks one the wiki integration adds
on top.

### Bases

| # | Requirement |
|---|---|
| B-1 | Named bases, isolated from one another: separate content, separate configuration, separate directives. (H) |
| B-2 | Create, inspect, update and delete a base. A delete is explicit and reports what it destroyed. (H) |
| B-3 | Aliases, so a deployment can address a base by a friendly name; one alias may be primary for display. (H) |
| B-4 | Per-base configuration, readable and writable: missions, disposition traits, retrieval arms, entity vocabulary, consolidation behaviour, token budgets, feature flags. (H) |
| B-5 | Configuration is **reset-able** to the server's defaults, distinct from being overwritten with something. (H) |
| B-6 | Statistics, and an ingestion time-series, so an operator can see a base is growing and when it last changed. (H) |
| B-7 | Clearing a base's derived knowledge is distinct from deleting the base. (H) |

### Ingestion

| # | Requirement |
|---|---|
| I-1 | Retain arbitrary content, synchronously or queued, with a caller-supplied idempotency key so a lost acknowledgement is retried without duplicating work. (H) |
| I-2 | Content is a string **or** an ordered list of blocks, so an image or attachment sits inline where it actually appears. (H) |
| I-3 | A document identifier groups items into one document; re-retaining replaces it, or appends to it. (H) |
| I-4 | Per-item provenance metadata, and a document-level metadata object. (H) |
| I-5 | Event time is settable, and **explicitly unsettable** for timeless reference material — a specification has no date. (H) |
| I-6 | Author-supplied entity names, with a mode that takes them literally rather than resolving them against existing entities. (H) |
| I-7 | Tags on every item, with five match modes and compound boolean expressions. (H) |
| I-8 | Binary files are ingested through a multipart path that reports an operation, not through a text field. (H) |
| I-9 | Previewing what extraction would produce, without storing it, with every prompt-affecting setting overridable for the call. (H) |
| I-10 | Previewing the prompts themselves, with no model call at all. (H) |
| I-11 | Bulk import and export of documents as an archive, asynchronously. (H) |

### Documents

| # | Requirement |
|---|---|
| D-1 | List documents with a total, filtered by identifier substring, tags, and a time window on a chosen time axis. (H) |
| D-2 | Read one document's metadata, its extracted-fact count per fact type, and its text where the deployment retains it. (H) |
| D-3 | A content digest per document, so a reconcile can skip what has not changed. (H) |
| D-4 | Re-tag a document without reprocessing its content, with the report that this invalidates and re-queues the observations it fed. (H) |
| D-5 | List and read chunks: the original text segments, their order, and whether they were truncated. (H) |
| D-6 | Reprocess a document on demand. (H) |
| D-7 | Delete a document and every fact extracted from it, reporting the count. (H) |

### Retrieval

| # | Requirement |
|---|---|
| R-1 | Search by meaning, not only by substring. (H) |
| R-2 | Exact-term search, so a product name, an error string, or an identifier finds what the user typed. (H) |
| R-3 | Relational search through the entity graph, so an indirect connection is findable. (H) |
| R-4 | Temporal search, both from dates in the query and from an explicit window supplied by the caller. (H) |
| R-5 | The four arms run together and their results fused, rather than the caller choosing one and losing the others. (H) |
| R-6 | A budget scale, not a token count, so a caller asks for "enough" and the deployment decides what that is. (H) |
| R-7 | Optional per-stage score floors, distinguished between the stages that rank and the stages that filter, so a caller can abstain deliberately. (H) |
| R-8 | A query-time anchor, so "what did we decide last month" is answerable relative to a stated moment. (H) |
| R-9 | Results carry provenance: the document, the chunk, the context, the entities, the timestamps, the per-stage scores, and the facts an observation was consolidated from. (H) |
| R-10 | Optionally return raw chunk text with the facts, for surrounding context. (H) |
| R-11 | Optional attachments, with their placeholders kept in the text. (H) |
| R-12 | Prefer consolidated observations over the raw facts they supersede, without losing result count. (H) |
| R-13 | An execution trace, so a retrieval that returned the wrong thing can be diagnosed. (H) |

### Reasoning

| # | Requirement |
|---|---|
| S-1 | Answer a question in prose from the base's contents, rather than returning passages for the caller to assemble. (H) |
| S-2 | The answer reports which facts it used. (H) |
| S-3 | A mission shapes how the answer is reasoned, and directives constrain it; neither affects retrieval. (H) |
| S-4 | Structured output against a caller-supplied JSON Schema, with a stated reason when it could not be produced — distinct from an answer that held nothing matching. (H) |
| S-5 | Tagged directives are scoped; untagged ones are global. (H) |
| S-6 | A mission, a question and its options are separately overridable per call. (H) |
| S-7 | An execution trace of tool and model calls, on request. (H) |

### Facts and curation

| # | Requirement |
|---|---|
| F-1 | List facts with a total, filtered by type, query, document, entity, tags, consolidation state and time window. (H) |
| F-2 | Read one fact with its entities, tags and full metadata. (H) |
| F-3 | Edit a fact's text, context, event window, type or entities, which re-embeds it and re-derives what depended on it. (H) |
| F-4 | Soft-retire a fact with a recorded reason, reversibly, keeping it for audit. (H) |
| F-5 | A fact's revision history, with each change's source text resolved. (H) |
| F-6 | Delete every observation derived from one fact and re-queue it. (H) |
| F-7 | A memory graph for visualisation, filterable by type and scope. (H) |
| F-8 | List and inspect entities, and an entity co-occurrence graph. (H) |

### Observations

| # | Requirement |
|---|---|
| O-1 | Observations are derived, editable by refresh, and never silently overwritten. (H) |
| O-2 | List the distinct scopes observations live in, so a base with per-tag scoping is navigable. (H) |
| O-3 | Preview which scopes a draft consolidation configuration would claim, and which it would leave to the default. (H) |
| O-4 | Trigger consolidation on demand. (H) |
| O-5 | Recover a failed consolidation. (H) |
| O-6 | Clear all observations while keeping facts. (H) |
| O-7 | A base can disable observation consolidation entirely. (H) |

### Pages

| # | Requirement |
|---|---|
| P-1 | Pages live in a tree of folders, arbitrarily nested, with names unique per folder. (H) |
| P-2 | A page is built from a question, and rebuilt when new knowledge lands in its scope. (H) |
| P-3 | A page reads consolidated beliefs rather than raw detail, never reads a sibling page, and has a document-sized budget. (H) |
| P-4 | Refresh mode distinguishes a full regeneration from an incremental edit that preserves untouched sections. (H) |
| P-5 | Refresh is triggered by consolidation or by a schedule, never both, with a minimum interval that folds a burst of triggers into one rebuild. (H) |
| P-6 | The tree reports, per page, whether it is stale, when it last refreshed, and when its last refresh failed. (H) |
| P-7 | Rename, move and reconfigure a node, applying only the fields present. (H) |
| P-8 | Search pages as whole documents, returning whole pages with snippets, fused server-side and without a reranking step — because a page search is a tool an agent chooses to call, and it has to be fast enough to be the first call. (H) |
| P-9 | Export the whole base as a portable markdown bundle, with a refresh log per page. (H) |
| P-10 | A page's tags **scope** what it is built from rather than labelling it, and the contract says so, because a tag invented at creation time to describe the topic will match nothing. (H) |
| P-11 | Deleting a page loses nothing: it re-projects from memory. (H) |

### Templates

| # | Requirement |
|---|---|
| T-1 | A versioned manifest configures a base and defines its directives and mental models. (H) |
| T-2 | The manifest has a published JSON Schema, fetchable at runtime. (H) |
| T-3 | Import applies by section, omitting one leaves that part unchanged, and matches mental models by identifier and directives by name so a re-import updates rather than duplicates. (H) |
| T-4 | Import has a validation-only mode reporting what it would do. (H) |
| T-5 | Export a base's explicit overrides only, so the result is portable and does not capture server defaults. (H) |
| T-6 | Export and import round-trip. (H) |
| T-7 | The manifest version is forward-compatible: older versions upgrade, newer-than-server is refused naming the upgrade. (H) |
| T-8 | Export a whole base, import one, and clone one, all asynchronously. (H) |

### Operations

| # | Requirement |
|---|---|
| A-1 | Every asynchronous submission reports an operation identifier. (H) |
| A-2 | List and read operations, filtered by status, with a total. (H) |
| A-3 | Cancel a pending or running operation; retry a failed one; delete a terminal one. (H) |
| A-4 | An operation reports its type, progress, retry count, next attempt and error, so a failed backlog is diagnosable. (H) |
| A-5 | A client-supplied identifier makes a resubmission return the original operation, and a mismatched reuse is refused. (H) |

### The authoritative wiki

| # | Requirement |
|---|---|
| W-1 | A project names one or more directories of markdown as the authoritative corpus for a base. (new) |
| W-2 | **Reconcile is the unit of work**: plan what would change, report it, and apply only what the user confirmed. There is no unconditional write. (new) |
| W-3 | A file's identity is a pure function of the base and its relative path, so an edit is an update, a rename is a delete and a create, and no state outside the base is required. (new) |
| W-4 | An unchanged file costs nothing: the plan compares digests against the documents' recorded content digests. (new) |
| W-5 | Each ingest records which documents it owns, and a prune only removes documents that owner recorded. Two ingests over one base cannot delete each other's work. (new) |
| W-6 | Frontmatter becomes tags — path segments, dates, and the author's own tags — so scope is a filter the retrieval already supports rather than a directory convention the API must learn. (new) |
| W-7 | Authored text is ingested with entity resolution off, because a person writing a name means the name. (new) |
| W-8 | Files with no event time are ingested as timeless. (new) |
| W-9 | The plan names every file it would create, change, leave alone and delete, with digests, so the user reads what is about to happen. (new) |
| W-10 | Application is asynchronous, batched, and idempotent per batch, because a large corpus is thousands of extractions and cannot be a synchronous call. (new) |
| W-11 | Binary files under the wiki are ingested through the binary path. (new) |
| W-12 | **The wiki remains the source of truth.** Deleting the base deletes derived knowledge, not the files. Deleting a page deletes a projection. Nothing in the base is the only copy of anything a person wrote. (new) |
| W-13 | Agent-written knowledge and wiki-derived knowledge are distinguishable on retrieval, by a provenance tag every ingest and every agent write is tagged with. (new) |
| W-14 | Provenance is a filter, not a decoration: a caller can ask for the wiki alone, or for what the assistants learned alone. (new) |

### Boundaries and the framework

| # | Requirement |
|---|---|
| N-1 | Every RPC declares `@toolbox.side-effects`, because that declaration is what a policy author reads. An unclassified operation is not a read. (new) |
| N-2 | No provider-specific type appears in the contract. No `bank_id`, no arm names, no score-floor semantics, no disposition scale. (new) |
| N-3 | Fixed option sets are enums, multi-value fields are `repeated`, and related options are grouped into sub-messages. (new) |
| N-4 | Paging is `page_size`/`page_token`/`next_page_token`, not limit and offset. (new) |
| N-5 | "Absent" and "empty" are distinguishable on every partial update. (new) |
| N-6 | Failures are classified with `api.ErrorKind`, and the transport maps kinds to codes in one place. (new) |
| N-7 | A capability the deployment reports as disabled is reported as unsupported, naming the flag, rather than as a 404 from a route that was never mounted. (new) |
| N-8 | The attachment endpoint's indistinguishability of "absent" and "invisible" is preserved; a provider must not reintroduce a probe. (new) |
| N-9 | Streaming: there is none. Every operation is unary, and adding one would be the only streaming contract in the tree. (new) |
| N-10 | The provider declares one record, not one per base, and a deployment running two backends distinguishes them by identifier. (new) |

## 4. The authoritative wiki

This is the part that is not a port, so it is worth being explicit about why it
looks the way it does.

### 4.1 The two directions, and why one of them is not the answer

The Hindsight backend projects a base onto a folder of markdown files, and keeps
that folder current. That direction is memory → disk, and it exists so a person
can `ls`, `grep` and edit. Its own documentation is clear that the projection is
derived:

> A knowledge page is a **projected view** over processed memory, the way a
> database view is not a table. […] Your raw documents remain the source of truth
> about *what was said*. The pages are the reconciled truth about *what holds*.

So the inverse question — how does authored text get in — has an obvious wrong
answer, and it is worth naming why it is wrong. **"Create a page whose body is
what I typed"** does not work, and cannot:

- A page *is* a mental model. Its body is always synthesized by a model from a
  question it is given. There is no writable body field for a page or a mental
  model anywhere in the public surface — verified against the description, not
  assumed.
- A page's default trigger is incremental refresh after every consolidation. A
  first build with no prior document to edit is a full generation. Whatever was
  typed is replaced before the first refresh, because there was nowhere to put it.
- A flag on the page node is documented as *"true = system-owned, false =
  hand-authored"*, which reads like the escape hatch. It occurs **once** in the
  whole API description, in a response-only structure, in no request. There is no
  way to set it and nothing documented makes a refresh skip it. It is reserved.
  Designing against it would be designing against nothing.

The consequence is the rule the rest of this section follows: **authored prose
becomes a document, not a page.** The prose is the source of truth about what
was said; the pages built from the facts extracted out of it are the reconciled
truth about what holds. That is the same division the backend already insists on
for the direction it does support.

### 4.2 What the wiki looks like

A directory tree of markdown, in a repository, reviewed like code:

```text
docs/
├── index.md                 # frontmatter: tags, title
├── architecture/
│   ├── overview.md
│   └── decisions/
│       └── 0001-one-way-wiki-sync.md
├── runbooks/
│   └── restore-a-base.md
└── glossary.md
```

Frontmatter is optional and, where present, becomes tags (W-6). The path becomes
tags. Nothing about the format is required: a file with no frontmatter is
ingested, and the path alone scopes it.

What the wiki deliberately is **not**: a place to store derived knowledge.
Observations, mental models and pages are the base's, and writing them by hand
would be writing into a projection that the next refresh would edit. If a person
wants to state something as fact, they write it in the wiki, and the base
reconciles it like anything else.

### 4.3 Reconcile

`PlanIngest` walks the configured roots, computes each file's document identity
and digest, compares against what the base already holds, and returns four
lists. It reads and it never writes. `ApplyIngest` takes the plan, a confirmation
value, and does the work asynchronously.

| | Files | Cost |
|---|---|---|
| `created` | not in the base | extraction, embedding, consolidation |
| `updated` | present, digest differs | extraction, embedding, consolidation; derived observations re-derived |
| `unchanged` | present, digest matches | nothing |
| `deleted` | recorded by this owner, no longer on disk | fact removal; dependent observations re-derived |

Four decisions inside that, each of which is a choice rather than a detail:

**Identity is a pure function of the base and the relative path.** W-3. This is
what makes the reconcile stateless. The alternative — an index file, as the
reference implementation for a different corpus keeps outside the synced tree —
introduces a second thing that can be lost, and two indexers that can disagree.
Deriving identity from the path means a lost index costs nothing: the next plan
recomputes it, and a digest comparison tells it what is unchanged.

**Diffing is by content digest, never by timestamp.** W-4. Filesystems lie about
timestamps; digests do not, and the documents already carry one.

**Ownership is recorded, and pruning respects it.** W-5. Each ingest writes its
own marker into the document metadata it creates. A prune removes a document
only if that marker names the owner doing the pruning. Without this, ingesting a
second directory into a base would delete the first one's corpus — which is the
failure mode a two-ingester deployment discovers by losing data.

**Frontmatter and path become tags.** W-6, W-7, W-8. Tags are what retrieval
already filters on, so a directory convention becomes a scope without the
retrieval side learning anything about directories. And the author's own
`tags:` are carried through rather than flattened into one namespace, because a
person's taxonomy and the framework's are different vocabularies.

### 4.4 Confirmation, and why it is a field

`PlanIngest` returns a plan. A person reads it. The agent relays the decision
back as a value on `ApplyIngest`.

Three rules converge on this shape, and it is worth naming all three:

- **A change to what a project says about itself is the user's to confirm**
  (AGENTS.md rule 17). A corpus is not a configuration file, but the reasoning
  carries over: ingesting a thousand files changes what every assistant in the
  deployment believes, and a person should see that happening.
- **A rule that must reach a human has to work on any transport** (rule 18). The
  HTTP MCP endpoint is stateless and cannot elicit. So a prompt would be a rule
  that holds only where the transport can ask, which is not a framework rule. A
  value the agent relays and the user answers works everywhere.
- **It has to be a field, not a two-call handshake**, so the proposal and the
  approval cannot be reordered. This is the same shape `AddSkill` already uses
  and for the same reason.

The same applies to `ImportTemplate` (T-4), for the same reasons.

### 4.5 Provenance, and what it is for

Every fact, document and observation carries a provenance tag: whether it came
from the wiki, from a conversation, or from the base's own reasoning. W-13, W-14.

The framework reserves a prefix for tags it assigns, so a corpus ingest cannot
collide with tags a person wrote, and so the distinction survives anyone editing
a file.

This buys three things, none of which are decoration:

- A caller can ask for the authoritative corpus alone — "what does our
  documentation actually say", with nothing the assistants inferred mixed in.
- A caller can ask for the accumulated knowledge alone.
- A citation can say which it was. A citation that cannot distinguish a written
  decision from an inferred one is worth much less.

It is also what makes W-5 enforceable in principle: ownership is a provenance
tag, so pruning and provenance are the same mechanism rather than two.

## 5. The contract

### 5.1 Services

Grouped by concern. Every RPC declares its side effects; that is not a
formality, because the declaration is what a policy author reads and what the
MCP gateway gates on.

| Service | RPCs |
|---|---|
| `KnowledgeBaseService` | `ListBases`, `GetBase`, `CreateBase`, `UpdateBase`, `DeleteBase`, `ResetBaseConfig`, `GetBaseConfig`, `UpdateBaseConfig`, `GetBaseStats`, `GetBaseIngestionSeries`, `ListBaseAliases`, `AddBaseAlias`, `SetPrimaryBaseAlias`, `RemoveBaseAlias`, `ClearBaseObservations` |
| `MemoryService` | `Retain`, `Recall`, `Reflect`, `ListMemories`, `GetMemory`, `CurateMemory`, `GetMemoryHistory`, `GetMemoryGraph`, `ListTags`, `PreviewExtraction`, `PreviewPrompts` |
| `DocumentService` | `ListDocuments`, `GetDocument`, `UpdateDocument`, `DeleteDocument`, `ListDocumentChunks`, `GetDocumentChunk`, `ReprocessDocument` |
| `PageService` | `GetPageTree`, `CreatePageFolder`, `CreatePage`, `GetPage`, `SearchPages`, `UpdatePageNode`, `DeletePageNode`, `ExportPageBundle` |
| `MentalModelService` | `ListMentalModels`, `CreateMentalModel`, `GetMentalModel`, `UpdateMentalModel`, `DeleteMentalModel`, `GetMentalModelHistory`, `RefreshMentalModel`, `PreviewMentalModelRefresh`, `ClearMentalModel` |
| `DirectiveService` | `ListDirectives`, `CreateDirective`, `GetDirective`, `UpdateDirective`, `DeleteDirective` |
| `ObservationService` | `ListObservationScopes`, `PreviewConsolidation`, `TriggerConsolidation`, `RecoverConsolidation`, `ClearBaseObservations` |
| `EntityService` | `ListEntities`, `GetEntity`, `GetEntityGraph` |
| `TemplateService` | `GetTemplateSchema`, `ExportTemplate`, `ImportTemplate`, `ExportBase`, `ImportBase`, `CloneBase` |
| `OperationService` | `ListOperations`, `GetOperation`, `CancelOperation`, `RetryOperation`, `DeleteOperation` |
| `IngestService` | `PlanIngest`, `ApplyIngest` |

`ClearBaseObservations` appears in two services above, which is a mistake in the
table and not in the design: it belongs to one of them and §14 records which.

### 5.2 Vocabulary and side effects

| RPC group | `@toolbox.side-effects` | Why |
|---|---|---|
| `List*`, `Get*`, `Preview*`, `Export*`, `SearchPages` | `read_only` | Nothing is written. Previews report what a write *would* do, which is why they are reads and why T-4 is a read. |
| `Retain`, `Create*`, `Update*`, `Add*`, `Set*`, `Trigger*`, `Recover*` | `create update` | |
| `Refresh*`, `Preview*` on a model that has a side effect | `update` | A refresh rewrites a document, so it is a write even though its input is a read. |
| `Delete*`, `Remove*`, `Cancel*`, `Clear*` | `delete` | |
| `PlanIngest` | `read_only` | It reads the corpus and the base and writes nothing. |
| `ApplyIngest` | `create update delete` | The only operation in the contract that can delete, create and update in one call, and therefore the one whose confirmation matters most. |
| `ImportTemplate` | `create update` | Applies configuration and defines directives; the content it defines is generated, not authored. |
| `ImportBase`, `CloneBase` | `create update delete` | Both can replace an existing base's contents. |

### 5.3 Message rules

Rules from N-3 through N-5, stated as rules:

- **Enums for fixed option sets.** `FactType`, `TagMatchMode`, `Budget`,
  `ExtractionMode`, `RefreshMode`, `ObservationScopeMode`, `UpdateMode`,
  `OperationStatus`, `Provenance`. The backend is inconsistent here — it enums
  some of these and leaves others as bare strings — and the contract does not
  inherit the inconsistency.
- **`repeated`, never comma-separated.** Tag groups are a recursive boolean
  expression (`leaf`, `and`, `or`, `not`, each leaf carrying its own match mode
  and an optional fuzzy tag resolution), modelled as a recursive message with a
  `oneof`. Not a JSON blob, and not a string.
- **Sub-messages for grouping.** A recall request is a dozen independent knobs;
  grouping them (`Scope`, `Window`, `ScoreFloors`, `Budget`) keeps the generated
  CLI and the JSON Schema legible, and keeps a future field from being a
  fourteenth top-level one.
- **`optional` for "absent means unchanged".** Partial updates are three-valued
  in the backend — absent leaves it, empty clears it — and proto3 without
  `optional` cannot say that. Getting it wrong on an update is how a re-tag
  silently clears a field.
- **`page_size` / `page_token` / `next_page_token`**, matching what the rest of
  the framework already does rather than the backend's offsets.
- **`int32`/`int64` for numbers, `google.protobuf.Timestamp` for times.** The
  backend speaks ISO-8601 strings; the conversion belongs in the provider, and
  the field comment should say which the caller sends.

### 5.4 Errors

| Condition | `api.ErrorKind` | ConnectRPC |
|---|---|---|
| Malformed or missing required field | `KindInvalid` | `InvalidArgument` |
| No such base, document, page, operation, fact | `KindNotFound` | `NotFound` |
| Duplicate page name in a folder; reused operation identifier | `KindAlreadyExists` | `AlreadyExists` |
| Feature the deployment reports disabled (N-7) | `KindUnsupported` | `Unimplemented` |
| Backend unreachable, timed out, or failing | `KindUnavailable` | `Unavailable` |
| Anything else | `KindInternal` | `Internal` |

The `KindUnavailable` case carries the distinction `pkg/core` already makes and
that a provider must not flatten: **not deployed** and **not answering** are
different facts with different fixes, and a provider that reports both as
"unavailable" has thrown away the diagnosis.

The attachment endpoint (R-11) is the one place where two different conditions
must produce the *same* answer (N-8), because the backend does that deliberately
to stop the endpoint being used to probe what a base holds.

## 6. Retrieval, in the terms the contract uses

The backend's four arms are an implementation strategy. The contract states what
a caller gets, not how, with one exception worth making: the caller *can* ask
for a subset, and that is part of the interface.

```
message RecallRequest {
  // The question. Required.
  string query = 1;

  // Which kinds of knowledge to search. Unset means all of them.
  repeated FactType types = 2;

  // How much to read back, as a scale rather than a token count, so the
  // deployment decides what "enough" costs it.
  Budget budget = 3;

  // Where to look. Omitted means the whole base.
  Scope scope = 4;

  // A stated moment, so "last month" is relative to something rather than to
  // the server's clock.
  google.protobuf.Timestamp query_time = 5;

  // A known period, supplied instead of being read out of the query. It ranks
  // by, and does not filter to, which is stated here because getting that
  // backwards is the easy mistake.
  TimeWindow window = 6;

  // Optional per-stage score floors, for deliberate abstention.
  ScoreFloors min_scores = 7;

  // Whether to consolidate results by preferring observations over the raw
  // facts they supersede.
  bool prefer_consolidated = 8;

  // What to attach to each result.
  IncludeOptions include = 9;
}
```

`IncludeOptions` carries the per-result extras as a message rather than a pile of
booleans, because they are all "give me more of X" and they belong together.

`ScoreFloors` distinguishes the stages that **rank** from the stages that
**filter**, and the contract comment has to say so, because the two are not
interchangeable: setting floors on a ranking stage does not restrict the
response, since a result surfaced by any stage is returned and reports no score
for the stages that did not surface it. A caller wanting abstention sets the
filtering stages. The backend documents this at length and the contract must not
lose it, because the generated MCP tool description is the only place an agent
will read it.

## 7. Pages

A page is a mental model configured as a document. The configuration is what
makes it a page, and it is worth stating because the default is doing real work:

| Setting | Default for a page | Why |
|---|---|---|
| Built from | consolidated beliefs only | Beliefs are deduplicated and evidence-backed, so a page reads as a settled document rather than a transcript. The backend enforces this structurally — the refresh agent is not given the raw-fact retrieval tool at all. |
| Reads other pages | never | Otherwise pages cite each other and one wrong claim propagates across the base. |
| Refresh mode | incremental | Edits the existing document with what is new, so hand-tuned structure survives. |
| Trigger | after consolidation | Rewrites when new knowledge lands in **its own** scope. |
| Budget | document-sized | It is a document, not an answer. |

Two consequences the contract must state, because both are easy to get wrong:

**A page's tags scope it.** They are not labels. A tagged page matches with
`all_strict` by default: a memory must carry *every* one of the page's tags, and
untagged memories are excluded entirely. So a page created with tags invented at
creation time to describe its topic matches nothing and generates as "I don't
have information about this", while a direct recall for the same query returns
everything — because recall was not given the same filter. The contract comment
says this, and the three ways to get it right (omit tags, widen the match, or
keep the strict default deliberately) are the contract's comment too.

**Staleness does not see deletions.** A page is stale when something in its scope
has been *written* since it last read. Deleting an in-scope fact leaves no
write behind, so a page citing a deleted fact keeps reporting itself current.
Stating this is more useful than leaving it to be found, because the fix is
different from what a reader assumes.

## 8. Directives and base configuration

Two things a person writes that the base keeps verbatim and never rewrites.
They are the hand-authored part that is not corpus, and §9.2 says why they are
not ingested.

**Directives** are hard rules the reasoning step must follow — "never recommend
a specific product", "always cite the source". They are matched by name, carry a
priority, are active or not, and are scoped by tags: untagged directives are
global, tagged ones apply only when the request's scope matches. A deployment
can apply every active directive regardless of scope when it means to.

**Base configuration** is the larger surface and is deliberately not enumerated
here — it is a long list of optional fields and the schema is published (T-2).
What the contract states is the shape: every field optional, applied as a
per-base override, everything else inheriting the server default. That is what
makes an exported template portable rather than a snapshot of one server's
defaults (T-5).

## 9. Two paths in, and why they are two

A deployment has two kinds of hand-authored material and they are not the same
kind of thing.

### 9.1 Prose → documents

Described in §4. It becomes facts, is consolidated, is reconciled, and is
retrievable. What comes back is facts in the document's own words, not its whole
content — extraction is lossy and non-deterministic, and that is accepted rather
than hidden. Where a deployment wants the authored words back byte-for-byte, the
file is the answer, not the base (W-12).

### 9.2 Configuration → a template

A template is hand-authored, versioned, schema-described, diffable, verified
before it is applied, and never rewritten. It is the artifact that belongs in a
repository next to the deployment's configuration, and it is what "export this
base's setup" produces.

The reason it is a separate operation rather than another field on ingest is
precisely that the two are stored differently. Prose is extracted into facts and
reconciled; a directive is stored as written and injected. A corpus ingest that
also tried to set configuration would put authored rules through a lossy
extraction, which is the one thing a directive must not be put through.

It is also the only place a person states *how the base should think* rather than
*what it should know*, and those deserve different review.

## 10. Agent tools

What an assistant may call, and what it may not. The split is not a formality:
it is the difference between a tool surface an agent can reason about and one it
has to page through.

**Offered:** `Retain`, `Recall`, `Reflect`, `ListMemories`, `GetMemory`,
`ListDocuments`, `GetDocument`, `ListTags`, `ListOperations`, `GetOperation`,
`GetPageTree`, `SearchPages`, `GetPage`, `ListMentalModels`, `GetMentalModel`,
`ListDirectives`, `ListBases`, `GetBase`, `PlanIngest`, `ApplyIngest`.

**Not offered to an agent, even where a policy permits it:** `DeleteBase`,
`DeleteDocument`, `DeletePageNode`, `DeleteMentalModel`, `DeleteDirective`,
`ClearBaseObservations`, `ImportTemplate`, `ExportBase`, `ImportBase`,
`CloneBase`, `RecoverConsolidation`, and everything to do with aliases, webhooks,
audit logs and traces.

Two framework rules decide this rather than preference. A contract's declaration
says what invoking a method does; a policy says who may invoke it; neither is
derived from the other, and the empty policy permits nothing. So every RPC above
is discovered and classified whether or not it is ever exposed, and exposure is
a policy decision made per deployment.

And the confirmation of §4.4 is a **value the agent relays**, not a prompt the
transport asks for — because the stateless HTTP endpoint cannot elicit, and a
rule that held only where it could would not be a framework rule.

## 11. Where the behaviour lives

- **`pkg/knowledge`** — the provider-neutral model and the engine interfaces the
  contract's messages convert to. This is where the behaviour is, because the
  framework needs it in process.
- **`pkg/knowledge/hindsight`** — a hand-written client for the backend's HTTP
  API, returning `api.Errorf`-classified failures. Hand-written rather than
  generated, because a generated client drags the whole 195-schema model into a
  module's dependency graph and the contract must not see those types anyway.
- **`subsystems/knowledgehindsight`** — mounts the client behind the contract and
  declares one provider record. It converts messages and holds no logic, exactly
  as `subsystems/skillgit` does over `pkg/skills`.

The reference in-memory implementation is **not** part of this specification. A
subsystem with seventy-odd RPCs does not get a second implementation that does
`strings.Contains` over a map; the contract is the deliverable and one honest
provider is better than two, one of which is a fiction. §14 records this as a
decision to confirm rather than an open question.

The backend is reachable three ways without any of this, and they are worth
stating because they are cheaper than the above and they are how a deployment
should evaluate whether to build any of it: its OpenAPI description can be
registered in the API catalog, its own MCP server can be exposed, and its
documented ingestion semantics are the ones §4 specifies. A deployment that
wants the capability this month can have it without a line of the code above.
That is a legitimate outcome, not a failure of the design.

## 12. Failure and degradation

| Situation | Behaviour |
|---|---|
| Backend unreachable at startup | The provider fails to start, naming the endpoint and the failure. It does not start and serve empty results, because a base that reports "no knowledge" when it is disconnected is the worst possible answer. |
| Backend unreachable during a call | `KindUnavailable`, and the error distinguishes not-deployed from not-answering. |
| A capability is disabled | `KindUnsupported` naming the flag (N-7), not a 404. |
| Ingestion partially fails | Per-file outcomes in the operation's report; the operation is failed, not silently short. |
| A wiki file is unreadable | Named in the plan as skipped, with the reason. A permission error on one file is not a reason to fail a thousand-file ingest. |
| A page's refresh fails | Recorded on the node, and the page does not rebuild while it is set. A base with one broken page is not a base that has silently lost a page. |
| A reconcile is interrupted | Idempotent per batch (W-10), so a rerun resumes rather than duplicating. |

## 13. What is deliberately not here

- **A second provider.** One backend, one provider. A second is a second
  provider behind the same contract, and it is not worth designing for before
  there is a second.
- **Writing prose into a page.** §4.1.
- **Page and mental model as separate concepts.** They are one object with
  different defaults. This specification collapses them into `PageService` and
  `MentalModelService` and §14 records the cost.
- **Webhooks, audit logs, trace logs, metrics.** Operator and infrastructure
  surface, reachable through the backend directly.
- **Memory defence.** A real feature with its own policy implications, worth its
  own investigation rather than a paragraph here.
- **Streaming.** §3, N-9.

## 14. Open questions

1. **Where does `ClearBaseObservations` live** — `KnowledgeBaseService` or
   `ObservationService`? It clears derived knowledge from a base, so the base is
   the subject; it is also one of the observation operations. Pick one.
2. **Are pages and mental models one service or two?** They are the same object
   with different defaults and the distinction is invisible in the API. Two
   services is more faithful and costs a concept; one is cheaper and loses the
   distinction a reader of the contract may want.
3. **Does the wiki live in this subsystem or a sibling?** A sibling
   `knowledgewiki` serving a corpus contract and calling this one over
   ConnectRPC obeys the cross-subsystem rule more literally and makes the
   reconcile testable with no backend at all. One subsystem is fewer modules.
4. **Is the corpus on disk at ingest time, or does the base hold the bytes?**
   §4 assumes the file is the copy and the base holds derived knowledge. A
   deployment that wants the base to be self-contained needs the text retained
   and accepts that it then has two copies that can disagree.
5. **Provenance tag namespace.** Reserved prefix and value spelling. It should be
   a framework decision, because a deployment that guesses differently cannot
   filter across two bases.
6. **Is `Reflect` a knowledge operation or an agent operation?** It runs a model
   loop over the base. `subsystems/agent` exists. Getting this wrong puts a
   second model-calling path in the framework, which is a boundary worth deciding
   deliberately rather than by default.
7. **Should the base's own configuration be project configuration?** A base's
   missions and directives are authored material, and a project configuration
   file is where this framework puts authored material. If they merge, ingesting
   a template becomes a rule-17 operation on the project's configuration file,
   which is a stronger reason to confirm than §4.4 already gives.

## 15. What a change would touch

Not a plan; a list, so the size is visible before anyone starts.

**New:** `pkg/knowledge/`, `pkg/knowledge/hindsight/`,
`subsystems/knowledgehindsight/` (contract, implementation, `docs_embed.go`,
`Makefile`, `go.mod`, command, tests), this document's siblings — a CI matrix
row, which is hand-written and whose absence fails nothing, and an ADR.

**Changed:** the knowledge contract (new), `docs/README.md` (this document),
`docs/feature-spec.md` (§4 and F-021, already updated), `docs/subsystems.md`,
`docs/status.md`, `docs/todos.md`, `AGENTS.md` if a rule falls out.

**Phasing.** Evaluate through the backend's own surfaces first — they need none
of this and they answer whether the backend fits the work at all. Then decide
§14 items 1–3 and 6, which are the ones that change the contract's shape. Then
`pkg/knowledge` and the client, tested against the real API description rather
than a live server so the suite stays deterministic and offline. Then the
provider. Then ingest and templates, which are last because they are the parts
that involve a person and the parts most likely to change shape after a first
real use.

## 16. Sources

- The investigation this supersedes:
  [`investigations/hindsight-knowledge-backend.md`](investigations/hindsight-knowledge-backend.md)
- Hindsight's machine-readable API description (0.10.1) and its documentation
  site, for every claim about the backend's behaviour. Structural claims in
  particular — the absence of a writable page body, the response-only provenance
  flag, the request shapes — were checked against the description rather than
  the prose, because the prose is written for humans and the description is what
  the backend will actually accept.
- [`feature-spec.md`](feature-spec.md) for the product-level framing this
  implements.
- [`architecture.md`](architecture.md) and
  [`decisions/0005-reusable-packages-behind-thin-providers.md`](decisions/0005-reusable-packages-behind-thin-providers.md)
  for the boundaries a provider subsystem lives inside.
