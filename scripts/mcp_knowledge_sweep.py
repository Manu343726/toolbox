#!/usr/bin/env python3
"""Call every exposed knowledge tool and report what answered.

The Go suite calls handlers directly, so a method that is in the contract, is registered, is
classified, and is not implemented passes every unit test: the handler is an embedded
`Unimplemented...Handler`, which is a real type that satisfies the interface and returns
`unimplemented` for everything. Nothing notices until a client calls it.

This is the sweep that notices. It calls every tool the policy exposes, with arguments as
plausible as it can manage, and classifies each answer. A method that is unreachable is a hole in
the contract; this lists them.
"""
import json
import os
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from hindsight_stub import StubBackend  # noqa: E402
from mcp_probe import Gateway, text_of  # noqa: E402
from mcp_knowledge_test import BASE, KNOWLEDGE_PREFIXES, corpus_fixture  # noqa: E402

# Arguments as plausible as the tool's own schema allows. A tool that needs a real identifier gets
# one that the stub backend knows about; anything else is called with the base alone, and a
# complaint about a missing field is itself an answer — the method was reached.
ARGS = {
    "content__get_content": {"base_id": BASE, "content_id": "docs/index.md", "include_body": True},
    "content__get_content_body": {"base_id": BASE, "content_id": "docs/index.md"},
    "content__list_content_chunks": {"base_id": BASE, "document_id": "doc-1"},
    "content__export_wiki": {"base_id": BASE},
    "query__search": {"base_id": BASE, "query": "restore"},
    "query__recall": {"base_id": BASE, "query": "how do we restore a base?"},
    "query__reflect": {"base_id": BASE, "query": "how do we restore a base?"},
    "query__preview_extraction": {"base_id": BASE, "content": "Drop the base.", "document_id": "d1"},
    "query__preview_prompts": {"base_id": BASE},
    "query__list_tags": {"base_id": BASE},
    "corpus__read_corpus_file": {"base_id": BASE, "path": "runbooks/restore.md"},
    "corpus__rebuild_corpus": {"base_id": BASE},
    "memory__get_memory": {"base_id": BASE, "memory_id": "fact-1"},
    "memory__get_memory_history": {"base_id": BASE, "memory_id": "fact-1"},
    "memory__get_memory_graph": {"base_id": BASE, "memory_id": "fact-1"},
    "entity__get_entity": {"base_id": BASE, "entity_id": "e1"},
    "entity__get_entity_graph": {"base_id": BASE},
    "mental_model__get_mental_model_history": {"base_id": BASE, "model_id": "m1"},
    "directive__get_directive": {"base_id": BASE, "directive_id": "d1"},
    "page__preview_page_refresh": {"base_id": BASE, "page_id": "p1"},
    "operation__get_operation": {"base_id": BASE, "operation_id": "op-1"},
    "observation__preview_consolidation": {"base_id": BASE},
    "template__export_template": {"base_id": BASE, "name": "t", "version": "1"},
    "template__export_base": {"base_id": BASE},
    "knowledge_base__list_bases": {},
    "template__get_template_schema": {},
    "corpus__plan_reconcile": {"base_id": BASE, "owner": "toolbox"},
    "knowledge_base__get_base_ingestion_series": {"base_id": BASE, "granularity": "day", "buckets": 5},
    "observation__preview_consolidation": {"base_id": BASE, "strategies": [
        {"mission": "consolidate architecture",
         "scopes": [{"tags": ["architecture"], "tags_match": "all_strict"}]}]},
    "knowledge_base__get_base": {"base_id": BASE},
    "knowledge_base__get_base_config": {"base_id": BASE},
    "knowledge_base__get_base_stats": {"base_id": BASE},
    "knowledge_base__get_base_ingestion_series": {"base_id": BASE},
    "knowledge_base__list_base_aliases": {"base_id": BASE},
    "mount__get_mount_status": {},
    "content__get_projection_revision": {"base_id": BASE},
}


def main():
    with tempfile.TemporaryDirectory(prefix="knowledge-sweep-") as root:
        corpus_fixture(root)
        os.environ["TOOLBOX_KNOWLEDGE_CORPUS_ROOTS"] = f"docs={root}"
        os.environ["TOOLBOX_KNOWLEDGE_STATE_DIR"] = os.path.join(root, ".state")

        with StubBackend() as stub:
            os.environ["HINDSIGHT_API_URL"] = stub.url
            gw = Gateway("--all")
            try:
                tools = [t for t in gw.tools() if t["name"].startswith(KNOWLEDGE_PREFIXES)]
                print(f"sweeping {len(tools)} exposed knowledge tools\n")
                unimplemented, answered, refused = [], [], []
                for tool in sorted(tools, key=lambda t: t["name"]):
                    name = tool["name"]
                    args = ARGS.get(name, {"base_id": BASE})
                    try:
                        text = text_of(gw.tool(name, args))
                        answered.append(name)
                        status = "ok"
                    except RuntimeError as e:
                        err = str(e)
                        if "not implemented" in err:
                            unimplemented.append(name)
                            status = "UNIMPLEMENTED"
                        else:
                            refused.append((name, err))
                            status = "reached, refused or complained"
                    print(f"  {status:28} {name}")

                print(f"\nanswered cleanly: {len(answered)}")
                print(f"reached but refused/complained: {len(refused)}")
                print(f"NOT IMPLEMENTED: {len(unimplemented)}")
                for name in unimplemented:
                    print(f"    {name}")
                for name, err in refused:
                    print(f"    {name}: {err[:160]}")
            finally:
                gw.close()
    return 1 if unimplemented else 0


if __name__ == "__main__":
    sys.exit(main())
