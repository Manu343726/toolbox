#!/bin/sh
# Start the aggregated Toolbox MCP gateway over stdio.
#
# Build output goes to stderr so stdout stays a valid MCP JSON-RPC stream.
#
# The host is rebuilt when it is missing *or older than the source it was built
# from*. Testing presence alone is what let a gateway run for a day against a
# binary from before the knowledge work: every tool call succeeded, every answer
# was about a subsystem that no longer existed, and nothing reported it. An agent
# following the documented flow — list features, expose, call — would have
# concluded a finished subsystem was invisible, which is a very different
# conclusion from "the binary is a day old".
#
# So: `TOOLBOX_MCP_REBUILD=1` forces a rebuild, `TOOLBOX_MCP_NO_BUILD=1` fails
# fast instead, and otherwise the binary is compared against the sources. The
# comparison is a `find -newer` over hand-written Go and proto files, which is one
# directory walk and costs nothing against a build.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BIN="$ROOT/cmd/toolbox/bin/toolbox"

stale() {
  [ -x "$BIN" ] || return 0
  # Generated code is gitignored and is a build output, so it is not consulted:
  # a descriptor regenerated with an unchanged timestamp is not a reason to rebuild.
  find "$ROOT/pkg" "$ROOT/subsystems" "$ROOT/cmd" "$ROOT/internal" \
       \( -name '*.go' -o -name '*.proto' -o -name 'go.mod' -o -name 'go.sum' \) \
       ! -name '*.pb.go' ! -name '*.connect.go' -newer "$BIN" -print -quit 2>/dev/null | grep -q .
}

if [ "${TOOLBOX_MCP_NO_BUILD:-0}" = "1" ]; then
  if [ ! -x "$BIN" ]; then
    echo "toolbox-mcp: $BIN is missing; run 'make build' first" >&2
    exit 1
  fi
  if stale; then
    echo "toolbox-mcp: $BIN is older than the source; it would answer for a build that no longer exists" >&2
    echo "toolbox-mcp: run 'make host', or unset TOOLBOX_MCP_NO_BUILD to rebuild here" >&2
    exit 1
  fi
elif [ ! -x "$BIN" ] || [ "${TOOLBOX_MCP_REBUILD:-0}" = "1" ] || stale; then
  echo "toolbox-mcp: building the Toolbox host" >&2
  make -C "$ROOT" host >&2
fi

exec "$BIN" mcp "$@"
