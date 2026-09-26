# Subsystem catalog

The repository currently contains sixteen subsystem modules. The
implementations are reference implementations intended to validate contracts and
composition; they are not yet production storage or AI execution engines.

One of them, `logfile`, is a **provider with no contract**: a logging backend has no
operations to address, so it has no command and no binary, and the root `build` skips a
subsystem without a `cmd/` directory. See [`logging.md`](logging.md).

## Feature subsystems

| Subsystem | Service contract | Current responsibility | Programmatic entrypoint | Maturity |
|---|---|---|---|---|
| `workflow` | `toolbox.workflow.v1.WorkflowService` | Store, list, retrieve, and validate versioned workflow definitions | `workflow.New(workflow.Options{})` | Reference CRUD/validation |
| `agent` | `toolbox.agent.v1.AgentService` | Store versioned provider-neutral agent profiles and tool references | `agent.New(agent.Options{})` | Reference CRUD |
| `skill` | `toolbox.skill.v1.SkillService` | Store versioned reusable skills and the names they require | `skill.New(skill.Options{})` | Reference CRUD |
| `prompt` | `toolbox.prompt.v1.PromptService` | Store versioned templates and render simple variables | `prompt.New(prompt.Options{})` | Reference CRUD/rendering |
| `knowledge` | `toolbox.knowledge.v1.KnowledgeService` | Store sources and perform deterministic metadata search | `knowledge.New(knowledge.Options{})` | Reference metadata search |
| `model` | `toolbox.model.v1.ModelService` | List provider-neutral models and invoke a deterministic reference provider | `model.New(model.Options{})` | Reference provider |
| `tool` | `toolbox.tool.v1.ToolService` | Declare tools and invoke explicitly registered local implementations | `tool.New(tool.Options{})` | Reference capability gateway |
| `policy` | `toolbox.policy.v1.PolicyService` | Evaluate simple allow/approval rules by policy ID | `policy.New(policy.Options{})` | Reference evaluator |

## Platform subsystems

| Subsystem | Service contract | Current responsibility | Programmatic entrypoint | Maturity |
|---|---|---|---|---|
| `registry` | `toolbox.registry.v1.RegistryService` | In-memory registration, leases, lookup, filtering, and resolver adapter | `registry.New(registry.Options{})` | Reference control plane |
| `health` | `toolbox.health.v1.HealthService` | Report serving/not-serving state for a component | `health.New(health.Options{})` | Reference health service |
| `documentation` | `toolbox.documentation.v1.DocumentationService` | Serve neutral documentation extracted from protobuf descriptors | `documentation.New(documentation.Options{})` | Reference documentation service |
| `logger` | `toolbox.logger.v1.LoggerService` | A deployment's log fanout, over ConnectRPC, for callers that are not this process | `logger.New(logger.Options{Router: r})` | Reference log fanout |
| `testecho` | `toolbox.testecho.v1.EchoService` | Integration fixture for reflection, typed calls, docs, and CLI | `testecho.New(testecho.Options{})` | Test fixture |

## Provider subsystems

A provider subsystem implements one of the framework's extension contracts. It
owns no feature contract of its own, and it declares what it does by exporting
`[]api.Provider` records — which formats it reads, which representations it
renders, which transports it reaches, which logging backends it provides — so a
deployment can find it without the framework knowing it exists.

A provider subsystem holds no behaviour. The work lives in a reusable root package —
`pkg/protocontract` for protobuf contracts, `pkg/openapi` for OpenAPI documents,
`pkg/mcp` for the Model Context Protocol, and `pkg/log` for the fanout — and the
subsystem mounts it behind a contract so a catalog in another process can reach the
same implementation. A host that runs both in one process uses the package directly
and needs no provider subsystem at all.

A logging provider is the case that goes furthest: its entries never take a network
hop, so it is not behind a contract at all. It declares itself for discovery and is
composed in process. `logfile` therefore has no command, no contract and no binary —
it is a provider, and a deployment names it from its `logging:` section.

| Subsystem      | Contracts served                                        | What it provides                                                                 |
| -------------- | ------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `apitools`     | `toolbox.apitools.v1.ApiToolsService`                    | Repository of servers, registered APIs, the format and transport index, and operation exposure; routes parsing, rendering, serving, and invocation to providers. Also usable in process as `api.Catalog`, `api.Registrar`, `api.Invoker`, and `api.ExposureSource` |
| `apiopenapi`   | `toolbox.api.v1.ApiParserService`, `ApiAdapterService`, `ApiInvokerService` | Parses OpenAPI 3.x documents, renders descriptions into OpenAPI documents, serves an adapted surface with a Swagger UI and a downloadable schema, and invokes operations over HTTP |
| `apigrpc`      | `toolbox.api.v1.ApiParserService`, `ApiInvokerService`   | Parses protobuf contracts from a FileDescriptorSet or from live reflection, and invokes methods over Connect, gRPC, or gRPC-Web |
| `apimcp`       | `toolbox.api.v1.ApiParserService`, `ApiAdapterService`, `ApiInvokerService` | Reads a live MCP server's tool list, renders a description as an MCP tool manifest, and calls a tool |
| `logfile`      | none — it has no operations to address                   | A rotating-file `slog.Handler` through lumberjack, and the `loghandler` declaration that makes it discoverable in the catalog |

`apitools` is a feature subsystem and can be adopted on its own; the providers work
without it, and a deployment can run providers in other processes. The provider
subsystems are optional in the same way: a single-process deployment calls
`pkg/protocontract` and `pkg/openapi` directly, and a distributed one serves them
over the contracts.

## Current RPC surface

### Workflow

- `PutWorkflow`
- `GetWorkflow`
- `ListWorkflows`
- `ValidateWorkflow`

### Agent

- `PutAgent`
- `GetAgent`
- `ListAgents`

### Skill

- `PutSkill`
- `GetSkill`
- `ListSkills`

### Prompt

- `PutPrompt`
- `GetPrompt`
- `ListPrompts`
- `RenderPrompt`

### Knowledge

- `PutSource`
- `GetSource`
- `Search`

### Model

- `ListModels`
- `Generate`

### Tool

- `ListTools`
- `InvokeTool`

### Policy

- `Evaluate`
- `ListPolicies`

### Registry

- `Register`
- `Heartbeat`
- `Deregister`
- `GetService`
- `ListServices`

### Health

- `Check`

### Documentation

- `GetDocumentation`
- `ListDocumentation`

## Foundation packages

| Package | Responsibility |
|---|---|
| `pkg/subsystem` | Server lifecycle, h2c serving, handshake, and reflection mounting |
| `pkg/discovery` | Reflection client, descriptor reconstruction, documentation lookup, dynamic unary calls |
| `pkg/core` | Resolver/client abstraction, metadata propagation, dynamic calls, typed binding |
| `pkg/docs` | Neutral documentation model and source-info descriptor parser |
| `pkg/cli` | Cobra commands and typed flags generated from reflected schemas |
| `pkg/mcp` | MCP servers generated from reflected services, with introspection and exposure control |
| `pkg/cliapp` | Shared standalone command runner |
| `pkg/host` | Explicit composition of subsystem factories |

## Dependency rules

- A feature subsystem may import root foundation packages.
- A feature subsystem may depend on another feature subsystem's contract and call
  it over RPC after resolution. It must not import another subsystem's
  implementation and call it in-process.
- The combined host may import all built-in modules to compose them.
- Runtime dependencies are represented by registry service references and declared
  dependencies, not Go imports.
- A third-party service can replace a built-in service if it exposes the same
  contract and metadata through ConnectRPC/reflection/registry.
- `pkg/cliapp` adds an `mcp` command to each standalone subsystem automatically.
  The combined host adds `toolbox mcp` for an aggregated MCP.
- MCP feature exposure is separate from reflection: only policy-allowed unary
  methods can become generated tools or be reached through `call_rpc`.
