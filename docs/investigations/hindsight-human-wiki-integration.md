# Human-Authored Markdown Wiki → Hindsight

> **Verified against Hindsight 0.10.1, 2026-09-27.** Every claim below was
> checked against `https://hindsight.vectorize.io/openapi.json` and the
> documentation site, and the corrections are made in place. Four things this
> document got wrong or left open are now settled:
>
> - **The update semantics it says to verify are `replace`.** §"The critical
>   update/delete issue" asks the right question and now answers it.
> - **Hindsight may not keep the original text at all.** `store_document_text` is
>   a per-base toggle and the retain docs say *"the content itself is never stored
>   verbatim"*. This strengthens the core rule rather than weakening it.
> - **Knowledge Pages cannot hold authored text, at any version of this API.**
>   There is no body field on any page request. §"Alternative: use Knowledge Base
>   pages as imported documents" is now a definite no instead of a conditional.
> - **Two internal inconsistencies are fixed:** the `document_id` spelling, and
>   `status: accepted`, which was not one of the three values the document itself
>   defines.
>
> [`knowledge.md`](../knowledge.md) is the specification that implements this.
> Where the two differ, the specification is right, and §17 of that document
> records why.

## Goal

Use a Git-controlled, human-authored Markdown wiki as a source of knowledge for a Hindsight-backed AI agent, while preserving a strict separation between:

1. **Human source of truth** — Markdown files maintained by people.
2. **Hindsight index/memory** — derived data optimized for agent retrieval.
3. **Hindsight Knowledge Base pages** — optional readable/projected documents, not the authoritative source.

The recommended direction is:

```text
Human Markdown Wiki
        │
        │ Git / CI sync
        ▼
   Hindsight ingest
        │
        ├── extracted memories / entities / graph
        │
        └── original document + chunks
                 │
                 ▼
              Agent
          recall / reflect
```

Do **not** make the Hindsight-generated wiki the canonical copy if the requirement is "only authored by humans." Keep the Git Markdown repository authoritative and treat Hindsight as a derived index.

## What Hindsight currently provides

Hindsight's current API exposes three relevant layers:

- **Memory**: `retain`, `recall`, and `reflect`.
- **Documents**: retained source documents and their chunks.
- **Knowledge Base**: folders/pages with hybrid search, Markdown export, and filesystem projection.

Hindsight's documentation describes Knowledge Pages as living documents generated from the bank's observations. They are a projection over processed memory, rather than the original source of truth.

The current API also has a Knowledge Base page creation endpoint, page search, page retrieval, tree operations, export, and node deletion/rename/move.

Sources:
- https://hindsight.vectorize.io/developer/knowledge-pages
- https://hindsight.vectorize.io/developer/api/knowledge-pages
- https://api.hindsight.vectorize.io/docs

## Recommended architecture

Use **two distinct concepts**:

### A. Human wiki

Example Git repository:

```text
wiki/
├── README.md
├── architecture/
│   ├── overview.md
│   ├── authentication.md
│   └── data-model.md
├── products/
│   ├── product-a.md
│   └── product-b.md
├── policies/
│   ├── security.md
│   └── deployment.md
└── decisions/
    ├── ADR-001.md
    └── ADR-002.md
```

This is the canonical source.

Humans edit it. Pull requests review it. Git records history. Hindsight never edits these files.

### B. Hindsight bank

The repository is synchronized into a dedicated Hindsight bank.

Each Markdown file should have a stable `document_id`, for example:

```text
wiki:architecture/authentication
wiki:decisions/ADR-001
```

That stable identity is important for incremental synchronization and updates.

> **Correction.** This document used `wiki:architecture/authentication.md` here
> and `wiki:architecture/authentication` in the frontmatter examples and the
> sync pseudo-code, and the two must be the same string or every lookup misses.
> The extension is not part of the identifier; the form above is the one to use
> throughout.

## Why `retain()` is the right ingestion path

Hindsight's retain API accepts documents, conversations, and raw content. It analyzes retained content, extracts structured facts, identifies entities, and builds a connected knowledge graph.

Markdown is explicitly supported as input.

Therefore a human Markdown page can be sent to Hindsight as a document:

```python
client.retain(
    bank_id="company-wiki",
    content=markdown_text,
    document_id="wiki:architecture/authentication.md"
)
```

For a Git repository, the synchronizer should send each changed Markdown file independently.

Relevant Hindsight documentation:

- https://hindsight.vectorize.io/developer/api/retain
- https://hindsight.vectorize.io/developer/api/documents
- https://hindsight.vectorize.io/developer/retain

## Important distinction: Knowledge Pages vs retained documents

Do not confuse these two.

### Knowledge Page

A Knowledge Page is a generated, readable view over Hindsight's processed knowledge.

Conceptually:

```text
memories
   ↓
consolidation
   ↓
knowledge page
```

It is useful as a human-readable representation of what Hindsight currently believes.

### Retained document

A retained document is source material supplied to Hindsight.

Conceptually:

```text
human Markdown
      ↓
    retain
      ↓
facts / entities / chunks / embeddings
      ↓
recall
```

For a human-authored wiki, the second path should be the authoritative ingestion path.

## Recommended source-of-truth rule

Use this invariant:

> Git Markdown is authoritative. Hindsight is disposable and reproducible.

If Hindsight is deleted, you should be able to rebuild it entirely from the Git repository.

This is the most important architectural property.

It is worth being precise about *why* it holds, because the obvious reading is
wrong. It does not hold because Hindsight keeps a copy of your files. Hindsight's
retain documentation says:

> The content itself is never stored verbatim; what gets stored are the
> structured facts the LLM extracts from it.

Raw text is persisted only if the per-base `store_document_text` setting is on,
and the API description does not state its default. With it off, the bank holds
no original text at all. The repository is the source of truth not because it is
the first copy but because it is the only copy that is *guaranteed* to exist —
which is a much better reason, and one that survives the setting being turned
off by someone who was tidying up.

It means you can:

- rebuild the bank,
- change extraction settings,
- change embeddings/retrieval configuration,
- migrate Hindsight,
- test a new Hindsight version,
- re-index a historical version of the wiki,

without losing the human knowledge.

## Repository metadata

I recommend adding lightweight YAML frontmatter to every wiki file.

Example:

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

Our API uses OAuth 2.1.

Access tokens expire after 60 minutes.

Production authentication is handled by Auth0.
```

The fields are application-level metadata. The critical field is the stable ID.

A useful minimum is:

```yaml
---
id: wiki:architecture/authentication
authority: human
source: company-wiki
---
```

Do not rely on the Markdown filename alone as the permanent identity if files may be moved.

## Stable document IDs

Use a deterministic ID derived from the repository-relative path unless you have another immutable identifier.

Example:

```text
wiki:architecture/authentication.md
```

If the file is renamed, treat that as a delete + create unless your synchronization layer has an explicit rename mapping.

This avoids accidentally leaving stale knowledge under the old identity.

## Sync algorithm

The synchronization process should be incremental.

### Initial import

1. Walk all `.md` files.
2. Parse frontmatter.
3. Compute a content hash.
4. Assign/read the stable document ID.
5. Retain each document into Hindsight.
6. Record the Git commit SHA and content hash in a local sync manifest.

Example:

```json
{
  "wiki:architecture/authentication.md": {
    "sha256": "...",
    "git_commit": "abc123"
  }
}
```

### Subsequent import

For every Git commit:

```text
changed file
     │
     ├── content unchanged → skip
     │
     └── content changed
             │
             ▼
        re-index document
```

Also detect:

```text
deleted file → delete corresponding Hindsight document
renamed file → delete old ID + ingest new ID
```

## The critical update/delete issue

Do not simply call `retain()` indefinitely with the new version of the same Markdown file and assume old facts disappear.

Your synchronization layer should explicitly handle document replacement/deletion according to the Hindsight document API and your chosen update mode.

> **Answered for 0.10.1.** A repeated `document_id` **replaces**. The
> `POST /memories` description says: *"If a memory item has a `document_id` that
> already exists, the old document and its memory units will be deleted before
> creating new ones (upsert behavior)."* `MemoryItem.update_mode` offers
> `replace` (the default) and `append`; the default is stated in prose only, not
> as a schema `default` key, so a generated client sees no default.
>
> One consequence this document does not draw: because the facts are deleted and
> re-extracted rather than amended, **their identifiers change**. Every
> observation consolidated from the old facts is re-derived, every page built on
> those observations goes stale, and any citation that named a fact identifier
> is left dangling. An edit to a runbook is therefore not a small write — it
> invalidates a chain. A synchroniser should report the size of that cascade
> rather than treating a retain as a write. See D-8 in [`knowledge.md`](../knowledge.md).

Hindsight exposes document operations including:

- list documents,
- inspect documents,
- inspect chunks,
- update documents,
- delete documents,
- reprocess documents.

See:

https://hindsight.vectorize.io/developer/api/documents

Before implementing production sync, verify the exact current update semantics for your Hindsight version, particularly whether a repeated `document_id` replaces, appends, or otherwise updates the stored source.

## Two ingestion modes

There are two sensible strategies.

### Strategy 1 — Normal Hindsight extraction

Use Hindsight's normal extraction pipeline.

```text
Markdown
   ↓
LLM extraction
   ↓
facts
entities
relationships
   ↓
Hindsight retrieval
```

This is the default recommendation when you want the agent to reason about the wiki as knowledge.

Advantages:

- facts are extracted,
- entities are linked,
- relationships can enter the graph,
- semantic retrieval works,
- keyword retrieval works,
- temporal/graph retrieval can participate where relevant.

### Strategy 2 — Chunk-oriented retrieval

Hindsight also supports a `chunks` extraction mode, which stores chunks as-is without LLM fact extraction.

This can make sense if the wiki is primarily a document corpus and you want retrieval close to conventional RAG.

Hindsight documents describe `chunks` mode as skipping LLM fact extraction and storing chunks as-is.

> **Precision note.** `retain_extraction_mode` is typed as a free-form nullable
> string in the API description, not as an enum. The five documented values —
> `concise` (default), `verbose`, `custom`, `verbatim`, `chunks` — exist only in
> the field's description text. A generated client cannot validate against them.
> If you wrap this, wrap it with a real enum and send the value explicitly.

For a human-authored company wiki, I would generally start with normal extraction and evaluate retrieval quality before switching to chunk-only ingestion.

## Recommended hybrid approach

For an important human wiki, I would actually expose **two retrieval paths** to the agent:

```text
                  Agent
                    │
             ┌──────┴──────┐
             │              │
          Wiki search     Recall
             │              │
             ▼              ▼
        whole documents   facts/entities
             │              │
             └──────┬───────┘
                    ▼
                 answer
```

Use:

### Wiki/document search when the agent needs:

- exact documentation,
- procedures,
- architecture descriptions,
- policy text,
- examples,
- source context,
- the canonical human wording.

### `recall()` when the agent needs:

- a specific fact,
- entity relationships,
- historical information,
- information spread across several documents,
- facts learned from conversations in addition to the wiki.

Hindsight's Knowledge Base search is currently hybrid BM25 + vector search and returns whole pages with snippets.

Hindsight's normal recall combines multiple retrieval strategies including semantic, keyword, graph, and temporal retrieval, followed by reranking.

> **Worth knowing precisely.** "BM25" here is a family name rather than a
> ranking function. Hindsight ships five pluggable text-search backends selected
> by `HINDSIGHT_API_TEXT_SEARCH_EXTENSION`, and the default `native` one is
> PostgreSQL `tsvector` + `ts_rank_cd`, which is TF-IDF and is documented as
> *"not true BM25"*. The conclusion in this document is unaffected — keyword
> search still has to stay on for a wiki, because `OAuth 2.1` and `ADR-014` are
> exactly the kind of string a vector arm misses — but do not assume the scoring
> is BM25 when you go looking for a knob to turn.
>
> One more asymmetry to be aware of: page search fuses **two** arms (keyword and
> vector) with reciprocal-rank fusion and deliberately has **no** reranker, so
> that it stays fast enough to be an agent's first call. Recall fuses **four** and
> does rerank. Turning keyword search off removes the keyword arm from page
> search as well, and a page-search call will silently become pure vector.

Sources:

- https://hindsight.vectorize.io/developer/knowledge-pages
- https://hindsight.vectorize.io/developer/configuration

## A useful mental model

Think of the human wiki as a **database of statements** and Hindsight as the **query/index engine**.

Do not ask:

> "How do I make Hindsight own my wiki?"

Ask:

> "How do I make Hindsight derive a retrieval representation from my wiki?"

That gives you much safer ownership semantics.

## Recommended agent behavior

Give the agent an explicit instruction along these lines:

```text
The company wiki is human-authored source material.

When answering questions about company policy, architecture,
procedures, or documented decisions:

1. Search the human-authored wiki.
2. Prefer current wiki documents over agent-generated summaries.
3. Treat Hindsight memories as retrieval aids, not authoritative
   replacements for the source document.
4. When the wiki and an inferred memory disagree, retrieve and
   inspect the source document.
5. Do not modify the human wiki.
```

The exact agent prompt is up to your application, but the principle is important: **retrieval authority should be explicit.**

## Do not feed Hindsight's generated wiki back into the human wiki

Avoid this loop:

```text
human wiki
    ↓
Hindsight
    ↓
generated Knowledge Page
    ↓
Git
    ↓
Hindsight
```

That creates an increasingly self-referential system.

Prefer:

```text
                 ┌───────────────┐
                 │ Human Git wiki│
                 └───────┬───────┘
                         │
                         ▼
                    Hindsight
                         │
                ┌────────┴────────┐
                ▼                 ▼
             memories         KB projection
                │
                ▼
               agent
```

The KB projection can be useful, but it is downstream.

## If you want Hindsight's Markdown wiki UI

There is still a good reason to use Hindsight's Knowledge Base.

Hindsight can project its knowledge base onto a filesystem as Markdown, and its documentation describes the tree as ordinary directories and Markdown files.

That is excellent for **reading what Hindsight believes**.

It should simply not be confused with your authoritative Git wiki.

You can therefore have:

```text
repo/wiki/                 ← human source
    architecture/
    policies/
    decisions/

hindsight/wiki/            ← Hindsight projection
    ...
```

The first is edited by humans.

The second is generated/maintained by Hindsight.

## Alternative: use Knowledge Base pages as imported documents

> **Closed for 0.10.1: not possible.** This section originally said "if your
> particular deployment/version supports the page options you need". It does not,
> and the reason is structural rather than a missing feature.
>
> `CreatePageRequest` and `UpdateNodeRequest` accept exactly `name`,
> `source_query`, `parent_id`, `tags`, `max_tokens` and `trigger`. There is no
> body, no content and no markdown field on either. A page's text is always
> synthesized from `source_query` by a model, and creating one stores placeholder
> content while a first build is scheduled.
>
> The flag that looks like an escape hatch is not one. `KnowledgeNode.managed` is
> described as *"Client-set flag: true = system-owned, false = hand-authored"*,
> and the documentation does describe it that way — but in the API description it
> occurs **exactly once**, in `KnowledgeNode`, which is a *response* schema, and
> in **no request anywhere**. It is readable and not settable. Designing against
> it would be designing against nothing.
>
> So: **retained documents for the canonical ingestion path**, Knowledge Pages for
> the generated projection. Not as a preference — as the only option the API
> offers.

The API exposes:

```text
POST /v1/default/banks/{bank_id}/knowledge-base/pages
```

and page search:

```text
GET /v1/default/banks/{bank_id}/knowledge-base/search
```

The current Knowledge Pages documentation emphasizes that pages are normally living documents generated from the bank's observations.

## Proposed production architecture

```text
                         Git repository
                              │
                              │ push / PR merge
                              ▼
                     ┌─────────────────┐
                     │ Wiki Sync Worker│
                     └────────┬────────┘
                              │
                    changed Markdown only
                              │
                 ┌────────────┴────────────┐
                 │                         │
                 ▼                         ▼
          content hash              stable document ID
                 │                         │
                 └────────────┬────────────┘
                              ▼
                       Hindsight retain
                              │
                ┌─────────────┼─────────────┐
                ▼             ▼             ▼
             facts        entities       chunks
                │             │             │
                └─────────────┼─────────────┘
                              ▼
                       Hindsight indexes
                              │
                 ┌────────────┴────────────┐
                 ▼                         ▼
             recall()                 KB search
                 │                         │
                 └────────────┬────────────┘
                              ▼
                            Agent
```

## Suggested sync worker

A small service is enough.

Pseudo-code:

```python
# Walk the merge, not the working tree. This document says sync on merge; a glob
# over the checkout will happily index a change nobody has reviewed.
COMMIT = "abc123"   # the commit being reconciled

for path in merged_tree.glob("**/*.md"):
    frontmatter, markdown = parse_frontmatter(path)

    # A file with no frontmatter is still a file. Fall back to the path rather
    # than raising: frontmatter is optional, and the identifier is derivable
    # from the file alone either way.
    document_id = frontmatter.get("id") or derive_id_from_path(path)

    content_hash = sha256(markdown.encode()).hexdigest()

    if manifest.get(document_id) == content_hash:
        continue

    hindsight.retain(
        bank_id=BANK,
        document_id=document_id,
        content=markdown,
        # Record who wrote this. Without it the prune below cannot tell this
        # worker's files from another worker's, and will delete them.
        metadata={"owner": WORKER_ID, "source_commit": COMMIT},
    )

    manifest[document_id] = content_hash

# Prune only what this worker put there. A bare
# `previously_indexed_ids - current_ids` deletes every document in the bank that
# this walk did not see, which is every document another worker synced. The bank
# may legitimately be shared with an assistant's conversation memory, which is
# not in any wiki tree at all and would simply vanish.
for document_id in previously_indexed_ids - current_ids:
    if owner_of(document_id) != WORKER_ID:
        continue
    hindsight.delete_document(
        bank_id=BANK,
        document_id=document_id,
    )

save_manifest(manifest)
```

> **What changed here, and why it matters more than it looks.** The original was
> correct for one worker over one directory, which is the easy case, and wrong in
> two ways that only show up in production. It indexed the working tree rather
> than the merge, contradicting the document's own advice four sections later. And
> its prune was unconditional, so adding a second directory — or letting an
> assistant retain conversation memory into the same bank — silently destroys
> everything it did not write. Both are the kind of bug that is invisible until
> somebody's documentation is gone.
>
> The `manifest` deserves a note too: it is an optimisation that avoids
> re-extracting unchanged files, **not** a source of truth. Identity and digest
> are both computable from the file itself, so losing the manifest costs one full
> re-index and nothing else. Any design where the manifest is the *only* record
> of what was indexed has made a second thing that can be lost.

The exact client method for document deletion/update should be aligned with the Hindsight client version you install.

## Git integration

A simple deployment is:

```text
developer
   │
   ▼
git commit
   │
   ▼
pull request
   │
   ▼
merge to main
   │
   ▼
CI/CD
   │
   ▼
wiki-sync
   │
   ▼
Hindsight
```

I would **not** sync on every local edit.

Sync after the human-authored content is committed/merged.

That makes the wiki's state reproducible:

```text
Git commit abc123
        ↓
Hindsight index corresponding to abc123
```

## Versioning

Put the source commit in your sync metadata.

For example:

```yaml
---
id: wiki:architecture/authentication
authority: human
source: company-wiki
source_commit: abc123
source_path: architecture/authentication.md
---
```

You do not necessarily need to put this metadata into the actual Markdown sent to Hindsight; your synchronizer can maintain it separately.

The important thing is that you can answer:

> "Which human-authored version produced this memory?"

## Provenance

For high-value knowledge, provenance is extremely useful.

The agent should be able to get back to:

```text
Hindsight memory
      ↓
document
      ↓
wiki:architecture/authentication.md
      ↓
Git commit
      ↓
human-authored Markdown
```

This lets the agent distinguish:

```text
"This is something Hindsight inferred"
```

from:

```text
"This is explicitly documented by humans."
```

That distinction becomes especially important when documents contain ambiguous or conflicting statements.

## Handling conflicting documents

Do not try to solve every contradiction in the sync layer.

Let Hindsight index the documents, but preserve source provenance.

For example:

```text
ADR-001: use PostgreSQL
ADR-014: migrate this service to CockroachDB
```

Hindsight can retrieve both.

The agent can then inspect:

- document date,
- document identity,
- explicit status,
- newer ADRs,
- source text.

For decisions, I recommend adding explicit metadata:

```yaml
---
id: wiki:decisions/ADR-014
kind: decision
status: active
supersedes:
  - wiki:decisions/ADR-001
---
```

> **Correction.** This example used `status: accepted`, which is not one of the
> three values the "Suggested Markdown conventions" section of this same document
> defines (`active | deprecated | draft`). A fourth value in one example and not
> in the definition is how a filter quietly returns nothing. Use `active`, and if
> a decision needs a rejected state, add it to the definition deliberately
> rather than to one example.

That makes organizational knowledge much easier for the agent to interpret.

## Suggested Markdown conventions

For durable organizational knowledge, use:

```yaml
---
id: wiki:...
kind: architecture | policy | decision | procedure | reference
status: active | deprecated | draft
authority: human
---
```

For ADRs:

```yaml
---
id: wiki:decisions/ADR-014
kind: decision
status: accepted
date: 2026-08-14
supersedes:
  - wiki:decisions/ADR-001
---
```

For policies:

```yaml
---
id: wiki:policies/security
kind: policy
status: active
owner: security
reviewed: 2026-08-01
---
```

These are particularly valuable because they give the extraction system explicit semantic signals instead of forcing the LLM to infer everything from prose.

## What I would build first

### Phase 1

Implement:

```text
Git Markdown
    ↓
Python sync script
    ↓
Hindsight retain()
```

Use stable document IDs and a hash manifest.

### Phase 2

Add:

```text
Agent
  ├── recall()
  └── human-wiki search
```

Make the agent explicitly retrieve human documentation for authoritative questions.

### Phase 3

Add provenance:

```text
memory → document → Git path → Git commit
```

### Phase 4

Add automatic synchronization on merge to `main`.

### Phase 5

Evaluate whether Hindsight Knowledge Pages add value as a separate human-readable projection.

## One important configuration decision

Hindsight currently documents several retrieval controls, including:

- semantic retrieval,
- BM25/text retrieval,
- graph retrieval,
- temporal retrieval,
- reranking.

For a wiki, **do not disable BM25/text search** unless you have tested the consequences.

Exact terminology matters enormously in technical documentation:

```text
OAuth 2.1
PostgreSQL
ADR-014
JWT
Kubernetes
CustomerID
```

Semantic similarity alone is not always sufficient.

Hindsight's current configuration documentation says text search contributes a keyword/BM25 arm and also affects Knowledge Page search.

Source:

https://hindsight.vectorize.io/developer/configuration

## The final architecture I recommend

```text
                   HUMAN AUTHORS
                         │
                         ▼
                  ┌──────────────┐
                  │ Git Markdown │
                  │    wiki      │
                  └──────┬───────┘
                         │
                    merged commit
                         │
                         ▼
                  ┌──────────────┐
                  │ Wiki Syncer  │
                  │ hash + IDs   │
                  └──────┬───────┘
                         │
                         ▼
                  ┌──────────────┐
                  │   Hindsight  │
                  │    retain    │
                  └──────┬───────┘
                         │
          ┌──────────────┼──────────────┐
          ▼              ▼              ▼
       memories       entities        chunks
          │              │              │
          └──────────────┼──────────────┘
                         ▼
                   Hindsight index
                         │
             ┌───────────┴───────────┐
             ▼                       ▼
          recall()              KB search
             │                       │
             └───────────┬───────────┘
                         ▼
                       AGENT
                         │
                         ▼
                 answer + provenance
```

### Core rule

**Humans write. Git owns. Hindsight indexes. Agents retrieve.**

That gives you the reverse direction you were looking for without sacrificing the most useful property of a human-authored wiki: a clear, reviewable, reproducible source of truth.

## Current Hindsight references

- Hindsight Knowledge Pages: https://hindsight.vectorize.io/developer/knowledge-pages
- Knowledge Pages API: https://hindsight.vectorize.io/developer/api/knowledge-pages
- Retain / ingestion: https://hindsight.vectorize.io/developer/api/retain
- Documents API: https://hindsight.vectorize.io/developer/api/documents
- Main methods: https://hindsight.vectorize.io/developer/api/main-methods
- Configuration/retrieval: https://hindsight.vectorize.io/developer/configuration
- Current HTTP API reference: https://api.hindsight.vectorize.io/docs

