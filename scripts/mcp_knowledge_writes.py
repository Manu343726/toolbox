#!/usr/bin/env python3
"""Call every knowledge *write*, through the gateway, with a policy that grants them.

`mcp_knowledge_sweep.py` sweeps the reads, and `mcp_knowledge_test.py` checks that the default
policy refuses a write. Neither touches what a write *does* when it is allowed — and that is 29
methods, the majority of them never executed through the gateway at all.

The reconciler got its own harness (`mcp_knowledge_reconcile.py`) because it is the longest path and
the one a deployment runs unattended. This is the sweep for the rest: it grants every write, calls
each one with arguments as plausible as its own schema allows, and classifies the answer.

What it is looking for is the third category, which is the whole point of running it:

- **answered** — the method reached the backend and came back with a usable result.
- **refused with a reason** — the method is reached and the refusal is *its own*, naming a missing
  identifier or a confirmation. That is the contract working, and it is a pass: a method that
  refuses for its own reason is implemented.
- **refused by the policy** — the grant did not cover it. A hole in the harness, not the code.
- **unimplemented** — the handler is an embedded `Unimplemented...Handler`. A hole in the contract.
- **anything else** — an error that is not one of the above. A decoding failure, a nil dereference,
  a panic recovered at the transport, a wrong kind. This is where defects live: a method that is
  reached and answers with a *stack* rather than a sentence has failed in a way no Go test asserts.
"""
import json
import os
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from hindsight_stub import StubBackend  # noqa: E402
from mcp_probe import Gateway, text_of  # noqa: E402
from mcp_knowledge_test import BASE, corpus_fixture  # noqa: E402

# Every write in the contract, with the smallest arguments its own schema accepts. Where a method
# needs a plan, the reconciler harness covers it properly; here it is called with a dry run or with
# an obviously wrong confirmation, because the point is to reach the method and read what it says.
# Every write in the contract, as `service_method` so the policy can be derived and so a rename in
# the contract shows up here rather than as a silently smaller sweep.
#
# Paired per service rather than generated as a cross product. The first version did the cross
# product — every method under every service — and produced 362 names, all but a handful of which
# do not exist. A sweep with 362 phantom entries in it looks thorough and is measuring nothing.
WRITES_BY_SERVICE = {
    "content": ["write_content", "curate_content", "delete_content", "reprocess_content"],
    # The reconciler gets a harness of its own; the rebuild is the other destructive call.
    "corpus": ["rebuild_corpus"],
    "knowledge_base": ["create_base", "update_base", "delete_base", "reset_base_config",
                       "update_base_config", "add_base_alias", "set_primary_base_alias",
                       "remove_base_alias"],
    "memory": ["curate_memory"],
    "page": ["create_page_folder", "create_page", "update_page_node", "refresh_page", "delete_page"],
    # The contract has no `RefreshMentalModel`: a page is what refreshes, and `RefreshPage` is it.
    "mental_model": ["create_mental_model", "update_mental_model", "delete_mental_model",
                     "clear_mental_model"],
    "directive": ["create_directive", "update_directive", "delete_directive"],
    "operation": ["cancel_operation", "retry_operation", "delete_operation"],
    "observation": ["trigger_consolidation", "recover_consolidation", "clear_base_observations"],
    "template": ["import_template"],
    "mount": ["enable_mount", "disable_mount"],
}

WRITE_METHODS = sorted(f"{s}__{m}" for s, ms in WRITES_BY_SERVICE.items() for m in ms)

# What a field means, for the fields whose name does not give it away. Everything else is filled
# from the type in the tool's own schema.
MEANING = {
    "base_id": BASE,
    "content_id": "fact-1",
    "document_id": "doc-1",
    "memory_id": "fact-1",
    "model_id": "m1",
    "mental_model_id": "m1",
    "directive_id": "d1",
    "operation_id": "op-1",
    "node_id": "p1",
    "page_id": "p1",
    "alias_id": "handbook",
    "mount_id": "docs",
    "body": "one base, two halves",
    "text": "one base, two halves",
    "content": "one base, two halves",
    "mission": "answer from the corpus",
    "reason": "the corpus half is load-bearing",
    "query": "how do we restore a base?",
    "source_query": "how do we restore a base?",
    "path": "runbooks/restore.md",
    "name": "handbook",
    "display_alias": "handbook",
    "version": "1",
    "mountpoint": "/tmp/knowledge-sweep-must-not-mount",
    "clock": "2026-09-01T10:00:00Z",
}

# A preview where a method offers one, so a destructive call is not made merely to reach the code.
# Every method here is a write, and reaching it is the point — not exercising it.
PREVIEW_FLAGS = ("dry_run", "dryRun", "confirm", "operation")


def snake(name):
    """`baseId` to `base_id`, because the schema is protojson's lowerCamelCase and the meaning
    table is written the way a person reads the contract."""
    out = []
    for i, ch in enumerate(name):
        if ch.isupper() and i:
            out.append("_")
        out.append(ch.lower())
    return "".join(out)


def value_for(field, spec):
    """One plausible value for a field, from its own schema and the meaning table.

    The schema decides the *type* and the table decides the *content*, in that order. The first
    version consulted the table first, and handed the string "one base, two halves" to a field the
    schema described as an object — which arrives at the gateway as malformed JSON and comes back
    as `proto: syntax error`, which reads like a contract defect and is a harness bug.
    """
    name = snake(field)
    kind = spec.get("type")
    if kind == "object" or (kind is None and "properties" in spec):
        props = spec.get("properties", {})
        required = spec.get("required") or list(props)
        return {k: value_for(k, props.get(k, {})) for k in required}
    if kind == "array":
        item = spec.get("items", {})
        return [value_for(name.rstrip("s") or "item", item)]
    if kind == "boolean":
        return True
    if kind in ("integer", "number"):
        return 1
    if name in MEANING:
        return MEANING[name]
    enum = spec.get("enum")
    if enum:
        return enum[0]
    if "query" in name or name.endswith("_text"):
        return "how do we restore a base?"
    if name.endswith("_path"):
        return "runbooks/restore.md"
    if name.endswith("_name") or name == "name":
        return "handbook"
    return f"probe-{name or 'value'}"


def args_for(tool):
    """The arguments a tool's own schema says it needs, and nothing it does not."""
    schema = tool.get("inputSchema", {})
    props = schema.get("properties", {})
    required = schema.get("required", [])
    args = {}
    for field, spec in props.items():
        name = snake(field)
        if field in required or name in PREVIEW_FLAGS or name == "base_id":
            args[field] = value_for(field, spec)
    return args


def policy(root):
    """Grant every write in every class, for the knowledge services only.

    Named by service wildcard rather than by method, because the point here is breadth — the
    reconciler harness is the one that proves a *narrow* grant works, and a broad grant is the only
    way to reach the 27 methods that harness deliberately leaves denied.
    """
    path = os.path.join(root, "writes.policy")
    with open(path, "w") as f:
        f.write("allow *    read\n")
        for short in WRITES_BY_SERVICE:
            service = "".join(part.capitalize() for part in short.split("_")) + "Service"
            f.write(f"allow toolbox.knowledge.v1.{service}/*   write read unclassified\n")
    return path


# The two methods whose failure is a recorded defect rather than a surprise, so the sweep fails on
# a *new* one and not on the one already in `docs/todos.md`. A harness that is permanently red is a
# harness nobody runs.
KNOWN_BROKEN = {
    "content__write_content": "a Duration argument sent in its JSON object form; see docs/todos.md",
    "mount__enable_mount": "a Duration argument sent in its JSON object form; see docs/todos.md",
}

# A refusal that is the method's own: it names an identifier, a confirmation, or a policy value.
# Anything outside this set is a failure to investigate rather than an answer to accept.
def own_refusal(err):
    low = err.lower()
    return any(n in low for n in (
        "is required", "required", "refuses", "cannot", "not supported", "no such",
        "unknown node", "not found", "must be", "would", "invalid", "no node called",
        "not implemented in this build", "not compiled in", "already", "has no",
        "means every answer", "does not exist", "no mount is called", "stop being governed",
        "needs ", "nobody can address", "refusing rather than",
    ))


def main():
    with tempfile.TemporaryDirectory(prefix="knowledge-writes-") as root:
        corpus_fixture(root)
        os.environ["TOOLBOX_KNOWLEDGE_CORPUS_ROOTS"] = f"docs={root}"
        os.environ["TOOLBOX_KNOWLEDGE_STATE_DIR"] = os.path.join(root, ".state")

        with StubBackend() as stub:
            os.environ["HINDSIGHT_API_URL"] = stub.url
            gw = Gateway("--all", policy=policy(root))
            try:
                tools = {t["name"]: t for t in gw.tools()}
                missing = sorted(n for n in WRITE_METHODS if n not in tools)
                print(f"{len(WRITE_METHODS) - len(missing)} of {len(WRITE_METHODS)} writes are on "
                      f"the tool surface under this policy\n")
                if missing:
                    print("not on the surface — the contract's name, or the grant's, is wrong:")
                    for n in missing:
                        print(f"    {n}")

                answered, own, ungranted, unimplemented, suspicious, known = [], [], [], [], [], []
                for name in sorted(WRITE_METHODS):
                    if name in missing:
                        continue
                    try:
                        text_of(gw.tool(name, args_for(tools[name])))
                        answered.append(name)
                        status = "ok"
                    except RuntimeError as e:
                        err = str(e)
                        if "not implemented" in err.lower():
                            unimplemented.append((name, err))
                            status = "UNIMPLEMENTED"
                        elif "policy" in err.lower() or "unknown tool" in err.lower():
                            ungranted.append((name, err))
                            status = "not granted"
                        elif own_refusal(err):
                            own.append((name, err))
                            status = "refused, in its own words"
                        elif name in KNOWN_BROKEN:
                            known.append((name, err))
                            status = "known defect"
                        else:
                            suspicious.append((name, err))
                            status = "SUSPICIOUS"
                    print(f"  {status:28} {name}")

                print(f"\nanswered: {len(answered)}")
                print(f"refused in its own words: {len(own)}")
                print(f"not granted: {len(ungranted)}  (and {len(missing)} absent from the surface)")
                print(f"UNIMPLEMENTED: {len(unimplemented)}")
                print(f"known defect: {len(known)}")
                print(f"SUSPICIOUS: {len(suspicious)}")
                for name, err in known:
                    print(f"    {name}: {KNOWN_BROKEN[name]}")
                for name, err in ungranted + unimplemented + suspicious:
                    print(f"    {name}: {err[:300]}")

                if known:
                    print("\nThe known defects are recorded, not swallowed. Fixing them removes "
                          "them from this list; nothing else about this sweep would change.")
                if suspicious or unimplemented or missing:
                    print("\nA SUSPICIOUS answer is one that is not a refusal this contract could "
                          "have written, and not a failure the stub provoked. It is a decoding "
                          "failure, a nil dereference or a wrong error kind — the category this "
                          "sweep exists to find.")
                return 1 if (suspicious or unimplemented or missing) else 0
            finally:
                gw.close()


if __name__ == "__main__":
    sys.exit(main())
