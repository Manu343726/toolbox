# Toolsbox architecture

## 1. Architecture goals

Toolsbox separates feature behavior from transport and deployment. A subsystem
can be replaced, moved to another process, or implemented by a third party
without changing the contracts used by other subsystems.

The architecture has four concerns:

1. **Feature contracts** — subsystem-owned protobuf APIs.
2. **Runtime foundation** — serving, reflection, registry resolution, typed and
   dynamic calls, documentation, and CLI generation.
3. **Process composition** — standalone subsystem binaries and the combined host.
4. **Domain policy** — capabilities, permissions, approvals, and reproducibility.

## 2. Module graph

```text
                         ┌──────────────────────┐
                         │  cmd/toolsbox host   │
                         │  imports factories   │
                         └──────────┬───────────┘
                                    │ composition only
       ┌────────────────────────────┼────────────────────────────┐
       ▼                            ▼                            ▼
┌──────────────┐             ┌──────────────┐             ┌──────────────┐
│ workflow     │             │ agent        │             │ registry     │
│ subsystem    │             │ subsystem    │             │ subsystem    │
└──────┬───────┘             └──────┬───────┘             └──────┬───────┘
       │                            │                            │
       └──────────────┬─────────────┴──────────────┬─────────────┘
                      │                            │
                      ▼                            ▼
             ┌──────────────────┐         ┌──────────────────┐
             │ pkg/core         │         │ pkg/discovery    │
             │ resolve + call   │         │ reflection       │
             └────────┬─────────┘         └────────┬─────────┘
                      │                            │
                      └──────────────┬─────────────┘
                                     ▼
                           ┌──────────────────┐
                           │ pkg/subsystem    │
                           │ h2c + reflection │
                           └──────────────────┘
```

Feature modules do not import one another. The arrows between feature modules
in a running deployment are RPC calls resolved through `core.Resolver` and the
registry.

## 3. Canonical subsystem layout

```text
subsystems/<name>/
├── proto/<name>.proto       # subsystem-owned contract
├── <name>.go                # implementation and New entrypoint
├── *_test.go                # unit/integration tests
├── docs_embed.go            # embeds source-info descriptor set
├── Makefile                 # independent build/test lifecycle
├── go.mod                   # independent module
├── <name>v1/                # generated protobuf package (ignored)
├── <name>v1connect/         # generated ConnectRPC package (ignored)
└── cmd/<name>/main.go        # standalone command
```

A subsystem is valid only if it can be tested and built from its own directory
with its own Makefile. The combined host is optional.

## 4. Runtime layers

### 4.1 Transport

`pkg/subsystem.Server` owns:

- listener creation;
- h2c HTTP serving;
- mounting generated ConnectRPC handlers;
- mounting gRPC reflection v1 and v1alpha;
- handshake metadata;
- graceful shutdown and background-worker cancellation.

It does not know the feature meaning of a service. A subsystem supplies its own
`subsystem.Service` values.

### 4.2 Endpoint registry

The registry answers:

> Which endpoint currently owns this subsystem or fully-qualified service?

The registry stores endpoint, implementation version, API version, advertised
service names, capabilities, dependencies, status, and lease information.

The registry is an independent subsystem. Other subsystems do not import its
implementation; they consume the public `core.Resolver` interface.

### 4.3 Reflection

Reflection answers:

> What services, methods, and protobuf message types are available at this
> endpoint?

The discovery client uses gRPC reflection to list services and fetch file
descriptors. It reconstructs `protoreflect` descriptors and caches schemas.
Reflection is not used to grant tool permissions.

### 4.4 Typed calls

A known service follows this sequence:

```text
service name
    │
    ▼
core.Resolver.Resolve
    │ failure => stop before client construction
    ▼
generated ConnectRPC constructor(endpoint)
    │
    ▼
typed method(request) -> typed response
```

This preserves compile-time request/response safety while allowing runtime
service placement.

### 4.5 Dynamic calls

An unknown external service follows this sequence:

```text
service name
    │
    ▼
resolver -> endpoint
    │
    ▼
reflection descriptor cache
    │
    ▼
dynamic unary invocation
```

Dynamic calls are an interoperability path, not the preferred path for
first-party code. Streaming dynamic calls are not currently supported.

## 5. Documentation and CLI flow

```text
.proto source comments
        │
        ▼
protoc --include_source_info
        │
        ▼
FileDescriptorSet embedded by docs_embed.go
        │
        ▼
pkg/docs neutral documentation model
        │
        ├──► DocumentationService
        └──► pkg/cli Cobra generator
```

The documentation service and CLI are separate concerns:

- `pkg/docs` parses and stores neutral documentation.
- The `documentation` subsystem exposes documentation over its own proto.
- `pkg/discovery` can obtain documentation from an embedded catalog or remote
  DocumentationService.
- `pkg/cli` turns reflected methods and documentation into commands and flags.

Subsystem commands do not hand-write RPC-specific Cobra methods or flags.

## 6. MCP gateway

The public `pkg/mcp` package turns a reflected ConnectRPC source into an MCP
server. It uses protobuf reflection for schemas and dynamic unary invocation,
but keeps authorization in an explicit `FeaturePolicy`:

```text
reflected service + explicit capability policy
                │
                ▼
        feature catalog (service/method)
                │
       ┌────────┴────────┐
       ▼                 ▼
always-on introspection   generated RPC tools
       │                 │
       └──── exposure ───┘
                │
                ▼
        call_rpc / generated call
                │
                ▼
       ConnectRPC endpoint
```

Each generated MCP instance owns its feature-exposure state. A subsystem
command creates an instance for its mounted services; `toolsbox mcp` creates
one instance over all selected host descriptors. Introspection tools remain
available when the initial feature surface is empty, allowing an agent to
list, read documentation for, expose, and hide individual methods. See
[`docs/mcp.md`](mcp.md) for the tool contract and deployment commands.

## 7. Composition modes

### Standalone mode

```sh
cd subsystems/workflow
make build
./bin/workflow
```

The subsystem starts its own service endpoint and can run without the combined
host. Dependencies must be supplied through a resolver or configuration.

### Selected mode

```sh
./bin/toolsbox --component workflow --component registry
```

The host constructs and starts only the selected subsystem factories.

### All mode

```sh
./bin/toolsbox --all
```

The host starts all built-in modules. Each module still registers and resolves
services through the same runtime interfaces. The combined process is a
composition convenience, not a privileged integration path.

## 8. Data ownership and persistence

Each subsystem owns its state and persistence. No feature should query another
feature's database directly. Cross-subsystem state is exchanged through:

- versioned RPC messages;
- artifact references;
- explicit events;
- registry metadata;
- policy and capability declarations.

The current reference stores are in-memory. Persistence interfaces and SQLite
adapters are planned work.

## 9. Extension model

A third-party subsystem is compatible when it:

1. serves a ConnectRPC contract from its own module;
2. exposes gRPC reflection;
3. publishes a registry descriptor with capabilities and dependencies;
4. follows the same metadata and error conventions;
5. can be resolved through `core.Resolver`.

Reflection alone is insufficient for agent exposure. A service must explicitly
declare tool capabilities, side effects, permissions, and policy requirements.

## 10. Architectural constraints

- No framework-wide aggregate feature proto.
- No feature-to-feature implementation imports.
- No hard-coded endpoint maps in workflow or agent logic.
- No provider-specific types in provider-neutral contracts.
- No prompt-only policy enforcement.
- No dynamic replacement for a known generated client without an explicit
  compatibility decision.
- No generated artifacts in source control.

## 11. Related documents

- [Feature specification](feature-spec.md)
- [Subsystem catalog](subsystems.md)
- [Development guide](development.md)
- [Testing guide](testing.md)
- [Current status](status.md)
- [TODO and roadmap](todos.md)
- [Protocol conventions](protocol.md)
- [MCP gateway](mcp.md)
- [Architecture decisions](decisions/README.md)
