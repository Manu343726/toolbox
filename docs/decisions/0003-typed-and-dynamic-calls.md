# ADR-0003: Typed-first service calls with dynamic interoperability

- **Status:** Accepted
- **Date:** 2026-09-25

## Context

First-party subsystems benefit from compile-time request/response safety, while
third-party services may expose contracts that are not linked into the caller.
A framework that supports only one style is either too coupled or too weak.

## Decision

Provide both paths in the public `pkg/core` API:

- `core.Bind` resolves a service and constructs a generated ConnectRPC client
  supplied by the caller. Known service calls remain type-safe.
- `core.Client.Invoke` and `InvokeJSON` use reflection and dynamic messages for
  unknown unary contracts.

Resolution always precedes client construction or invocation. A failed
resolution is an error, not a fallback to a hard-coded endpoint.

## Consequences

- Built-in callers get generated request/response types and compiler checks.
- External services can participate without a compile-time dependency.
- The core package remains independent of feature protobufs.
- Dynamic streaming is not supported until a streaming-specific contract and
  client design exists.
- Metadata and policy propagation must be implemented for both call paths.

## Rejected alternatives

- Dynamic calls for all services: rejected because they discard type safety.
- Generated clients with hard-coded URLs: rejected because they bypass runtime
  composition and registry availability.
- A generic string-based tool dispatcher as the primary API: rejected because
  it weakens validation and makes schema evolution risky.
