# Toolbox feature specification

## 1. Product definition

Toolbox is a provider-neutral framework for building AI-assisted workflows.
It lets a user compose domain rules, knowledge, prompts, skills, agents, models,
and tools into a working environment without binding the workflow to one model
provider or one deployment topology.

The framework is useful when work must be repeatable, governed by domain rules,
and executed by more than one agent or service. Target users include:

- software developers and platform engineers;
- system administrators and operations teams;
- security and compliance teams;
- teams building internal AI tooling;
- authors of domain-specific workflow packs.

## 2. Goals

### Primary goals

1. Make workflows composable from independent services.
2. Keep domain rules explicit, versioned, and enforceable outside prompts.
3. Make service contracts provider-neutral and independently deployable.
4. Allow built-in and third-party services to participate through the same
   ConnectRPC/reflection/registry model.
5. Provide a portable environment containing definitions, skills, prompts,
   knowledge references, policies, and capability declarations.
6. Make the runtime observable, testable, and replaceable at subsystem
   boundaries.

### Non-goals for the foundation

- A universal model-provider SDK.
- A visual workflow editor.
- A hosted control plane.
- A general-purpose plugin package manager.
- An implicit security model based only on prompt text.
- Automatic exposure of every reflected RPC as an agent tool.

## 3. Design principles

### Independence

A subsystem owns its protobuf contract, implementation, state, command, and
lifecycle. Feature packages do not import one another. They communicate through
ConnectRPC and runtime discovery.

### Explicit composition

A workflow refers to services, capabilities, skills, prompts, knowledge sources,
and policies by stable identifiers. It does not embed provider-specific client
objects or hard-coded process addresses.

### Typed first, dynamic when necessary

If a contract is linked into a caller, the caller should construct the generated
ConnectRPC client. Reflection-based dynamic invocation is for external or
otherwise unknown contracts. Reflection is schema discovery, not authorization.

### Policy at the boundary

Capabilities declare side effects and permissions. A policy service evaluates
whether an action is allowed and whether approval is required. Prompts and
documentation are context, not security controls.

### Reproducibility

Workflow definitions, agent profiles, skills, prompts, model selections, and
policy references are versioned. A run records the versions and context needed
to explain or reproduce its behavior.

## 4. Domain model

### Domain pack

A domain pack is a portable bundle of definitions and references:

```text
domain-pack/
├── workflows/
├── agents/
├── skills/
├── prompts/
├── knowledge/
└── policies/
```

The initial storage format is not finalized. The important contract is that a
pack can be loaded without importing a provider-specific Go package.

### Workflow definition

A workflow is a versioned, provider-neutral graph. It declares inputs, nodes,
dependencies, policies, and outputs. Nodes refer to capabilities or services;
they do not contain provider-specific SDK calls.

The current reference service accepts a JSON graph envelope and validates its
basic JSON object shape. A future typed graph schema is a planned evolution.

### Agent profile

An agent profile declares:

- identity and version;
- instruction or prompt references;
- skill references;
- tool capability references;
- knowledge source references;
- policy references.

The profile does not select a provider-specific model implementation. Model
selection belongs to the model service and run configuration.

### Skill

A skill is a reusable instruction and capability contract. It may require
specific tools and policies. Skills are versioned and independently addressable.

### Prompt

A prompt is a provider-neutral template with declared variables. The reference
implementation performs simple `{{variable}}` substitution.

### Knowledge source

A knowledge source identifies a document collection or ingestion input. The
reference implementation stores source metadata and performs deterministic
metadata search. Production ingestion, embeddings, vector indexes, and reranking
are behind the same service contract.

### Capability and tool

A capability is a semantic operation. A tool is a capability exposed through a
service. Tool metadata includes description, input schema, required permission,
and whether invocation is mutating.

Reflection may discover a tool's RPC schema, but a manifest or policy layer must
explicitly declare whether an agent may invoke it.

### Policy snapshot

A policy snapshot is an immutable versioned rule set. A run records the policy
snapshot used for every consequential action. The reference policy service
supports simple allow/approval decisions; the production rule language is not
finalized.

## 5. Runtime requirements

### Service lifecycle

Every subsystem must be able to:

1. Construct its server programmatically.
2. Start on a configured or random loopback endpoint.
3. Mount its own ConnectRPC services.
4. Expose gRPC reflection.
5. Report health and registration metadata.
6. Shut down gracefully.
7. Run without the combined host.

### Endpoint discovery

The registry resolves a service name to an endpoint. Reflection then describes
the methods and messages at that endpoint. The two operations must remain
separate.

### Typed calls

For a known contract:

1. Resolve the fully-qualified service name.
2. Fail if resolution is unavailable.
3. Construct the generated client with the resolved endpoint.
4. Invoke generated methods with generated request and response types.

For an unknown contract:

1. Resolve an endpoint.
2. Fetch and cache its reflection descriptor.
3. Invoke supported unary methods dynamically.
4. Reject unsupported streaming behavior clearly.

### Context propagation

Calls should propagate request, trace, run, actor, workspace, and policy
metadata. Authentication and authorization metadata must not be smuggled through
untyped request fields.

### MCP exposure

Every ConnectRPC service can be projected into an MCP server. Reflection
supplies the service schema, documentation, and unary invocation mechanics.
An explicit feature policy supplies authorization. The MCP surface must
support:

1. generated tools for allowed unary methods;
2. introspection tools for listing services and features;
3. feature documentation and schema inspection;
4. runtime expose/hide operations that change the tool surface;
5. a generic `call_rpc` tool subject to the same policy and exposure gate;
6. independent per-service MCPs and one aggregated MCP over discovered
   services;
7. automatic `mcp` subcommands on standalone subsystem commands and the
   combined host.

## 6. Functional requirements

| ID | Requirement | Current state |
|---|---|---|
| F-001 | Independent subsystem modules and local proto files | Implemented |
| F-002 | ConnectRPC serving and gRPC reflection | Implemented |
| F-003 | Registry registration, leases, lookup, and resolution | Reference implementation |
| F-004 | Typed service-to-service binding | Implemented |
| F-005 | Dynamic unary invocation for unknown services | Implemented |
| F-006 | Protobuf documentation extraction | Implemented |
| F-007 | Schema-driven CLI generation | Implemented for unary methods |
| F-008 | Workflow definition storage and validation | Reference implementation |
| F-009 | Agent, skill, prompt, and knowledge catalogs | Reference implementation |
| F-010 | Model-provider abstraction | Reference catalog/provider |
| F-011 | Tool capability catalog and invocation boundary | Reference implementation |
| F-012 | Policy evaluation and approval signal | Reference implementation |
| F-013 | Combined one-subsystem/all-subsystem host | Implemented |
| F-014 | Production persistence and migrations | Not implemented |
| F-015 | Streaming client invocation | Not implemented |
| F-016 | Authentication, authorization, and secret management | Not implemented |
| F-017 | MCP generation from reflected ConnectRPC services | Implemented for unary methods |
| F-018 | MCP introspection and runtime feature exposure | Implemented |
| F-019 | Independent and aggregated MCP deployment | Implemented |
| F-020 | Session-isolated MCP exposure over HTTP | Not implemented |

## 7. Quality requirements

- Public APIs are documented and typed.
- Every feature has unit tests for success, validation, not-found, duplicate, and
  lifecycle behavior as applicable.
- Shared concurrency and lifecycle code passes race tests.
- Independent subsystem tests pass with the Go workspace disabled.
- Generated artifacts are reproducible from proto and Makefiles.
- Errors use canonical ConnectRPC status codes.
- No provider-specific types leak into provider-neutral feature contracts.
- MCP feature exposure is concurrency-safe and does not bypass the feature
  policy through the generic RPC tool.
- MCP schema generation and introspection are covered by in-memory protocol
  tests and a real reflection integration test.

## 8. Acceptance criteria for the foundation milestone

The foundation is complete when:

- a new subsystem can be added without modifying another feature package;
- its proto, generated clients, tests, Makefile, and command are self-contained;
- reflection and registry resolution work against a live endpoint;
- a known service can be called with a generated client after resolution;
- an unresolved service fails before typed client construction;
- the CLI can generate unary commands from reflection and documentation;
- the combined host can launch one or all built-in subsystems;
- a standalone subsystem can launch an independent MCP and the host can launch
  an aggregated MCP;
- MCP introspection can list, document, expose, hide, and call allowed unary
  features;
- all subsystem tests and root checks pass.

The current repository satisfies this foundation milestone. Production workflow
execution and governed agent runs are the next product milestone.
