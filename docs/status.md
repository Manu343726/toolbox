# Current development status

**Status date:** 2026-09-25<br>
**Branch:** `main`<br>
**Implementation baseline:** `58f5f46 docs: add project agent guidelines and skills`

## Summary

The repository has a working foundation for independently deployable
ConnectRPC subsystems. The foundation is green and independently buildable;
the feature services are currently reference implementations.

## Delivered

### Architecture and tooling

- Separate Go module and Makefile for every subsystem.
- Subsystem-owned proto files and generated ConnectRPC clients.
- Public foundation packages for serving, discovery, core calls,
  documentation, CLI generation, command running, and host composition.
- `go.work` workspace for local multi-module development.
- Combined `cmd/toolsbox` host for one-subsystem and all-subsystem modes.
- Project `AGENTS.md` and four task-specific skills under `.agents/skills/`.

### Project documentation

- Documentation index and feature specification.
- Expanded architecture and subsystem catalog.
- Development, testing, and protocol conventions.
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
- External process supervision, remote registry bootstrap, authentication,
  authorization, secret management, and TLS policy are not complete.
- Generated artifacts require a Makefile target in a fresh checkout.

## Delivery status

The foundation milestone is complete. The next milestone is a governed
end-to-end workflow: persist a domain pack, execute a workflow through a
versioned policy snapshot, call an agent through a typed client, retrieve
knowledge, invoke an approved tool, and emit an auditable run record.
