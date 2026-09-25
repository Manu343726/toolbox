# Testing guide

## Test strategy

Toolbox uses a layered test strategy:

1. **Pure unit tests** for stores, parsers, validators, and state transitions.
2. **Handler tests** for protobuf request validation and ConnectRPC error codes.
3. **Integration tests** for generated handlers, reflection, discovery, typed
   clients, and generated CLI commands.
4. **Race tests** for shared lifecycle, registry, watcher, host, and MCP
   exposure code.
5. **Independent-module tests** with `GOWORK=off` to verify subsystem boundaries.

## Required checks

From the repository root:

```sh
make check-tests
make test
make vet
```

For changes to shared concurrency or lifecycle code:

```sh
go test -race ./...
```

For a subsystem change:

```sh
GOWORK=off make -C subsystems/<name> test
```

The root `check-tests` target prevents a subsystem from being added without a
unit test file.

## Test coverage by subsystem

Every subsystem should cover the following where applicable:

| Area | Required cases |
|---|---|
| Store/state | create, replace, version selection, filtering, deterministic ordering |
| Handler | valid request, invalid request, not found, duplicate/conflict |
| RPC boundary | response values and canonical ConnectRPC status codes |
| Metadata | capabilities, permissions, side effects, dependencies |
| Lifecycle | startup, readiness, cancellation, shutdown, expiry |
| Concurrency | race-safe access and watcher behavior |
| Integration | reflection, typed calls, dynamic calls, generated CLI where relevant |

Do not use coverage percentage as a substitute for behavioral coverage. A
small, well-tested protocol adapter is preferable to a large suite of shallow
tests.

## Test style

Use `testify/assert` and `testify/require`:

```go
response, err := handler.Method(ctx, connect.NewRequest(&pb.Request{...}))
require.NoError(t, err)
assert.Equal(t, want, response.Msg)
```

Prefer:

- table-driven cases with `t.Run`;
- public behavior over private implementation details;
- injected clocks for leases and expiry;
- `t.Cleanup` for servers and listeners;
- bounded contexts and channels;
- deterministic ports and no arbitrary sleeps;
- explicit assertions on error codes.

Never skip a failing test, replace an assertion with a log, or make a test pass
by weakening its expected behavior.

## Integration vertical slice

`subsystems/testecho` is the executable vertical slice. Its tests exercise:

- starting an independent subsystem;
- gRPC reflection discovery;
- descriptor reconstruction;
- dynamic unary invocation;
- typed generated-client invocation through `core.Bind`;
- embedded protobuf documentation;
- schema-driven CLI command and flag generation;
- unavailable-service behavior.

When foundation behavior changes, update this vertical slice before adding
feature-specific workarounds.

## MCP gateway tests

The `pkg/mcp` tests use the official MCP SDK in-memory transport. They cover:

- generated feature tools and input/output JSON Schema;
- always-available introspection tools;
- minimal versus full initial exposure;
- `tools/list` changes after expose/hide;
- generated and generic `call_rpc` invocation;
- hidden-feature and policy-denial call gates;
- documentation retrieval;
- streaming rejection;
- concurrent exposure mutation under `-race`.

The real reflection/MCP integration slice lives in `subsystems/testecho`:

```sh
go test ./pkg/mcp
go test -race ./pkg/mcp
GOWORK=off make -C subsystems/testecho test
```

## Concurrency tests

Use the race detector for code involving:

- registry watchers and leases;
- host startup and shutdown;
- shared documentation catalogs;
- discovery descriptor caches;
- background workers;
- streams and cancellation.

Prefer channel synchronization and context deadlines over timing assumptions.

## Test data

Keep fixtures close to the subsystem that owns them. Test-only services belong
in an explicitly marked fixture subsystem such as `testecho`; do not add test
contracts to a production feature's proto file.
