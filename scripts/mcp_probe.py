#!/usr/bin/env python3
"""Drive the Toolbox MCP gateway over stdio, the way an agent does.

The MCP tool binding in this session was spawned from a binary that predates the
knowledge work, so it cannot see it. This speaks the same protocol to the
current binary: JSON-RPC 2.0, newline-delimited, over stdio — the same path
`scripts/toolbox-mcp.sh` execs.
"""
import json
import subprocess
import sys
import os

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.path.join(ROOT, "cmd", "toolbox", "bin", "toolbox")


class Gateway:
    def __init__(self, *args, policy=None):
        argv = [BIN, "mcp", *args]
        if policy:
            argv += ["--policy", policy]
        self.p = subprocess.Popen(
            argv,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, bufsize=1,
        )
        self.n = 0
        self.call("initialize", {
            "protocolVersion": "2025-11-25",
            "capabilities": {},
            "clientInfo": {"name": "mcp-probe", "version": "0"},
        })
        self.notify("notifications/initialized")

    def _send(self, obj):
        self.p.stdin.write(json.dumps(obj) + "\n")
        self.p.stdin.flush()

    def notify(self, method, params=None):
        self._send({"jsonrpc": "2.0", "method": method, "params": params or {}})

    def call(self, method, params=None):
        self.n += 1
        self._send({"jsonrpc": "2.0", "id": self.n, "method": method, "params": params or {}})
        while True:
            line = self.p.stdout.readline()
            if not line:
                err = self.p.stderr.read()
                raise RuntimeError(f"gateway closed the stream while waiting for {method}\n{err[-2000:]}")
            line = line.strip()
            if not line:
                continue
            msg = json.loads(line)
            if msg.get("id") != self.n:
                continue  # a notification or a late reply
            if "error" in msg:
                raise RuntimeError(f"{method}: {msg['error']}")
            return msg.get("result", {})

    def tools(self):
        return self.call("tools/list").get("tools", [])

    def tool(self, name, args=None):
        return self.call("tools/call", {"name": name, "arguments": args or {}})

    def close(self):
        try:
            self.p.stdin.close()
        except Exception:
            pass
        self.p.wait(timeout=10)


def text_of(result):
    """MCP tool results carry content blocks; the payload is in the first text one."""
    if result.get("isError"):
        raise RuntimeError("tool reported an error: " + json.dumps(result)[:800])
    for block in result.get("content", []):
        if block.get("type") == "text":
            return block.get("text", "")
    return ""


def show(title, value):
    print(f"\n=== {title} ===")
    print(value)


def _cli():
    # The knowledge provider reports "nothing to start" unless the deployment has a corpus to
    # reconcile into a base — a base with no corpus is the engine half and nothing a person can
    # review, so a deployment without one deliberately gets no knowledge subsystem. Point it at
    # a directory for the probe.
    corpus = os.environ.get("PROBE_CORPUS", "")
    if corpus:
        os.environ["TOOLBOX_KNOWLEDGE_CORPUS_ROOTS"] = f"docs={corpus}"
    gw = Gateway("--all")
    try:
        names = [t["name"] for t in gw.tools()]
        show("tool count", len(names))
        # The generated tool names are prefixed by service in snake case, so a filter on
        # "nowledge" finds only the KnowledgeBaseService ones. The knowledge contract's
        # services are the ones below.
        prefixes = ("content__", "query__", "corpus__", "mount__", "knowledge_base__",
                    "memory__", "page__", "mental_model__", "directive__", "observation__",
                    "entity__", "template__", "operation__")
        know_tools = [n for n in names if n.startswith(prefixes)]
        show("knowledge tools", "\n".join(know_tools) or "(none)")

        svc = json.loads(text_of(gw.tool("list_services", {})))
        know = [s for s in svc.get("services", []) if "knowledge" in s["name"]]
        show("knowledge services via MCP", json.dumps(
            [{"name": s["name"], "features": s["features"], "exposed": s["exposed"]} for s in know], indent=2))
        show("total services", len(svc.get("services", [])))
        show("total features", sum(s["features"] for s in svc.get("services", [])))
    finally:
        gw.close()


if __name__ == "__main__":
    _cli()
