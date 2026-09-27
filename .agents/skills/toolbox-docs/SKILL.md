---
name: toolbox-docs
description: Write or review a specification, an ADR, an investigation, or a reference document. Use when creating or editing anything under docs/, when a change makes existing documentation wrong, or when a document needs to survive the thing it describes being replaced.
---

# Writing documentation and specifications

Use this skill when writing or editing anything under `docs/`. For *extracting*
documentation from protobuf descriptors and generating CLI help, use
`documentation-cli` instead — that one is about code, this one is about prose.

A specification is read by someone who was not in the conversation and cannot
ask a question. That single fact decides most of the rules below.

## The failure this skill exists to prevent

The knowledge specification and its investigation both spent their length
arguing about a system they never defined. They used `bank`, `observation`,
`mental model`, `consolidation`, `retain`, `recall`, `reflect`, `managed`,
`delta` and `all_strict` as if the reader already knew what Hindsight was. A
reader who did not know could follow the argument, and every claim in it,
without noticing they had no way to check anything.

Everything below follows from not doing that.

## 1. Define every external system on first use, and link it

The first time a document names something built by someone else, it says what
that thing is, and gives a URL. Not at the end in a sources list — on first
use, where the reader is actually stuck.

```markdown
Hindsight (https://hindsight.vectorize.io, HTTP API 0.10.1) is a memory
backend. You give it documents; it extracts facts from them, consolidates
those into observations, and answers questions over both. It is written by
Vectorize, not by this project, and nothing in Toolbox depends on it existing.
```

A reader who stops reading at that line should know enough to decide whether
the rest of the document concerns them.

**The test:** find every proper noun that is not defined in the repository, and
check that each one is explained and linked the first time it appears. Vendored
project names (`Hindsight`, `PostgreSQL`, `ConnectRPC`) are the ones that slip
through, because the author has been staring at them for hours.

## 2. A quote is evidence, and it can contradict you

If you quote a source, check the quote says what you are using it to say. The
knowledge spec quoted Hindsight's *"Your raw documents remain the source of truth
about what was said"* — while the same site's retain page says *"The content
itself is never stored verbatim"*, and `store_document_text` can turn it off
entirely. The quote was accurate and the conclusion drawn from it was wrong.

- Mark quotes as quotes (blockquote, or quotation marks) so nobody mistakes
  prose for a finding.
- When a source contradicts itself, say so and say which part you relied on.
- When a quote is paraphrased rather than verbatim, do not present it as a quote.

## 3. Every external claim must be re-checkable in a minute

For each claim about a third party, record enough to verify it: the source, the
version, and where in that source to look. "Hindsight replaces on a repeated
`document_id`" is a rumour. "Hindsight replaces on a repeated `document_id` —
`MemoryItem.update_mode` in `https://hindsight.vectorize.io/openapi.json`, and
the `POST /memories` endpoint description, both at 0.10.1" is a fact.

**Structural claims come from the machine-readable description, not the prose
docs.** Field names, request shapes, and which fields exist in a request versus
a response are things prose gets wrong and descriptions do not. Checking one
grep of `openapi.json` beats reading a page and hoping.

When the description and the prose disagree, say which you used and why. When
something is documented in prose but absent from the schema, that is worth a
sentence — a generated client will not see it.

## 4. Sources live in the repository

A document that cites `~/Downloads/notes.md` cites something that will not
survive a reinstall, and nobody else can check it. Copy the source into
`docs/investigations/` — unedited, so disagreements with it stay visible — and
cite the path.

## 5. State the status, the version, and the weight

Every document opens with what it is and how much to trust it:

```markdown
**Status: specification. Nothing here is implemented.**
**Backend: Hindsight 0.10.1, checked 2026-09-27.**
```

An investigation, a specification, an ADR and a status page are four different
things and a reader must be able to tell them apart in one line.

**Say what is not decided.** A specification that hides its open questions
reads as more finished than it is. Number them, order them by how much they
change the design, and record a settled question as a decision with its
reasoning rather than quietly deleting the question.

**Record rejected alternatives.** "Two read surfaces, one per half" plus a
sentence on why it fails is worth more than the reader rediscovering it.

## 6. Write about the present accurately

An investigation that reads `subsystems/knowledge` in §2 and "The knowledge
subsystem today" in the heading is a trap six months later, when the section is
still true and the heading is not. When the repository moves under a document:

- Say what the document describes and when, at the top.
- Mark the stale sections where they are, not only in the header.
- Fix counts and names that have gone stale, or delete them.

## 7. Requirements are numbered, and each is falsifiable

A requirement someone cannot fail is not a requirement. Give each an id, a
statement that could be true or false, and the requirement group it came from.
`U-3 | One read shape. A consumer reads content without first asking where it
came from.` is testable. "The API should be unified" is not.

**Say the failure mode, not the aspiration.** "If the caller asks for nothing
we return nothing" is checkable; "we degrade gracefully" is not.

## 8. Concrete beats abstract — always

The test for a sentence: could a reader act on it without asking what it means?

| Abstract | Concrete |
| --- | --- |
| "A single surface over two substrates is easy to fake." | "The easy version exposes pages and the wiki as two sets of RPCs, calls that 'one API', and leaves every consumer to work out which set it is holding." |
| "Confirmation applies for mutating operations." | "`ApplyReconcile` takes a `confirm` value the agent relays. It is a field rather than a two-call handshake so the proposal and the approval cannot be reordered." |
| "The reconcile is idempotent per batch." | "Re-running a reconcile after a dropped connection finishes the files it missed and does not re-extract the ones it finished." |
| "N-11: origin-specific behaviour is stated in the contract." | "`Mutability` is on the content. A write against a derived page is refused with `FailedPrecondition` and the reason." |

Prefer a name, a path, a field, a flag, a status code, a command, or a file you
could paste in. Prefer a real example — `OAuth 2.1`, `ADR-014` — over "a
specific term". If a paragraph would survive unchanged after swapping the
project for a different one, it is abstract; cut it or make it specific.

**One worked example beats three principles.** A spec with a real file, a real
command and a real result is checkable by a reader. A spec of principles is
only checkable by the author.

## 9. Every relative link resolves

Check them against the filesystem, do not eyeball them. A link to a document
that was renamed is worse than no link, because it looks like a citation.

```sh
# resolves every relative markdown link under docs/
python3 - <<'PY'
import pathlib, re
bad = []
for p in pathlib.Path('docs').rglob('*.md'):
    for m in re.finditer(r'\[([^\]]*)\]\(([^)]+)\)', p.read_text()):
        t = m.group(2)
        if t.startswith(('http://', 'https://', '#', 'mailto:')):
            continue
        if not (p.parent / t.split('#')[0]).resolve().exists():
            bad.append(f'{p}: {t}')
print('\n'.join(bad) or 'all resolve')
PY
```

## 10. Mark the provider's vocabulary as the provider's

A provider-neutral contract must not inherit one implementation's words. Keep a
mapping table in the document, and say plainly which words are excluded and
why. The reader needs to know that "bank" appearing in a code sample means the
contract calls it a "base", on purpose.

## Checklist before committing a doc change

- [ ] Every external system defined and linked on first use
- [ ] Every external claim re-checkable (source, version, location)
- [ ] Quotes verified against the thing they are used to prove
- [ ] Every source in the repository, every relative link resolves
- [ ] Status and version at the top; open questions and rejected alternatives present
- [ ] Sections describing removed code marked where they are
- [ ] Requirements numbered and falsifiable
- [ ] No sentence that would survive swapping the project for another one
- [ ] `make test` passes if code was touched
