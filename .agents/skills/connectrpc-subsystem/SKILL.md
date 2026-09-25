---
name: connectrpc-subsystem
description: Implement and maintain a standalone ConnectRPC subsystem with local protobuf contracts, generated handlers, Makefiles, and commands.
---

# ConnectRPC subsystem workflow

Use this skill for changes under `subsystems/<name>/`, protobuf edits, service
handlers, generated clients, or standalone commands.

## Start

1. Read `AGENTS.md` and `docs/architecture.md`.
2. Read the subsystem's proto and implementation.
3. Run the subsystem's existing `make test` before changing behavior when
   possible.
4. Load the protobuf design rules before modifying a `.proto` file.

## Implement

- Keep the proto in `subsystems/<name>/proto/`.
- Use a versioned protobuf package and a `go_package` inside the subsystem
  module.
- Document every RPC, message, field, enum, and enum value.
- Implement handlers behind the subsystem package's public programmatic
  entrypoint.
- Keep the command thin: it should call the shared `pkg/cliapp` runner or a
  similarly generated CLI path, not define RPC-specific Cobra commands.
- Use `subsystem.Config`/`subsystem.Server` for lifecycle, h2c serving, and
  reflection.

## Generate and test

From the subsystem directory:

```sh
make proto
make test
make build
```

From the repository root:

```sh
make test
make vet
```

The Makefile generates Go, ConnectRPC, and a descriptor set with source info.
Never hand-edit or commit generated files.

## Error and lifecycle rules

- Validate request fields before touching state.
- Use ConnectRPC status codes rather than opaque string errors.
- Propagate context cancellation and deadlines.
- Do not silently ignore descriptor or registration errors.
- Test invalid, not-found, duplicate, expiry, and shutdown paths.
- For streaming methods, preserve streaming metadata and test the streaming
  behavior explicitly.
