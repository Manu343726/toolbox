# Current development status

**Status date:** 2026-09-25<br>
**Branch:** `main`<br>
**Pre-MCP implementation baseline:** `58f5f46 docs: add project agent guidelines and skills`<br>
**MCP implementation:** added in the current change

## Product goal

Toolbox is the foundation for a fully AI-assisted working environment: shared
knowledge with retrieval, governed external tool calls, multi-agent definition
and coordination, and versioned workflow definition. Rules, skills, prompts,
knowledge, tools, and policies are authored once as versioned assets and reused
by every agent, workflow, and model.

## Summary against that goal

| Foundation                        | State today                                                          | Remaining                                                                 |
| --------------------------------- | -------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| Knowledge base and retrieval      | Knowledge sources and deterministic metadata search                 | Ingestion, chunking, embeddings, indexes, reranking, source ACLs           |
| External tool calls               | Declared tool capabilities and policy-gated invocation               | Real capability manifests, permission scopes, sandboxing, typed results    |
| Multi-agent definition and coordination | Versioned agent profiles with capability references             | Coordination contract, handoffs, shared-context rules, escalation, loop guards |
| Workflow definition               | Versioned workflow definitions with validation                       | Execution, branching, retries, approvals, auditable run record            |

The supporting framework is green and independently buildable; the capability
services are reference implementations, and the product pillars above are the
P0/P1 roadmap.

## Delivered

### Architecture and tooling

- Separate Go module and Makefile for every subsystem.
- Subsystem-owned proto files and generated ConnectRPC clients.
- Public foundation packages for serving, discovery, core calls,
  documentation, CLI generation, command running, and host composition.
- `go.work` workspace for local multi-module development.
- Combined `cmd/toolbox` host for one-subsystem and all-subsystem modes.
- Project `AGENTS.md` and five task-specific skills under `.agents/skills/`.

### Project documentation

- Documentation index and feature specification.
- Expanded architecture and subsystem catalog.
- Development, testing, and protocol conventions.
- MCP gateway design and deployment documentation.
- Current status, prioritized roadmap, and accepted architecture decisions.

### Runtime foundation

- ConnectRPC handler mounting.
- gRPC reflection v1/v1alpha.
- Machine-readable subsystem handshake metadata.
- Registry registration, leases, lookup, and endpoint resolution.
- Dynamic unary invocation for unknown external services.
- Type-safe generated client binding through `core.Bind`.
- Metadata propagation for request, trace, actor, run, workspace, and policy
  context.
- Source-info descriptor documentation extraction.
- Schema-driven Cobra CLI generation.
- `pkg/mcp` MCP gateway built on the official Go MCP SDK.
- Runtime feature exposure, documentation introspection, and generic `call_rpc`.
- Independent subsystem MCP commands and an aggregated host `mcp` command.
- Graceful server startup and shutdown.

### Reference subsystems

Twelve subsystem modules exist:

```text
agent
documentation
health
knowledge
model
policy
prompt
registry
skill
testecho
tool
workflow
```

They provide versioned in-memory stores, validation, metadata search, simple
rendering, capability declarations, policy decisions, and a deterministic model
provider. They are useful for integration and as executable contract examples.

## Validation

The following checks passed after the current implementation:

```sh
make check-tests
make test
make test-short
make vet
make build
go test -race ./...
go test -race ./pkg/mcp
GOWORK=off make -C subsystems/agent test
```

The test suite includes unit tests for every subsystem and integration tests
for reflection, documentation, typed calls, dynamic calls, CLI generation, and
combined host registration.

## Known limitations

- No durable storage or migration framework exists yet.
- Workflow execution is not implemented; workflows are stored and validated.
- Agent profiles do not yet execute model calls.
- The model service has a deterministic reference provider only.
- Knowledge search does not yet perform ingestion, embeddings, vector search,
  or reranking.
- Tool invocation is a local reference boundary, not a sandboxed execution
  system.
- Policy rules and approval signaling are intentionally minimal.
- Dynamic invocation supports unary methods only; streaming invocation is
  rejected until a streaming client exists.
- MCP exposure is implemented for unary methods; HTTP exposure is currently
  process-wide rather than session-isolated.
- MCP exposure is in-memory and is not persisted across restarts.
- External process supervision, remote registry bootstrap, authentication,
  authorization, secret management, and TLS policy are not complete.
- Generated artifacts require a Makefile target in a fresh checkout.

## Delivery status

The foundation milestone is complete. The next milestone is a governed
end-to-end workflow: persist a domain pack, execute a workflow through a
versioned policy snapshot, call an agent through a typed client, retrieve
knowledge, invoke an approved tool, and emit an auditable run record.
