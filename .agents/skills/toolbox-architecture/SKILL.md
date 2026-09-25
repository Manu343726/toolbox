---
name: toolbox-architecture
description: Design and review independent Toolbox subsystems, module boundaries, runtime composition, and service-to-service dependencies.
---

# Toolbox architecture

Use this skill when adding a subsystem, changing the host, introducing a
runtime dependency, or reviewing whether a design preserves composability.

## Boundary checklist

- Put the subsystem under `subsystems/<name>/` with its own `go.mod`,
  `Makefile`, `proto/`, implementation package, and `cmd/<name>/`.
- Keep one subsystem's feature contract in its own proto file.
- Do not import another feature subsystem from implementation code.
- Put reusable behavior in a public root package such as `pkg/core`,
  `pkg/discovery`, `pkg/subsystem`, `pkg/docs`, or `pkg/cli`.
- Represent cross-subsystem dependencies as capabilities or service references,
  not Go package references.

## Runtime checklist

- Expose ConnectRPC handlers and gRPC reflection.
- Resolve endpoints through a `core.Resolver`.
- Use generated clients with `core.Bind` for known contracts.
- Use dynamic discovery only when the contract is not linked.
- Fail clearly when a required service is unavailable.
- Make service metadata, capabilities, side effects, and dependencies explicit.
- Ensure the subsystem can run standalone without the combined host.

## Review questions

1. Can this subsystem compile and test independently?
2. Can an external implementation replace it without changing callers?
3. Are endpoint discovery and schema reflection distinct?
4. Does a typed call remain type-safe after resolution?
5. Are policy and approval boundaries outside prompts and descriptions?
6. Are generated artifacts excluded from the module's source boundary?
7. Are all state transitions and failure paths tested?
