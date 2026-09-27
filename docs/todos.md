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
- [x] Add automatic subsystem `mcp` commands and the host `toolbox mcp`
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
      skills, prompts, knowledge bases, and runs.
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

### Define multi-agent coordination

- [ ] Add a coordination contract declaring agent roles, permitted handoffs, and
      the artifact each handoff carries.
- [ ] Add shared-context rules: which knowledge bases, skills, and tool capabilities
      transfer on a handoff and which stay private.
- [ ] Add escalation targets for approval, failure, and ambiguity.
- [ ] Evaluate the receiving profile's reach and the active policy snapshot
      before a handoff proceeds.
- [ ] Record every handoff in the run trail with the profile and workflow
      versions involved.
- [ ] Guard against coordination loops and unbounded delegation depth.

**Done when:** a workflow can route work between two or more versioned agent
profiles, with a recorded, policy-checked handoff and no ad-hoc messaging
outside it.

## P1 — production runtime

### Model providers

- [ ] Define provider adapter interfaces for text, structured output, embeddings,
      and tool calls.
- [ ] Add provider-specific modules without changing workflow/agent contracts.
- [ ] Add timeouts, token accounting, retries, and provider error mapping.
- [ ] Add deterministic fake providers for tests.

### Knowledge base

Deliberately not tracked as a checklist. The subsystem is specified in full in
[`knowledge.md`](knowledge.md) and that specification is the plan; a checklist
that paraphrases it would be a second thing to keep in step with the first. It is
implemented — the contract, the provider, and the corpus engine behind it — so what
is left is what the specification itself records as open.

- [ ] Add a second backend as a second mount of `pkg/knowledge`. The rule is
      load-bearing here: the subsystem holds no logic, so a second provider is a
      second adapter rather than a second subsystem. Nothing in the design prevents
      it and nothing in the tree yet demonstrates it — and the `Content`
      conversion, which was the one place a second backend would have found
      duplication, now goes through the domain.
- [ ] Decide the §15 open questions this implementation did not settle.
- [ ] The subsystem package is at 61%. Most of the remainder is the projection, the registry, the
      state store and the git plumbing, all of which the subsystem tests reach only indirectly.
      The domain underneath is at 90.9% with no function untested, so this is about which
      subsystem-level paths are worth a test rather than about a hole in the design.

Both of the following were gaps recorded here and are now closed. They are
listed because a reader comparing this file with the specification will find the
specification's account of them, and because "closed" is a claim worth being able
to check:

- **The compound trigger.** `PageTrigger` now carries `tag_groups` as the
  boolean expression the backend's own filter is, alongside the flat `tags`
  shorthand — and refuses both at once, because two filters over one input with
  no combining rule is a filter whose meaning depends on which field a reader
  looked at. `knowledge.TagFilter` carries the shape and its rules.
- **The `Content` conversion.** `pkg/knowledge` builds the domain `Content` and
  the provider converts it in one place. Doing so surfaced a disagreement that
  had been there all along: `ListContent` and `GetContent` reported different
  mutability for the same file.

Not to be done, and recorded so it is not proposed again: a second, in-memory
reference provider. The contract is the deliverable and one honest provider beats
two, one of which is a fiction.

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

- [ ] Add `toolbox registry list/get`.
- [ ] Add `toolbox service inspect <name>`.
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
