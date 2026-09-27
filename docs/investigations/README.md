# Investigations

Documents that were read to make a decision. Not specifications: each one is
what was found and what it implies, kept so the reasoning behind a decision
survives the decision being rewritten.

| Document | What it is | Status |
|---|---|---|
| [`hindsight-knowledge-backend.md`](hindsight-knowledge-backend.md) | A survey of the memory backend's API and three candidate placements for it, plus the reasoning that the inverse of its filesystem projection cannot be "create a page with the body I typed". | Superseded by [`knowledge.md`](../knowledge.md), which is the specification. Kept for the evidence. |
| [`hindsight-human-wiki-integration.md`](hindsight-human-wiki-integration.md) | An integration design for using a human-authored markdown wiki with the backend, written outside the project. | An **input** to [`knowledge.md`](../knowledge.md). Where the two differ, the specification is right. |

## How they relate

The order they were written in is the order they build on each other:

```
hindsight-knowledge-backend.md     the backend, surveyed; three ways to place it
            │
            ▼
hindsight-human-wiki-integration.md   how the corpus relates to that backend
            │
            ▼
knowledge.md                       the specification, written with both open
```

The first document established that a knowledge page is a projection over
processed memory and has no writable body, which is why authored prose has to
become a document rather than a page. The second took that as given and worked
out the corpus side: the frontmatter contract, the identity rule, the
commit-correspondence property, and the rule that the corpus is written by people
rather than by the tool.

The specification then had to answer a question the second document correctly
declined to guess at — what a repeated document identifier actually does to the
facts already extracted from it. The answer is in §5.4 and D-8 of
[`knowledge.md`](../knowledge.md), and it is the one place where the
specification is ahead of its inputs.

## A note on the vendored document

`hindsight-human-wiki-integration.md` is the file as it was written elsewhere,
unedited. It is kept as it is so the design can be read as written and the
specification's disagreements with it stay visible rather than smoothed over. If a
new version of that design arrives, replace the file and record what the
specification changed to match — that difference is usually the most useful thing
in the repository.
