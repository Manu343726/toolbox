#!/bin/sh
# Start the aggregated Toolbox MCP gateway over stdio.
#
# Build output goes to stderr so stdout stays a valid MCP JSON-RPC stream.
# The gateway is built only when it is missing. Set TOOLBOX_MCP_REBUILD=1 to
# force a rebuild, or TOOLBOX_MCP_NO_BUILD=1 to fail fast instead of building.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BIN="$ROOT/cmd/toolbox/bin/toolbox"

if [ "${TOOLBOX_MCP_NO_BUILD:-0}" = "1" ]; then
  if [ ! -x "$BIN" ]; then
    echo "toolbox-mcp: $BIN is missing; run 'make build' first" >&2
    exit 1
  fi
elif [ ! -x "$BIN" ] || [ "${TOOLBOX_MCP_REBUILD:-0}" = "1" ]; then
  echo "toolbox-mcp: building the Toolbox host" >&2
  make -C "$ROOT" host >&2
fi

exec "$BIN" mcp "$@"
