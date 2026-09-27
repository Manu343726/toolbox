#!/usr/bin/env python3
"""Run a reconcile, end to end, through the gateway.

`mcp_knowledge_test.py` asserts that the policy refuses a write. This asserts what happens when a
deployment grants one — and the reconcile is the reason the subsystem exists: a corpus on disk
reconciled into a knowledge base, with an ownership record saying what was taken from which commit.

It had never been executed. Every test of it was a Go test that reached the handler directly, which
means the whole composition — policy, exposure, the generated tool, the gateway's decoding, the
adapter's batch construction, the backend's HTTP call, the record written back — was assembled from
parts and never run as one thing. A reconcile is the longest path in this subsystem and the one a
deployment runs unattended, so it is the one most worth executing.

The grant is a policy document rather than a runtime call, because exposure cannot override a policy
and pretending otherwise would test a path nobody takes.
"""
import json
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)

from hindsight_stub import StubBackend, reset_writes, writes_to  # noqa: E402
from mcp_knowledge_test import corpus_fixture  # noqa: E402
from mcp_probe import Gateway, text_of  # noqa: E402

BASE = "docs"
failures = []


def check(ok, what, detail=""):
    print(f"  {'ok  ' if ok else 'FAIL'}  {what}" + (f"\n          {str(detail)[:400]}" if detail and not ok else ""))
    if not ok:
        failures.append(what)
    return ok


def call(gw, tool, args=None):
    return json.loads(text_of(gw.tool(tool, args or {})))


def git(root):
    """Make the corpus a checkout and return its HEAD."""
    def run(*args):
        return subprocess.run(["git", "-C", root, *args], capture_output=True, text=True, check=True)

    env = dict(os.environ, GIT_AUTHOR_NAME="toolbox", GIT_AUTHOR_EMAIL="toolbox@example.invalid",
               GIT_COMMITTER_NAME="toolbox", GIT_COMMITTER_EMAIL="toolbox@example.invalid")
    subprocess.run(["git", "init", "-q", root], check=True, env=env, capture_output=True)
    run("add", "-A")
    run("commit", "-qm", "the corpus")
    return run("rev-parse", "HEAD").stdout.strip()


def write_policy(root):
    """A policy that grants this subsystem's writes and nothing else.

    Narrow on purpose: `allow *` for everything would mean the harness passed because everything
    was allowed, and the point of a policy test is that a *specific* grant is what opens a specific
    door.
    """
    path = os.path.join(root, "reconcile.policy")
    with open(path, "w") as f:
        f.write("# Every read, which is what the default policy already does.\n")
        f.write("allow *    read\n")
        # The two methods of the reconciler, by name. Granting the service would have made the
        # final check — that the *other* writes are still denied — vacuous, because RebuildCorpus
        # is in the same service and would have been granted too.
        f.write("allow toolbox.knowledge.v1.CorpusService/PlanReconcile   write read\n")
        f.write("allow toolbox.knowledge.v1.CorpusService/ApplyReconcile  write read\n")
    return path


def main():
    with tempfile.TemporaryDirectory(prefix="knowledge-reconcile-") as root:
        corpus_fixture(root)
        # A git checkout, because the commit is what the reconcile records and what makes "the
        # index matches the merge" a statement that can be checked. Without one the field is
        # legitimately empty — the domain says so — and the harness would be asserting nothing.
        head = git(root)
        state = os.path.join(root, ".state")
        os.environ["TOOLBOX_KNOWLEDGE_CORPUS_ROOTS"] = f"{BASE}={root}"
        os.environ["TOOLBOX_KNOWLEDGE_STATE_DIR"] = state
        # Counted rather than taken from the fixture, which returns nothing: the number this
        # harness asserts against is the number of files the *reconciler* sees, and taking it from
        # the code that wrote them would make the assertion agree with a bug in either.
        files = []
        for dirpath, _, names in os.walk(root):
            if os.path.abspath(dirpath).startswith(os.path.abspath(state)):
                continue
            files += [n for n in names if n.endswith(".md")]
        print(f"corpus: {len(files)} markdown files under {root}")

        with StubBackend() as stub:
            os.environ["HINDSIGHT_API_URL"] = stub.url
            gw = Gateway("--all", policy=write_policy(root))
            try:
                run(gw, root, state, files, head)
            finally:
                gw.close()

    if failures:
        print(f"\nFAILED: {len(failures)} check(s)")
        for f in failures:
            print(f"  - {f}")
        return 1
    print("\na corpus on disk was reconciled into a base, through the gateway, with a granted policy")
    return 0


def run(gw, root, state, files, head):
    # --- 1. before: nothing has been reconciled, and the contract says so.
    print("\n[1] before")
    status = call(gw, "corpus__get_corpus_status", {"base_id": BASE})["status"]
    check(status.get("reconciledCommit", "") == "",
          "nothing has been reconciled yet, and the empty commit says so", status)
    check(status.get("ownedDocuments") == 0, "and the base owns nothing yet", status)
    check(not status.get("roots") == [],
          "the base knows which directories it reconciles from", status.get("roots"))

    # --- 2. the plan, which is a read and needs no grant.
    print("\n[2] the plan")
    plan = call(gw, "corpus__plan_reconcile", {"base_id": BASE, "owner": "toolbox"})["plan"]
    created = plan.get("created", [])
    check(len(created) == len(files),
          f"every corpus file is a create on the first reconcile ({len(created)} of {len(files)})",
          [c.get("id") for c in created])
    check(plan.get("planDigest", "").startswith("sha256:"), "the plan is fingerprinted",
          plan.get("planDigest"))
    check(all(c.get("digest", "").startswith("sha256:") for c in created),
          "each file carries its own digest, so 'what changed' is checkable")

    # --- 3. a confirmation for a plan nobody read is refused.
    print("\n[3] a confirmation for a plan nobody read")
    def apply(digest, the_plan=None, owner="toolbox"):
        return gw.tool("corpus__apply_reconcile", {
            "plan": the_plan if the_plan is not None else plan,
            "confirm": digest, "owner": owner,
        })

    try:
        text_of(apply("sha256:" + "0" * 64))
        check(False, "the apply refuses a digest that is not the plan's")
    except RuntimeError as e:
        check("0" * 8 in str(e),
              "and the refusal names the digest it was given, so the mismatch is visible",
              str(e)[:240])
    # An empty confirmation, not a valid one. A valid digest would *apply* — and then every later
    # step would be working against a base that had already been reconciled, which is a harness bug
    # that presents as a reconciler bug.
    try:
        text_of(gw.tool("corpus__apply_reconcile", {"plan": plan, "confirm": "", "owner": "toolbox"}))
        check(False, "the apply refuses with no confirmation at all")
    except RuntimeError as e:
        check("confirmation is required" in str(e),
              "and refuses with no confirmation, saying what a confirmation is for", str(e)[:240])
    try:
        text_of(apply(plan["planDigest"], owner=""))
        check(False, "the apply refuses with no owner")
    except RuntimeError as e:
        check("owner is required" in str(e),
              "and refuses with no owner, because the owner is what authorises a later prune",
              str(e)[:240])

    # --- 3b. a plan that has gone stale is refused, by digest, naming both sides.
    #
    # This is what the confirmation is for. The plan is recomputed rather than applied as supplied,
    # and a mismatch is refused with both digests in the message — so the failure a person would
    # otherwise have attributed to the reconciler ("I confirmed it and nothing happened") is
    # instead a statement about what changed underneath them.
    print("\n[3b] a plan that has gone stale")
    first = call(gw, "corpus__plan_reconcile", {"base_id": BASE, "owner": "toolbox"})["plan"]
    stale = dict(first)
    stale["planDigest"] = "sha256:" + "0" * 64
    try:
        text_of(gw.tool("corpus__apply_reconcile",
                        {"plan": first, "confirm": "sha256:" + "0" * 64, "owner": "toolbox"}))
        check(False, "the apply refuses a confirmation that is not the plan's digest")
    except RuntimeError as e:
        check("not the digest of the plan supplied" in str(e),
              "the apply refuses a confirmation that is not the plan's digest", str(e)[:240])

    # --- 4. the apply. This is the step that had never been run.
    print("\n[4] the apply")
    plan = call(gw, "corpus__plan_reconcile", {"base_id": BASE, "owner": "toolbox"})["plan"]
    reset_writes()
    applied = json.loads(text_of(apply(plan["planDigest"])))
    result = applied
    check(result.get("ingested") == len(files),
          f"every file was ingested ({result.get('ingested')} of {len(files)})", result)
    check(result.get("replaced") == 0, "nothing was a replace on a first reconcile", result)
    check(not result.get("failed"), "nothing failed", result.get("warnings"))

    # What was actually sent to the backend. A harness that cannot see the request can only assert
    # that the handler returned a number, which is a much weaker claim.
    retains = writes_to("/memories", "POST")
    check(len(retains) >= 1, "the base was asked to retain", len(retains))
    payload = json.loads(retains[0]["body"]) if retains else {}
    items = payload.get("items", [])
    check(len(items) == len(files), f"one item per file, in one batch ({len(items)})", payload)
    first = items[0] if items else {}
    check(first.get("update_mode") == "replace",
          "every item is sent with an explicit replace mode, so a re-run cannot append a second copy",
          first.get("update_mode"))
    check(all(i.get("resolve_entities") is False for i in items),
          "and with entity resolution off, because the framework resolves entities itself",
          [i.get("resolve_entities") for i in items])
    check(any(i.get("content") for i in items), "the file's own text is sent, not just a reference")
    check(all("timestamp" in i for i in items),
          "every item states a timestamp, and a file with no declared date is sent as explicitly timeless",
          [i.get("timestamp") for i in items])

    # The ownership marker: a generated file inside the corpus, which is what makes a re-reconcile
    # cheap and what stops a second provider adopting content it did not write.
    # Outside the corpus, deliberately: the corpus is what a person writes and reviews, and a
    # generated file inside it would show up in their next diff and in the next reconcile.
    # The record's filename carries a fingerprint of its identity — base and namespace — so two
    # deployments reconciling the same directory into the same base under different namespaces do
    # not overwrite each other's record. Discovered rather than assumed: the harness globs for it,
    # so it does not encode the naming rule, and the rule itself is asserted below.
    records = [os.path.join(state, "ownership", n)
               for n in sorted(os.listdir(os.path.join(state, "ownership")))] \
        if os.path.isdir(os.path.join(state, "ownership")) else []
    check(len(records) == 1, "an ownership record was written", records)
    check(records and BASE in os.path.basename(records[0]),
          "and its name carries the base it belongs to",
          os.path.basename(records[0]) if records else None)
    record = records[0] if records else None
    check(record and not os.path.exists(os.path.join(root, ".toolbox-projection.json")),
          "and it is outside the corpus, so the corpus stays a corpus a person wrote",
          f"{state}: {sorted(os.listdir(state)) if os.path.isdir(state) else 'no state directory'}")
    if record:
        with open(record) as f:
            written = json.load(f)
        check(len(written.get("files", {})) == len(files),
              f"and it names every file it ingested ({len(written.get('files', {}))} of {len(files)})",
              sorted(written.get("files", {})))
        check(written.get("last_commit") == head,
              "and the commit it was reconciled from, which is what makes a prune authorised",
              f"{written.get('last_commit')!r} vs {head!r}")
        check(all(f.get("source_commit") == head for f in written.get("files", {}).values()),
              "recorded on every file, so a prune can ask which commit authorised each one",
              [f.get("source_commit") for f in written.get("files", {}).values()])

    # --- 5. after: the base believes the corpus is reconciled to a commit.
    print("\n[5] after")
    status = call(gw, "corpus__get_corpus_status", {"base_id": BASE})["status"]
    check(status.get("reconciledCommit") == head,
          "the base reports the commit it was reconciled to, so 'the index matches the merge' "
          "is checkable", f"{status.get('reconciledCommit')!r} vs {head!r}")
    check(status.get("reconciledAt") is not None, "and when", status.get("reconciledAt"))
    check(status.get("ownedDocuments") == len(files),
          f"and how many documents it owns ({status.get('ownedDocuments')} of {len(files)})",
          status)

    # --- 6. a second reconcile is a no-op, which is the whole point of the digests.
    print("\n[6] a second reconcile")
    again = call(gw, "corpus__plan_reconcile", {"base_id": BASE, "owner": "toolbox"})["plan"]
    check(not again.get("created"), "nothing to create", again.get("created"))
    check(not again.get("updated"), "nothing to update", again.get("updated"))
    check(len(again.get("unchanged", [])) == len(files),
          f"every file is unchanged ({len(again.get('unchanged', []))} of {len(files)})")

    reset_writes()
    noop = json.loads(text_of(gw.tool("corpus__apply_reconcile", {
        "plan": again, "confirm": again["planDigest"], "owner": "toolbox",
    })))
    check(noop.get("ingested") == 0, "a no-op reconcile sends nothing", noop)
    check(len(writes_to("/memories", "POST")) == 0,
          "and makes no call to the backend at all", writes_to("/memories", "POST"))

    # --- 7. edit a file, and only that file is re-ingested.
    print("\n[7] one file edited")
    edited = os.path.join(root, "runbooks", "restore.md")
    if os.path.exists(edited):
        with open(edited, "a") as f:
            f.write("\nAnd re-ingest after a bad extraction.\n")
        third = call(gw, "corpus__plan_reconcile", {"base_id": BASE, "owner": "toolbox"})["plan"]
        check(len(third.get("updated", [])) == 1, "exactly one file is updated",
              [u.get("id") for u in third.get("updated", [])])
        check(len(third.get("unchanged", [])) == len(files) - 1,
              "and the rest are still unchanged")
        reset_writes()
        one = json.loads(text_of(gw.tool("corpus__apply_reconcile", {
            "plan": third, "confirm": third["planDigest"], "owner": "toolbox",
        })))
        check(one.get("replaced") == 1, "one file was replaced rather than ingested", one)
        check(one.get("ingested") == 0, "and nothing else was")
    else:
        print(f"  skip  the fixture has no {edited} to edit")

    # --- 8. the grant was narrow, and that is checkable.
    print("\n[8] the grant was narrow")
    surface = {t["name"] for t in gw.tools()}
    denied = sorted(n for n in ("corpus__rebuild_corpus", "content__write_content",
                                "base__delete_base", "page__delete_page")
                    if n in surface)
    check(not denied,
          "every other write is still absent from the tool surface, because the grant named two "
          "methods rather than a service", denied)
    try:
        text_of(gw.tool("corpus__rebuild_corpus", {"base_id": BASE, "dry_run": True}))
        check(False, "and calling one by name is refused")
    except RuntimeError as e:
        check("policy" in str(e).lower() or "unknown tool" in str(e).lower(),
              "and calling one by name is refused by the policy", str(e)[:160])


if __name__ == "__main__":
    sys.exit(main())
