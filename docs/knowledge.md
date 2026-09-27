# Knowledge

**Status: specification. Nothing here is implemented.** This document specifies a
knowledge base subsystem that does not exist yet, replacing the placeholder that
was removed in the same change. Where it states a decision it is a decision this
document makes and a later change is expected to follow; where it states an open
question it is listed in §15 rather than answered.

## What the backend is

**Hindsight** (<https://hindsight.vectorize.io>, HTTP API **0.10.1**, machine-readable
description at `/openapi.json`) is a memory backend written by Vectorize, not by
this project. It is the only external system this specification depends on, and
everything below is written against it.

You hand it documents. It runs an LLM over them, breaks each one into individual
**facts**, links those facts to **entities** and to each other, and later
**consolidates** clusters of related facts into deduplicated **observations** that
carry their evidence. It answers questions by running four retrieval strategies
in parallel — semantic vector search, keyword search, entity-graph traversal, and
temporal search — fusing them with reciprocal-rank fusion and reranking
(Hindsight calls the combination TEMPR). On top of that it will **reflect**: given
a question, it writes a document answering it and keeps rewriting it as the bank
changes. That stored document is a **mental model**, and a mental model placed in
a folder tree and configured with a trigger is a **knowledge page**.

```text
your files ──retain()──▶ Hindsight ──LLM──▶ facts ──consolidate──▶ observations
                                                               │
                                                     reflect (your question)
                                                               ▼
                                                    mental model = knowledge page
```

The vocabulary Hindsight uses, and what this document calls it instead:

| Hindsight | This document | Note |
|---|---|---|
| bank (`bank_id`) | **base** | the isolation unit: its own memories, config, pages, directives |
| memory unit | **fact** | one extracted statement, typed `world` or `experience` |
| observation | **observation** | consolidated, deduplicated, evidence-backed belief |
| mental model | **mental model** / **page** | a synthesized document answering one question |
| directive | **directive** | hand-written rule the reasoning step must follow |
| retain / recall / reflect | `WriteContent`+reconcile / `Recall` / `Reflect` | the contract is provider-neutral (§2) |
| `managed`, `delta`, `all_strict`, TEMPR, disposition traits | *not used* | §2 explains why these stay out |

Three properties of that backend shape this entire specification, and each is
verified against the API description rather than the prose:

1. **Nothing a person wrote can be stored in a knowledge page.** A page has no
   writable body field anywhere in the public surface. §5.1.
2. **Re-retaining a `document_id` replaces it** — the old document and its facts
   are deleted and re-extracted. D-8, §5.4.
3. **A page's `tags` filter what it is built from**, and default to `all_strict`,
   so a page tagged with descriptive labels it was not given matches nothing.
   §8.

If you want to read it yourself: the concept page
[`/developer/knowledge-pages`](https://hindsight.vectorize.io/developer/knowledge-pages),
the API reference under `/developer/api/*`, and
[`/developer/observations`](https://hindsight.vectorize.io/developer/observations)
for the consolidation model. There is no `/developer` index page; the paths are
listed in the site sidebar.

The investigation that led here — the API survey, the reasoning about
preloading, and the three placement options — is kept in
[`investigations/hindsight-knowledge-backend.md`](investigations/hindsight-knowledge-backend.md).
This document supersedes it where they differ. The corpus-side design it also
implements is
[`investigations/hindsight-human-wiki-integration.md`](investigations/hindsight-human-wiki-integration.md).

---

## 1. What this subsystem is for

An assistant that forgets everything between sessions can only know what the
caller pastes in. A person looking for the runbook can only find it if they
remember that it exists. This subsystem is **one base both of them read from and
write to**: the wiki people write, the facts and observations the system derives
from it and from conversation, and the pages it renders from those — behind the
same calls.

### 1.1 The three layers

Three kinds of thing, written by three different parties. Telling them apart
correctly is most of the work.

|  | The authored corpus | The memory engine | The projection |
|---|---|---|---|
| **What it is** | What people wrote: architecture notes, runbooks, decisions, policies | What the system derived from it and from conversation: facts, entities, observations, models | Readable documents rendered from the engine's current beliefs |
| **Who writes it** | People, in files, reviewed by other people | The system, from retained content | The system, on a refresh |
| **Stored as** | Markdown in a Git repository | The memory backend's indexes | Backend pages, and a markdown export of them — see the whole-wiki view in §5.8, which puts all three layers in one tree |
| **What makes it trustworthy** | Authorship and review. A named person wrote it and someone accepted it | Evidence. Every fact traces to a document, every observation to its facts | Nothing on its own. It is what the system believes *now* |
| **If deleted** | Nothing to rebuild from — this is the loss | **Rebuilt in full from the corpus** | Rebuilt from the engine |
| **What it cannot do** | Notice that a decision was reversed last month | Show you the sentence someone wrote **if the deployment turned raw-text persistence off**, which no default is stated for | Be quoted as authority |

The middle column is worth reading twice, because it was wrong here and D-2 and
D-5 said otherwise. The engine **does** hold a path back to the words: the client
has `GetDocument`, `GetChunk` and `ListDocumentChunks`, so a caller can be shown
the sentence somebody wrote. What it cannot promise is that the words are *there*
— that depends on the per-base `store_document_text` setting, whose default the
API description does not state. The engine is a lossy store with a
best-effort way back to the original, which is a different claim from one that
cannot show it at all, and only the first is true.

The first two are where knowledge comes from. The third is the engine writing
itself out. It exists because *"what does the system currently believe about
this"* is a real question, and a person should be able to read the answer
without writing a query. It is a view, not a source — nothing should cite it.

Two consequences follow, and they are the two rules this subsystem is built on:

- **The corpus is authoritative. Everything else can be deleted and rebuilt.**
  Drop the base, re-run the reconcile against the repository, and the corpus
  comes back. That is what makes the rest of this design cheap to change: the
  extraction settings, the backend, the whole deployment can be replaced and the
  facts re-derived, because the only thing that was ever real is the markdown
  somebody wrote.
- **Generated pages are never reconciled back in as authored content.** If they
  were, the second generation would be partly reasoning from the first one's
  output and the fifth from the fourth's. A corpus file always came from a
  person, which is checkable, and it is the reason to trust it.

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
authored, retained, or derived — is something you filter on, not something that
splits the API into separate endpoints. §4 is the type that makes that work.

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
4. **One API over two backends is easy to fake.** The easy version exposes the
   backend's pages and the wiki as two sets of RPCs, calls that "one API", and
   leaves every consumer to work out which set it is holding. §4 and §10 are
   about the alternative.

## 2. Terminology

Every word below replaces a specific word the backend uses, and the difference
between a fact, a document and a page is not cosmetic: it decides what a caller
may write, what gets rewritten underneath them, and what a citation is worth.

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
| W-5 | A file's identity is declared, not inferred from where it happens to sit: the frontmatter `id` when the file has one, and a path-derived identifier otherwise. Both are computable from the file alone, so no state outside the base is required, and a **move** of a file that declares an id keeps its identity. §5.10. (W) |
| W-6 | An unchanged file costs nothing: the plan compares digests against the content's recorded digest, and an unchanged modification time skips the read entirely. The digest decides; the timestamp only avoids work. (W) |
| W-7 | Each reconcile records which content it owns, and a prune only removes content that owner recorded — never by listing the base, which would surface content nobody owns. The ownership record is bound to the destination it was built for, and a record that does not match is **refused**, naming what differs. Two reconciles over one base cannot delete each other's work. (W) |
| W-8 | Frontmatter becomes tags — path segments, dates, and the author's own tags — so scope is a filter retrieval already supports rather than a directory convention the API must learn. The **source path** goes in `metadata` rather than in a tag, because a citation needs a field to point at and a filter does not. (W) |
| W-9 | Authored text is ingested with `resolve_entities` off, because a person writing a name means the name. The backend's default is **on**, and on means a name close to one already in the base may resolve to that one instead — so this is set explicitly on every ingest, never inherited. (W) |
| W-10 | Files with no event time are ingested as timeless, with `timestamp: "unset"` — the one spelling the backend accepts for "this has no date", and the one no shipped client uses. (W) |
| W-11 | The plan names every file it would create, change, leave alone and delete, with digests, so the user reads what is about to happen. (W) |
| W-12 | Application is asynchronous, batched, and idempotent per batch, because a large corpus is thousands of extractions and cannot be a synchronous call. The backend's asynchrony is a flag on the ingest request rather than a separate job API, so a batch is one request, and idempotence is a caller-supplied `operation_id` on it: re-sending one returns the original operation and does no work, and re-using one for a different operation is a `409`. (W) |
| W-13 | Binary files under the corpus are ingested through the binary path. (W) |
| W-14 | **The corpus remains the source of truth.** Deleting a base deletes derived knowledge, not the files. Deleting a page deletes a projection. Nothing in the base is the only copy of anything a person wrote. (W) |
| W-15 | **The corpus is written by people, through their editor, and reviewed through Git.** Not through this API. An assistant never writes a corpus file, and neither does the subsystem on its own. A write that existed only in the base would be a second source of truth, and the next reconcile would delete it; a write that landed in the file behind somebody's editor would produce a conflict with their tooling. §10.2. (U) |
| W-16 | **A reconcile reports drift in the other direction too**: derived knowledge the corpus contradicts, so that a base's beliefs are visibly wrong when the documentation says otherwise. (U) |
| W-17 | **The base is disposable and rebuildable.** Deleting it and reconciling from the repository reproduces the corpus, and the operation is supported rather than merely possible. (U) |
| W-18 | **A commit corresponds to an index.** A reconcile records the commit it reconciled, and a base can be asked what commit it reflects, so "the index matches the merge" is a checkable statement. (W) |
| W-19 | **Reconcile runs on merged content, not on every local edit.** An uncommitted or unreviewed change is not knowledge the deployment should believe. (W) |
| W-20 | **The projection never flows back into the corpus.** Generated pages are not reconciled as authored content, so the system cannot accumulate its own output as input. (U) |
| W-21 | **Contradictions between two authored documents are preserved, not resolved.** An ADR that supersedes another is the corpus's own statement about which holds, and the reconcile passes both through with that metadata intact rather than picking one. (W) |

### The whole-wiki view

A base is two halves. Read one half and you have a documentation set or a set of
generated documents; read both as one thing and you have what a person actually
wants when they ask a system what it knows. See §5.8.

| # | Requirement |
|---|---|
| X-1 | **A base projects to a single markdown tree spanning both halves.** An authored corpus file and a derived page sit in the same tree, in the same index, because a reader should not have to know which half produced a file to read it. (U) |
| X-2 | **Every projected file states its origin in frontmatter**, so "was this written by a person or generated?" is answered by opening the file rather than by knowing where it sits. (U) |
| X-3 | **An authored file is projected verbatim** — the bytes the reconcile ingested, not a re-rendering. Re-rendering a reviewed document produces a *different* document, and a diff between what was reviewed and what is published is a question nobody asked. (W) |
| X-4 | **A derived page is projected from the backend's own page bundle**, not rendered a second time by this framework. The bundle already exists, and rendering it ourselves would be a second translation of one artifact — which framework rule 11 exists to prevent. §12.2. (H) |
| X-5 | **One index at the root**, in tree order, linking every projected file. The index is generated and is never part of the corpus. (U) |
| X-6 | **The projection is derived and regenerable in full.** A file in it is not a source, an edit to it is discarded by the next projection rather than applied, and deleting the whole tree costs nothing. (W) |
| X-7 | **A caller can project a subtree, one origin, or everything.** A base with three thousand files must be readable one page at a time without exporting three thousand. (U) |
| X-8 | **A projection records what it was taken from** — the base, the corpus commit it reflects, and the backend's answer on whether any page in it is stale — so a reader can tell a current view from an out-of-date one. (W) |
| X-9 | **The projection is never a reconcile input.** The whole-wiki view is the one artifact in this system that looks exactly like a corpus and is not one, so this is stated as a requirement rather than left to W-20 to cover. (U) |

### Mounting the wiki

A bundle of files is useful once. A filesystem is useful for as long as somebody
is reading. See §5.9.

| # | Requirement |
|---|---|
| M-1 | **The whole-wiki projection can be mounted as a FUSE filesystem**, so `cat`, `rg`, an editor and an agent's file tools all work against it with no vocabulary of their own. The mount is an RPC — `EnableMount` — not a command, so an agent can perform it and choose the mountpoint. §5.9 records why the original text said otherwise and what was wrong with the reasoning. (U) |
| M-2 | **The mount is strictly read-only.** Every write path returns `EROFS`. A writable mount would suggest the wiki is editable, which X-6 and W-15 both forbid, and an edit that vanishes on the next regeneration is worse than one that was refused. (W) |
| M-3 | **A change invalidates the kernel's cache for what changed, not just the next poll.** Content changes call `NotifyContent`, removals call `NotifyDelete`, and a changed tree calls `NotifyEntry` on the affected names. See §5.9 for what this does and does not tell an editor. (H) |
| M-4 | **Staleness is bounded even where notification is unavailable.** The mount sets short `AttrTimeout` and `EntryTimeout` values, so a kernel or filesystem without `FUSE_NOTIFY_INVAL_INODE` still converges, and the bound is a stated number rather than "eventually". (H) |
| M-5 | **The change signal is a revision, not a diff.** One cheap call reports whether the projection changed and which half changed; the mount re-fetches the whole projection when it did. §5.9. (U) |
| M-6 | **The mount is a client, and the FUSE dependency is in the binary and not in the provider.** A host with no FUSE, no `/dev/fuse`, or no `fusermount3` still builds the subsystem, still serves the contract — all thirteen services, `MountService` included — and still exports the same bundle. The mount failing is never the subsystem failing. Concretely: the FUSE code is behind a `fuse` build tag, `GetMountStatus` reports `fuse_available` and names the tag, and the mutating methods return `Unimplemented` naming it. (U) |
| M-7 | **A mount that cannot start says why, and says which piece is missing** — `fusermount3` absent, `/dev/fuse` absent, the path already mounted, a path that is not a directory, the policy refusing — and never leaves a stale mountpoint behind. It is a refusal rather than an exit code because there is no process to exit: the mount is a resource the subsystem holds, and a caller learns about the failure from the call that asked for it. (H) |
| M-8 | **The mount serves exactly what `ExportWiki` serves**, filtered by the same policy. There is no second read path with a different authorization, because a filesystem that shows more than the API does is a hole in the policy boundary. (U) |
| M-9 | **The mount is optional and nothing depends on it.** `ExportWiki` is the feature; the mount is one way to consume it. A deployment that never mounts loses nothing, and a caller can read `GetMountStatus` and discover there is no mount without attempting one. (U) |
| M-10 | **A mount is told where it is.** `EnableMount` and `GetMountStatus` both report the host the mountpoint is on, because the mountpoint is a path on the machine running the *subsystem* and a caller on another machine has nothing it can resolve. The alternative — reporting only a path — is a report a caller cannot check, which is the failure mode §5.9's namespace caveat is about. (U) |

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
| P-11 | Deleting a page loses the body and nothing else of substance: it re-projects from memory. **But the `source_query` — the question the page answers — and its place in the tree live on the node and are not re-derivable**, so `DeleteContent` on a page is not the same as deleting a row. (H) |
| P-12 | A page is content of origin `derived`, and is therefore readable through the same call as anything else rather than through a page-only read. (U) |
| P-13 | **A caller can learn what a page is no longer standing on.** A refresh that finds claims citing facts the base no longer holds records what those facts said and removes the claims, and that report is available — on a dry run or a kept trace, never inferred from the page, because a page citing a deleted fact still reports itself current. (H) |

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
| N-9 | Streaming: there is none. Every operation is unary, and adding one would be the only streaming contract in the tree. **This was tested against the one feature that most wanted a stream** — the wiki mount's change notification — and held: a polled revision covers it, and §5.9 records the rejection. (new) |
| N-10 | The provider declares one record, not one per base, and a deployment running two backends distinguishes them by identifier. (new) |
| N-11 | **Origin-specific behaviour is stated in the contract, not discovered.** A caller can tell from the contract what a write will do to each origin, because "edits are applied at the source and reconciled in" is a promise and a silent overwrite is a bug. (U) |
| N-12 | **Exact-term search is on by default for a corpus-backed base.** Technical prose is full of identifiers — `OAuth 2.1`, `ADR-014`, `CustomerID` — that semantic similarity alone does not reliably find, so a base reconciling a corpus keeps its keyword and text arms enabled. A deployment may turn them off having measured the cost; the point is that turning them off is a decision rather than a default. The backend exposes this as a toggle whose own default is not stated in its description, so the provider sets it explicitly for a corpus-backed base rather than inheriting it. (W) |
| N-13 | **The extraction mode is a per-base choice, and both modes are first-class.** Full extraction gives facts, entities and relationships, and is the default; a chunk-oriented mode stores the text as-is without model extraction, for a corpus that is read rather than reasoned over. Which one a base uses is recorded, because a base whose mode changed has different knowledge and a reader should be able to tell. (W) |


## 4. The content model

This message is the point of the whole design. If a corpus file, a retained
document and a generated page were three resources with three reads each, "one
API" would be a slogan — every consumer would still have to know which family it
was holding.

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

The record that makes a citation worth something: what produced this content, and
what it was derived from.

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

**One constraint on what a citation may promise, because the backend does not
make it easy.** A fact cited by a reasoning answer comes back as an identifier
and its text, and **usually without the document it came from**; the document id
is on the retrieval results inside the reasoning trace instead. So a cited fact
cannot be resolved to a file from the citation alone — it takes a join against
the trace, and the trace is only returned when the caller asks for it and is
large when it is. This surface should therefore resolve citations itself and
return `source` populated, rather than handing a caller an identifier and the
obligation to reconstruct where it came from. It is also why there are two
different questions to keep apart: *what the answer drew on* (the whole trace)
and *what it cited* (the narrower, ordered, deduplicated set). A person wants
the second; a reader debugging a wrong answer wants the first.

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

The backend projects a base onto a folder of markdown files, and keeps that
folder current — `ExportKnowledgeBase` on the client, over a bundle schema whose
files are the units. That direction is memory → disk, and it exists so a person
can `ls`, `grep` and edit. Whether this specification uses that export or renders
the same tree itself is §12.2's open question. Its own documentation is clear that the projection is
derived:

> A knowledge page is a **projected view** over processed memory, the way a
> database view is not a table. […] The pages are the reconciled truth about
> *what holds*.

The same page also says *"your raw documents remain the source of truth about
what was said"*, and that half needs qualifying, because the retain
documentation says the opposite:

> Each item is a piece of raw content […] **The content itself is never stored
> verbatim; what gets stored are the structured facts the LLM extracts from
> it.**

Raw text is persisted only if `store_document_text` is on — a per-base setting
whose default the API description does not state. With it off there is no
original text in the bank at all. **This is why the corpus has to stay on disk in
Git rather than being trusted to the base** (W-14): the repository is the only
copy that is guaranteed to exist, and the base holds derived facts it may not
even be keeping the words of.

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
  not through here. W-15. This is the one place the two audiences get different
  treatment: a corpus file has an editor, hooks, a blame view and a review
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
and its facts are deleted and re-extracted rather than appended to. This is
stated in the `POST /memories` endpoint description and in prose on
`MemoryItem.update_mode`; the schema itself carries no `default` key, so a
generated client sees no default and the provider must send it explicitly. That is
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
timestamps; digests do not, and the content already carries one. The
implementation upstream keeps for this is two-stage and worth copying: an
unchanged modification time skips the file without reading it, and a moved
modification time with an identical digest refreshes the timestamp without
re-ingesting. The timestamp is a free way to avoid work; the digest is what
decides.

**Ownership is recorded, and pruning respects it.** W-7. Each reconcile writes
its own marker into the content metadata it creates, and a prune removes content
only if that marker names the owner doing the pruning. Without this, reconciling
a second directory into a base would delete the first one's documentation — the
failure mode a two-reconciler deployment discovers by losing data.

Two details in that are learned rather than invented, both from the shipped
client in §5.10. **A prune walks the ownership record, never the base.** Listing
what a base holds would also return the content nobody owns — a retained
conversation, a second corpus — and a reconciler that deleted those would be
indistinguishable from data loss. The cost is named rather than hidden: content
orphaned by a lost ownership record is not pruned until its file is touched
again. **And the record is bound to where it was built, and fails closed.** An
ownership record is only valid for the backend, the base, the corpus root and the
identifier namespace it was written for; reusing one across those would classify
another target's files as already reconciled *and* authorise deleting content in
a base this reconciler never wrote to. A record that does not match is therefore
refused, naming each field that differs, rather than repaired. This is not
defensive programming for its own sake: the same project shipped that bug and
fixed it that way. See §5.10.

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

A person reads something the system inferred, decides it should be true, and
wants it to stick. Three ways to do that, cheapest first:

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

### 5.8 The whole-wiki view

A base is two halves, and until now this document has described them as two
things to be read — corpus files through `GetContent`, pages through
`GetContentTree` and `GetContent`. What a person actually wants when they ask a
system what it knows is *one* body of documentation they can browse. X-1 is that
view, and it is the one feature here that neither half provides alone.

**The shape.** One directory, mirroring `GetContentTree`. Authored files sit
where the corpus puts them. Derived pages sit where their folder tree puts them.
A generated `index.md` at the root lists everything in tree order, and each file
carries its origin in frontmatter, so a reader can tell a reviewed runbook from
a rendered page by opening it rather than by knowing the base.

**The two halves are handled differently, on purpose.** An authored file is
copied verbatim (X-3) — the bytes the reconcile ingested. A derived page comes
from the backend's own page bundle (X-4). The asymmetry is the point: a
re-rendered authored file would differ from the reviewed original, and a diff
between them is a question nobody asked, whereas the backend's pages *are* a
rendering and their canonical form is whatever the backend says it is.

**This is not a corpus, and that is the thing most likely to be got wrong.** The
view is byte-for-byte shaped like a wiki, which makes it the one artifact in this
system that a well-meaning agent could feed back into a reconcile. X-6 and X-9
say it is derived and regenerable, and that reconcile never reads it. The
generated `index.md` in particular must never be ingested: it is the one file
that describes the whole tree and is therefore the one that could make the
second generation reason from the first.

**Not a filesystem mount.** Hindsight ships `hindsight fs mount`, which mirrors a
bank's page tree to disk and keeps it current. It is the right tool at a
terminal and the wrong one here — it is a long-running process around an export
endpoint this subsystem can call directly, and it projects pages only. §12.2 has
the details and the line where its coverage stops.

### 5.9 Mounting the wiki

`ExportWiki` returns a bundle. A bundle is a thing you download; a filesystem is
a thing you read, and for a person auditing what a system believes it is the
difference between looking and reading. M-1 is a FUSE mount of the same
projection.

**The mount is an RPC, and the reason it is not a command is worth recording
because the original reasoning pointed the other way.** An earlier version of
this document said the mount could not be an RPC: *"a mount lives in the
filesystem namespace of whoever runs it, so an RPC cannot create one that the
caller can see — if the subsystem runs on another host, the mountpoint it
created would be on the wrong machine."*

That is true and it is not a reason to refuse. The premise it rests on is that
the caller and the subsystem are on different machines, and in the deployment
this exists for they are not: the mount is for a person auditing what a system
believes, on the machine where they work, against a deployment that is often
local. And the premise is checkable rather than assumed — `MountStatus` reports
the host, so a caller on another machine is *told* rather than silently given a
mountpoint it cannot see. The earlier text turned a real property into a
design constraint, and the cost of that was that the one operation needing a
filesystem namespace was the one operation an agent could not perform.

So the contract carries `MountService` — `GetMountStatus`, `EnableMount`,
`DisableMount` — and the caller chooses the mountpoint. A command in the
subsystem's `cmd/` is not needed for it, and a mount that outlives an RPC is
supervised by the provider for the life of the server, which a command's
lifetime could not have given it.

**The namespace caveat, stated rather than left to be found.** The mountpoint is
a path on the machine running the **subsystem**, not on the machine holding the
`toolbox` binary. An agent whose transport is stdio is in the same namespace and
sees it; one connected over HTTP is not, and on a different machine entirely
there is nothing to see. `EnableMount` therefore states which host it mounted
on, and `GetMountStatus` reports it on every read — so a caller asking "is it
mounted?" always learns where, and never has to infer it from a path it cannot
resolve.

**FUSE stays out of the provider's module graph (M-6), behind a build tag.** The
`fuse` tag is what makes both placements true at once: the RPC exists in the
contract and is served by every build, and `EnableMount` on a build without the
tag returns `Unimplemented` *naming the tag*, while `GetMountStatus` reports
`fuse_available: false` and says so. A host without FUSE therefore still builds
this subsystem, still serves all thirteen services, and still exports the same
bundle — and a caller that tries to mount learns precisely what to rebuild
rather than failing obscurely.

**Change notification, precisely.** The honest version of this feature is that
"notified" means two different things and only one of them is reliable:

| What changed | Mechanism | What the reader gets |
|---|---|---|
| A page or file's **content** | `fs.Inode.NotifyContent(off, sz)` | The kernel's cached copy is dropped, so the next `read()` returns the new bytes. **`cat`, `rg` and an agent's file tools are correct immediately.** |
| A file was **removed** | `fs.Inode.NotifyDelete(name, child)` | Cache dropped *and* an inotify event is sent, per the library's own documentation: *"equivalent to `NotifyEntry`, but also sends an event to inotify watchers."* |
| A **name** appeared or its type changed | `fs.Inode.NotifyEntry(name)` | The next `LOOKUP` is re-run. **No inotify event.** |

The row that matters is the first. `NotifyContent` invalidates the cache; it does
**not** emit an inotify event, so a GUI editor with the file open will not be
told to reload. That is a property of the notification API, not a bug to be
worked around, and no amount of polling changes it. M-4 is the mitigation: short
`AttrTimeout` and `EntryTimeout` mean a re-stat converges, so a reader that
re-reads on focus — which is most of them — sees the new content within the
bound. **The bound is a number the implementation states, not "eventually",**
and this document does not claim editor reload without a test that shows it.

**The change signal: two cheap probes, one revision.** There is no server-side
change feed, and M-5 therefore reports a revision rather than a diff:

- **The engine half** — one `GetKnowledgeBaseTree` call. The tree carries every
  node's `is_stale`, `timestamp` and `last_refresh_failed_at`, so a refresh that
  has not happened yet is visible without re-exporting anything. Hash the tree
  into the revision.
- **The authored half** — the corpus is a local directory this subsystem already
  walks, so `mtime` and size per file is enough. No re-hash of content, which
  matters at three thousand files.

`GetProjectionRevision` returns that revision, which half moved, and the commit
the corpus half reflects. A caller polling it re-fetches `ExportWiki` and
invalidates on a change; the mount, which lives inside the subsystem, computes the
same revision through the same two probes and does not go out over the wire to ask
its own host. One implementation, two callers: a projection that reported one
revision over RPC and another in process would disagree exactly when it mattered.

Polling one small call is also why **no streaming RPC is added**: N-9 keeps this
tree unary, and a watch stream would be the only streaming contract in the
repository for a feature that a two-second poll serves (§15 records the
rejection).

**It must not be a second read path.** M-8 is the requirement that keeps the
policy boundary honest: the mount reads through the same calls, under the same
policy, as `ExportWiki`. A FUSE filesystem that shows a base's conversation
memory to someone whose policy would refuse it over RPC is a hole, and putting
the mount inside the policy boundary is cheaper than auditing it afterwards.

**What it costs to run.** `fusermount3` is setuid on most distributions, so an
unprivileged user can mount; a container without `/dev/fuse` cannot, and neither
can a host whose kernel lacks `FUSE_NOTIFY_INVAL_INODE` (Linux 7.13+) for the
fast path. M-7 says a failed mount says which of these it was — a refusal with a reason,
not an exit code, because there is no process to exit — and M-6 says it never
affects the subsystem. Per the repository's own
testing rule, **no test may require a mount to work** — the mount's logic is
tested over a plain `io/fs` filesystem with a fake revision source, and the FUSE
wiring is exercised by hand.

**Library.** `github.com/hanwen/go-fuse/v2` (v2.11.0): pure Go, no cgo, four
small dependencies. `bazil.org/fuse` was the alternative and its last release
predates this by three years.

### 5.10 There is a shipped answer, and this design is the part of it that is not Obsidian

The obvious question about §5.1 through §5.5 is whether this is reinventing
something that already exists. It partly is, and finding out is worth more than
either answer would have been. The full survey — every source and test file of
the client, and a requirement-by-requirement mapping — is
[`investigations/hindsight-obsidian-integration.md`](investigations/hindsight-obsidian-integration.md).

**Hindsight ships a first-party client for exactly this**:
[`@vectorize-io/hindsight-obsidian`](https://hindsight.vectorize.io/sdks/integrations/obsidian),
source in
[`hindsight-integrations/obsidian/`](https://github.com/vectorize-io/hindsight/tree/v0.10.1/hindsight-integrations/obsidian).
It syncs a human-maintained markdown vault into a bank, one way, and its stated
hard rule is *"Hindsight is never a second source of truth."* Version 0.3.0 was
published to npm on **2026-09-25**, and its algorithm is the same three calls
this design uses:

| Event | Call |
|---|---|
| note created / edited | `retain(document_id = note path)`, `update_mode: "replace"` |
| note renamed | `deleteDocument(old)` + `retain(new)` |
| note deleted | `deleteDocument(path)` |
| "sync now" | reconcile: ingest drifted notes, prune orphans |

**It is not adopted as an implementation, and there are four reasons, in order
of weight.** It is TypeScript against the Obsidian runtime, so a Go subsystem
using it means a second, unmanaged writer to the base. Its ownership record is
bound to the vault's **absolute path**, so a fresh clone anywhere else fails
closed and forces a full re-extraction — and for a repository, whose whole point
is that it is cloned to many paths, that alone is disqualifying. It cannot record
which commit it reconciled (W-18) and cannot produce a plan for a person to
confirm (W-4). And it has no notion of `authority`, `supersedes`, `status`,
`owner` or `reviewed` — the frontmatter contract in §5.2 is most of what this
subsystem is for.

**And it disagrees with W-19 outright, which is the one difference that is about
what the system is rather than how it is built.** Its sync-on-edit defaults *on*
and its live watch mode is a headline feature: it believes whatever is on disk
within seconds. This design believes only merged commits, because a shared corpus
is reviewed in Git precisely so that what the deployment believes was somebody's
decision. Both are right for their own corpus — a personal vault has no review
step, so waiting for a merge would mean never syncing — but they are not
interchangeable, and it is the clearest evidence that the client is a client and
not a smaller version of this.

**What it does remove is the invention.** §5.4 becomes a transcription of a
design that has been in continuous use since 2026-06-08, and most of what
follows are mechanisms this document had stated as a goal without one, or had
not thought of at all. Adopted, each with the reason upstream gives:

- **Prune by the local index, never by listing the base** (W-7). Listing would
  surface content this subsystem does not own — a retained conversation, another
  corpus — and delete it. The cost is stated rather than hidden: an orphan left
  behind by a lost index is not pruned until the file is touched again.
- **The index is bound to its destination and fails closed.** Identity is
  `(api origin, base, corpus root, id namespace)`; a persisted index whose
  binding does not match is **refused, naming each differing field**, because
  reusing one across bases "could authorize prune DELETEs against a bank this
  ingester never wrote to". Upstream shipped that as a data-deletion bug and fixed
  it in 0.2.1 (2026-08-10) by adding the binding rather than a warning. W-7 is
  the goal; this is the mechanism, and it is now written down instead of
  implied.
- **Timestamp as a free pre-filter, digest as the truth** (W-6). Identical
  `mtime` skips without reading the file; `mtime` moved with an identical
  `sha256` refreshes the timestamp and does not re-ingest.
- **Provenance in `metadata`, scope in tags** (W-8). The source path goes in the
  item's `metadata` because that is what a citation needs to point back at a
  file; the scope dimensions go in tags because that is what filtering needs.
  A `context` field on the item names the subsystem.
- **Idempotence by key, not by hope** (W-12). The ingest request takes a
  caller-supplied `operation_id`, and re-sending one returns the original
  operation and does no work — a retry after a lost acknowledgement does not
  enqueue a duplicate, and re-using an id for a different operation is a `409`.
  The client sends none, so its retries duplicate; this design will not.
- **Entity resolution off, explicitly** (W-9). `resolve_entities` defaults to
  **on**, where on means a name close to one already in the base may resolve to
  that one *instead of the one the author wrote*. The client never sets it, so an
  authored runbook's "Acme Corp" can attach itself to a different "Acme" in the
  same base. This is the clearest defect found in the survey and it is a
  correctness bug for a corpus, not a tuning question.
- **Consolidation scope chosen, not inherited** (W-8). `observation_scopes`
  decides how many consolidation passes a tagged memory gets — one per tag, one
  with all tags together, or one global untagged pass *"useful for deduplicating
  across volatile per-call provenance tags"*. A corpus ingested with `vault:`,
  `folder:` and date tags is mostly provenance tags, so the default is a decision
  this design has to make on purpose rather than inherit.

**And one deliberate divergence.** That client uses the path as identity,
always, so a move is a delete and a fresh extraction. §5.2 keeps the frontmatter
`id` — for the same reason its no-`id` case is already delete-and-create —
because a corpus gets reorganised and a reorganisation should not silently
re-extract everything and orphan every fact that referenced the old paths.

Four further facts from reading it, which the contract relies on and none of
which the client acts on:

- `tags` and `tag_groups` are **mutually exclusive** server-side, and the grouped
  form is a boolean tree rather than a list (§7.1).
- Recall has **no hard date-range filter**, which is why dates are expressed as
  bucket tags rather than a field (W-8).
- `retain` takes `async: true` rather than a separate job API (W-12).
- **A fact cited by a reasoning answer comes back without its document id**
  (§4.3), so a citation cannot be resolved to a file without walking the
  reasoning trace. This one changed a requirement: it is why this surface
  resolves citations itself instead of handing a caller an identifier.

**What still has no upstream analogue at all:** §5.5 confirmation, §5.6
promotion, §5.7 contradiction handling, §5.8 the whole-wiki view, §5.9 the
mount, and W-16's drift report. None of them is about getting text into a bank,
which is the whole of what the client does.

**So the honest accounting is: this removes the reconciliation design work and
none of the composition.** The full survey — every source and test file, the
version history, and the findings in its §9 that this document acts on — is
[`investigations/hindsight-obsidian-integration.md`](investigations/hindsight-obsidian-integration.md);
why the question went unanswered for months while the answer sat in the
repository is §5.7 of
[`investigations/hindsight-knowledge-backend.md`](investigations/hindsight-knowledge-backend.md).

**One trap worth naming.** A user with a plain Git markdown corpus could point
that CLI at it today and get most of §5.4 for free. This design should say so in
its README rather than let someone discover it — while being clear about what the
stopgap costs them: no plan to confirm, no record of which commit was
reconciled, no authority metadata, and a per-clone-path index.

## 6. The contract

### 6.1 Services

`ContentService` and `QueryService` come first because that is what callers
actually use; everything below them is administration. Every RPC carries
`@toolbox.side-effects`, because that annotation is what a policy author reads
to decide who may call it and what the MCP gateway checks before exposing it.

| Service | RPCs |
|---|---|
| `ContentService` | `ListContent`, `GetContent`, `WriteContent`, `CurateContent`, `DeleteContent`, `GetContentTree`, `ListContentChunks`, `ReprocessContent`, `ExportWiki`, `GetProjectionRevision` |
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

Three rows differ from what the backend offers, and the differences are the point:

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

`ExportWiki` is on `ContentService` and not on `PageService` because it spans
both halves, and a service named for pages cannot return a tree containing
authored files. It returns the same `{path, content}` bundle shape as
`ExportPageBundle`, which is the backend's own shape and is the reason the two
can be composed without a second translation (X-4, §12.2).

`GetProjectionRevision` sits next to it because they are one feature: the bundle
is the data and the revision is how a client knows it changed. It is unary and
cheap — one `GetKnowledgeBaseTree` call plus a directory stat — which is what
lets the mount poll and lets N-9 stand (§5.9). **There is no streaming watch
RPC**, and §15 records why rather than leaving it to be re-proposed.

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

Three constraints on filtering are worth carrying into the contract rather than
discovering in a client. **Scope filters are grouped or flat, never both** — the
backend documents `tags` and `tag_groups` as mutually exclusive, so this surface
takes one shape and the provider sends the grouped one. **The grouped form is a
boolean tree, not a list**: a list of groups is AND-ed, and each element is a
tag leaf or an `and` / `or` / `not` over more of the same, recursively. A leaf
carries `match` — `any`, `all`, `any_strict`, `all_strict` or `exact`, defaulting
to `any_strict` — and optionally `resolve: fuzzy`, which matches its tags
against the base's own tags by trigram similarity, so a misspelled filter still
lands. A contract that flattened this to a list of tag sets would throw away the
only part of it that is expressive. And **there is no date-range filter**: a
caller asking for "last quarter" is asking for a set of tags, which is why W-8
turns dates into bucket tags at ingest instead of leaving them as a field nobody
can filter on.

One more asymmetry worth knowing because it is invisible until it surprises: the
two reasoning calls have **different default depths** — a recall defaults to
`mid` and a reflect to `low`. A surface that exposes one "depth" setting has to
decide which default it is overriding, and should say so rather than let the
backend's answer differ per call.

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

A page is a mental model with a trigger and a place in a folder tree. Nothing
about the message says "page" — the same request creates a mental model — so the
defaults are the only thing distinguishing them, and they are worth writing down:

| Setting | Default for a page | Why |
|---|---|---|
| Built from | consolidated observations | Observations are deduplicated and carry their evidence, so a page reads as a settled document rather than a transcript. Enforced structurally: with only `observation` in scope the refresh agent is not given the raw-memory recall tool. It can still expand a memory to its original chunk when tracing a claim, so "reads beliefs, not the transcript" is about the retrieval surface, not a firewall. |
| Reads other models | never | `exclude_mental_models: true` hides **every** sibling mental model, not just sibling pages — otherwise pages cite each other and one page's wrong claim gets quoted as fact by the next. |
| Refresh mode | incremental | Edits the existing document with what is new, so hand-tuned structure survives. |
| Trigger | after consolidation, if something new landed in **this page's** tags | A page about `auth` does not rewrite when somebody's runbook about `deploy` changes. |
| Budget | document-sized | It is a document, not an answer. |

Two defaults here have bitten people, and the contract comment has to carry both
because the generated MCP tool description *is* the comment:

**A page's tags filter it. They are not labels.** A tagged page matches with
`all_strict`: a memory must carry *every* one of the page's tags, and memories
with no tags at all are excluded. So creating a page with tags you invented to
describe the topic — "What do we know about auth?" with tags
`["authentication", "security"]` — matches nothing, and the page generates as
"I don't have information about this", while a direct recall for the same
question returns plenty, because recall was not handed the same filter. Three
ways out, and the caller picks: drop the tags, widen the match mode, or keep
`all_strict` and tag the source material to match.

**Staleness tracks writes, not deletions.** A page reports itself stale when
something in its scope has been *written* since it last read. Deleting an
in-scope fact leaves no write behind, so a page that cites the deleted fact keeps
reporting itself current. The fix is a forced refresh, not waiting.

**…but a refresh does clean up deleted claims.** The `is_stale` flag misses
deletions; the refresh itself does not. Hindsight runs a retraction pass that
finds facts a document cited which no longer exist in the bank, records what they
said, and removes the claims that rested on them. Two things follow, and both are
contract-visible: a forced refresh is how you learn that a page was quoting
something the base no longer holds, and the retraction is reported only on a
dry-run or a kept trace, never on the page itself. A caller that wants to know
"is this page still standing on anything real" has to ask for the trace, or run
the dry run. P-13.

## 9. Directives and base configuration

Two things a person writes that the base stores exactly as given and never
rewrites: directives, and the base's own configuration. They are the hand-authored
part that is *not* prose, and §10.3 says why they do not go through extraction
like the corpus does.

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

Reading is identical for both, and reading is most of what callers do. A caller lists
content, reads content, browses a tree and searches — and origin is a field on
what comes back rather than a choice made before the call. Both halves share
tags, both are filtered the same way, and both are cited the same way with
origin stated.

Writing is shared with one exception, and the exception is the whole
documentation half. A person writes a corpus file in their editor and commits
it; `WriteContent` cannot do that and there is no RPC that can. Everything else
either audience may do: an assistant retains, a person retains through the CLI,
an assistant writes a document, a person curates a fact. What decides which
rules apply is the content's `origin`, not who is asking (U-5, W-15).

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

A person writes two kinds of thing into a deployment: prose, and configuration.
Only the prose is extracted.

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
- **`pkg/knowledge/hindsight`** — an adapter over the backend's **official Go
  client**, converting its types and its failures to the framework's. Not a
  hand-written HTTP client; see §12.1 for what changed and why.
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

### 12.1 The backend client is the official Go one

**This reverses an earlier decision in this document, and the earlier reasoning
was wrong in a way worth recording.** The previous text said the client is
hand-written *"because a generated client drags the whole 195-schema model into a
module's dependency graph and the contract must not see those types anyway."*

The second half is right and the first half does not follow from it. The contract
must not see the backend's types — that is framework rule 8, it is not
negotiable, and it requires a conversion layer **whether the client is generated
or hand-written**. Converting provider types at the boundary is the requirement.
Choosing not to use the provider's own client is not, and nothing else in the
argument supported it.

The client is `github.com/vectorize-io/hindsight/hindsight-clients/go`, generated
by openapi-generator from the same `openapi.json` this specification is written
against — so it is not a second opinion of the API, it is the API. It is also
close to free: its own `go.mod` declares **Go 1.18** and three modules
(`stretchr/testify`, `gopkg.in/validator.v2`, and `go.yaml.in/yaml/v3` as an
indirect). An earlier version of this document said 1.24.0 and named yaml.v3
directly; both were read off the wrong file, and the pin is a commit rather than
a tag so there is no version to check them against. It covers every operation this
specification needs, and the ones §15 has not decided yet as well. Sixteen API
services, 99 methods, and every one of them was reachable; the mapping is:

| Need | Client service and method |
|---|---|
| Ingest | `Memory.RetainMemories`, `Files.FileRetain` |
| Read facts | `Memory.RecallMemories`, `Memory.ListMemories`, `Memory.GetMemory`, `Memory.UpdateMemory` |
| Correct a fact, and its history | `Memory.UpdateMemory`, `Memory.GetObservationHistory` |
| Read the original text | `Documents.GetDocument`, `Documents.GetChunk`, `Documents.ListDocumentChunks` |
| Documents | `Documents.ListDocuments`, `Documents.DeleteDocument`, `Documents.ReprocessDocument`, `Documents.UpdateDocument` |
| Pages | `KnowledgeBase.GetKnowledgePage`, `CreateKnowledgePage`, `CreateKnowledgeFolder`, `UpdateKnowledgeNode`, `DeleteKnowledgeNode`, `GetKnowledgeBaseTree`, `SearchKnowledgeBase`, `ExportKnowledgeBase` |
| Page refresh, and P-13's report | `MentalModels.RefreshMentalModel`, `DryRunRefreshMentalModel`, `GetMentalModelHistory` |
| Directives | `Directives.ListDirectives`, `CreateDirective`, `UpdateDirective`, `DeleteDirective` |
| Entities | `Entities.ListEntities`, `GetEntity`, `GetEntityGraph` |
| Re-derive | `Banks.TriggerConsolidation`, `RecoverConsolidation`, `ClearObservations`; `Memory.PreviewConsolidationStrategies`, `ListObservationScopes`, `ClearMemoryObservations` |
| Asynchronous work | `Operations.ListOperations`, `GetOperationStatus`, `CancelOperation`, `RetryOperation`, `DeleteOperation` |
| Configuration | `Banks.GetBankConfig`, `UpdateBankConfig`, `ResetBankConfig`, `UpdateBankDisposition` |
| Templates | `BankTemplates.ExportBankTemplate`, `ImportBankTemplate`, `GetBankTemplateSchema` |
| Statistics | `Banks.GetAgentStats`, `GetMemoriesTimeseries`; `Monitoring`, `Audit`, `LLMTraces` |

### 12.2 Four shapes the backend does not give, and what was done about them

These are recorded because each one changed a contract method rather than being absorbed
in the adapter, and a reader comparing this specification to the code needs to know which is
which. All four were found by implementing against the client, not by reading the description.

**A trigger filters by a tag-group tree, not a flat tag list.** `MentalModelTriggerInput` carries
`tag_groups` — a recursive `leaf` / `and` / `or` / `not` — plus `tags_match`, and no `tags`. So
"these two tags, or all three of those" is expressible on a trigger and is not expressible here.
`PageTrigger` therefore carries a flat list and a match mode, which the adapter maps to a single
leaf. That is exact for everything the contract can say, so nothing is lost in the translation —
only in what can be said, and a compound trigger is the thing that cannot be. Adding it is a
`oneof` over the same four cases `TagGroup` already uses for retrieval.

**A page read does not report which mental model maintains it.** `GetKnowledgePage` returns the
node's identity, tags and body; `mental_model_id` is on `KnowledgeNode`, which only the tree
carries. Since a model's trigger is what decides whether a refresh is full or incremental, an
operation that needs the model has to read the tree. `RefreshPage` and `PreviewPageRefresh` do.

**Cancel is `DELETE /operations/{id}` and the record removal is `DELETE /operations/{id}/delete`.**
The two names invert what the paths suggest. This is called out in `CancelOperation`'s own comment
because the alternative reading — that a cancel is a request the backend may not honour — is what
the naming invites, and the operation is a hard stop while the record survives.

**A tag group discriminates on its JSON key, and there is no wrapper.** The four alternatives of
the `oneof` reach the wire as bare objects: a leaf is `{"tags": [...]}`, an `and` is
`{"and": [...]}`, an `or` is `{"or": [...]}`, a `not` is `{"not": {...}}`. There is no `type`
field and no `leaf` wrapper, because the generator's oneof handling is symmetric — it marshals
and unmarshals the same bare shape. Two consequences worth stating: the *outbound* direction the
adapter writes is therefore correct as written and needed no change, and the *inbound* direction
fails with `data failed to match schemas in anyOf(...)` on a wrapped form, which is a decode error
naming the type rather than an empty trigger. Also: **`not` takes a leaf and not an arbitrary
group**, so the algebra is shallower than a recursive tree suggests.

**Three reads declare an empty response schema**, which is why the generated client returns
`interface{}` for them: a single fact (`GetMemory`), a fact's history (`GetObservationHistory`) and
a model's refresh history (`GetMentalModelHistory`). A generator that invented a type for these
would have been wrong about the API, so it did not. Each is decoded through the one declared
sibling that has the shape — `MemoryUnitListItem` for the two fact reads — and the inference is
documented at the point it is made, because it is the only assumption in the adapter worth
flagging.

**What the adapter still has to do, which is the part the client cannot do for
us.** The generated error type implements `Error() string` and nothing else; the
status code, the raw body and the unpacked model are behind `Body()` and
`Model()` on a concrete type, so a caller has to assert to reach them. Framework
rule 12 requires the failure to arrive as an `api.ErrorKind` that the transport
can map. So the provider asserts, reads the body, and classifies — and that
function is ours either way. Using the client does not remove the conversion; it
removes the part that would have been written by hand and gone stale.

**The one real cost is the pin, and it is a cost worth paying.** The client is a
subdirectory module with no tags of its own: the repository's subdirectory tags
cover `tools/*` and `integrations/*` and do not include `hindsight-clients/go`,
so `go get ...@v0.10.0` fails with *unknown revision* and the module resolves
only as a pseudo-version pinned to a commit. Three consequences, all deliberate:

1. `go.mod` names a **commit, not a Hindsight release**. Nothing in the build
   says which Hindsight it was built against, so the provider records the
   expected version in its own configuration and compares it with the backend's
   version endpoint at startup. Two independent facts, and the check is cheap.
2. **An upgrade is a deliberate act** — `go get` at a chosen commit and a review
   of the diff in generated types. For a `0.x` dependency of a system whose
   surface moves every few weeks, that is an advantage rather than a cost, and it
   should be written down so nobody later "fixes" it into `@latest`.
3. `openapi-generator-cli.jar` is committed inside the module directory. It
   lands in the module cache and never in our binary, because nothing imports
   it, but it is roughly 30 MB of download for anyone building the provider.

### 12.2 The page half is the backend's export; the composition is ours

**The backend already projects a base to markdown files, and the engine half of
the whole-wiki view is therefore free.** `GET /knowledge-base/export` returns
`KnowledgePageBundleResponse` — a flat list of `{path, content}`, comprising a
nested `index.md`, one `<id>.md` per page, and a `<id>.log.md` refresh history
for pages that have been rebuilt. Each file carries YAML frontmatter (`type:
"index"`, `type: "log"`, and an `id`). The client exposes it as
`ExportKnowledgeBase`, so it is one call.

There is also a CLI around it:

> The CLI can mirror a bank's knowledge base onto disk: `hindsight fs mount
> --bank my-bank`. The folder tree becomes real directories, each page a real
> markdown file with YAML frontmatter, kept current by a background refresh loop.
> […] No SDK, no API client, no new vocabulary. **The same content is available
> as a portable markdown bundle over the API**, for exporting or […]

We do not need the mount, and it is worth being clear about why: **the mount is
a long-running CLI process, and what it mirrors is the export endpoint we already
call.** `ExportKnowledgeBase` returns the same bundle with no polling loop and no
supervision. The mount is the right tool for a person at a terminal; a subsystem
that has to serve this over ConnectRPC wants the bundle.

**So the engine half is free, and here is the line where it stops.** The mount
projects *the knowledge base* — the page tree. It does not project the corpus,
and it cannot, for two independent reasons:

1. **The corpus is a document in the backend, not a page.** Once reconciled, a
   reviewed wiki file is a retained Hindsight *document*. Documents are exported
   by a different endpoint entirely — `POST /document-transfer/export` — and that
   is a **transfer ZIP of extracted facts, entity names, causal links and
   chunks**, explicitly excluding embeddings and database ids, built for
   *migration* (re-embed with the target model, re-resolve entities, no LLM
   extraction). It is a machine archive. It is not a wiki view, and anyone who
   finds it looking for one will get a ZIP of vectors' upstream.
2. **The backend cannot tell which documents were authored.** Nothing on a
   retained document records that it came from a reviewed file with
   `authority: normative`, `status: accepted` and a `reviewed` date, and nothing
   records the `supersedes` chain or the commit. The mount has no way to
   distinguish a reviewed runbook from a fact extracted out of a chat transcript.
   Projecting the corpus through it would **relabel authored material as
   derived** — which is the one thing a projection must never do, and which X-2
   and X-3 exist to prevent.

**Therefore: the page half is the backend's export, reused as-is (X-4), and the
composition — one index, one tree, both origins, origin stated per file — is
ours.** That is the whole-wiki view in §3 and §5.8, and it is a small amount of
code over two reads: `ExportKnowledgeBase` for the pages, and the corpus
directory this subsystem already walks for reconcile.

**One thing deliberately not relied upon.** The mount is described as a mirror
kept current by a background refresh loop, with no write-back path documented,
and the sibling `hindsight-admin export-bank` is explicitly labelled *"Read-only
— safe to run against a live instance."* Whether write-back is impossible or
merely undocumented, this design does not depend on the answer: W-15 forbids a
corpus write path through this API whatever the backend would accept, and X-6
makes the projection regenerable so that a two-way filesystem would be a
convenience rather than a capability.

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

Three of these were open in the previous revision and are now settled — two by
the integration design, one by reading the backend's own shipped client — so they
are recorded as decisions rather than questions. The rest are open, ordered by how
much they change the contract's shape.

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

**Settled — the backend ships a client for this, and it changed five requirements**
(§5.10, surveyed in
[`investigations/hindsight-obsidian-integration.md`](investigations/hindsight-obsidian-integration.md)).
The question was
[asked upstream](https://github.com/vectorize-io/hindsight/discussions/4830) on
2026-09-27 and is still unanswered, but it did not need an answer: the workflow
ships as a first-party client, and it was published to npm two days before the
question was asked. Reading its 42 files changed five requirements rather than
the design: the ownership record is now bound to its destination and fails closed
(W-7), change detection is two-stage with a timestamp pre-filter (W-6), dates and
scopes are bucket tags with the path in `metadata` (W-8), batches are idempotent
by `operation_id` (W-12), and `resolve_entities` is turned off explicitly because
the backend's default resolves an author's name to somebody else's (W-9). It also
produced one contract requirement the client gave no way to find: a cited fact
comes back without its document id, so this surface resolves citations itself
(§4.3). What it did **not** produce is a reason to adopt it: it syncs unreviewed
edits, which W-19 refuses, and it records no commit.

**Settled — the wiki is mounted by a command, not by an RPC** (§5.9). A FUSE
mount exists in the filesystem namespace of the process that creates it, so an RPC
whose handler runs on another host would mount it on the wrong machine. The
contract exposes the bundle and the revision; a hand-written `mount`
subcommand in the subsystem's own `cmd/` consumes them. The rejected
alternative — a `MountWiki` RPC returning a handle, and the server holding the
mount open — would only work in the single-host case, would put a filesystem in
the lifecycle of a request, and would leave an unmountable mountpoint whenever
the server stopped.

**Settled — change notification is a polled revision, not a watch stream**
(§5.9). A server-streaming `WatchProjection` would be the only streaming
contract in the repository, which N-9 currently rules out, and it would be the
first thing to break if the contract moved transports. One small unary call
covers both halves — the backend's tree for the engine, `mtime` and size for the
corpus — and a two-second interval is well inside what a person reading a wiki
needs. The rejected cost is real and worth stating: a poll cannot tell a client
the *reason* a revision moved, only that it did.

**Settled — the page projection reuses the backend's own export** (§12.2). It
exports the page tree as a markdown bundle in one call, and a CLI mount exists
around it. Rendering pages ourselves would be a second translation of one
artifact, which framework rule 11 exists to prevent. What the export cannot do
is project the *corpus* — it has no way to know which documents were authored —
so the whole-wiki view composes the export with the corpus walk rather than
replacing either. The CLI mount is not used: it is a long-running process around
the same endpoint, and this subsystem needs the bundle.

**Settled — the backend client is the official Go one** (§12.1). The previous
revision said it would be hand-written. The stated reason conflated two things:
that the contract must not see provider types, which requires a conversion layer
either way, and a preference for writing the HTTP client by hand, which nothing
supported. The client is generated from the same description this document is
written against and requires two modules of its own, so it is adopted. The
rejected alternative — a hand-written client over ~15 HTTP calls this
specification needs — would be less code only in the first month, and would
track the backend by hand for every one of the following twelve.

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
4. **Is a moved file without an `id` a warning or an error?** Narrower than it
   was: the *behaviour* is settled, because W-5 already specifies
   delete-and-create and the shipped client does the same thing for every file
   (a path is always its identity, so a rename is a delete plus a forced
   ingest). What is still open is only whether the reconcile **reports** it as a
   note in the plan or refuses — a decision about how strict this framework is
   about its own frontmatter contract, and one that probably differs between a
   project that never moves files and one that reorganises quarterly.
5. **Where does `ClearBaseObservations` live** — `KnowledgeBaseService` or
   `ObservationService`? It clears derived knowledge from a base, so the base is
   the subject; it is also one of the observation operations. Pick one.
6. **Are pages and mental models one service or two?** They are one object with
   different defaults and the distinction is invisible in the API. Two services
   is more faithful and costs a concept; one is cheaper and loses the distinction
   a reader of the contract may want.

   **5 and 6 are settled by the implementation, and the reasoning is worth keeping
   because it is the kind of thing a later reader would otherwise re-litigate.** Both
   went to `ObservationService` and `MentalModelService` — the *narrower* home in
   each case, against the "the base is the subject" argument.

   The argument for the narrow home is that the grouping must be a question a caller
   can ask *before* it knows what it is asking. An operator reaching for
   `ClearBaseObservations` is reaching for it because something looked wrong in the
   consolidation layer, and that is a different conversation from "what does this base
   know" — so putting it in `KnowledgeBaseService` would make every base read require
   the rights to rebuild the base's beliefs. The same applies to a mental model: a
   model is the synthesized *document* and a page is a position in a tree that one
   maintains, and a deployment that never uses the page tree still wants to reason
   and to see what it reasoned.

   The cost is a concept — thirteen services, not eleven — and it is paid knowingly.
   What the implementation also found is that the two are not as close as the
   question assumed: a page *is* a mental model in the backend, and `CreatePage`
   reports both identifiers. So the distinction is not invisible in the API; it is
   invisible in the *request*, which is why the services are separate and
   `CreatePageResponse` still names both.
7. **Provenance tag namespace — now only half open.** The *structural*
   dimensions are settled by what a base already has to filter on, and reusing
   their established spelling costs nothing: `vault:`, `folder:` (one per
   ancestor, cumulative), `created:` and `updated:` as year and year-month
   buckets. Buckets rather than a date field because the backend has no
   date-range filter at all, so a filterable date has to be a tag. What is still
   open is the **framework-reserved** half — which dimensions this framework
   claims for itself (`corpus:`, `kind:`, `authority:`, `owner:`, the ownership
   marker W-7 needs) and whether a namespace this framework did not invent should
   be adopted wholesale so that a base written by the shipped client and a base
   written by this one are filterable by the same expression. It should be a
   framework decision rather than a deployment's, because a deployment that
   guesses differently cannot filter across two bases, which is the one
   capability the unified surface exists to provide. Note that a misspelled filter
   is not fatal: a leaf may resolve its tags by trigram similarity, which softens
   the cost of a wrong namespace without removing the reason to decide.
8. **Should a base's own configuration be project configuration?** A base's
   missions and directives are authored material, and a project configuration
   file is where this framework puts authored material. If they merge, importing
   a template becomes a rule-17 operation on the project's configuration file,
   which is a stronger reason to confirm than §5.5 already gives.

## 16. What a change would touch

Not a plan; a list, so the size is visible before anyone starts.

All of the following exists. The list is kept because the next change needs to know what a
change to one of these costs.

**`pkg/knowledge/`** — the content model, the frontmatter parser, the corpus walk, file
identity, the ownership record, the reconcile planner, the applier, the projection and the
whole-wiki view. Tested over a plain `io/fs`, so no test requires a mount or a backend.

Coverage is **73.7%**, and the shape of what is missing is worth saying rather than rounding up.
The uncovered part is the read-only `io/fs` surface — `Open`, `ReadDir`, `ReadFile`, `Read`,
`sizeOf`, `Stat` and the projection cache's `Current` / `RefreshIfChanged`. Those are the paths a
FUSE mount takes and a test cannot take without a mount, which is the repository's own rule: no
test may require a mount to work. Everything with a decision in it — the planner's move detection
and drift, the applier's batching and ownership guard, the identity rules, the frontmatter, the
projection's composition — is covered, and the batch key derivation in particular was found
*wrong* by a test written after this number was first stated (see below).

**A change to the reconcile touches the planner, the applier, the ownership record and every
test that asserts a plan's digest** — and the digest is what `ApplyReconcile`'s confirmation is
checked against, so it is a compatibility surface of its own.

**`pkg/knowledge/hindsight/`** — the adapter over
`github.com/vectorize-io/hindsight/hindsight-clients/go`, pinned to a commit (see §12.1 for
why the pin is a commit and not a version). **A backend API version change lands here first**,
and §12.2 lists the four places the client cannot help: three untyped reads and the tag-group
trigger. The `trigger.go` translation is the one to watch — it is the only place where what
this contract can express is narrower than what the backend accepts.

**`subsystems/knowledgehindsight/`** — the contract (13 services, 79 RPCs), the
implementation, `docs_embed.go`, `Makefile`, `go.mod`, `cmd/`, tests. **A change to the
contract regenerates the descriptor, and the generated files are gitignored**, so a change to
`knowledge.proto` is a `.proto` diff plus a `make proto` run and never a staged `.pb.go`.

**One dependency outside the contract's path.** `github.com/hanwen/go-fuse/v2` is imported by
`mount_fuse.go` and by nothing else, and that file is behind a `fuse` build tag. So a host
without FUSE still builds the subsystem, still serves all thirteen services including
`MountService`, and `EnableMount` returns `Unimplemented` naming the tag. That is M-6, and it
is why the mount is an RPC without putting FUSE in every build (§5.9).

**Two methods were in the contract, registered, classified — and not implemented.** `ExportWiki`
and `GetProjectionRevision` returned `unimplemented`, because an embedded
`Unimplemented...Handler` is a real type that satisfies the interface. No unit test noticed: the
handler tests call the handlers they are testing, and a handler that was never written has no
handler test. Both are now implemented — `ExportWiki` composes `ProjectWiki` over the walked
corpus and the backend's own page bundle rather than rendering a second copy, and
`GetProjectionRevision` is `RevisionSource` over the same two probes the mount uses.

**Three more, found the same way.**

- **`QueryService.PreviewPrompts` never sent the body the endpoint requires.** The endpoint takes
  the operation whose prompts to render, one of `retain`, `consolidation` or `reflect`, and the
  adapter sent none of it — so the call failed against any real backend. It now sends the
  operation, defaults it to `retain`, and **refuses an operation outside the three**: the default
  is `retain` and a caller shown the extractor's prompts when they meant the reasoner has been
  told something false about the system. `PreviewPromptsRequest` gained the two fields it needs
  and documents why there is deliberately nothing else to set.
- **`KnowledgeBaseService` addressed a base with a `BaseRef` message while the other twelve
  services used `base_id`.** Two shapes for one thing, and the two said different things —
  `BaseRef.name` was "identifier or alias" and `base_id` was "the base to read from", which are
  the same because `resolveBaseForRead` takes either. `BaseRef` is gone.
- **The MCP gateway ran a binary from the day before.** `scripts/toolbox-mcp.sh` rebuilt only
  when the binary was *missing*, so a gateway stayed up answering for a tree that no longer
  existed, and every call succeeded while describing a subsystem that was not there. It now
  compares the binary against the hand-written sources and rebuilds when the source is newer, and
  `TOOLBOX_MCP_NO_BUILD=1` refuses rather than serving a build that is out of date.

**The test that found all of it**, and what it is for: `scripts/mcp_knowledge_test.py` and
`scripts/mcp_knowledge_sweep.py`. The sweep calls every tool the policy exposes and reports which
answer, which refuse, and which are `unimplemented`; the test asserts the parts that a Go test
cannot see — that the exposure footprint is exactly the read-only set, that a missing backend
arrives classified rather than as `internal`, and that a write through `call_rpc` is refused by
naming the method and the policy. `scripts/hindsight_stub.py` is the backend it runs against,
serving the required-field shapes the generated client insists on, so a change to those
requirements shows up as a decode error naming a field rather than as a silently empty response.

**One change outside the knowledge tree, and it was a real bug.** The host assumed every
factory returns a server. A factory that reports "nothing to start" — a contributor, or a
provider this deployment has not configured — answers `(nil, nil)`, and the knowledge
provider's does exactly that when there is no corpus. Every phase dereferenced it, so a
deployment with no knowledge backend could not build a command tree: the failure was "cannot
print help" and nothing pointed at the absent subsystem. Fixed once, where the list is built,
in `pkg/host/host.go`.

**Two things this implementation found in `pkg/knowledge` itself**, both fixed, both worth
recording because a test is what found them and a reader would otherwise assume they had always
been so:

- **The batch key covered entries the batch never sent.** `Apply` derived the idempotency key
  from the chunk as it went in, so an entry whose file could not be read — dropped from the batch —
  was still in the key. Two runs whose batches differed only in a failed read would claim the same
  key for different payloads, and the backend would skip the difference with nothing reporting it.
  The key is now derived from the entries that survived, and sorted, so it is a function of the
  *set*. `TestTheBatchKeyCoversWhatWasSentAndNotWhatWasAttempted` pins it.
- **`PlanOptions` had no way to follow a binary file, so the whole binary path was unreachable.**
  `WalkOptions.FollowBinary` existed and the applier implemented a batch that carries an
  attachment as bytes beside its chunk, but the planner passed `WalkOptions` without it, so nothing
  could ever set it. `PlanOptions.FollowBinary` now exists, defaults to false — an attachment is
  ingested beside a chunk rather than extracted from, and a corpus of a thousand documents may hold
  a few thousand images — and is what reaches the path.

**Not built, on purpose.** The reference in-memory provider. §12 states why, and §15 records it as
a decision to confirm rather than an oversight.

**Not yet on the path, and it is a gap against framework rule 4.** `pkg/knowledge.Content` — the
type carrying the `Valid` invariant, the `Mutability` classification and the three-way `Location` —
is the content model this document names as the place the behaviour lives, and the provider does
not produce it. `ContentService` converts from `knowledge.File`, `knowledge.OwnershipEntry` and
`kh.Document` straight into protobuf messages, so the mapping sits in the provider; rule 4 says a
provider mounts a package and converts messages *from* it, which puts that conversion one level
down.

It is a refactor of a passing suite rather than a design change, and it is the first thing to do
before a second backend — because a second backend is exactly what would expose the duplication.
Nothing is wrong with what ships: the contract is served, the model is tested, and the duplication
is one adapter wide. But it is duplication, and a reader of rule 4 arriving here should be told.

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
- **Hindsight 0.10.1.** The machine-readable description at
  <https://hindsight.vectorize.io/openapi.json> is the source for every
  structural claim, because it is what a client will actually be accepted by and
  the prose is not: the absence of a writable page body (`CreatePageRequest` and
  `UpdateNodeRequest` have `name`, `source_query`, `parent_id`, `tags`,
  `max_tokens`, `trigger` and nothing else); `KnowledgeNode.managed` appearing
  exactly once, in a response schema, in no request; `update_mode` being
  `replace`/`append` with the default stated only in prose; and
  `tags_match` defaulting to `all_strict` *only when the page has tags*. The
  concept pages behind the arguments are
  [`/developer/knowledge-pages`](https://hindsight.vectorize.io/developer/knowledge-pages),
  [`/developer/observations`](https://hindsight.vectorize.io/developer/observations),
  [`/developer/retrieval`](https://hindsight.vectorize.io/developer/retrieval),
  [`/developer/mental-models`](https://hindsight.vectorize.io/developer/mental-models)
  and [`/developer/admin-cli`](https://hindsight.vectorize.io/developer/admin-cli).
  The `fs mount` quotes in §5.8 and §12.2 are from the *Projected as Real Files*
  section of `/developer/knowledge-pages`; the read-only labelling of
  `hindsight-admin export-bank` is from `/developer/admin-cli`. §12.2 also rests
  on the two export schemas read directly out of the description:
  `KnowledgePageBundleResponse` (markdown files: `index.md`, `<id>.md`,
  `<id>.log.md`) and `DocumentExportSubmitResponse` (a transfer ZIP of facts,
  entity names, causal links and chunks, with no embeddings or database ids).
  Where the description and the prose disagree, this document says which it used;
  see §5.1 for the one place it matters most. §5.9's FUSE claims are not
  Hindsight's and are cited to the library instead:
  `github.com/hanwen/go-fuse/v2` v2.11.0 — `fs.Inode.NotifyContent`,
  `NotifyEntry` and `NotifyDelete` in `fs/inode.go`, the last documented as
  *"equivalent to `NotifyEntry`, but also sends an event to inotify watchers"*
  where the other two do not, and `fuse.EntryTimeout` / `fuse.AttrTimeout` for
  M-4. Read out of the module rather than from memory, which is how the
  asymmetry in §5.9's table was found.
- [`investigations/hindsight-obsidian-integration.md`](investigations/hindsight-obsidian-integration.md)
  — the full survey of the shipped client, and the requirement-by-requirement
  mapping §5.10 and W-6/W-7/W-8/W-9/W-12 rest on. Every claim in it is cited to
  a file in `hindsight-integrations/obsidian/` at `v0.10.1` or to the API
  description. What this document acted on is its §9: **§9a–c are three backend
  affordances the client leaves unused** — `resolve_entities` left on, no
  `operation_id`, `observation_scopes` unchosen — and all three map onto
  requirements already written here. **§9d records three state and documentation
  defects** in the published artifact, one of them a shipped test whose name
  contradicts its own body.
- [Discussion #4830](https://github.com/vectorize-io/hindsight/discussions/4830)
  is cited for its **status only** — that the question was asked on 2026-09-27
  and remains unanswered. **No claim in any of these documents rests on it**,
  because the workflow it asks about turned out to ship two days earlier as a
  client rather than as a reply.

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
