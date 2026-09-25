# ADR-0001: Independent subsystem modules

- **Status:** Accepted
- **Date:** 2026-09-25

## Context

Toolbox needs composable AI workflows whose services can be developed,
tested, deployed, and replaced independently. A single aggregate Go package or
framework-wide proto would make feature changes couple unrelated services and
would make external replacement difficult.

## Decision

Every feature is an independent subsystem under `subsystems/<name>/` with its
own Go module, Makefile, local proto contract, generated package,
implementation, tests, and command. Feature implementation packages do not import
one another. The combined host imports factories only for composition.

## Consequences

- A subsystem can be tested and built independently.
- Contracts remain close to their implementation and documentation.
- Cross-feature dependencies are runtime RPC dependencies and must be explicit.
- A root workspace and combined host are needed for local integration testing.
- Module publishing requires a stable foundation-module version and replacement
  of local `replace` directives.

## Rejected alternatives

- One aggregate `platform.proto`: rejected because it couples unrelated feature
  contracts and makes independent versioning difficult.
- Feature packages importing one another: rejected because it turns runtime
  composition into compile-time coupling.
- A plugin-only model with a shared global registry object: rejected because it
  obscures service boundaries and makes external implementations harder.
