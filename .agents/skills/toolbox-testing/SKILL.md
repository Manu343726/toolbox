---
name: toolbox-testing
description: Write deterministic testify unit and integration tests for Toolbox subsystems, ConnectRPC handlers, registries, and shared runtime packages.
---

# Toolbox testing

Use this skill whenever behavior changes or a new subsystem is added.

## Required coverage

Every subsystem must have tests for:

- successful create/read/update/list behavior;
- version or replacement semantics where applicable;
- input validation and invalid protobuf fields;
- not-found and duplicate/conflict paths;
- ConnectRPC error-code mapping;
- capability, policy, or permission metadata;
- shutdown, expiry, or cancellation when lifecycle state is involved.

Shared foundation packages need focused unit tests plus integration tests when
reflection, generated handlers, or service-to-service clients are involved.

## Test style

- Use `testify/assert` for independent checks and `testify/require` when a
  failure should stop the test.
- Prefer table-driven tests for input matrices.
- Use `t.Run` with descriptive names.
- Test public behavior rather than private implementation details.
- Inject clocks, endpoint providers, and invokers instead of using sleeps.
- Bound goroutines and channels; fail instead of hanging forever.
- Use `t.Cleanup` for servers, listeners, and temporary resources.
- Never weaken assertions, skip tests, or hide failures behind logs.

## Useful patterns

For a handler:

```go
response, err := handler.Method(ctx, connect.NewRequest(&pb.Request{...}))
require.NoError(t, err)
assert.Equal(t, expected, response.Msg)
```

For a ConnectRPC failure:

```go
_, err := handler.Method(ctx, connect.NewRequest(&pb.Request{}))
require.Error(t, err)
assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
```

For typed service-to-service behavior, test both successful typed binding and
resolution failure. The generated constructor must not be called when the
service cannot be resolved.

## Required commands

```sh
make test
make vet
go test -race ./...
GOWORK=off make -C subsystems/<name> test
```

Run the independent-module command for changes to a subsystem. A feature is not
complete until its behavior is covered by unit tests and its public boundary is
covered by an appropriate integration test.
