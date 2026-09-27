# Knowledge

**Status: specification. Nothing here is implemented.** This document specifies a
knowledge base subsystem that does not exist yet, replacing the placeholder that
was removed in the same change. Where it states a decision it is a decision this
document makes and a later change is expected to follow; where it states an open
question it is listed in §15 rather than answered.

The investigation that led here — the Hindsight API survey, the reasoning about
preloading, and the three placement options — is kept in
[`investigations/hindsight-knowledge-backend.md`](investigations/hindsight-knowledge-backend.md).
This document supersedes it where they differ.

---

## 1. What this subsystem is for

An assistant that forgets everything between sessions is limited to whatever
context the caller pastes in. A person holding the institutional memory of a
project is limited to whatever they remember to open. **This subsystem is the
system that both of them work from**: the knowledge, the memory, and the
documentation, in one place, reachable by both.

### 1.1 The three layers

There are three layers here, and keeping them apart is most of the design.

|  | The authored corpus | The memory engine | The projection |
|---|---|---|---|
| **What it is** | What people wrote: architecture notes, runbooks, decisions, policies | What the system derived from it and from conversation: facts, entities, observations, models | Readable documents rendered from the engine's current beliefs |
| **Who writes it** | People, in files, reviewed by other people | The system, from retained content | The system, on a refresh |
| **Stored as** | Markdown in a Git repository | The memory backend's indexes | Backend pages, and a markdown export of them |
| **What makes it trustworthy** | Authorship and review. A named person wrote it and someone accepted it | Evidence. Every fact traces to a document, every observation to its facts | Nothing on its own. It is what the system believes *now* |
| **If deleted** | Nothing to rebuild from — this is the loss | **Rebuilt in full from the corpus** | Rebuilt from the engine |
| **What it cannot do** | Notice that a decision was reversed last month | Show you the sentence someone wrote | Be quoted as authority |

The corpus and the engine are the two halves in §1.1's sense; the projection is
the engine's rendering of itself, and it is **not** a third source of truth. It
exists because "what does the system currently believe about this" is a real
question with a real answer, and a person should be able to read that answer
without a query language.

Two consequences follow, and they are the two rules this subsystem is built on:

- **The corpus is authoritative. The engine is disposable and reproducible.**
  Delete the base and it can be rebuilt in full from the repository. That is not
  a nice property, it is the property that makes every other decision safe —
  because it means the derived side can be regenerated when the backend changes,
  when the extraction settings change, when the whole thing is migrated, and
  when you want to index a historical version of the corpus.
- **The projection never flows back into the corpus.** Feeding generated pages
  back in would make the system increasingly self-referential, and every
  generation would be reasoning partly from its own previous output. The corpus
  is written by people; that is the whole of its provenance.

Neither substitutes for the others. A base with only an engine has nothing its
users can review. A base with only a corpus has nothing an assistant can reason
with. A base that treats the projection as authority has nothing trustworthy.

### 1.2 Why they coexist in one base

They share a base because the questions people and assistants ask cross the
line constantly. *"Why is the retry count three?"* is answered by a runbook
somebody wrote and by something an assistant learned during an incident. A
knowledge base that keeps those in two stores answers half of it twice and the
other half not at all.

So a base holds both, and **provenance** — whether a piece of knowledge was
authored, retained, or derived — is a filter rather than a partition. §4 is the
type that makes this possible.

### 1.3 One surface, not two

The half of the system a person uses and the half an assistant uses are **the
same API**, and that is the requirement, not a convenience.

- A consumer does not branch on where knowledge came from in order to **read**
  it. It searches, it gets results, and origin is an attribute on each one it
  can filter or group by.
- Both can **write**. An assistant retains; a person writes a file; a person can
  also retain through the CLI, and an assistant can also correct a fact. Neither
  audience is restricted to its own half.
- Both can **edit what the other wrote**. A person curates a fact the system
  extracted. An assistant corrects a document it read. A person promotes a
  derived observation into the authored record by writing the file that states
  it (§5.6), and the reconcile makes their version the one that holds.

§10 states where this homogeneity stops, because it does stop, and pretending
otherwise would be the easier mistake.

### 1.4 What makes this hard

1. **Recall has to be selective.** A base holds more than fits in a context
   window, so retrieval has to find the relevant part and cite it. Substring
   matching does not do this; a reader looking for a specific term and a reader
   asking "why do we do it this way" need different searches.
2. **The authored corpus outlives any tool.** A base that only knows what a
   model extracted cannot show a person the sentence they wrote, and a base that
   rewrites its own prose cannot be reviewed at all.
3. **Derived knowledge is worth keeping.** What an assistant learns across
   sessions — a preference, a decision, a correction — is knowledge no file
   contains.
4. **A single surface over two substrates is easy to fake.** The temptation is to
   expose the backend's pages and the wiki as two resource families and call
   that one API. §4 and §10 are about not doing that.

## 2. Terminology

Vocabulary is load-bearing here, because the two halves are different kinds of
thing and the difference is the whole design.

| Term | Meaning |
|---|---|
| **knowledge base** | A named, isolated body of knowledge, holding both halves. The unit of separation between subjects, projects or tenants. |
| **base** | Short form. The contract's word, not the backend's. |
| **content** | The one addressable, readable unit of knowledge. A wiki file, a retained document, and a derived page are all content. §4. |
| **origin** | How a piece of content came to exist: authored by a person, retained from a source, or derived by the system. An attribute, never a separate type. |
| **location** | Where content is addressable: a path in the corpus, a document identifier, or a position in the page tree. |
| **provenance** | The record of what produced content and what it is derived from. Queryable, not decorative. |
| **memory** | The half the system maintains: facts, observations, mental models, pages. |
| **documentation** | The half people maintain: the authored corpus, and the pages rendered from the other half. |
| **document** | A container for retained content and the unit a fact traces to. One origin of content. |
| **chunk** | The segment a document was split into before extraction. Holds the text that produced a fact. |
| **fact** | One extracted statement. A world fact (objective) or an experience fact (the base's own actions). |
| **observation** | A consolidated, deduplicated, evidence-bearing belief synthesised from facts. |
| **mental model** | A synthesized document answering one question, rebuilt when its scope changes. |
| **page** | A mental model configured as a living document in a folder tree. The unit an assistant browses. |
| **directive** | A hand-authored rule the reasoning step must follow. Never rewritten. |
| **wiki** | The directory of markdown a project owns. The authored corpus, and the source of truth about what was written. |
| **corpus** | The wiki, read as a whole. The input to a reconcile. |
| **reconcile** | Bringing the corpus and a base into agreement: report the differences, then apply the confirmed ones. |
| **base template** | A versioned manifest configuring a base and defining its directives and mental models. |
| **operation** | A unit of asynchronous work the backend reports on: extraction, consolidation, a refresh. |

Terms deliberately **not** used, because they would import one implementation's
vocabulary into a provider-neutral contract: *bank* (a base is a base),
*observation scope*, *TEMPR*, *reranker*, *consolidation strategy*, *disposition
trait*, *entity*, *proof count*, *premise*, *dedup*.

Two words the framework uses elsewhere that this document uses narrowly, and
says so rather than assuming: **document**, which is one origin of content and
not the word for content in general; and **page**, which is one origin of
content and not a synonym for documentation.

## 3. Requirements

Numbered so a change can be traced to what it satisfied. "H" marks a capability
the memory backend provides directly, "W" one the corpus integration adds, and
"U" one the unified surface requires regardless of which half produced it.

### Bases

| # | Requirement |
|---|---|
| B-1 | Named bases, isolated from one another: separate content, separate configuration, separate directives, separate corpora. (H) |
| B-2 | Create, inspect, update and delete a base. A delete is explicit and reports what it destroyed. (H) |
| B-3 | Aliases, so a deployment can address a base by a friendly name; one alias may be primary for display. (H) |
| B-4 | Per-base configuration, readable and writable: missions, disposition traits, retrieval arms, entity vocabulary, consolidation behaviour, token budgets, feature flags. (H) |
| B-5 | Configuration is **reset-able** to the server's defaults, distinct from being overwritten with something. (H) |
| B-6 | Statistics, and an ingestion time-series, so an operator can see a base is growing and when it last changed. (H) |
| B-7 | Clearing a base's derived knowledge is distinct from deleting the base, and distinct from unlinking the corpus. (H) |
| B-8 | A base names the corpus it reconciles from, so two bases over one directory are distinguishable and independently reconcilable. (U) |

### The unified surface

The thesis of this document, as testable statements. Each is something a test
can fail.

| # | Requirement |
|---|---|
| U-1 | **One content type.** Authored corpus, retained content and derived pages are the same message with different `origin` and `location`, not three resource families. §4. (U) |
| U-2 | **One search.** A single query spans content, pages and facts, and every result carries its origin. There is no "search the wiki" versus "search the memory" that a consumer has to choose between before it knows what it is looking for. (U) |
| U-3 | **One read shape.** A consumer reads content without first asking where it came from, and learns the origin from the content. (U) |
| U-4 | **One browse.** A tree over the base shows authored and derived content in the same walk, each labelled. A person sees their documentation; an assistant sees the pages. (U) |
| U-5 | **Both audiences may write.** An assistant retains; a person writes a file; a person may also retain through the CLI or the API, and an assistant may also write a document. Neither is confined to its own half. (U) |
| U-6 | **Both audiences may edit the other's work.** A person curates an extracted fact; an assistant corrects a document; a person promotes a derived observation into the authored record by writing the file that states it, and their version then holds. §5.6. (U) |
| U-7 | **Origin is a filter, not a partition.** A caller can ask for the authored corpus alone, the accumulated memory alone, or both, and every filtering and search option works the same either way. (U) |
| U-8 | **A citation states its origin.** A result says whether it came from a file somebody wrote, a document somebody retained, or the system's own reasoning — because a citation that cannot tell a written decision from an inferred one is worth much less. (U) |
| U-9 | **The same read surface for a person and an assistant.** The generated CLI and the generated MCP tools are the same generator over the same contract, so a capability cannot exist for one audience and not the other. (U) |
| U-10 | **The corpus is addressable through the API, not only through the filesystem.** A person can list, read and write corpus content without leaving the tool, and an assistant can read documentation the same way it reads anything else. (U) |

### Content

| # | Requirement |
|---|---|
| C-1 | Content has an id, a title, a body, tags, an origin, a location, provenance and a revision. (U) |
| C-2 | `origin` is an enum: authored, retained, derived. It is set by how the content came to exist and is not chosen by a caller. (U) |
| C-3 | `location` discriminates: a path relative to the corpus root, a document identifier, or a position in the page tree with a backing model. One field, three shapes, because "where is it" is one question. (U) |
| C-4 | `provenance` records what produced the content and what it was derived from, and is returned on read. (U) |
| C-5 | `revision` carries a content digest and timestamps, so a consumer can tell whether it has seen a version. (U) |
| C-6 | **Mutability is declared and differs by origin**: authored content is edited at its source and reconciled in, retained content is curated, derived content is regenerated. The contract states the rule per origin rather than leaving a write that will be overwritten to be discovered. (U) |
| C-7 | Deleting content is distinct from unlinking it: a derived page can be deleted and will re-project, an authored file's content is removed by editing the file, and a retained document is deleted outright. (U) |

### The documentation half

The corpus is not an input to be consumed once. It is a body of documentation
that is read, browsed, searched, edited and reconciled, and it is the half a
person owns. See §5 for the design.

| # | Requirement |
|---|---|
| W-1 | A project names one or more directories of markdown as the corpus for a base. (W) |
| W-2 | Corpus content is listed and read through the API with the same calls as any other content, so documentation is a first-class thing and not a filesystem an API consumer has to be given separately. (U) |
| W-3 | The corpus is browsable as a tree, and its tree sits in the same walk as derived pages. (U) |
| W-4 | **Reconcile is the unit of work**: plan what would change, report it, and apply only what the user confirmed. There is no unconditional write. (W) |
| W-5 | A file's identity is declared, not inferred from where it happens to sit: the frontmatter `id` when the file has one, and a path-derived identifier otherwise. Both are computable from the file alone, so no state outside the base is required, and a **move** of a file that declares an id keeps its identity. (W) |
| W-6 | An unchanged file costs nothing: the plan compares digests against the content's recorded digest. (W) |
| W-7 | Each reconcile records which content it owns, and a prune only removes content that owner recorded. Two reconciles over one base cannot delete each other's work. (W) |
| W-8 | Frontmatter becomes tags — path segments, dates, and the author's own tags — so scope is a filter retrieval already supports rather than a directory convention the API must learn. (W) |
| W-9 | Authored text is ingested with entity resolution off, because a person writing a name means the name. (W) |
| W-10 | Files with no event time are ingested as timeless. (W) |
| W-11 | The plan names every file it would create, change, leave alone and delete, with digests, so the user reads what is about to happen. (W) |
| W-12 | Application is asynchronous, batched, and idempotent per batch, because a large corpus is thousands of extractions and cannot be a synchronous call. (W) |
| W-13 | Binary files under the corpus are ingested through the binary path. (W) |
| W-14 | **The corpus remains the source of truth.** Deleting a base deletes derived knowledge, not the files. Deleting a page deletes a projection. Nothing in the base is the only copy of anything a person wrote. (W) |
| W-15 | **The corpus is written by people, through their editor, and reviewed through Git.** Not through this API. An assistant never writes a corpus file, and neither does the subsystem on its own. A write that existed only in the base would be a second source of truth, and the next reconcile would delete it; a write that landed in the file behind somebody's editor would produce a conflict with their tooling. §10.2. (U) |
| W-16 | **A reconcile reports drift in the other direction too**: derived knowledge the corpus contradicts, so that a base's beliefs are visibly wrong when the documentation says otherwise. (U) |
| W-17 | **The base is disposable and rebuildable.** Deleting it and reconciling from the repository reproduces the corpus, and the operation is supported rather than merely possible. (U) |
| W-18 | **A commit corresponds to an index.** A reconcile records the commit it reconciled, and a base can be asked what commit it reflects, so "the index matches the merge" is a checkable statement. (W) |
| W-19 | **Reconcile runs on merged content, not on every local edit.** An uncommitted or unreviewed change is not knowledge the deployment should believe. (W) |
| W-20 | **The projection never flows back into the corpus.** Generated pages are not reconciled as authored content, so the system cannot accumulate its own output as input. (U) |
| W-21 | **Contradictions between two authored documents are preserved, not resolved.** An ADR that supersedes another is the corpus's own statement about which holds, and the reconcile passes both through with that metadata intact rather than picking one. (W) |

### The memory half — ingestion

| # | Requirement |
|---|---|
| I-1 | Retain arbitrary content, synchronously or queued, with a caller-supplied idempotency key so a lost acknowledgement is retried without duplicating work. (H) |
| I-2 | Content is a string **or** an ordered list of blocks, so an image or attachment sits inline where it actually appears. (H) |
| I-3 | A document identifier groups items into one document; re-retaining replaces it, or appends to it. (H) |
| I-4 | Per-item metadata, and a document-level metadata object. (H) |
| I-5 | Event time is settable, and **explicitly unsettable** for timeless material — a specification has no date. (H) |
| I-6 | Author-supplied entity names, with a mode that takes them literally rather than resolving them against existing entities. (H) |
| I-7 | Tags on every item, with five match modes and compound boolean expressions. (H) |
| I-8 | Binary files are ingested through a multipart path that reports an operation, not through a text field. (H) |
| I-9 | Previewing what extraction would produce, without storing it, with every prompt-affecting setting overridable for the call. (H) |
| I-10 | Previewing the prompts themselves, with no model call at all. (H) |
| I-11 | Bulk import and export of documents as an archive, asynchronously. (H) |

### The memory half — documents

| # | Requirement |
|---|---|
| D-1 | List documents with a total, filtered by identifier substring, tags, and a time window on a chosen time axis. (H) |
| D-2 | Read one document's metadata, its extracted-fact count per fact type, and its text where the deployment retains it. (H) |
| D-3 | A content digest per document, so a reconcile can skip what has not changed. (H) |
| D-4 | Re-tag a document without reprocessing its content, with the report that this invalidates and re-queues the observations it fed. (H) |
| D-5 | List and read chunks: the original text segments, their order, and whether they were truncated. (H) |
| D-6 | Reprocess a document on demand. (H) |
| D-7 | Delete a document and every fact extracted from it, reporting the count. (H) |
| D-8 | Replacing a document's content **changes the identifiers of the facts extracted from it**, so every observation and page derived from those facts is re-derived and a citation naming a fact is dangling. A reconcile reports that cascade: it is the most expensive consequence of an edit and the least visible. (H) |

### The memory half — facts and curation

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
| F-9 | A fact cites the content it came from, so a person correcting a wrong answer is handed the file to fix rather than a flag to raise. (U) |

### The memory half — observations

| # | Requirement |
|---|---|
| O-1 | Observations are derived, editable by refresh, and never silently overwritten. (H) |
| O-2 | List the distinct scopes observations live in, so a base with per-tag scoping is navigable. (H) |
| O-3 | Preview which scopes a draft consolidation configuration would claim, and which it would leave to the default. (H) |
| O-4 | Trigger consolidation on demand. (H) |
| O-5 | Recover a failed consolidation. (H) |
| O-6 | Clear all observations while keeping facts. (H) |
| O-7 | A base can disable observation consolidation entirely. (H) |
| O-8 | An observation names the facts it was consolidated from, so a belief can be checked against its evidence and a superseded fact's removal reaches the observation that cited it. (H) |

### The memory half — pages

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
| P-12 | A page is content of origin `derived`, and is therefore readable through the same call as anything else rather than through a page-only read. (U) |

### Querying both halves

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
| R-9 | Results carry provenance: the content, the chunk, the context, the entities, the timestamps, the per-stage scores, and the facts an observation was consolidated from. (H) |
| R-10 | Optionally return raw chunk text with the facts, for surrounding context. (H) |
| R-11 | Optional attachments, with their placeholders kept in the text. (H) |
| R-12 | Prefer consolidated observations over the raw facts they supersede, without losing result count. (H) |
| R-13 | An execution trace, so a retrieval that returned the wrong thing can be diagnosed. (H) |
| R-14 | **One query spans both halves.** A single search returns authored content, retained content and derived pages together, and origin filters narrow it. (U) |
| R-15 | Results are ordered by relevance across origins, not grouped by origin, so a caller asking one question gets its best answer whatever produced it. (U) |
| S-1 | Answer a question in prose from the base's contents, rather than returning passages for the caller to assemble. (H) |
| S-2 | The answer reports which facts it used, and which of them came from authored documentation. (H) |
| S-3 | A mission shapes how the answer is reasoned, and directives constrain it; neither affects retrieval. (H) |
| S-4 | Structured output against a caller-supplied JSON Schema, with a stated reason when it could not be produced — distinct from an answer that held nothing matching. (H) |
| S-5 | Tagged directives are scoped; untagged ones are global. (H) |
| S-6 | A mission, a question and its options are separately overridable per call. (H) |
| S-7 | An execution trace of tool and model calls, on request. (H) |
| S-8 | **Reasoning spans both halves**, and a caller can require that an answer be grounded in authored documentation only — for the question where "the system believes it" is not good enough and "we wrote it down" is. (U) |

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
| T-9 | A template is documentation about a base, and is exportable as content so it can be read, reviewed and diffed like anything else in the system. (U) |

### Operations

| # | Requirement |
|---|---|
| A-1 | Every asynchronous submission reports an operation identifier. (H) |
| A-2 | List and read operations, filtered by status, with a total. (H) |
| A-3 | Cancel a pending or running operation; retry a failed one; delete a terminal one. (H) |
| A-4 | An operation reports its type, progress, retry count, next attempt and error, so a failed backlog is diagnosable. (H) |
| A-5 | A client-supplied identifier makes a resubmission return the original operation, and a mismatched reuse is refused. (H) |

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
| N-11 | **Origin-specific behaviour is stated in the contract, not discovered.** A caller can tell from the contract what a write will do to each origin, because "edits are applied at the source and reconciled in" is a promise and a silent overwrite is a bug. (U) |
| N-12 | **Exact-term search is on by default for a corpus-backed base.** Technical prose is full of identifiers — `OAuth 2.1`, `ADR-014`, `CustomerID` — that semantic similarity alone does not reliably find, so a base reconciling a corpus keeps its keyword and text arms enabled. A deployment may turn them off having measured the cost; the point is that turning them off is a decision rather than a default. The backend exposes this as a toggle whose own default is not stated in its description, so the provider sets it explicitly for a corpus-backed base rather than inheriting it. (W) |
| N-13 | **The extraction mode is a per-base choice, and both modes are first-class.** Full extraction gives facts, entities and relationships, and is the default; a chunk-oriented mode stores the text as-is without model extraction, for a corpus that is read rather than reasoned over. Which one a base uses is recorded, because a base whose mode changed has different knowledge and a reader should be able to tell. (W) |


## 4. The content model

This is the type that makes one surface possible. If the two halves were two
resource families, "one API" would be a claim rather than a fact.

```proto
// Content is the one addressable, readable unit of knowledge in a base.
//
// Authored documentation, retained content and derived pages are all Content.
// They differ in `origin` and in where `location` points, and in nothing else a
// reader has to branch on — which is what lets a consumer read a person's
// runbook and a synthesized page through the same call.
message Content {
  // Stable identifier within the base.
  string id = 1;
  // Base this content belongs to.
  string base_id = 2;

  // How this content came to exist. Set by the operation that created it, and
  // never chosen by a caller.
  Origin origin = 3;

  // Human-readable title, used in listings and as a fallback for a missing body.
  string title = 4;

  // The content itself, as markdown. Empty for content whose text the
  // deployment does not retain; `has_body` says so rather than a caller
  // guessing from an empty string.
  string body = 5;
  bool has_body = 6;

  // Tags. Their meaning is the filter's, not the content's: this is what
  // retrieval scopes by, and what makes a directory convention a scope without
  // the retrieval side learning anything about directories.
  repeated string tags = 7;

  // Where the content is addressable. One question, three shapes.
  Location location = 8;

  // What produced this content, and what it was derived from.
  Provenance provenance = 9;

  // Digest and timestamps, so a consumer can tell whether it has seen a version.
  Revision revision = 10;

  // How this origin may be changed. See C-6 and §10.2.
  Mutability mutability = 11;
}
```

### 4.1 Origin

```proto
enum Origin {
  ORIGIN_UNSPECIFIED = 0;
  // Written by a person in the corpus, and edited there.
  ORIGIN_AUTHORED = 1;
  // Retained from a source: a conversation, an import, an attachment.
  ORIGIN_RETAINED = 2;
  // Synthesized and maintained by the system from other content.
  ORIGIN_DERIVED = 3;
}
```

Three values, and the point of the enum is what it is *not*: it is not a type
hierarchy. A caller does not hold an `AuthoredContent` and a `DerivedContent` and
have to know which one it got. It holds `Content` and reads `origin`.

The value is never settable by a caller. A person cannot mark their runbook as
derived, and an assistant cannot launder an inference into documentation. The
only way content becomes `AUTHORED` is a person writing it in the corpus.

### 4.2 Location

The field that makes one type work, because "where is this" has three honest
answers, and pretending otherwise would either lose the path or lose the tree.

```proto
message Location {
  // What kind of place this is.
  LocationKind kind = 1;

  // AUTHORED: a path relative to the corpus root. RETAINED: the document
  // identifier.
  string path = 2;

  // DERIVED: the folder path, the page name, and the identifier of the model
  // that maintains the body.
  string tree_path = 3;
  string page_id = 4;
  string backing_model_id = 5;
}
```

A consumer that wants to open a person's file gets a path it can show them. A
consumer that wants to know where a synthesized page sits in the tree gets a
tree path. Neither has to ask first.

### 4.3 Provenance

The record that makes a citation worth something, and that makes the two halves
distinguishable at query time.

```proto
message Provenance {
  // Which half produced this, as a value in the framework's reserved tag
  // namespace. A caller filters on it, and a deployment cannot collide with it
  // because the prefix belongs to the framework.
  string origin_tag = 1;

  // For RETAINED: the source. For AUTHORED: the file, and the revision it was
  // reconciled from.
  string source = 2;
  string source_revision = 3;

  // What this was derived from: the document, the facts, the backing model.
  repeated string derived_from = 4;
}
```

`origin_tag` and `derived_from` are what W-7 and F-9 need, and they are one
mechanism rather than two: ownership is a provenance tag, so pruning and
provenance cannot disagree.

### 4.4 Why one type, concretely

"One surface" is worth testing rather than asserting. Three consumers, one
content type:

| Consumer | Wants | Has to know origin? |
|---|---|---|
| An assistant answering "how do we deploy" | The runbook a person wrote, and what the system learned about deploying | No. It gets both, ordered by relevance, with origin on each result. |
| A person browsing the base | Their own documentation, and the pages the system maintains | Only to label what they are looking at. |
| A person fixing a wrong answer | The file to edit | Yes — and it is one field on the result, not a different search. |

The alternative shape, a corpus service and a memory service with their own
reads, fails the first row immediately and fails it in the way that matters: the
assistant would have to run two searches and merge them, with no shared ranking
and no way to say "only what we wrote down".

## 5. The documentation half

The corpus is not an input to be consumed once. It is a body of documentation
that is read, browsed, searched, edited and reconciled, and it is the half a
person owns.

### 5.1 The one direction that is not available

The memory backend projects a base onto a folder of markdown files, and keeps
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

### 5.2 What the corpus looks like

A directory tree of markdown, in a repository, reviewed like code:

```text
docs/
├── index.md                 # frontmatter: tags, title
├── architecture/
│   ├── overview.md
│   └── decisions/
│       └── 0001-one-way-corpus-sync.md
├── runbooks/
│   └── restore-a-base.md
└── glossary.md
```

Frontmatter is optional, and where present it is a contract rather than a
suggestion. A file with none is reconciled, and the path alone scopes it; a file
with frontmatter gets its identity, its kind and its status from there, and the
extraction step gets explicit signals instead of inferring everything from prose.

```markdown
---
id: wiki:architecture/authentication
title: Authentication
kind: architecture
status: active
authority: human
source: company-wiki
---

# Authentication

Our API uses OAuth 2.1. Access tokens expire after 60 minutes.
```

| Field | Required | Meaning |
|---|---|---|
| `id` | recommended | The stable identity, globally unique within the base. Overrides the path-derived identifier, and is what makes a **move** a move rather than a delete and a create. |
| `title` | optional | A display title where the filename is a poor one. |
| `kind` | optional | One of `architecture`, `policy`, `decision`, `procedure`, `reference`. Becomes a tag, so retrieval can be scoped to "the policies" or "the decisions". |
| `status` | optional | One of `active`, `deprecated`, `draft`. Becomes a tag, and is what lets a consumer discount a superseded document. |
| `authority` | optional | `human` for a person-written document. The default and the only value a person should write; it exists so a file states what it is rather than the deployment assuming it. |
| `source` | optional | Which corpus or repository this came from, when a base reconciles more than one. |
| `supersedes` | optional | Identifiers this document replaces. For decisions. W-21. |
| `owner`, `reviewed`, `date` | optional | Who owns it, when it was last reviewed, when it was decided. Metadata for a reader; nothing depends on them. |
| `source_commit`, `source_path` | optional | Which commit and path this version came from. Usually the reconciler records these rather than a person writing them. |

Two of these are load-bearing and the rest are conveniences:

- **`id` changes the identity rule.** Without it, identity is the
  repository-relative path and a move is a delete plus a create. With it, a move
  keeps the identity and every fact extracted from the file survives the move.
  Which is right depends on whether the corpus is reorganised, and a
  reorganised corpus with path identity silently re-extracts everything and
  orphans the facts — so the recommendation is `id` on anything that might move,
  and the reconciler says so when it sees a moved file without one.
- **`supersedes` is how authored conflicts stay authored.** An ADR that
  supersedes an earlier one is the corpus making its own statement about which
  holds. The reconciler does not act on it beyond carrying it as metadata and a
  tag: the contradiction is resolved by a person, in a file, in review — not by
  a synchroniser guessing. W-21.

`kind` and `status` are enums in the contract, not free strings, because
retrieval filters on them and a typo in a tag is a silently empty result.

What the corpus deliberately is **not**: a place to store derived knowledge.
Observations and pages are the system's, and writing them by hand would be
writing into a projection the next refresh would edit. If a person wants to state
something as fact, they write it in the corpus, and the base reconciles it like
anything else — which is §5.6.

### 5.3 The corpus is content, not a side channel

The requirement that makes this half a peer rather than a feeder (U-2, U-3,
U-10, W-2, W-3):

- Corpus files are `Content` with `origin = AUTHORED` and a path in their
  `location`. They are listed and read by the same calls that read a retained
  document or a derived page.
- The corpus tree appears in the same browse walk as the page tree, labelled by
  origin, so a person sees their documentation and the pages the system maintains
  in one place (U-4).
- A person **reads** corpus content through the same `ReadContent` they read
  anything else through, and through `ReadCorpusFile` when they want the file
  itself with its frontmatter. There is no documentation-only read, and no reason
  for one.
- A person **writes** corpus content through their editor and their Git review,
  not through here. W-15. This is the one asymmetry in the whole design and it is
  deliberate: a corpus file has an editor, hooks, a blame view and a review
  process, and a tool that wrote it behind their back would fight all four. It
  also means the tool never has to be trusted with someone's documentation, which
  is a much easier property to reason about than "it only writes files you
  approved".
- An assistant reads documentation through the same `ReadContent` it reads
  anything else through, and **never writes it**.

### 5.4 Reconcile

`PlanReconcile` walks the configured roots, computes each file's identity and
digest, compares against what the base already holds, and returns four lists. It
reads and it never writes. `ApplyReconcile` takes the plan, a confirmation value,
and does the work asynchronously.

| | Files | Cost |
|---|---|---|
| `created` | not in the base | extraction, embedding, consolidation |
| `updated` | present, digest differs | extraction, embedding, consolidation; dependent observations re-derived |
| `unchanged` | present, digest matches | nothing |
| `deleted` | recorded by this owner, no longer on disk | fact removal; dependent observations re-derived |

Four decisions inside that, each of which is a choice rather than a detail:

**Identity is read from the file, never looked up.** W-5. The frontmatter `id`
when there is one, and a path-derived identifier when there is not. Either way
it is computable from the file alone, which is what makes the reconcile
stateless. The alternative — an index file mapping paths to identities, which is
what a synchroniser for a different corpus keeps outside the synced tree —
introduces a second thing that can be lost and two indexers that can disagree.
Reading identity from the file means a lost index costs nothing: the next plan
recomputes it and a digest comparison says what is unchanged.

The two cases behave differently on a **move**, and that difference is the whole
reason `id` exists:

| | No `id` | With `id` |
|---|---|---|
| Edit | digest differs, content replaced | same |
| Move | **delete + create**; facts re-extracted, the old ones pruned | identity unchanged; facts follow the content |
| Rename | delete + create | identity unchanged |

A corpus that gets reorganised and has no `id` fields silently re-extracts
everything and orphans every fact that referenced the old paths. The reconciler
reports a moved file with no declared identity as `moved` rather than treating
it as two unrelated files, so whoever reorganised the tree finds out.

**Replacement really is a replace, and that is what we want — with a caveat worth
naming.** A repeated `document_id` on the backend *replaces*: the old document
and its facts are deleted and re-extracted rather than appended to. That is
correct for a corpus, because an edited runbook must not leave the previous
version's facts retrievable — an assistant answering from a superseded sentence
is worse than one that answers from nothing. The caveat is D-8: the facts get new
identifiers, so everything consolidated from them is re-derived and a citation
naming a fact goes dangling. The reconcile reports the cascade rather than leaving
it to be found.

**A commit is recorded, and is not identity.** W-18. Every reconcile records the
commit it reconciled against, on the base and on each document it touched, so
"the index matches the merge" is a question with an answer. It is deliberately
*not* part of identity: a commit is a property of a run, not of a document, and
putting it in identity would mean every commit re-extracts the whole corpus.
The provenance chain a consumer walks is memory → document → path → commit →
markdown, and every link in it is a field rather than a lookup.

**Diffing is by content digest, never by timestamp.** W-6. Filesystems lie about
timestamps; digests do not, and the content already carries one.

**Ownership is recorded, and pruning respects it.** W-7. Each reconcile writes
its own marker into the content metadata it creates, and a prune removes content
only if that marker names the owner doing the pruning. Without this, reconciling
a second directory into a base would delete the first one's documentation — the
failure mode a two-reconciler deployment discovers by losing data.

**Frontmatter and path become tags.** W-8, W-9, W-10. Tags are what retrieval
already filters on, so a directory convention becomes a scope without the
retrieval side learning anything about directories. The author's own `tags:` are
carried through rather than flattened into the framework's namespace, because a
person's taxonomy and the framework's are different vocabularies.

### 5.5 Confirmation, and why it is a field

`PlanReconcile` returns a plan. A person reads it. The agent relays the decision
back as a value on `ApplyReconcile`.

Three rules converge on this shape, and all three are worth naming:

- **A change to what a project says about itself is the user's to confirm**
  (AGENTS.md rule 17). A corpus is not a configuration file, but the reasoning
  carries over: reconciling a thousand files changes what every assistant in the
  deployment believes, and a person should see that happening.
- **A rule that must reach a human has to work on any transport** (rule 18). The
  HTTP MCP endpoint is stateless and cannot elicit. A prompt would therefore be a
  rule that holds only where the transport can ask, which is not a framework
  rule. A value the agent relays and the user answers works everywhere.
- **It has to be a field, not a two-call handshake**, so the proposal and the
  approval cannot be reordered. The same shape `AddSkill` already uses, for the
  same reason.

The same applies to `ImportTemplate` (T-4), for the same reasons.

`PlanReconcile` and `ApplyReconcile` are also where W-19 lands: a reconcile runs
against **merged** content, not against a working tree. A base that indexed an
uncommitted change would believe something no reviewer has seen, and the
reproducibility property in §1.1 would be a claim rather than a fact. A
deployment that wants a local preview runs the plan, which is a read, and sees
what would change without any of it being believed.

### 5.6 Promotion: how the human loop closes

The half that makes this a system rather than two stores. A person reads
something the system inferred, and wants it to be authoritative. Three ways, in
increasing order of ceremony:

1. **They correct the fact.** `CurateMemory` (F-3). The fact's text changes and
   everything derived from it re-derives. The correction lives in the base and
   nowhere else.
2. **They write the file.** They add a page to the corpus stating it. The next
   reconcile ingests it as `AUTHORED`, and from then on the base can cite a
   person for the claim rather than an inference. The derived version is not
   deleted — it is superseded, and F-9 means a reader can see that the
   authoritative version now exists.
3. **They do nothing.** The inference stays, and stays labelled as one.

The direction matters and is not symmetric: **the corpus wins.** An authored file
that contradicts a derived observation is not a conflict to be resolved
symmetrically; the documentation is the record of what the people decided, and a
model's belief about the same thing is a hypothesis about a document it has read.
That is why W-16 asks a reconcile to report derived knowledge the corpus
contradicts — the drift a person most wants to know about is the base believing
something their own documentation says otherwise.

### 5.7 Two kinds of contradiction, handled differently

This is worth separating, because the two cases have opposite right answers and
a design that treats them alike gets one of them wrong.

**Authored against derived — the corpus wins.** A file states something; the
system inferred something else. A person, in review, decided. The derived claim
is superseded, not deleted, and the corpus is now citable for it.

**Authored against authored — nobody wins, and the synchroniser must not try.**
An ADR says use PostgreSQL; a later ADR says migrate to something else. Both are
in the corpus, both are true of some moment, and the question of which holds is a
question about the organisation, not about the text. A synchroniser that picked
one would be making that decision silently, and the decision is the expensive
one. So both are indexed, both are retrievable, and the corpus's own metadata
does the work:

- `status` says whether a document is active, deprecated or a draft.
- `supersedes` says which decision replaced which, written by a person in a
  reviewed file.
- The commit each version was reconciled from says which is newer in practice,
  though newer is not the same as current — a document can be revised to say
  something older.

A consumer that needs one answer retrieves both and applies the metadata. An
engine that consolidates them into a single "the database is X" observation has
answered a question nobody asked, and the observation is the thing a person then
has to unpick. This is why the reconcile reports rather than resolves (W-21), and
why the tags carry `status` and `kind` through rather than flattening them.


## 6. The contract

### 6.1 Services

Ordered so the unified surface comes before the machinery behind it, because
that is the order a consumer meets them in. Every RPC declares its side effects;
that is not a formality, because the declaration is what a policy author reads
and what the MCP gateway gates on.

| Service | RPCs |
|---|---|
| `ContentService` | `ListContent`, `GetContent`, `WriteContent`, `CurateContent`, `DeleteContent`, `GetContentTree`, `ListContentChunks`, `ReprocessContent` |
| `QueryService` | `Search`, `Recall`, `Reflect`, `ListTags`, `PreviewExtraction`, `PreviewPrompts` |
| `CorpusService` | `PlanReconcile`, `ApplyReconcile`, `GetCorpusStatus`, `ReadCorpusFile`, `RebuildCorpus` |
| `KnowledgeBaseService` | `ListBases`, `GetBase`, `CreateBase`, `UpdateBase`, `DeleteBase`, `ResetBaseConfig`, `GetBaseConfig`, `UpdateBaseConfig`, `GetBaseStats`, `GetBaseIngestionSeries`, `ListBaseAliases`, `AddBaseAlias`, `SetPrimaryBaseAlias`, `RemoveBaseAlias` |
| `MemoryService` | `GetMemory`, `CurateMemory`, `GetMemoryHistory`, `GetMemoryGraph` |
| `PageService` | `CreatePageFolder`, `CreatePage`, `UpdatePageNode`, `RefreshPage`, `PreviewPageRefresh`, `ExportPageBundle` |
| `MentalModelService` | `ListMentalModels`, `CreateMentalModel`, `UpdateMentalModel`, `DeleteMentalModel`, `GetMentalModelHistory`, `ClearMentalModel` |
| `DirectiveService` | `ListDirectives`, `CreateDirective`, `GetDirective`, `UpdateDirective`, `DeleteDirective` |
| `ObservationService` | `ListObservationScopes`, `PreviewConsolidation`, `TriggerConsolidation`, `RecoverConsolidation`, `ClearBaseObservations` |
| `EntityService` | `ListEntities`, `GetEntity`, `GetEntityGraph` |
| `TemplateService` | `GetTemplateSchema`, `ExportTemplate`, `ImportTemplate`, `ExportBase`, `ImportBase`, `CloneBase` |
| `OperationService` | `ListOperations`, `GetOperation`, `CancelOperation`, `RetryOperation`, `DeleteOperation` |

Three things in that table are the unification, and each is worth reading
against the shape it replaces:

- **`ContentService` has no per-origin services under it.** The reads that used
  to be `GetDocument`, `GetPage` and a corpus read are now `GetContent` (U-3).
  `ListContentChunks` and `ReprocessContent` remain because chunks and
  reprocessing are things a *retained* document has and an authored file or a
  derived page does not; they are origin-specific and are named as such.
- **`PageService` lost its read methods.** `GetPageTree` became
  `GetContentTree`, which walks authored and derived content together (U-4), and
  `SearchPages` became `Search` with a scope (U-2). What is left on `PageService`
  is what is genuinely page-specific: creating, configuring and refreshing a
  projection.
- **`MemoryService` lost its reads and its query.** `GetMemory` and `CurateMemory`
  stay because a fact is not content — it is a statement the system extracted, and
  reading a fact and reading the document it came from are different questions at
  different fidelities. `Recall` became `QueryService.Recall` because it is a
  query, not a storage concern.

`CorpusService` exists at all because reconcile is not a content operation: it
is a comparison between a directory and a base, and the answer is a plan.

It has no write. W-15: the corpus is written by people, through their editor,
and reviewed through Git. `ReadCorpusFile` exists so a person can read
documentation through the same tool they read everything else with, and
`RebuildCorpus` exists so the disposability property in §1.1 is an operation
rather than a promise — drop the base, rebuild it from the repository, and
compare.


### 6.2 Vocabulary and side effects

| RPC group | `@toolbox.side-effects` | Why |
|---|---|---|
| `List*`, `Get*`, `Preview*`, `Export*`, `SearchPages` | `read_only` | Nothing is written. Previews report what a write *would* do, which is why they are reads and why T-4 is a read. |
| `Retain`, `Create*`, `Update*`, `Add*`, `Set*`, `Trigger*`, `Recover*` | `create update` | |
| `Refresh*`, `Preview*` on a model that has a side effect | `update` | A refresh rewrites a document, so it is a write even though its input is a read. |
| `Delete*`, `Remove*`, `Cancel*`, `Clear*` | `delete` | |
| `PlanReconcile` | `read_only` | It reads the corpus and the base and writes nothing. |
| `ApplyReconcile` | `create update delete` | The only operation in the contract that can create, update and delete in one call, and so the one whose confirmation matters most. |
| `RebuildCorpus` | `create update delete` | Drops the base's derived knowledge and reconciles the corpus again. The only operation that destroys anything on the documentation side, and therefore the one §15 question 3 is about. |
| `ImportTemplate` | `create update` | Applies configuration and defines directives; the content it defines is generated, not authored. |
| `ImportBase`, `CloneBase` | `create update delete` | Both can replace an existing base's contents. |

### 6.3 Message rules

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

### 6.4 Errors

| Condition | `api.ErrorKind` | ConnectRPC |
|---|---|---|
| Malformed or missing required field | `KindInvalid` | `InvalidArgument` |
| No such base, content, page, operation, fact | `KindNotFound` | `NotFound` |
| Duplicate page name in a folder; reused operation identifier | `KindAlreadyExists` | `AlreadyExists` |
| A write against content whose origin is not writable that way (C-6) | `KindFailedPrecondition` | `FailedPrecondition` |
| Feature the deployment reports disabled (N-7) | `KindUnsupported` | `Unimplemented` |
| Backend unreachable, timed out, or failing | `KindUnavailable` | `Unavailable` |
| Anything else | `KindInternal` | `Internal` |

The `KindUnavailable` case carries the distinction `pkg/core` already makes and
that a provider must not flatten: **not deployed** and **not answering** are
different facts with different fixes, and a provider that reports both as
"unavailable" has thrown away the diagnosis.

The `KindFailedPrecondition` row is the one the unified surface introduces and
it is load-bearing. `WriteContent` is one operation (U-5), and the three origins
answer it differently (§10.2), so a write that cannot mean what the caller asked
for is a precondition and not an invalid argument — the request was well formed,
the content just does not accept that kind of change. It is also the answer that
keeps the documentation half safe: an assistant that tries to write a derived
page is told that pages are regenerated, and an assistant that tries to write
somebody's runbook is told it is edited at its source, rather than either
succeeding and being overwritten later.

The attachment endpoint (R-11) is the one place where two different conditions
must produce the *same* answer (N-8), because the backend does that deliberately
to stop the endpoint being used to probe what a base holds.

## 7. One query over both halves

This is U-2 stated as a contract, and it is the requirement most likely to be got
wrong by accident — because the backend offers a fact search and a page search as
two calls, and the easy thing is to expose them as two and let a consumer choose.

### 7.1 Search

`Search` spans content and returns `Content`. A person's runbook, a retained
conversation and a synthesized page are the same result type, ranked against
each other (R-15), with origin on each. The backend's page-level search and its
fact-level search are both reached through this, because the caller cannot and
should not know they were two.

### 7.2 Recall and Reflect

`Recall` and `Reflect` are about *facts* rather than *documents*, and stay
separate, for a reason worth stating rather than assuming: a caller asking "what
do we know about retries" usually wants the passages to read, a caller asking
"why do we do it this way" usually wants the document that explains it, and a
caller asking "should we change this" wants prose. Collapsing all three into one
call would mean every caller pays for the reasoning it did not want.

They still span both halves, because a fact's origin is a fact's origin: a fact
extracted from a runbook and a fact extracted from a conversation are the same
kind of thing, and `origin` on the result says which.

The backend's four retrieval arms are an implementation strategy. The contract
states what a caller gets, not how, with one exception worth making: the caller
*can* ask for a subset, and that is part of the interface.

```proto
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

  // Restrict to one or more origins. Unset means all of them, which is the
  // point: the common case is not choosing (U-7).
  repeated Origin origins = 10;
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

## 8. Pages

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

## 9. Directives and base configuration

Two things a person writes that the base keeps verbatim and never rewrites.
They are the hand-authored part that is not corpus, and §10.3 says why they are
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

## 10. The two halves, and where they differ

### 10.1 Where they are the same

Everything in the read path, and that is most of the surface. A caller lists
content, reads content, browses a tree and searches — and origin is a field on
what comes back rather than a choice made before the call. Both halves share
tags, both are filtered the same way, and both are cited the same way with
origin stated.

The write path is shared too, with one branch. An assistant retains; a person
writes a file or uses `WriteContent`, which lands in the file; an assistant can
also write a document; a person can also retain. The branch is not "which
audience" but "which origin", and origin is a property of the content rather
than of the caller (U-5).

### 10.2 Where they differ, and why that is not a failure

Three things do not unify, and each is a real difference rather than an
inconsistency to paper over.

**Mutability (C-6, N-11, W-15).** Authored content is edited at its source —
in the file, reviewed through Git — and reconciled in from there. Retained
content is curated. Derived content is regenerated and cannot be written at all.
There is exactly one writable origin, and it is the one the deployment's users
write in their editor.

A single `WriteContent` therefore does not mean all three, and a uniform one
that pretended to would either lose a person's edit or hand an assistant a way to
overwrite somebody's documentation. So `Mutability` is on the content, the
contract states the rule per origin, and a write against the wrong origin is
refused with `FailedPrecondition` and the reason — which is a better answer than
accepting it and discarding it later, and a better answer than granting the
write at all.

This is the one place the "both audiences may write" claim in §1.3 is narrowed,
and the narrowing is a decision rather than an omission: both audiences may
write *knowledge*, and only people may write *documentation*.

**Granularity of extraction.** A fact is not content. It is one statement the
system pulled out, it exists only for retained and authored material, and
reading a fact and reading the document it came from are different questions at
different fidelities — one is the extracted claim, the other is the text. So
`GetMemory` stays separate, and so do chunks.

**Where the body comes from.** A derived page's body is generated. An authored
file's body is the file. A retained document's body may or may not be retained,
depending on a per-base setting. `has_body` exists for that last case, because
an empty string and "we did not keep it" are different answers and a consumer
that cannot tell them apart will render a blank page and call it a bug in the
base.

### 10.3 Configuration is a third path, and stays separate

There are two kinds of hand-authored material in a system, and the second is
not prose.

**Prose → content.** §5. It becomes facts, is consolidated, is reconciled, and is
retrievable. What comes back is facts in the document's own words, not its whole
content — extraction is lossy and non-deterministic, and that is accepted rather
than hidden. Where a deployment wants the authored words back byte-for-byte, the
file is the answer, not the base (W-14).

**Configuration → a template.** A template is hand-authored, versioned,
schema-described, diffable, verified before it is applied, and never rewritten.
It is the artifact that belongs in a repository next to the deployment's
configuration, and it is what "export this base's setup" produces.

These are separate operations precisely because they are stored differently.
Prose is extracted into facts and reconciled; a directive is stored as written
and injected. A reconcile that also set configuration would put authored rules
through a lossy extraction, which is the one thing a directive must not be put
through.

It is also the only place a person states *how the base should think* rather than
*what it should know*, and those deserve different review — which is why a
template is readable as content (T-9) but is not reconciled as prose.

## 11. What each audience gets

### 11.1 The same surface

U-9: the generated CLI and the generated MCP tools are the same generator over
the same contract, so a capability cannot exist for one audience and not the
other. There is no "knowledge features for agents" and "knowledge features for
people" — there is one set of operations, reachable two ways, and the difference
between the two is which policy is in force and who is asking.

**Shared, and what an assistant gets by default:** `Search`, `Recall`,
`Reflect`, `ListContent`, `GetContent`, `GetContentTree`, `WriteContent`,
`CurateContent`, `GetMemory`, `GetMemoryHistory`, `ListTags`, `ListOperations`,
`GetOperation`, `ListBases`, `GetBase`, `ListDirectives`, `ListMentalModels`,
`PlanReconcile`, `ApplyReconcile`.

**Documentation, and what a person gets that an assistant usually should not:**
`ReadCorpusFile`, `GetCorpusStatus`, `RebuildCorpus`. The second is the one worth
naming: a person needs to know whether their documentation is reconciled, which
commit the base reflects, what the plan would do, and what the base believes that
their files contradict (W-16, W-18). An assistant can be given it and usually has
no use for it. There is no corpus write on this list or any other, because there
is no corpus write (W-15).

**Operator surface, offered to neither by default:** `DeleteBase`,
`DeleteContent`, `DeletePageNode`, `DeleteMentalModel`, `DeleteDirective`,
`ClearBaseObservations`, `ImportTemplate`, `ExportBase`, `ImportBase`,
`CloneBase`, `RecoverConsolidation`, and everything to do with aliases, webhooks,
audit logs and traces.

### 11.2 What decides the split

Two framework rules, not preference. A contract's declaration says what invoking
a method does; a policy says who may invoke it; neither is derived from the
other, and the empty policy permits nothing. So every RPC above is discovered and
classified whether or not it is ever exposed, and exposure is a policy decision
made per deployment.

And the confirmation of §5.5 is a **value the agent relays**, not a prompt the
transport asks for — because the stateless HTTP endpoint cannot elicit, and a
rule that held only where it could would not be a framework rule. That applies to
`ApplyReconcile` and to `ImportTemplate`: both change what the deployment
believes, so both propose and a person decides.

The corpus case does not need this treatment because it is not offered. An
assistant with a write on somebody's documentation would be able to change what
the project says about itself, and no confirmation value makes that a good idea —
so the write does not exist, and the rule that would have governed it is
unnecessary rather than implemented.

## 12. Where the behaviour lives

- **`pkg/knowledge`** — the content model, the engine interfaces the contract's
  messages convert to, and the reconcile. This is where the behaviour is, because
  the framework needs it in process. The reconcile belongs here rather than in
  the provider because it is pure computation over a directory and a set of
  digests, with no backend in it at all — which is what makes it testable offline,
  and §15 question 3 turns on whether it should be its own subsystem instead.
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
provider is better than two, one of which is a fiction. §15 records this as a
decision to confirm rather than an open question.

The backend is reachable three ways without any of this, and they are worth
stating because they are cheaper than the above and they are how a deployment
should evaluate whether to build any of it: its OpenAPI description can be
registered in the API catalog, its own MCP server can be exposed, and its
documented ingestion semantics are the ones §5 specifies. A deployment that
wants the capability this month can have it without a line of the code above.
That is a legitimate outcome, not a failure of the design.

## 13. Failure and degradation

| Situation | Behaviour |
|---|---|
| Backend unreachable at startup | The provider fails to start, naming the endpoint and the failure. It does not start and serve empty results, because a base that reports "no knowledge" when it is disconnected is the worst possible answer. |
| Backend unreachable during a call | `KindUnavailable`, and the error distinguishes not-deployed from not-answering. |
| A capability is disabled | `KindUnsupported` naming the flag (N-7), not a 404. |
| A reconcile partially fails | Per-file outcomes in the operation's report; the operation is failed, not silently short. |
| A corpus file is unreadable | Named in the plan as skipped, with the reason. A permission error on one file is not a reason to fail a thousand-file reconcile. |
| A page's refresh fails | Recorded on the node, and the page does not rebuild while it is set. A base with one broken page is not a base that has silently lost a page. |
| A reconcile is interrupted | Idempotent per batch (W-12), so a rerun resumes rather than duplicating. |
| A file is edited between the plan and the apply | The apply uses the digests from the plan and reports any file whose digest no longer matches, rather than reconciling a version nobody saw. A confirm that approved one thing must not quietly apply another. |
| A corpus file is edited while the base is serving | No effect on reads: the base holds what it reconciled and `revision` says which version. The next plan reports the drift. Read-your-writes across a filesystem is not a property this can offer, and pretending otherwise would be worse than saying so. |
| A corpus file is edited, so its facts are replaced | **Fact identifiers change.** A replaced document's facts are deleted and re-extracted, so every observation consolidated from the old ones, and every page built from those observations, has to be re-derived — and any citation that named a fact identifier is now dangling. The reconcile reports the scope of that cascade rather than leaving a caller to discover it, because it is the operation's most expensive consequence and its least visible one. |

## 14. What is deliberately not here

- **A second provider.** One backend, one provider. A second is a second
  provider behind the same contract, and it is not worth designing for before
  there is a second.
- **Writing prose into a page.** §5.1.
- **Two read surfaces, one per half.** The whole point of this subsystem is that
  there are not. §4.4 states the shape that fails, so this is recorded as a
  rejected alternative rather than an open question.
- **Webhooks, audit logs, trace logs, metrics.** Operator and infrastructure
  surface, reachable through the backend directly.
- **Memory defence.** A real feature with its own policy implications, worth its
  own investigation rather than a paragraph here.
- **Streaming.** N-9.

## 15. Open questions

Two of these were open in the previous revision and are now settled by the
integration design, so they are recorded as decisions rather than questions. The
rest are open, ordered by how much they change the contract's shape.

**Settled — the corpus is not writable through the API** (W-15). A corpus file
has an editor, hooks, a blame view and a review process, and a tool that wrote
it behind someone's back would fight all four. It also means the tool never has
to be trusted with a person's documentation, which is a far easier property to
reason about than "it only writes files you approved". The previous revision
listed this as a question and proposed `WriteCorpusFile`; both are gone.

**Settled — a file's identity comes from its frontmatter when it has one**
(W-5). A path alone is not identity for a corpus that gets reorganised, because
a move would then be a delete and a create and every fact would be orphaned. The
reconciler reports a moved file with no declared identity rather than treating it
as two files, so the omission is visible.

1. **Is `Reflect` a knowledge operation or an agent operation?** It runs a model
   loop over the base. `subsystems/agent` exists. Getting this wrong puts a
   second model-calling path in the framework, which is a boundary worth deciding
   deliberately rather than by default. It is the largest remaining question,
   because reasoning over documentation is something a person wants too — and if
   reasoning belongs to the agent subsystem, this subsystem's answer to "should we
   change this" is a search, which is a smaller promise than this document makes.
2. **Does the corpus live in this subsystem or a sibling?** A sibling serving a
   corpus contract and calling this one over ConnectRPC obeys the
   cross-subsystem rule more literally, and makes the reconcile testable with no
   backend at all — the reconcile is pure computation over a directory and a set
   of digests. One subsystem is fewer modules, and one surface is the thesis.
   These pull in opposite directions and the thesis is winning, but the testability
   argument is the strongest thing anyone could raise for the other side.
3. **What does `RebuildCorpus` do when the corpus and the base disagree?** W-17
   says a base is rebuildable, which implies dropping and reconciling. Whether it
   is allowed to do that in place, or must refuse when the base holds retained
   content that did not come from the corpus, is unresolved — and it matters,
   because a base shared between a corpus and an assistant's conversation memory
   cannot be rebuilt without losing the second half.
4. **Is a moved file without an `id` a warning or an error?** W-5 says the
   reconciler reports it. Whether that is a note in the plan or a refusal is a
   decision about how strict this framework is about its own frontmatter
   contract, and the answer probably differs between a project that never moves
   files and one that reorganises quarterly.
5. **Where does `ClearBaseObservations` live** — `KnowledgeBaseService` or
   `ObservationService`? It clears derived knowledge from a base, so the base is
   the subject; it is also one of the observation operations. Pick one.
6. **Are pages and mental models one service or two?** They are one object with
   different defaults and the distinction is invisible in the API. Two services
   is more faithful and costs a concept; one is cheaper and loses the distinction
   a reader of the contract may want.
7. **Provenance tag namespace.** Reserved prefix and value spelling. It should be
   a framework decision, because a deployment that guesses differently cannot
   filter across two bases, which is the one capability the unified surface exists
   to provide.
8. **Should a base's own configuration be project configuration?** A base's
   missions and directives are authored material, and a project configuration
   file is where this framework puts authored material. If they merge, importing
   a template becomes a rule-17 operation on the project's configuration file,
   which is a stronger reason to confirm than §5.5 already gives.

## 16. What a change would touch

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
§15 items 1–5, which are the ones that change the contract's shape. Then
`pkg/knowledge` and the client, tested against the real API description rather
than a live server so the suite stays deterministic and offline. Then the
provider. Then ingest and templates, which are last because they are the parts
that involve a person and the parts most likely to change shape after a first
real use.

## 17. Sources

- **The integration design this implements**:
  [`investigations/hindsight-human-wiki-integration.md`](investigations/hindsight-human-wiki-integration.md).
  It settled the corpus layering, the source-of-truth invariant, the frontmatter
  contract, the commit-correspondence property, the two ingestion modes, and the
  retrieval authority directives. Where this document differs it is because the
  backend's machine-readable description was consulted and the design's open
  question about replacement semantics has since been answered — see §5.4 and
  D-8.
- The investigation that preceded both:
  [`investigations/hindsight-knowledge-backend.md`](investigations/hindsight-knowledge-backend.md)
- Hindsight's machine-readable API description (0.10.1) and its documentation
  site, for every claim about the backend's behaviour. Structural claims in
  particular — the absence of a writable page body, the response-only provenance
  flag, the request shapes, and the `replace` default on a repeated document
  identifier — were checked against the description rather than the prose,
  because the prose is written for humans and the description is what the
  backend will actually accept.

### One open question in the source design, answered here

The integration design says, correctly, to verify the backend's update semantics
before building a production sync and not to assume old facts disappear. Checked
against the description for 0.10.1, which settles it:

> If a memory item has a `document_id` that already exists, the old document and
> its memory units will be deleted before creating new ones (upsert behavior).

and on the item itself, `update_mode` defaults to `replace`, which *"deletes old
data and reprocesses from scratch"*, with `append` as the alternative.

So old facts **do** disappear, which is what a corpus sync wants. The
consequence the design document does not draw is D-8: because the facts are
deleted and re-extracted, their identifiers change, so observations consolidated
from them are re-derived, pages built on those observations go stale, and a
citation naming a fact identifier goes dangling. A synchroniser that treats a
retain as a write and ignores the cascade ends up with a base that looks
consistent and cites nothing.
- [`feature-spec.md`](feature-spec.md) for the product-level framing this
  implements.
- [`architecture.md`](architecture.md) and
  [`decisions/0005-reusable-packages-behind-thin-providers.md`](decisions/0005-reusable-packages-behind-thin-providers.md)
  for the boundaries a provider subsystem lives inside.
