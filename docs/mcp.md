# MCP gateway

Toolbox exposes reflected ConnectRPC services as Model Context Protocol (MCP)
servers. The implementation lives in `pkg/mcp` and uses the official
`github.com/modelcontextprotocol/go-sdk` for protocol handling.

## Design

```text
.proto comments + descriptor sets
              │
              ▼
      gRPC reflection / docs
              │
              ▼
       pkg/mcp.Server
   ┌──────────┼───────────┐
   ▼          ▼           ▼
introspection feature   call_rpc
tools       tools       tool
   │          │           │
   └──── exposure gate ───┘
              │
              ▼
    ConnectRPC dynamic invocation
```

Reflection is used to discover schemas and to invoke unknown unary methods. It
is not the authorization mechanism. `mcp.FeaturePolicy` is the explicit
boundary that decides which reflected methods may be exposed or called.

## Feature model

A feature is one reflected RPC method:

- stable id: `<fully-qualified-service>/<method>`
- generated tool name: `<short-service>__<method>`, for example
  `workflow__get_workflow`
- protobuf input and output descriptors
- documentation extracted from protobuf comments
- service capability metadata
- streaming and callable flags
- allowed and exposed state

A service with explicit capabilities is eligible for method exposure. A service
without capabilities is denied by default. External sources can provide a
`ServiceMetadata.AllowedMethods` list to narrow a service to an explicit
method allow-list.

Only unary methods become generated tools. Streaming methods remain visible in
introspection with `callable: false` and produce a clear error if an agent tries
to expose or call them.

## Introspection tools

The following tools are always present, even when the initial feature footprint
is empty:

| Tool | Purpose |
|---|---|
| `list_services` | List represented services, feature counts, exposure totals, and capabilities |
| `list_features` | List allowed features, including hidden ones; optionally include policy-denied methods |
| `describe_feature` | Return input/output JSON Schema, metadata, exposure state, and documentation |
| `read_feature_documentation` | Read feature documentation without exposing the generated tool |
| `expose_feature` | Add an allowed unary feature to `tools/list` |
| `hide_feature` | Remove a feature from `tools/list` and reject calls until re-exposed |
| `feature_exposure` | Report allowed, exposed, hidden, and denied totals and per-feature state |
| `call_rpc` | Call an exposed unary method by fully-qualified service and method name |

`expose_feature` and `hide_feature` accept either `feature` or the
`service` + `method` pair. `call_rpc` accepts a protobuf JSON `request` object.

Exposure mutations update the live MCP tool set and cause the SDK to emit the
standard `notifications/tools/list_changed` notification. Hidden features stay
discoverable so an agent can grow its tool surface deliberately.

## Prompt-footprint model

By default a generated server exposes every policy-allowed unary feature. Start
with only the eight management tools when a smaller discovery payload is
preferred:

```sh
toolbox-echo mcp --minimal
```

A typical agent flow is:

1. Call `list_features` or `list_services`.
2. Call `describe_feature` for the operations it needs.
3. Call `expose_feature` for only those operations.
4. Call the generated tools or `call_rpc`.
5. Call `hide_feature` when the working set shrinks.

Hidden features are not a security boundary. The feature policy is. Exposure
controls the MCP tool surface and prevents accidental calls to methods the
agent has not selected.

## Generated tool schemas

The generator converts protobuf descriptors into JSON Schema using protobuf JSON
field names. It supports scalar fields, enums, bytes, repeated fields, maps,
nested messages, presence-based required fields, and bounded recursion. The
output schema is also advertised when the method has a response message.

Tool descriptions come from the protobuf method comments embedded through the
subsystem descriptor set. Handwritten MCP documentation is not required.

## Independent and aggregated deployment

### One subsystem

Every standalone subsystem command receives an automatic `mcp` subcommand:

```sh
./bin/workflow mcp
./bin/workflow mcp --service toolbox.workflow.v1.WorkflowService
./bin/workflow mcp --minimal
./bin/workflow mcp --include-reflection
```

`--service` is useful when one subsystem mounts multiple contracts. The
underlying `pkg/mcp` adapters are:

- `mcp.NewFromSubsystem` — all services mounted by one started subsystem.
- `mcp.NewFromSubsystemService` — one service as an independent MCP.
- `mcp.NewFromDescriptors` — all started subsystem descriptors as one MCP.
- `mcp.NewFromServiceEndpoints` — arbitrary discovered endpoints as one MCP.
- `mcp.NewFromResolver` — resolve a supplied service list through a
  `core.Resolver` and aggregate the resulting endpoints.

### Aggregated host

The combined host exposes one MCP over all selected built-in subsystems:

```sh
./bin/toolbox mcp --all
./bin/toolbox mcp --component workflow --component agent
./bin/toolbox mcp --service toolbox.knowledge.v1.KnowledgeService
./bin/toolbox mcp --all --minimal
```

`--component` and `--service` are repeatable. `--component` selects
subsystems; `--service` filters the fully-qualified service names inside the
selected subsystems. `--minimal` starts with introspection tools only.

## API catalogs

A second path builds the same gateway from a catalog of registered API
descriptions: `mcp.NewFromAPICatalog(ctx, catalog, invoker, options)`. A catalog is
any `api.Catalog`, and an invoker is any `api.Invoker` — in a deployment those come
from the API catalog subsystem, so the operations an agent gains by registering an
API on the fly are exposed under the same rules as reflected methods: the same
policy boundary, the same exposure lifecycle, the same management tools.

The catalog's own decisions are authoritative. A catalog that tracks exposure
reports every operation it knows, exposed or not; an operation it reports as hidden
is not offered even though the gateway's own policy would allow it, and one it
denies is never a tool. An operation the catalog has never heard of falls back to
the gateway's policy, so a catalog that predates a decision cannot shrink the
surface by accident. A catalog without an invoker still describes and documents its
operations: a described operation is not a callable one, and the server says which
half is missing.

### MCP as a format and a target

The gateway is also a provider implementation, because MCP is a format the standard
model can be read from and rendered into:

- **Read a server.** `mcp.Describe(ctx, endpoint, options)` reads a live server's
  own tool list into a description, the way a gRPC endpoint is read out of its
  reflection. A third-party MCP server is registered in the catalog like anything
  else, exposed by a declared capability, and called through an invoker.
- **Read a manifest.** `mcp.DescribeDocument` reads a published manifest, so an API
  published earlier — or carried in a configuration file — can be registered with no
  server running.
- **Publish a description.** `mcp.Render` produces tool definitions and
  `mcp.Manifest` writes them as a document. This is the *same* translation the
  gateway uses, so a tool published from a description and a tool served from it are
  the same tool: same name, same arguments, same prose.
- **Call a tool.** `mcp.Invoker` calls one, keeping a protocol session per endpoint
  rather than reopening a conversation per call.

A tool manifest states no authorization facts. Capabilities stay empty unless the
server declared the `x-toolbox-capabilities` extension, so a third-party MCP server
is not a fully exposed tool surface the moment it is registered — an operation
nobody declared a capability for is described, not exposed. What a tool *does* say
about its consequences is carried as side effects, from its annotations.

The provider subsystem `subsystems/apimcp` serves all three contracts for a
deployment that needs them addressable. Its serving face refuses, and says why:
forwarding a tool call needs an invoker for the original API's transport, which the
catalog selects.

### Choosing the source of the surface

`toolbox mcp` builds its surface from reflection by default. `--mcp-source catalog`
builds it from the catalog instead, after registering every started subsystem from
the contract that subsystem serves:

```sh
toolbox mcp --mcp-source catalog --component apitools --component knowledge
```

Both name the same operations identically, so switching the source renames nothing
an agent already uses. What the catalog adds is exposure per operation, the ability
to re-publish a description in another format, and registrations that outlive the
gateway. Every started subsystem is registered whole, so a provider's extension
contracts are part of the catalog like everything else; what an agent gets from
them is decided by exposure, as with any other operation.

Where several providers serve one contract, two operations reduce to the same short
name, and a name more than one operation claims is qualified with the API that
tells them apart:

```text
apimcp__api_parser__parse_api
apigrpc__api_parser__parse_api
```

A name nothing else needs keeps the name an agent already learned, so adding a
provider renames no tool that was unambiguous before.

## OpenCode sessions

The repository `opencode.json` wires two local MCP servers into every OpenCode
session started in this project:

| Server     | Command                                            | Purpose                                     |
| ---------- | -------------------------------------------------- | ------------------------------------------- |
| `toolbox`  | `sh -c "exec scripts/toolbox-mcp.sh --all"`         | Aggregated gateway for all built-in subsystems |
| `debug-mcp` | `npx -y @debugmcp/mcp-debugger@<version> stdio`      | Headless DAP debugging for nine languages  |

`scripts/toolbox-mcp.sh` builds `cmd/toolbox/bin/toolbox` through `make host`
when it is missing, keeps every build line on stderr so stdout stays a valid
JSON-RPC stream, and then execs the gateway. `TOOLBOX_MCP_REBUILD=1` forces a
rebuild; `TOOLBOX_MCP_NO_BUILD=1` fails fast instead of building. Startup,
catalog, and execution timeouts are raised because the first connection may
build the host and the debugger downloads through `npx`.

OpenCode does not spawn `["sh", "script.sh", ...]` for local MCP servers — the
process exits before the script runs and the client reports "Connection
closed". Keep the `sh -c "exec …"` form in `opencode.json`.

The `toolbox-docs` reference exposes `docs/` to the agent, and `AGENTS.md`
carries the introspection-first workflow. A session typically does:

```text
toolbox.list_services      -> toolbox.list_features
toolbox.describe_feature   -> toolbox.read_feature_documentation
toolbox.expose_feature     -> toolbox.<service>__<method>
toolbox.call_rpc           -> toolbox.hide_feature / toolbox.feature_exposure
```

Validate the configuration with `opencode mcp list`; both servers must report
`connected` before feature work starts.

## Programmatic use

```go
bridge, err := mcp.NewFromSubsystem(ctx, server, mcp.Options{
    Name:            "workflow-mcp",
    InitialExposure: mcp.ExposeNoFeatures,
})
if err != nil {
    return err
}
defer func() { _ = server.Shutdown(context.Background()) }()
return bridge.ServeStdio(ctx)
```

For a remote aggregate:

```go
source, err := mcp.NewEndpointSource(mcp.ServiceEndpoint{
    Name: "workflow",
    URL:  "http://127.0.0.1:9000",
    Services: []mcp.ServiceMetadata{{
        Name:         "toolbox.workflow.v1.WorkflowService",
        Capabilities: []string{"workflow.definition.read"},
    }},
})
if err != nil {
    return err
}
bridge, err := mcp.New(ctx, source, mcp.Options{
    Policy:          mcp.AllowAllFeatures(),
    InitialExposure: mcp.ExposeNoFeatures,
})
```

`Server.HTTPHandler()` returns an official Streamable HTTP handler when a
remote transport is needed. The current exposure state is scoped to the
`Server` instance; the stdio command has one instance per process. The HTTP
handler currently shares that state across sessions, so session-isolated
footprints are a planned enhancement.

## Security and side effects

- A reflected method is not callable until the feature policy allows it.
- `call_rpc` checks the same policy and exposure state as generated tools.
- Mutating methods should be declared in service capabilities and later in a
  method-level policy layer with required permissions and approval metadata.
- MCP exposure does not replace the subsystem's own authentication,
  authorization, policy, or audit checks.
- Do not expose a remote endpoint with `AllowAllFeatures` unless it is trusted.
- Keep generated MCP bound to loopback or protect the HTTP endpoint with the
  deployment's authentication and origin controls.

## Current limitations

- Generated tools support unary methods only; streaming invocation is rejected
  explicitly.
- HTTP exposure state is process-wide until per-session MCP servers are added.
- Exposure is not persisted across restarts.
- The default capability policy is service-level; use
  `ServiceMetadata.AllowedMethods` or a custom `FeaturePolicy` for
  method-level authorization.
- Tool annotations such as destructive/read-only hints are not inferred from
  provider or subsystem metadata yet.
- Authentication, secret propagation, and durable exposure policy belong to
  the future policy/security milestone.

## Tests

The foundation tests use the official MCP SDK in-memory transport and cover:

- tool generation and JSON Schema output;
- management-tool availability in minimal mode;
- expose/hide transitions and `tools/list` changes;
- generic RPC invocation and hidden-feature call gating;
- policy denial independent of reflection;
- documentation lookup;
- concurrent exposure mutation under the race detector;
- real reflection and MCP invocation through `subsystems/testecho`.

Run:

```sh
go test ./pkg/mcp
go test -race ./pkg/mcp
GOWORK=off make -C subsystems/testecho test
```
