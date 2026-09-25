# ADR-0002: Separate registry resolution from reflection

- **Status:** Accepted
- **Date:** 2026-09-25

## Context

A client needs to know both where a service runs and what its protobuf contract
contains. gRPC reflection can describe a known endpoint, but it does not locate
an endpoint across a distributed deployment.

## Decision

Use two explicit mechanisms:

- `toolsbox.registry.v1.RegistryService` resolves subsystem/service names to
  endpoints, capabilities, versions, and leases.
- gRPC reflection v1/v1alpha describes services and descriptors at a resolved
  endpoint.

The common server mounts reflection. The registry remains an independent
subsystem. Discovery caches descriptors but does not use reflection as an
authorization mechanism.

## Consequences

- A service can be unavailable at the endpoint-resolution step or the RPC step,
  producing distinct and useful errors.
- External services need registration metadata and reflection but no special
  plugin SDK.
- Registry and reflection contracts can evolve independently.
- A future registry can use DNS, static configuration, or a remote control
  plane without changing callers.

## Rejected alternatives

- Reflection-only discovery: rejected because it cannot locate a service.
- A hard-coded endpoint map: rejected because it breaks dynamic composition.
- Treating every reflected method as a tool: rejected because schema visibility
  is not permission or side-effect metadata.
