# Protocol conventions

## ConnectRPC

- Services use generated ConnectRPC handlers and clients.
- The wire protocol is compatible with Connect, gRPC, and gRPC-Web where the
  generated handler supports it.
- gRPC reflection v1 and v1alpha are mounted for service/schema discovery.
- Streaming metadata is preserved in descriptors. The current dynamic client
  supports unary methods only.

## Naming

- Protobuf packages are versioned: `toolsbox.<subsystem>.v1`.
- Service names are fully qualified and end in `Service`.
- RPC methods use imperative PascalCase names.
- Subsystem names are stable lowercase identifiers such as `workflow`.
- Capability names are namespaced strings such as `knowledge.search`.

## Errors

Use canonical ConnectRPC status codes:

| Code | Use |
|---|---|
| `InvalidArgument` | Malformed or semantically invalid request |
| `NotFound` | Requested resource or service does not exist |
| `AlreadyExists` | Immutable resource conflicts during creation |
| `FailedPrecondition` | Resource is not in a valid state |
| `PermissionDenied` | Policy or authorization rejects an action |
| `Unauthenticated` | Missing or invalid identity |
| `Unavailable` | Dependency, registry, or endpoint is unavailable |
| `DeadlineExceeded` | Context deadline expired |
| `Unimplemented` | Known method is not implemented |
| `Internal` | Unexpected implementation failure |

Error details should be machine-readable where possible. Do not expose secrets
or full sensitive payloads in error messages.

## Metadata

Runtime metadata should be propagated through headers/interceptors rather than
feature-specific request fields. The current core metadata supports:

```text
X-Toolsbox-Request-Id
X-Toolsbox-Trace-Id
X-Toolsbox-Actor-Id
X-Toolsbox-Run-Id
X-Toolsbox-Workspace-Id
X-Toolsbox-Policy-Id
```

Subsystems may add protocol-specific metadata, but authentication credentials
must use the transport/security mechanism rather than ordinary feature fields.

## Registry metadata

A registration should contain:

- stable subsystem name;
- endpoint;
- implementation and API versions;
- fully-qualified service names;
- explicit capabilities;
- dependencies;
- status;
- lease information.

Capabilities are semantic declarations. They are not inferred from method
names alone.

## Versioning

- Keep a major API version in the protobuf package.
- Prefer additive fields and methods within a major version.
- Reserve removed field numbers and names.
- Use a new package/version for breaking changes.
- Record compatibility expectations in subsystem documentation.
- Record the exact workflow, agent, skill, prompt, model, and policy versions in
  a run record.

## MCP exposure

The MCP gateway uses the official Model Context Protocol Go SDK. A reflected
RPC method is a candidate feature, but it becomes a tool only when an explicit
`FeaturePolicy` allows it. Exposure changes the `tools/list` surface and emits
`notifications/tools/list_changed`; it is not a replacement for authorization.

The always-on MCP management surface is:

- `list_services`
- `list_features`
- `describe_feature`
- `read_feature_documentation`
- `expose_feature`
- `hide_feature`
- `feature_exposure`
- `call_rpc`

`call_rpc` uses the same policy and exposure gate as generated tools. Streaming
methods are introspectable but explicitly rejected by the unary gateway.

## Documentation

Comments in `.proto` files are the source of generated service documentation
and CLI help. Subsystem Makefiles generate descriptor sets with source info and
embed them through `docs_embed.go`. Do not maintain a second hand-written API
reference that can drift from the proto contract.
