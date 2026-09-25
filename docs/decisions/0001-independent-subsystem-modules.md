# ADR-0001: Independent subsystem modules

- **Status:** Accepted, amended by
  [ADR-0009](0009-cross-subsystem-calls.md) (feature packages may depend on a
  peer's *contract*; they may not import a peer's *implementation*)
- **Date:** 2026-09-25

## Context

Toolbox needs composable AI workflows whose services can be developed,
tested, deployed, and replaced independently. A single aggregate Go package or
framework-wide proto would make feature changes couple unrelated services and
would make external replacement difficult.

## Decision

Every feature is an independent subsystem under `subsystems/<name>/` with its
own Go module, Makefile, local proto contract, generated package,
implementation, tests, and command. A feature may depend on a peer's generated
contract and call it over RPC; it does not import a peer's implementation and call
it in-process. The combined host imports factories only for composition.

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
- Feature packages importing a peer's *implementation*: rejected because it turns
  runtime composition into compile-time coupling and links two lifecycles into one
  process. Importing a peer's generated contract was wrongly rejected with it; see
  [ADR-0009](0009-cross-subsystem-calls.md).
- A plugin-only model with a shared global registry object: rejected because it
  obscures service boundaries and makes external implementations harder.
