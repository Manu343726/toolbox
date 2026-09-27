# 0014 — A knowledge base is a corpus, an engine, and a mount; the corpus is never written through

Date: 2026-09-27
Status: accepted (written after the implementation, which changed two things in it — see
*What the implementation changed*)

## Context

A knowledge base is the thing a user asks when they want to know what an AI system believes. The
temptation is to expose that as a CRUD surface: `WritePage`, `DeleteDocument`, `AddMemory`. It
is the wrong shape, and the reason is not architectural tidiness — it is that a CRUD surface makes
the *derived* half of a base indistinguishable from the *authored* half, and they have
completely different lifecycles.

Hindsight, the backend this mounts, holds two things in one place:

- **Facts**, extracted from documents by an LLM. They are claims with provenance chains, and a
  `replace` invalidates their identifiers, so a citation naming one becomes dangling.
- **Mental models and pages**, synthesized documents that are kept and rewritten as the base
  changes. They are what the system currently believes, and they change without review.

A third thing belongs in the same deployment and is *not* in the backend at all: a directory of
markdown somebody wrote, with an editor, hooks, a blame view and a review process.

Three options for the relationship between the corpus and the base:

| Option | What it costs |
|---|---|
| The backend is the store; writes go through the API | A tool that edits prose behind a person's back, fights their editor, their hooks and their review, and creates a second source of truth the next reconcile deletes |
| The corpus is imported once and never reconciled | A base that drifts from the documents it claims to be, with no way to tell — a reader sees a confidently wrong base and the repository says otherwise |
| The corpus is authoritative and the base is derived | The base is rebuildable from a directory, and every question about a disagreement has an answer that is not "whichever wrote last" |

The third was chosen. It has a cost — a reconcile is asynchronous, a base lags its corpus, and
"what does the system believe" and "what does the document say" are different questions — and the
cost is why the design has to answer a second question.

## Decision

**The corpus is authoritative. The backend is derived. And the corpus is never written through
the API.**

`CorpusService` has no write for the corpus. A `WriteContent` against authored content is
refused with a pointer to the file rather than quietly accepted, and a `DeleteContent` against
authored content is refused the same way. The corpus has an editor; this does not.

**A reconcile is a plan and a confirmation, and the confirmation carries the plan's digest.**
`PlanReconcile` computes a plan from the directory and the ownership record. `ApplyReconcile`
takes a `confirm` and refuses unless it equals the digest of the plan as recomputed. So the
proposal and the approval cannot be reordered, and a confirmation cannot be applied to a directory
that has changed since the person read it. This is the failure a person would otherwise attribute
to the reconciler.

It is a value the agent relays and the user answers, which is what makes it work on a transport
that cannot elicit at all (repository rule 19).

**The derived half is served whole, and the two origins are namespaced.** A wiki file, a
retained document and a generated page are all `Content` with an `Origin` a caller did not
choose. `ExportWiki` returns both halves at once under `file/` and `generated/`, so the tree says
which half a file came from before anybody opens it, and two files with the same relative path
cannot collide.

**The mount is an RPC and it is strictly read-only.** `MountService` exposes
`GetMountStatus`, `EnableMount` and `DisableMount`; every write path through the filesystem
returns `EROFS`. `ExportWiki` is the feature and the mount is one way to consume it, so nothing
depends on it. FUSE is behind a `fuse` build tag: a host without it still builds the subsystem,
still serves all thirteen services, and `EnableMount` returns `Unimplemented` *naming the tag*.

## What the implementation changed

**The mount stopped being a command.** The specification said it had to be: *"a mount lives in
the filesystem namespace of whoever runs it, so an RPC cannot create one that the caller can
see."* That is true, and it is not a reason to refuse — the premise it rests on is that the
caller and the subsystem are on different machines, and in the deployment this exists for they
are not. The mount is for a person auditing what a system believes, on the machine where they
work, against a deployment that is often local.

And the premise is checkable rather than assumable: `MountStatus` reports the host, so a caller
on another machine is *told* rather than silently handed a mountpoint it cannot resolve. The
earlier text turned a real property into a design constraint, and the cost was that the one
operation needing a filesystem namespace was the one operation an agent could not perform.

**A template import reports what it did rather than what it left behind.** The backend's import
response names every directive and mental model it *created* and every one it *updated*. The
first version of the contract read the base back afterwards instead. That is strictly worse: a
read-back cannot distinguish a created directive from an updated one, and a caller told "created"
about an update concludes that a rule they had written had been replaced.

**`CurateMemory` reports its cascade as unmeasured rather than as zero.** Correcting a fact makes
the backend re-derive everything consolidated from it, and it does not report how much. Reporting
zeros would tell a caller nothing else moved, which is the one answer that would be wrong. The
counters are zero and the notes say they are unmeasured.

## Consequences

**A base lags its corpus, and that is visible.** `GetCorpusStatus` reports the commit the last
successful reconcile used, so "the index matches the merge" is a statement that can be checked
rather than believed.

**The ownership record is fail-closed.** A record that could not be loaded switches pruning *off*
and the reason becomes a warning on the plan, because a plan that silently omitted its deletions
would read as "nothing to remove". A lost record means a lost ability to prune, never an
accidental one.

**Corpus identity is bound, not inferred.** The record carries the backend origin, the absolute
corpus root and the identifier namespace, because a record from one clone of a repository does
not describe another clone, and changing the namespace renumbers every identity.

**Everything else is a projection.** A page, an index and a log are all derived and all
regenerable. The corpus is the only thing in the deployment a person edits.

## Alternatives not taken

**A CRDT over the corpus and the base.** It would have made the lag disappear and made every
question about a disagreement unanswerable in the way that matters. The disagreement is the signal.

**Writing the corpus through the API, with the review process in the RPC.** The review process is
a person reading a diff. An API that applies one is a second thing that changes a file, and a file
with two writers has no owner.

**Serving the derived half only.** It would be smaller and would answer "what does the system
believe", which is not the question. A reader who disagrees with the base has no way to find out
that the document says otherwise — and that is exactly the moment the deployment exists for.
