# Toolbox architecture

## 1. Architecture goals

Toolbox separates feature behavior from transport and deployment. A subsystem
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
                         │  cmd/toolbox host   │
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

## 5. Public foundation packages

Subsystems and the host compose from these shared packages instead of copying
SDK behavior.

| Package         | Responsibility                                                                   |
| --------------- | -------------------------------------------------------------------------------- |
| `pkg/subsystem` | Transport and lifecycle SDK: listeners, h2c, generated handler mounting, reflection, handshake metadata, graceful shutdown |
| `pkg/discovery` | Reflection-based service/schema discovery, descriptor caching, documentation lookup, dynamic unary invocation |
| `pkg/core`      | `core.Resolver` endpoint resolution plus service-to-service calls; `core.Bind` constructs generated, type-safe clients after resolution succeeds |
| `pkg/docs`      | Neutral documentation model parsed from descriptor sets generated with `--include_source_info` |
| `pkg/cli`       | Cobra command generator driven by reflected methods and documentation            |
| `pkg/cliapp`    | Shared standalone-command runner; adds the automatic `mcp` subcommand to every subsystem command |
| `pkg/mcp`       | MCP gateway built from a reflected source: feature catalog, exposure state, policy gating, introspection tools, stdio and HTTP transports |
| `pkg/host`      | Composes independently built subsystem factories in one process; aggregates their descriptors for the combined MCP; derives the API provider set from started subsystems' capabilities |
| `pkg/api`       | Standard, provider-neutral description of an API: `API`, `Service`, `Operation`, `Schema`, `Server`, plus indexed format and transport descriptors and the framework's own extension contract. Also the framework's Go interfaces — `Catalog`, `Registrar`, `Invoker`, `ExposureSource` — and the failure classification every provider reports |
| `pkg/protocontract` | Reads a protobuf service contract, from a FileDescriptorSet or a live endpoint's reflection, and calls the operations it declares. A plain Go package: no transport, no service registration |
| `pkg/openapi`   | Reads, renders, serves, and calls APIs described by OpenAPI 3.x documents, with no transport of its own |

A subsystem implements its own service and may import any of these packages. It
must not import another feature subsystem.

## 6. Documentation and CLI flow

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

## 7. API introspection and providers

The framework does not know any description language. `pkg/api` defines the
standard description every format is parsed into and every format is rendered
from, and a format or transport is an open identifier the framework never
enumerates.

Three contracts form the extension surface, each implemented by a subsystem:

| Contract              | Direction                                    | Capability prefix  |
| --------------------- | -------------------------------------------- | ------------------ |
| `ApiParserService`    | description document → standard description   | `api.parse.<fmt>`  |
| `ApiAdapterService`   | standard description → target representation  | `api.render.<tgt>` |
| `ApiInvokerService`   | operation + server → live response            | `api.invoke.<tr>`  |

An adapter reads only the standard description, so rendering a protobuf contract
as an OpenAPI document is the same operation as the reverse. An adapter also
serves: `RenderApi` returns the target's schema files, and `ServeApi` serves that
target's surface while tunneling to the original server, which it receives
alongside the description. The OpenAPI provider reserves paths for the target's
Swagger UI and a downloadable schema, and refuses a base path that would collide
with a path it generates.

Providers are discovered from the capabilities a started subsystem declares, and
bound by provider identifier through the generated framework clients. A provider is
never bound by service name, because several providers serve the same contract on
purpose and a name cannot tell them apart.

The catalog subsystem — `subsystems/apitools` — stores servers, API descriptions,
the indexed format and transport descriptors, and operation exposure, and routes
parsing, rendering, serving, and invocation to whichever provider claims the work. It
imports no provider subsystem; a deployment supplies a provider directory. It also
exposes itself as `api.Catalog`, `api.Registrar`, `api.Invoker`, and
`api.ExposureSource`, so a composition in the same process uses it directly.

### Behaviour lives in packages, subsystems are their addressable form

The OpenAPI and protobuf implementations are reusable root packages, not subsystem
logic. A subsystem mounts a package behind a contract so a catalog in another
process can reach it; a host that has both in one process calls the package. See
[`decisions/0005-reusable-packages-behind-thin-providers.md`](decisions/0005-reusable-packages-behind-thin-providers.md).

### Automatic exposure of a host's own subsystems

A host can register what it runs in the catalog, with nothing written per subsystem:

1. Each started subsystem's contract is read from the subsystem itself.
2. The capabilities its manifest declared are attached to the services they belong
   to, because a contract cannot state them.
3. The description is stored and bound to the subsystem's endpoint.
4. The operations a declared capability covers are exposed. An operation no
   capability covers is not a tool, and an operation the catalog's policy refuses
   is reported rather than forced.

Every started subsystem is registered whole. Nothing is left out because of what it
happens to serve: health, the registry, and the framework's own extension contracts
are services like any other, and a provider subsystem exists to serve the extension
contracts. Whether an agent gets one is decided by whether it declared a capability
and whether the deployment exposes it, never by a list of service names. A
deployment that wants a service out of the catalog says so by name.

The MCP gateway can build its tool surface from that catalog, naming the same
operations as the reflection path, so the source of the surface can be chosen per
deployment. Several providers serve one contract, so two operations can reduce to
the same short tool name. A name more than one operation claims is qualified with
the API or the endpoint that tells them apart, and a name nothing else needs is left
alone, so a name an agent already learned does not change.

The Model Context Protocol is itself one of these formats, in the same package that
serves it: a live MCP server is read through its own tool list, a published manifest
is read as a document, and a description is rendered as a manifest. Reading a server
states no capabilities, so a third-party MCP server's operations stay unexposable
until a deployment declares what it authorizes.

See [`decisions/0004-api-introspection-and-providers.md`](decisions/0004-api-introspection-and-providers.md)
for the decision and its consequences.

## 8. MCP gateway

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
command creates an instance for its mounted services; `toolbox mcp` creates
one instance over all selected host descriptors. Introspection tools remain
available when the initial feature surface is empty, allowing an agent to
list, read documentation for, expose, and hide individual methods.

Every service a subsystem serves is a candidate. A tool appears because a
capability was declared for it and the deployment exposed it, not because of
what its service is called, so a health check and a provider's extension
contract are treated exactly like a feature subsystem's own operations. Only
the protocol's reflection services are left out, because they provide no
capability to expose. See [`decisions/0007-one-rule-for-every-feature.md`](decisions/0007-one-rule-for-every-feature.md)
and [`docs/mcp.md`](mcp.md) for the tool contract and deployment commands.

## 9. Composition modes

These modes exist to serve the product requirement that a user adopts either the
whole environment or only the capabilities they need. Each mode produces the
same behavior per capability; only the set of capabilities in the process
changes.

### Standalone mode

```sh
cd subsystems/workflow
make build
./bin/workflow
```

The subsystem starts its own service endpoint and can run without the combined
host. Dependencies must be supplied through a resolver or configuration.

A standalone subsystem also exposes itself independently to agents, so a user
can adopt one capability without running any other:

```sh
./bin/knowledge mcp
```

### Selected mode

```sh
./bin/toolbox --component workflow --component registry
```

The host constructs and starts only the selected subsystem factories.

### All mode

```sh
./bin/toolbox --all
```

The host starts all built-in modules. Each module still registers and resolves
services through the same runtime interfaces. The combined process is a
composition convenience, not a privileged integration path.

### Additive adoption

A capability moved between modes keeps its contract, policy, and documentation
and is not migrated. Adding a subsystem to a running deployment does not change
the contract, endpoint, or behavior of the subsystems already deployed; new
cross-subsystem traffic appears only when a definition explicitly references the
new capability. This is why a subsystem may not import another subsystem: doing
so would make the cost of leaving one out non-zero.

## 10. Data ownership and persistence

Each subsystem owns its state and persistence. No feature should query another
feature's database directly. Cross-subsystem state is exchanged through:

- versioned RPC messages;
- artifact references;
- explicit events;
- registry metadata;
- policy and capability declarations.

The current reference stores are in-memory. Persistence interfaces and SQLite
adapters are planned work.

## 11. Extension model

A third-party subsystem is compatible when it:

1. serves a ConnectRPC contract from its own module;
2. exposes gRPC reflection;
3. publishes a registry descriptor with capabilities and dependencies;
4. follows the same metadata and error conventions;
5. can be resolved through `core.Resolver`.

Reflection alone is insufficient for agent exposure. A service must explicitly
declare tool capabilities, side effects, permissions, and policy requirements.

## 12. Architectural constraints

- No framework-wide aggregate feature proto.
- No feature-to-feature implementation imports.
- No hard-coded endpoint maps in workflow or agent logic.
- No provider-specific types in provider-neutral contracts.
- No prompt-only policy enforcement.
- No dynamic replacement for a known generated client without an explicit
  compatibility decision.
- No generated artifacts in source control.

## 13. Related documents

- [Feature specification](feature-spec.md)
- [Subsystem catalog](subsystems.md)
- [Development guide](development.md)
- [Testing guide](testing.md)
- [Current status](status.md)
- [TODO and roadmap](todos.md)
- [Protocol conventions](protocol.md)
- [MCP gateway](mcp.md)
- [Architecture decisions](decisions/README.md)
