# 5. Reusable packages behind thin provider subsystems

Status: accepted<br>
Date: 2026-09-25
Supersedes nothing. Extends [ADR-0004](0004-api-introspection-and-providers.md).

## Context

ADR-0004 introduced the standard API description and three extension contracts, and
shipped two providers: an OpenAPI provider and a protobuf-contract provider. Both
were written as subsystems first. All of their behaviour — reading a document,
rendering a document, serving a surface, calling an operation — lived in the
subsystem module, and the subsystem's only job was to expose that behaviour over
ConnectRPC.

That works for a catalog in another process. It is the wrong shape for the
framework's own use, because the framework needs this behaviour in process:

- A host that describes its own subsystems needs to read a contract from a local
  endpoint. Reaching for a parser subsystem to ask it to reflect on a sibling in
  the same process is three hops for something that is one function call.
- An MCP gateway that builds its surface from a catalog needs to *call* what it
  describes. Routing that call out to an invoker subsystem and back is the same
  detour.
- A test that wants a round trip through two formats needs two module wiring
  changes and a workspace, when it should need two imports.

The tell was the test fixtures. Exercising the parser needed a subsystem module to
point at, so the root packages were tested with a fabricated endpoint and the
subsystems were tested with a real one. That inversion is the smell: the package
that holds the logic could not be tested against reality without a deployment
around it.

## Decision

**Behaviour lives in reusable root packages. Subsystems are the addressable form of
a package, and nothing else.**

- `pkg/protocontract` reads a protobuf service contract — from a FileDescriptorSet
  or from a live endpoint's reflection — into the standard description, and calls
  the operations of such a contract.
- `pkg/openapi` reads, renders, serves, and calls APIs described by OpenAPI
  documents.
- `subsystems/apigrpc` and `subsystems/apiopenapi` mount those packages behind the
  framework's contracts. They declare capabilities, publish descriptors, and
  convert messages. They contain no translation logic.

Both paths go through the same code, so they cannot disagree: the subsystem is a
wrapper, not a second implementation.

### What a subsystem is for

A subsystem earns its place when its contract has to be *addressable*: a catalog in
another process, or a deployment whose providers run separately from the features
they serve. A subsystem that only ever runs in the same process as its caller is a
process boundary drawn for no reason, and it costs a module, a Makefile, a command,
and a deployment decision to keep.

The rule that follows: a provider subsystem is the *form* of a reusable package, and
a package is the substance. Neither is allowed to grow a second copy of the other.

### Failures are classified, not phrased

A package that knows nothing about any transport cannot say "InvalidArgument",
because that is a transport's word. It says what went wrong — a classification the
framework defines in `api.ErrorKind` — and the transport layer maps the
classification to its own codes.

Without this, splitting a package out of a subsystem silently degrades every error
into "internal", and the codes a caller sees stop meaning anything. The
classification also makes messages better: "no credential is available" is a
deployment gap and "no credential for scheme X" is a request mistake, and the two
deserve different codes.

### Automatic exposure

With the logic in packages, a host can register its own subsystems in the API
catalog directly:

1. Read each started subsystem's contract from the subsystem itself, through
   `protocontract`.
2. Attach the capabilities the subsystem's manifest declared for a service. A
   contract cannot state them — a protobuf contract knows which methods exist and
   nothing about what a deployment authorizes — so the join happens at registration.
3. Store the description, and expose the operations a declared capability covers.

Nothing is written per subsystem, and nothing is exposed that no capability covers.
A subsystem whose services are all platform plumbing is skipped, with a reason.

The MCP gateway can then build its surface from that catalog instead of from
reflection. Both surfaces name the same operations identically, so switching the
source of the surface renames nothing an agent already uses; what the catalog adds is
per-operation exposure, the ability to re-publish a description in another format,
and registrations that survive a gateway restart.

### A provider is bound by identity

Several subsystems serve the same contract on purpose: serving one is how a format
or a transport is contributed. A client bound by service name is therefore
ambiguous the moment a second provider appears, and it silently reaches whichever
endpoint claimed the name first — a wrong-provider bug that looks like a
wrong-request bug. Provider clients are bound by provider identifier, using the
address the provider's own record carries, and the resolver is a fallback for a
deployment that places providers elsewhere.

## Consequences

- The framework's own API integration needs no provider subsystem at all, so a
  single-process deployment gets it with nothing configured.
- A third-party format is a package plus, optionally, a subsystem. The package is
  what a host embeds; the subsystem is what a distributed deployment serves.
- `api.Registrar`, `api.Catalog`, `api.Invoker`, and `api.ExposureSource` are the
  framework's Go interfaces, and a composition wires them directly. The ConnectRPC
  contracts remain for the distributed case, and the catalog exposes both.
- Tests for a provider's behaviour live with the package and run against real
  endpoints, because a real endpoint is now one function call away.
- The failure classification is a public contract. A transport that ignores it loses
  the distinction between "the caller sent something wrong" and "the deployment is
  misconfigured", so mapping it is not optional for a provider subsystem.
