# Toolbox MCP gateway

Use this skill when changing `pkg/mcp`, `pkg/cliapp`'s `mcp` command, or the
combined host's aggregated MCP.

## Boundary

- Keep reflection, JSON Schema conversion, feature catalogs, introspection
  tools, and `call_rpc` dispatch in `pkg/mcp`.
- Treat reflection as schema discovery, not authorization. A method must be
  allowed by an explicit `FeaturePolicy` before exposure or invocation.
- Do not hand-write RPC-specific MCP tools in a subsystem command.
- Subsystem commands get `mcp` through `pkg/cliapp`; the host composes the
  aggregate through `pkg/host` and `pkg/mcp`.

## Exposure

- Management/introspection tools are always available.
- Generated feature tools are allowed unary methods only.
- `expose_feature` and `hide_feature` mutate the live `tools/list` surface and
  must remain idempotent.
- `call_rpc` must use the same policy and exposure gate as generated tools.
- Hidden features remain discoverable through `list_features` and
  `describe_feature`.
- Streaming methods are introspectable but rejected clearly by the unary
  gateway.

## Schemas and calls

- Generate input/output JSON Schema from `protoreflect` descriptors using
  protobuf JSON field names.
- Preserve documentation from `pkg/docs` in feature descriptions and the
  documentation introspection tool.
- Decode requests with `protojson` and marshal responses as structured JSON.
- Use canonical, actionable tool errors for policy denial, hidden features,
  malformed requests, unsupported streaming, and RPC failures.

## Transports and lifecycle

- Use the official `github.com/modelcontextprotocol/go-sdk/mcp` transport
  abstractions.
- Stdio is the default command transport; stdout must contain only MCP frames.
- `HTTPHandler` may expose Streamable HTTP, but document that exposure is
  process-wide until session isolation is implemented.
- Propagate context cancellation into RPC calls and clean up subsystem servers
  when the MCP transport ends.

## Tests

Use the SDK in-memory transport and `testify`:

- tool names, descriptions, input/output schemas, and management-tool surface;
- minimal initial exposure and full initial exposure;
- expose/hide transitions and `tools/list` changes;
- generated tool calls and `call_rpc` round trips;
- policy denial independent of reflection;
- documentation lookup and streaming rejection;
- concurrent expose/hide under `go test -race ./pkg/mcp`;
- real reflection through `GOWORK=off make -C subsystems/testecho test`.
