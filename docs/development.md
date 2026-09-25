# Development guide

## Prerequisites

- Go 1.27 or the version declared in the module files.
- `protoc`.
- `protoc-gen-go`.
- `protoc-gen-connect-go`.
- GNU Make.
- A POSIX shell for the Makefiles.

Install the protobuf plugins through the project Makefile:

```sh
make proto-tools
```

The plugins are installed in `~/go/bin`, which the Makefiles add to `PATH`.

## Repository layout

```text
pkg/                       public foundation packages
subsystems/<name>/         independent subsystem modules
cmd/toolsbox/              combined host module
docs/                      detailed project documentation
.agents/skills/             project-specific agent skills
go.work                    local workspace for development
```

Each subsystem has its own `go.mod`, `Makefile`, `proto/`, implementation,
tests, and `cmd/` directory. A subsystem can be copied or built without the
combined host as long as its foundation-module dependency is available.

## Common commands

From the repository root:

```sh
make test          # all subsystems, host, and foundation tests
make test-short    # fast test suite
make check-tests   # verify every subsystem has a test file
make vet           # vet every module
make fmt           # format hand-written Go
make build         # standalone subsystem binaries plus host
make host          # combined host only
make clean         # remove generated code, descriptors, and binaries
```

Run one subsystem independently:

```sh
cd subsystems/workflow
make proto
make test
make build
```

To verify that the module does not accidentally depend on the workspace:

```sh
GOWORK=off make -C subsystems/workflow test
```

## Adding a subsystem

1. Choose a stable subsystem name and capability namespace.
2. Create `subsystems/<name>/` with its own `go.mod` and Makefile.
3. Add one or more versioned `.proto` files under `subsystems/<name>/proto/`.
4. Generate Go and ConnectRPC code through `make proto`.
5. Implement the service and expose a programmatic `New` entrypoint.
6. Add `docs_embed.go` for the source-info descriptor set.
7. Add unit tests before wiring the command.
8. Add `cmd/<name>/main.go` using `pkg/cliapp`.
9. Add the module to `go.work` and the root Makefile discovery loop.
10. Register the subsystem in the combined host only when it is a built-in.
11. Update `docs/subsystems.md`, `docs/status.md`, and relevant API docs.

A feature subsystem must not import another feature subsystem. If it needs a
runtime dependency, inject or resolve a `core.Resolver` and call the generated
client after resolution.

## Protobuf workflow

Put the contract next to the subsystem that owns it:

```text
subsystems/<name>/proto/<name>.proto
```

Use a fully qualified `go_package` inside that subsystem module. Every service,
RPC, message, field, enum, and enum value must be documented because comments
are consumed by `pkg/docs` and `pkg/cli`.

The subsystem Makefile generates:

1. `*.pb.go` protobuf types.
2. `*.connect.go` typed handlers and clients.
3. `proto/<name>.pb` with `--include_source_info` for documentation.

Generated files are ignored. Never edit them manually and never stage them.
Run `make proto` after every contract change.

## Service implementation

Use `pkg/subsystem.Server` rather than duplicating HTTP server setup. Mount only
the subsystem's own services, expose reflection, and return explicit descriptor
metadata. Keep implementation state private to the subsystem.

For side effects:

- declare capabilities and required permissions;
- classify mutating/external actions;
- evaluate policy before invocation;
- use canonical ConnectRPC errors;
- propagate context deadlines and cancellation.

## Service-to-service calls

Known contracts use generated clients:

```go
client, err := core.Bind(ctx, runtimeClient, serviceName, generatedConstructor)
if err != nil {
    return err
}
return client.SomeMethod(ctx, request)
```

Do not call the generated constructor with a hard-coded endpoint. Resolve the
endpoint first. Use `core.Client.Invoke` only for a contract that is not linked
into the caller.

## Adding a CLI

Do not hand-write RPC flags in a subsystem command. Use `pkg/cliapp` and let
`pkg/cli` discover the service through reflection. Add CLI tests for command
names, flags, help comments, request construction, output, and errors.

## Dependencies and modules

The root foundation module is public and reusable. Subsystem modules use local
`replace` directives during repository development. Before publishing an
independent subsystem, replace the local path with a released foundation-module
version and verify its own `go.mod`/`go.sum` in isolation.

Do not add a framework-wide proto file to collect feature messages. Shared
transport concepts belong in Go foundation packages or a deliberately designed
platform subsystem.

## Troubleshooting

### Generated packages are missing

Run the subsystem's `make proto`, not `go test` directly in a fresh checkout.
Generated files are intentionally ignored.

### A standalone subsystem cannot resolve the foundation module

Check its `go.mod` `replace` directive and run:

```sh
go mod tidy
GOWORK=off make test
```

### Reflection lists a service but schema lookup fails

Verify that the service descriptor is linked into the process, the service name
is fully qualified, and the generated handler is mounted. Reflection only
returns services whose descriptors are available to the server.

### A typed call fails before reaching the service

Check the resolver result first. `core.Bind` intentionally does not construct a
client when the service cannot be resolved.
