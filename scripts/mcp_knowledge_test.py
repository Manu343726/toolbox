#!/usr/bin/env python3
"""Exercise the knowledge contract through the MCP gateway.

This is the test the Go suite cannot do. The unit tests call handlers directly and assert on what
the subsystem sends to a stub backend; this goes the way an agent goes — the gateway generates
tools from reflected descriptors, a policy decides which are callable, and the call crosses three
layers before a handler sees it. A contract that compiles and passes every unit test can still be
unreachable, misnamed, or denied by a policy nobody noticed.

It runs in two phases because two different things can be wrong and they need different answers:

  - **without a backend**, the reads that are local must answer and the reads that are not must
    fail with a classified error naming the cause. "Connection refused" is a backend absence;
    "internal" is a bug. Only an end-to-end call tells them apart, because a handler test stubs the
    one layer where the distinction is made.
  - **with a stub backend**, everything answers, and the engine half is a real process talking
    real HTTP.

Run it with no arguments; it manages the corpus, the stub and the gateway itself.
"""
import json
import os
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from hindsight_stub import StubBackend  # noqa: E402
from mcp_probe import Gateway, text_of  # noqa: E402

BASE = "docs"
KNOWLEDGE_PREFIXES = ("content__", "query__", "corpus__", "mount__", "knowledge_base__",
                      "memory__", "page__", "mental_model__", "directive__", "observation__",
                      "entity__", "template__", "operation__")

# Every one of these is in the contract and in the descriptor, and must be absent from the tool
# list. Rule 7: registering an API, describing a subsystem, or starting a gateway never exposes an
# operation on its own — only a policy permits one, and the empty policy permits nothing.
MUST_BE_DENIED = [
    "corpus__apply_reconcile", "content__write_content", "content__delete_content",
    "mount__enable_mount", "mount__disable_mount", "memory__curate_memory",
    "template__import_template", "template__import_base", "template__clone_base",
    "page__create_page", "page__delete_page", "page__refresh_page", "page__create_page_folder",
    "page__update_page_node", "mental_model__create_mental_model",
    "mental_model__delete_mental_model", "mental_model__clear_mental_model",
    "directive__create_directive", "directive__update_directive", "directive__delete_directive",
    "observation__trigger_consolidation", "observation__recover_consolidation",
    "observation__clear_base_observations", "operation__cancel_operation",
    "operation__retry_operation", "operation__delete_operation",
    "knowledge_base__delete_base", "knowledge_base__update_base_config",
]

failures = []


def check(name, ok, detail=""):
    print(f"  {'ok  ' if ok else 'FAIL'}  {name}" + (f"  — {detail}" if detail else ""))
    if not ok:
        failures.append(name)


def corpus_fixture(root):
    """A small corpus. The point is the shape, not the content: an index, a declared identity, a
    kind and a date, so the frontmatter parser has something to parse."""
    os.makedirs(os.path.join(root, "runbooks"), exist_ok=True)
    with open(os.path.join(root, "index.md"), "w") as f:
        f.write("---\ntitle: Index\n---\n\n# Index\n\nStart here.\n")
    with open(os.path.join(root, "runbooks", "restore.md"), "w") as f:
        f.write("---\nid: wiki:restore\nkind: procedure\nstatus: active\ndate: 2026-09-01\n---\n\n"
                "# Restore a base\n\nDrop the base, keep the repository, reconcile again.\n")


def call(gw, tool, args=None, tolerate_error=False):
    try:
        return json.loads(text_of(gw.tool(tool, args)))
    except RuntimeError as e:
        if tolerate_error:
            return {"_error": str(e)}
        raise


def is_refusal(text, *needles):
    low = text.lower()
    return any(n.lower() in low for n in needles)


def phase_one(root):
    """No backend. The local reads must work; the rest must fail in a way that says why."""
    print("\n[1] reachable, and the policy is what denies a write")
    gw = Gateway("--all")
    try:
        names = [t["name"] for t in gw.tools()]
        svc = json.loads(text_of(gw.tool("list_services", {})))
        services = [s for s in svc["services"] if "knowledge" in s["name"]]
        check("13 services registered", len(services) == 13, f"got {len(services)}")
        check("79 methods across them", sum(s["features"] for s in services) == 79,
              f"got {sum(s['features'] for s in services)}")

        know = [n for n in names if n.startswith(KNOWLEDGE_PREFIXES)]
        check("tools generated for the exposed set", len(know) >= 30, f"got {len(know)}")
        for tool in MUST_BE_DENIED:
            check(f"{tool} denied", tool not in names)

        print("\n[2] local reads answer with no backend at all")
        got = call(gw, "corpus__get_corpus_status", {"base_id": BASE})
        status = got.get("status", got)
        check("corpus status names its roots", len(status.get("roots", [])) >= 1, json.dumps(got)[:200])

        got = call(gw, "corpus__plan_reconcile", {"base_id": BASE, "owner": "toolbox"})
        plan = got.get("plan", {})
        check("a plan is computed over the directory", len(plan.get("created", [])) >= 2,
              f"created={len(plan.get('created', []))}")
        # protojson's default is lowerCamelCase, so a field declared `plan_digest` arrives as
        # `planDigest`. The test asserts on the wire name rather than the declaration, because
        # the wire name is what a caller has to write.
        check("the plan carries a digest to confirm", bool(plan.get("planDigest")),
              json.dumps(plan)[:240])

        # A `bytes` field is base64 in JSON, which is the encoding rather than a wrapper: the
        # field is the file's bytes and nothing else. Decoding it here is what lets the test say
        # something about the content rather than about the transport.
        import base64
        got = call(gw, "corpus__read_corpus_file", {"base_id": BASE, "path": "runbooks/restore.md"})
        body = base64.b64decode(got.get("raw", "")).decode("utf-8", "replace")
        check("a corpus file reads its own bytes", "Drop the base" in body, body[:160])
        # The declared identity travels with it, and the frontmatter does not: it is metadata the
        # reconciler acts on, and leaving it in the extracted text would teach the extractor that
        # a person wrote "kind: procedure".
        check("the declared identity travels with it", "wiki:restore" in json.dumps(got),
              json.dumps(got)[:200])
        # `ReadCorpusFile` returns the file as it is on disk, frontmatter and all, alongside the
        # metadata the reconciler parsed out of it. Stripping the frontmatter is the *ingest*
        # path's job — the extractor should not be taught that a person wrote "kind: procedure"
        # — and a read that stripped it would be a read of something other than the file.
        check("the read is the file as it is on disk", body.startswith("---"), body[:80])

        print("\n[3] a backend that is not there fails in a way that says so")
        got = call(gw, "knowledge_base__list_bases", {}, tolerate_error=True)
        err = got.get("_error", "")
        # This is the assertion a handler test cannot make. The three layers between the tool and
        # the backend are where a classification gets lost, and "internal" here would mean a
        # deployment cannot tell "the backend is down" from "the subsystem is broken".
        check("a missing backend is not reported as internal", "internal" not in err.lower(), err[:200])
        check("the error names what was being done", "listing knowledge bases" in err, err[:200])
        check("the error says what it means", "not deployed" in err or "nothing is listening" in err,
              err[:200])
        check("the error keeps the cause", "connection refused" in err.lower(), err[:240])

        got = call(gw, "query__recall", {"base_id": BASE, "query": "how do we restore?"},
                   tolerate_error=True)
        check("a recall against no backend is refused, not empty", "_error" in got, str(got)[:200])
    finally:
        gw.close()


def phase_two(root, stub_url):
    """A stub backend. Everything answers, across real HTTP."""
    print("\n[4] with a backend, the engine half answers")
    os.environ["HINDSIGHT_API_URL"] = stub_url
    gw = Gateway("--all")
    try:
        got = call(gw, "knowledge_base__list_bases", {})
        bases = got.get("bases", [])
        check("list_bases returns the base", len(bases) == 1, json.dumps(got)[:200])
        check("the base is named", bases and bases[0].get("name"), json.dumps(bases)[:200])

        got = call(gw, "content__list_content", {"base_id": BASE})
        items = got.get("content", [])
        check("authored and derived content are one surface", len(items) >= 2, f"got {len(items)}")
        # An enum is a name on the wire, not a number and not a lowercase string — a JSON
        # consumer gets `ORIGIN_AUTHORED`, which is the contract's own spelling.
        origins = sorted({i.get("origin") for i in items})
        check("origins are named, not guessed",
              set(origins) <= {"ORIGIN_AUTHORED", "ORIGIN_RETAINED", "ORIGIN_DERIVED",
                               "ORIGIN_UNSPECIFIED"}, str(origins))
        authored = [i for i in items if i.get("origin") == "ORIGIN_AUTHORED"]
        check("authored content is found in the corpus", len(authored) >= 2, str(len(authored)))
        # Authored content is `NONE` and not `CURATED`, and that is the rule rather than an
        # omission: a runbook is edited at its source and reconciled in, so a write through
        # this surface would be a second source of truth the next reconcile deletes. An agent
        # told `NONE` goes and edits the file; one told `CURATED` writes here and watches it
        # vanish. The field exists so it does not have to be inferred.
        check("authored content is not writable through the API", all(
            i.get("mutability") == "MUTABILITY_NONE" for i in authored),
            json.dumps(authored)[:240])

        got = call(gw, "page__export_page_bundle", {"base_id": BASE})
        files = got.get("files", [])
        check("the page bundle comes back", len(files) >= 1, json.dumps(got)[:200])
        check("every bundled file says it is generated", all(
            f.get("origin") == "generated" for f in files), json.dumps(files)[:200])

        got = call(gw, "content__get_projection_revision", {"base_id": BASE})
        check("the projection revision answers", bool(got), json.dumps(got)[:200])

        print("\n[5] the eight new services are reachable, not just registered")
        for tool, args, name in [
            ("directive__list_directives", {"base_id": BASE}, "DirectiveService"),
            ("entity__list_entities", {"base_id": BASE}, "EntityService"),
            ("entity__get_entity_graph", {"base_id": BASE}, "EntityService graph"),
            ("mental_model__list_mental_models", {"base_id": BASE}, "MentalModelService"),
            ("observation__list_observation_scopes", {"base_id": BASE}, "ObservationService"),
            ("operation__list_operations", {"base_id": BASE}, "OperationService"),
            ("template__get_template_schema", {}, "TemplateService"),
            ("template__export_template", {"base_id": BASE, "name": "t", "version": "1"},
             "TemplateService export"),
            ("mount__get_mount_status", {}, "MountService"),
        ]:
            got = call(gw, tool, args, tolerate_error=True)
            check(f"{name} answers through MCP", "_error" not in got, str(got)[:200])

        got = call(gw, "mount__get_mount_status", {})
        blob = json.dumps(got)
        check("a build without FUSE says so", "fuse" in blob.lower(), blob[:240])
        check("and it names the tag rather than failing obscurely", "fuse" in blob.lower()
              and ("tag" in blob.lower() or "unimplemented" in blob.lower()), blob[:240])

        print("\n[6] a denial is a denial, and it says which method")
        got = call(gw, "call_rpc", {
            "service": "toolbox.knowledge.v1.CorpusService",
            "method": "ApplyReconcile", "request": {},
        }, tolerate_error=True)
        err = got.get("_error", "")
        check("a write through call_rpc is refused", "_error" in got, str(got)[:200])
        check("the refusal names the method", "ApplyReconcile" in err, err[:240])
        check("and it says the policy refused it",
              is_refusal(err, "denied", "policy", "not permitted", "exposed"), err[:240])
    finally:
        gw.close()


def main():
    with tempfile.TemporaryDirectory(prefix="knowledge-corpus-") as root:
        corpus_fixture(root)
        os.environ["TOOLBOX_KNOWLEDGE_CORPUS_ROOTS"] = f"docs={root}"
        # A state directory per run, so an ownership record from an earlier phase is not
        # mistaken for this one's.
        os.environ["TOOLBOX_KNOWLEDGE_STATE_DIR"] = os.path.join(root, ".state")

        phase_one(root)
        with StubBackend() as stub:
            phase_two(root, stub.url)

    print(f"\n{'FAILED: ' + ', '.join(failures) if failures else 'all checks passed'}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
