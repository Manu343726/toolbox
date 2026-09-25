# Toolsbox architecture

## Boundary rule

Every subsystem is an independent RPC project:

```text
subsystems/<name>/
  proto/       # subsystem-owned protobuf contract
  <name>.go     # implementation and programmatic entrypoint
  *_test.go    # unit tests
  cmd/<name>/  # standalone launcher
  Makefile     # independent build/test lifecycle
  go.mod       # independent Go module
```

A subsystem may import the public foundation module (`pkg/subsystem`, `pkg/core`, `pkg/discovery`, `pkg/docs`, and `pkg/cli`). It must not import another feature subsystem. Dependencies between subsystems are runtime ConnectRPC dependencies, not Go package dependencies.

## Runtime paths

```text
generated Connect handler
        │
        ▼
subsystem.Server
  ├── mounts the subsystem's own services
  ├── mounts gRPC reflection v1/v1alpha
  ├── serves an h2c HTTP endpoint
  └── exposes lifecycle/descriptor metadata
        │
        ├── RegistryService (endpoint discovery)
        ├── gRPC reflection (schema discovery)
        └── generated Connect clients (typed calls)
```

`gRPC reflection` describes methods and messages at a known endpoint. `RegistryService` resolves which endpoint owns a service. The two operations are intentionally separate.

## Service-to-service calls

There are two supported paths:

### Known contract

1. Resolve the fully-qualified service name through `core.Resolver`.
2. Construct the generated client with the resolved endpoint via `core.Bind`.
3. Call generated methods with generated request/response types.

If resolution fails, `Bind` returns an error and the generated constructor is never called.

### Unknown contract

1. Resolve an endpoint.
2. Use `pkg/discovery` to fetch and cache the service descriptor.
3. Invoke a unary method dynamically with `core.Client.Invoke` or `InvokeJSON`.

Streaming methods are discovered but dynamic invocation rejects them until a streaming client API is added.

## Documentation and CLI

Subsystem Makefiles generate a `FileDescriptorSet` with `--include_source_info`. The public `pkg/docs` parser extracts service, method, and field comments from that set. Each subsystem embeds the descriptor set and registers it in the process-wide documentation catalog.

The CLI generator consumes reflected method descriptors and the documentation model. It creates Cobra commands and typed flags without hand-written per-method code:

```text
protobuf schema + comments
        │
        ▼
pkg/discovery
        │
        ▼
pkg/cli.Generator
        │
        ▼
Cobra command tree
```

The documentation service is itself an independent subsystem. A service may expose it, but the common server does not assume that every subsystem has the same protocol.

## Process composition

`pkg/host` accepts subsystem factories explicitly. The combined `cmd/toolsbox` host imports the built-in subsystem modules and composes them, but the host is not required for standalone subsystem builds. External services can use the same ConnectRPC/reflection contract without being compiled into the host.
