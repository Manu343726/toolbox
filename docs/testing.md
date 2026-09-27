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
| Metadata | declared side effects, permissions, dependencies |
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

## Exercising a subsystem through the gateway

The layers above cannot see three things, and all three are real:

- **whether a method is reachable at all.** An embedded `Unimplemented...Handler` satisfies the
  generated interface, so a method that is in the contract, registered, documented and classified
  can return `unimplemented` and pass every test that calls the handlers it is testing — because
  it has no handler.
- **whether the policy lets an agent call it.** A method can be correctly implemented and
  correctly classified and still be invisible to every agent, because only a policy permits one.
- **whether an error survives three layers.** A classification made in a handler can be lost
  between it and the tool result, and "the backend is down" arriving as `internal` is a
  deployment that cannot be diagnosed.

`scripts/mcp_knowledge_sweep.py` calls every tool the policy exposes and reports which answer,
which refuse and which are `unimplemented`; `scripts/mcp_knowledge_test.py` asserts the parts a
Go test cannot reach. Both drive a real gateway process over stdio against
`scripts/hindsight_stub.py`, a stub backend serving the required-field shapes the generated client
insists on.

```sh
make host
python3 scripts/mcp_knowledge_sweep.py     # per-tool reachability
python3 scripts/mcp_knowledge_test.py      # the assertions
python3 scripts/mcp_knowledge_dump.py content__list_content   # one full response, untruncated
python3 scripts/mcp_knowledge_mount.py     # the mount, and the failure path
```

`mcp_knowledge_mount.py` is the one that needs a tag, and it builds one: the mount is behind
`fuse`, and without `TAGS=fuse` it would be testing the *absence* of FUSE rather than the
behaviour of a mount. It establishes whether the machine can mount at all by building a minimal
FUSE mount with none of this repository's code in it, so a skip is evidence rather than a guess —
`/dev/fuse` existing and `fusermount3` being setuid are both necessary and neither is sufficient,
and this is a machine where they are and a mount is still refused.

On a machine that cannot mount, the read half is skipped and the **failure** half still runs, which
is the half that matters there: a refused mount must say which piece was missing, must not be
reported as serving, and must leave an ordinary usable directory rather than a registered-but-dead
mount. That last one is M-7, it was violated, and it is invisible to any test over `io/fs` — which
is precisely why the rule says the wiring is exercised by hand.

They need a corpus, which the scripts create for themselves in a temporary directory, and they
manage the stub and the gateway. **Rebuild the host first**: the gateway is a process that holds a
binary, so a source change is not a running change, and `scripts/toolbox-mcp.sh` rebuilds on a
stale binary precisely so that a caller is not left testing a build that no longer exists.

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
