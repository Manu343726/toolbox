#!/usr/bin/env python3
"""Print one full response for a named tool.

A truncated failure detail cannot be asserted on, and every finding in this area has been a
truncation: the field was there, or was not, and only the whole message says which.
"""
import json
import os
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from hindsight_stub import StubBackend  # noqa: E402
from mcp_knowledge_test import BASE, corpus_fixture  # noqa: E402
from mcp_probe import Gateway, text_of  # noqa: E402

TOOL = sys.argv[1] if len(sys.argv) > 1 else "content__list_content"
ARGS = json.loads(sys.argv[2]) if len(sys.argv) > 2 else {"base_id": BASE}

with tempfile.TemporaryDirectory(prefix="knowledge-dump-") as root:
    corpus_fixture(root)
    os.environ["TOOLBOX_KNOWLEDGE_CORPUS_ROOTS"] = f"docs={root}"
    os.environ["TOOLBOX_KNOWLEDGE_STATE_DIR"] = os.path.join(root, ".state")
    with StubBackend() as stub:
        os.environ["HINDSIGHT_API_URL"] = stub.url
        gw = Gateway("--all")
        try:
            try:
                out = json.loads(text_of(gw.tool(TOOL, ARGS)))
            except RuntimeError as e:
                raw = str(e).split("tool reported an error: ", 1)
                out = json.loads(raw[1])["content"][0]["text"] if len(raw) > 1 else raw[0]
            print(json.dumps(out, indent=2) if not isinstance(out, str) else out)
        finally:
            gw.close()
