# 4. API introspection and provider subsystems

Status: accepted<br>
Date: 2026-09-25

## Context

The framework originally spoke one description language: protobuf. Reflection
described a ConnectRPC service, and the MCP gateway turned reflected methods into
tools. That worked, but it made protobuf the framework's assumption rather than
one of its formats. A user with an OpenAPI service, or one with an internal IDL,
had two choices: ask the framework to grow a special case, or hand-write an MCP
wrapper that drifts from the real contract.

The same problem recurred on the way out. There was no way to publish an API in a
format it was not written in, and no way to serve a described API in another
format.

## Decision

Introduce a standard, provider-neutral description of an API — `pkg/api` — and
make every format and transport an *open identifier* rather than a case in an
enumeration. The framework then carries no built-in format at all, and the two
built-in providers are ordinary subsystems that happen to exist.

### The standard description

`pkg/api` defines `API`, `Service`, `Operation`, `Schema`, `Server`,
`FormatDescriptor`, and `TransportDescriptor`. The unit of exposure is the
operation, because a tool, a permission, and an approval decision all name an
operation.

Identifiers are derived — an operation's id is built from its API, service, and
operation names — so a description cannot claim an identity it does not have, and
so the same operation has the same id whichever language described it.

### Open identifiers, indexed descriptors

Format and transport are strings, not enums. Two reasons:

1. An enumeration is a closed set. A user adding a format would need a framework
   change, which is the coupling this work exists to remove.
2. Discovery needs to answer "which formats can this deployment parse?" for
   formats the framework has never heard of. A catalog answers that by indexing
   `FormatDescriptor` and `TransportDescriptor` records contributed by the
   providers themselves.

The only enumerations that remain are the JSON value types, which JSON defines,
and a registered server's lifecycle status, which the framework's registry
defines.

### Three extension contracts

The framework exposes three contracts in `pkg/api/proto/api.proto`, and each is
implemented by a subsystem:

| Contract                | Direction                                  | Claimed by                    |
| ----------------------- | ------------------------------------------ | ------------------------------ |
| `ApiParserService`      | description document → standard description | `api.parse.<format>`           |
| `ApiAdapterService`     | standard description → target representation | `api.render.<target>`          |
| `ApiInvokerService`     | operation + server → live response         | `api.invoke.<transport>`       |

An adapter is direction-agnostic: it reads the standard description and does not
care which format the description was parsed from. Rendering a protobuf contract
as an OpenAPI document and rendering an OpenAPI document as a protobuf contract
are the same operation.

An adapter has two faces, and both are required for the feature to be useful:

- `RenderApi` returns the target's schema files. Translation only.
- `ServeApi` serves that target's surface and tunnels requests to the original
  server. The provider receives the original server as well as the description:
  the description says what the surface looks like, and the server says where the
  work happens.

The OpenAPI provider's served surface reserves paths under its base prefix for
the target's own documentation and schema, and refuses a base path that would
collide with a path it generates. The schema is downloadable from the served
surface in both JSON and YAML.

### Discovery through capabilities

A provider subsystem advertises what it can do through the capabilities it
already declares, and the host derives the provider set from them. A catalog
therefore needs no knowledge of any provider subsystem, and adding support for a
format is adding a subsystem.

### The catalog

`subsystems/apitools` stores the servers, the registered API descriptions, the
indexed format and transport descriptors, and the exposure state. It parses
nothing and invokes nothing itself; it routes to providers through a directory
that the deployment supplies.

Exposure is the catalog's product surface. An operation is allowed when it, its
service, or its API declares a capability, so registering an API never silently
exposes it. Only allowed, unary, server-bound operations can be exposed.

## Consequences

- A user integrates a new API format by writing one subsystem implementing
  `ApiParserService`, and a new transport by writing one implementing
  `ApiInvokerService`. Neither needs a framework change.
- A user publishes an API in a format they did not write it in by writing a
  subsystem implementing `ApiAdapterService`.
- A gRPC contract can be published as an OpenAPI document, documented with a
  Swagger UI, and downloaded — all by composing a parser with an adapter.
- The MCP gateway reads the same standard description, so a registered API's
  operations become agent tools through the same exposure rules as reflected
  methods.
- The framework contract lives in the root module rather than in a subsystem,
  because a third-party provider must be able to implement it without importing a
  feature subsystem. This is the one place a protobuf contract is not
  subsystem-owned, and it is a protocol, not a feature.
- The `apitools` subsystem is a feature like any other: the host may run it, or a
  deployment may not, and the providers work without it.
