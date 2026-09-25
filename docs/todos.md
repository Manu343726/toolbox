# TODO and roadmap

Todos are ordered by dependency and user value. `P0` blocks a dependable
end-to-end workflow; `P1` makes the platform production-oriented; `P2` expands
the ecosystem.

## Delivered — MCP foundation

- [x] Add `pkg/mcp` on the official Go MCP SDK.
- [x] Generate unary RPC tools from reflected protobuf descriptors.
- [x] Add JSON Schema input/output generation and protobuf documentation.
- [x] Add always-on list/describe/expose/hide/exposure/documentation tools.
- [x] Add a policy-gated generic `call_rpc` tool.
- [x] Add independent subsystem and aggregated host MCP adapters.
- [x] Add automatic subsystem `mcp` commands and the host `toolsbox mcp`
      command.
- [x] Add in-memory MCP protocol tests and a real `testecho` integration slice.

## P0 — governed workflow execution

### Define run context and event contracts

- [ ] Add a versioned run context containing workspace, actor, run ID, policy
      snapshot, trace ID, and idempotency key.
- [ ] Define run, step, event, approval, and artifact protobuf contracts.
- [ ] Define canonical retry, timeout, cancellation, and idempotency semantics.
- [ ] Add tests for context propagation and replay-safe requests.

**Done when:** a workflow can create a run, execute a step, and produce a
reproducible event/artifact trail.

### Add persistence interfaces

- [ ] Define storage interfaces for workflow definitions, agent profiles,
      skills, prompts, knowledge sources, and runs.
- [ ] Provide SQLite adapters for local deployments.
- [ ] Add migrations, transaction boundaries, and concurrency tests.
- [ ] Keep storage interfaces independent from ConnectRPC handlers.

**Done when:** each subsystem can restart without losing its state and remains
independently deployable.

### Implement workflow execution

- [ ] Define a typed workflow graph/node model or a formally validated graph
      schema.
- [ ] Add sequential and parallel execution.
- [ ] Add conditionals, retries, timeouts, and explicit approval nodes.
- [ ] Resolve agent and tool dependencies through `core.Resolver`.
- [ ] Record step inputs, outputs, versions, and policy decisions.

**Done when:** a workflow can invoke a versioned agent profile through a typed
ConnectRPC client and resume after a controlled failure.

### Enforce policy and approval boundaries

- [ ] Add immutable policy snapshots.
- [ ] Evaluate policy before every tool and mutating capability invocation.
- [ ] Persist approval requests and decisions.
- [ ] Propagate policy context through nested service calls.
- [ ] Prevent prompts or documentation from bypassing policy checks.

**Done when:** an unapproved mutating action cannot reach a tool service.

### Add an auditable run record

- [ ] Store immutable run/step events.
- [ ] Add correlation IDs and trace propagation.
- [ ] Define artifact references and retention rules.
- [ ] Add a run inspection API and CLI output.

**Done when:** an operator can explain which definitions, policies, models,
and tools participated in a run.

## P1 — production runtime

### Model providers

- [ ] Define provider adapter interfaces for text, structured output, embeddings,
      and tool calls.
- [ ] Add provider-specific modules without changing workflow/agent contracts.
- [ ] Add timeouts, token accounting, retries, and provider error mapping.
- [ ] Add deterministic fake providers for tests.

### Knowledge ingestion

- [ ] Add source adapters for files, URLs, and databases.
- [ ] Add chunking, metadata extraction, embeddings, indexes, and reranking.
- [ ] Add source ACLs and policy-aware retrieval.
- [ ] Add ingestion retries and observability.

### Tool execution

- [ ] Add a capability manifest format separate from reflection.
- [ ] Add permission scopes, approval policies, sandbox profiles, and resource
      limits.
- [ ] Add idempotency and cancellation guarantees.
- [ ] Add typed tool results in addition to the reference JSON boundary.

### External services and supervision

- [ ] Add registry bootstrap configuration for standalone deployments.
- [ ] Add external component descriptors and process supervision.
- [ ] Refresh registry entries and descriptors after restarts.
- [ ] Define heartbeat, lease, and stale-service behavior.
- [ ] Add third-party service conformance tests.

### Security

- [ ] Define authentication and authorization metadata.
- [ ] Add mTLS or signed service identity.
- [ ] Add secret references without exposing secret values in requests/logs.
- [ ] Add policy checks to discovery, documentation, registry, and tool paths.

### MCP production hardening

- [ ] Add per-session MCP exposure for Streamable HTTP connections.
- [ ] Persist exposure and tool-footprint policy where appropriate.
- [ ] Add method-level capability, permission, mutating, and approval metadata.
- [ ] Add MCP tool annotations and approval elicitation.
- [ ] Add authentication, authorization, and secret propagation to MCP calls.
- [ ] Add conformance tests against multiple MCP clients.
- [ ] Add streaming MCP tool invocation or an explicit streaming feature
      contract.

### Streaming

- [ ] Add typed client-stream, server-stream, and bidi-stream support to
      `pkg/core`.
- [ ] Add streaming reflection metadata to CLI/tool generation.
- [ ] Add cancellation, backpressure, and partial-failure tests.
- [ ] Keep dynamic streaming invocation explicitly unsupported until its
      contract is designed.

## P1 — developer experience

- [ ] Add `toolsbox registry list/get`.
- [ ] Add `toolsbox service inspect <name>`.
- [ ] Add generated CLI support for nested messages, maps, oneofs, and
      required fields.
- [ ] Add shell completion generated from enum values.
- [ ] Add configuration files for registry endpoint, TLS, and storage.
- [ ] Add structured logs and OpenTelemetry traces.

## P2 — ecosystem

- [ ] Domain-pack packaging and signing.
- [ ] Service capability marketplace/catalog.
- [ ] Scheduler and long-running run recovery.
- [ ] Human approval UI/CLI.
- [ ] Remote deployment adapters.
- [ ] Versioned migration and compatibility tooling.

## Definition of done for every TODO

- The behavior is specified in `docs/feature-spec.md` or the relevant subsystem
  document.
- The implementation has public API documentation.
- The subsystem passes its independent Makefile tests with `GOWORK=off`.
- Shared behavior has integration coverage and race coverage where relevant.
- Protobuf comments and generated documentation are updated.
- `make test`, `make vet`, and `make build` pass.
- The change is committed and pushed to configured remotes.
