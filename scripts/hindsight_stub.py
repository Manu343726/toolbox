#!/usr/bin/env python3
"""A stand-in for the memory backend, for the MCP end-to-end test.

The Go suite has an equivalent in `recordingEngine` and in the subsystem's `backend` stub. This
one exists because the Go suite cannot reach this: it starts a real process, serves real HTTP, and
the gateway resolves a real endpoint and makes real calls across three layers.

It answers only what the probe calls, with the required fields the generated client insists on. A
stub answering a partial body produces "no value given for required property X", which is a failure
about the stub rather than about the code under test — so every body here carries what the client
validates, and a change to the client's requirements shows up as a decoding error naming the field
rather than as a silently empty response.
"""
import json
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BANKS = {
    "bank_id": "docs",
    "display_alias": "docs",
    "disposition": {"skepticism": 0, "literalism": 0, "empathy": 0},
}

ALIASES = {"bank_id": "docs", "aliases": [{"alias": "docs", "primary": True}]}

STATS = {
    "bank_id": "docs", "total_nodes": 0, "total_links": 0, "total_documents": 1,
    "nodes_by_fact_type": {}, "links_by_link_type": {}, "links_by_fact_type": {},
    "links_breakdown": {}, "pending_operations": 0, "failed_operations": 0,
}

VERSION = {
    "api_version": "0.10.1",
    "features": {
        "observations": True, "mcp": True, "worker": True, "bank_config_api": True,
        "bank_llm_health": True, "file_upload_api": True, "document_export_api": True,
        "document_import_api": True, "audit_log": True, "llm_trace": True,
        "store_document_text": True,
    },
}

# The engine half: one page and one folder, so the tree walk and the export have something to
# return and a caller's "which half changed" has two answers to give.
TREE = {
    "roots": [
        {"id": "f1", "kind": "folder", "name": "runbooks", "parent_id": "", "children": [
            {"id": "p1", "kind": "page", "name": "restore", "parent_id": "f1",
             "mental_model_id": "m1", "tags": ["runbook"],
             # A tag group discriminates on its JSON key: the generator's oneof is
             # serialised bare, so a leaf is `{"tags": [...]}` and an `and` is
             # `{"and": [...]}`. There is no wrapper and no `type` field, and a stub that
             # sends `{"leaf": {...}}` fails to decode — which is how the wire form was
             # established rather than guessed.
             "trigger": {"mode": "delta", "fact_types": ["observation"],
                         "exclude_mental_models": True, "refresh_after_consolidation": True,
                         "tags_match": "all_strict", "tag_groups": [{"tags": ["runbook"]}]},
             "timestamp": "2026-09-01T10:00:00Z"},
        ]},
    ],
}

EXPORT = {"files": [
    {"path": "runbooks/restore.md", "content": "# Restore\n\nDrop the base, keep the repository.\n"},
]}

MENTAL_MODELS = {"items": [
    {"id": "m1", "bank_id": "docs", "name": "restore",
     "source_query": "how do we restore a base?", "content": "# Restore", "tags": ["runbook"],
     "last_refreshed_at": "2026-09-01T10:00:00Z"},
], "total": 1, "limit": 0, "offset": 0}

DIRECTIVES = {"items": [
    {"id": "d1", "bank_id": "docs", "name": "Cite the document",
     "content": "Every claim names the file it came from.", "tags": ["safety"]},
], "total": 1, "limit": 0, "offset": 0}

ENTITY_LIST = {"items": [
    {"id": "e1", "canonical_name": "base", "mention_count": 3,
     "first_seen": "2026-09-01T10:00:00Z", "last_seen": "2026-09-02T10:00:00Z"},
], "total": 1, "limit": 50, "offset": 0}

OPERATIONS = {"bank_id": "docs", "operations": [], "total": 0, "limit": 50, "offset": 0}

SCOPES = {"scopes": [{"tags": [], "count": 0}], "total": 1, "limit": 0, "offset": 0}

TEMPLATE_SCHEMA = {"version": "1", "fields": [
    {"name": "reflect_mission", "type": "string", "required": True,
     "description": "what the base is for", "default": ""},
]}

# Path suffix -> body. Longest suffix wins, so a general route cannot shadow a specific one.
FACTS = {"items": [{"id": "fact-1", "text": "one base, two halves", "fact_type": "world",
                    "document_id": "doc-1", "tags": ["architecture"],
                    "updated_at": "2026-09-01T10:00:00Z", "edited_at": "2026-09-02T10:00:00Z"}],
         "total": 1, "limit": 50, "offset": 0}

FACT_HISTORY = {"items": [
    {"id": "fact-1", "text": "what the extractor said", "updated_at": "2026-09-01T10:00:00Z"},
    {"id": "fact-1", "text": "what a person said", "updated_at": "2026-09-02T10:00:00Z",
     "edited_at": "2026-09-02T10:00:00Z"},
]}

DIRECTIVE_ONE = {"id": "d1", "bank_id": "docs", "name": "Cite the document",
                 "content": "Every claim names the file it came from.", "tags": ["safety"]}

ENTITY_ONE = {"id": "e1", "canonical_name": "base", "mention_count": 3,
              "first_seen": "2026-09-01T10:00:00Z", "last_seen": "2026-09-02T10:00:00Z",
              "observations": [{"text": "one base has two halves",
                                "mentioned_at": "2026-09-01T10:00:00Z"}]}

ENTITY_GRAPH = {"nodes": [
    {"data": {"id": "e1", "label": "base", "mentionCount": 3}},
    {"data": {"id": "e2", "label": "corpus", "mentionCount": 1}}],
    "edges": [{"data": {"id": "l1", "source": "e1", "target": "e2",
                        "linkType": "mentions", "weight": 80}}],
    "total_entities": 2, "total_edges": 1, "limit": 50}

MODEL_ONE = {"id": "m1", "bank_id": "docs", "name": "restore",
             "source_query": "how do we restore a base?", "content": "# Restore", "tags": ["runbook"]}

MODEL_HISTORY = {"items": [
    {"content": "# Restore", "created_at": "2026-09-01T10:00:00Z", "trigger": "consolidation"}]}

MODEL_DRY_RUN = {"mental_model_id": "m1", "name": "restore", "requested_mode": "delta",
                 "effective_mode": "delta", "outcome": "would_update", "would_persist": True,
                 "scope": {"tags": ["runbook"], "count": 1, "handled_by": 0},
                 "window": {"tags": [], "count": 0, "handled_by": 0},
                 "facts": {"matched": 3, "taken": 2, "observations": 1}}

OPERATION_ONE = {"operation_id": "op-1", "status": "running", "task_type": "retain",
                 "items_count": 120, "created_at": "2026-09-01T10:00:00Z",
                 "progress": {"stage": "extract", "at": "2026-09-01T10:00:00Z",
                              "processed": 30, "total": 120}}

STRATEGIES = {"strategies": [
    {"active": True, "claimed_count": 10, "rules": [
        {"match_count": 12, "taken_count": 10, "observation_count": 2,
         "samples": [{"tags": ["architecture"], "count": 12, "handled_by": 0}]}]}],
    "default": {"match_count": 17, "observation_count": 0, "samples": []},
    "scopes_scanned": 17, "complete": True}

TEMPLATE_EXPORT = {"version": "1", "bank": {"reflect_mission": "answer from the corpus first"},
                   "directives": [{"name": "Be plain.", "content": "Be plain."}],
                   "mental_models": []}

CHUNKS = {"items": [{"id": "c1", "text": "Drop the base", "chunk_index": 0,
                     "document_id": "doc-1"}], "total": 1, "limit": 0, "offset": 0}

ROUTES = [
    # Longest suffix wins, so each of these is registered ahead of the route it would otherwise be
    # shadowed by. `/entities` and `/entities/graph` are the pair that needs it.
    ("/memories/fact-1/history", FACT_HISTORY),
    ("/memories/fact-1", FACTS["items"][0]),
    ("/mental-models/m1/history", MODEL_HISTORY),
    ("/mental-models/m1/dry-run-refresh", MODEL_DRY_RUN),
    ("/mental-models/m1", MODEL_ONE),
    ("/directives/d1", DIRECTIVE_ONE),
    ("/entities/graph", ENTITY_GRAPH),
    ("/entities/e1", ENTITY_ONE),
    ("/documents/doc-1/chunks", CHUNKS),
    ("/consolidation-strategies/preview", STRATEGIES),
    ("/operations/op-1", OPERATION_ONE),
    ("/export", TEMPLATE_EXPORT),
    ("/bank-template-schema", TEMPLATE_SCHEMA),
    ("/knowledge-base/tree", TREE),
    ("/knowledge-base/export", EXPORT),
    ("/mental-models", MENTAL_MODELS),
    ("/directives", DIRECTIVES),
    ("/entities", ENTITY_LIST),
    ("/memories", FACTS),
    ("/operations", OPERATIONS),
    ("/observations/scopes", SCOPES),
    ("/aliases", ALIASES),
    ("/stats", STATS),
    ("/config", {"bank_id": "docs", "config": {}, "overrides": {}}),
    ("/version", VERSION),
    ("/memories/recall", {"results": []}),
    ("/reflect", {"text": "an answer", "based_on": {}}),
    ("/banks", {"banks": [BANKS], "total": 1, "limit": 0, "offset": 0}),
]


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _body(self):
        length = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(length) if length else b""

    def _respond(self, code, payload):
        raw = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def handle_request(self, method):
        self._body()
        path = self.path.split("?")[0]
        best, body = None, {}
        for suffix, payload in ROUTES:
            if path.endswith(suffix) and (best is None or len(suffix) > len(best)):
                best, body = suffix, payload
        if best is None:
            self._respond(404, {"detail": f"the probe stub has no route for {path}"})
            return
        self._respond(200, body)

    def do_GET(self):
        self.handle_request("GET")

    def do_POST(self):
        self.handle_request("POST")

    def do_PATCH(self):
        self.handle_request("PATCH")

    def do_DELETE(self):
        self.handle_request("DELETE")


class StubBackend:
    def __enter__(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        return self

    def __exit__(self, *exc):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


if __name__ == "__main__":
    with StubBackend() as stub:
        print(stub.url)
