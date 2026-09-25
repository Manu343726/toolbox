# Subsystem catalog

The repository currently contains twelve independent subsystem modules. The
implementations are reference implementations intended to validate contracts and
composition; they are not yet production storage or AI execution engines.

## Feature subsystems

| Subsystem | Service contract | Current responsibility | Programmatic entrypoint | Maturity |
|---|---|---|---|---|
| `workflow` | `toolsbox.workflow.v1.WorkflowService` | Store, list, retrieve, and validate versioned workflow definitions | `workflow.New(workflow.Options{})` | Reference CRUD/validation |
| `agent` | `toolsbox.agent.v1.AgentService` | Store versioned provider-neutral agent profiles and capability references | `agent.New(agent.Options{})` | Reference CRUD |
| `skill` | `toolsbox.skill.v1.SkillService` | Store versioned reusable skills and required capability/policy references | `skill.New(skill.Options{})` | Reference CRUD |
| `prompt` | `toolsbox.prompt.v1.PromptService` | Store versioned templates and render simple variables | `prompt.New(prompt.Options{})` | Reference CRUD/rendering |
| `knowledge` | `toolsbox.knowledge.v1.KnowledgeService` | Store sources and perform deterministic metadata search | `knowledge.New(knowledge.Options{})` | Reference metadata search |
| `model` | `toolsbox.model.v1.ModelService` | List provider-neutral models and invoke a deterministic reference provider | `model.New(model.Options{})` | Reference provider |
| `tool` | `toolsbox.tool.v1.ToolService` | Declare tools and invoke explicitly registered local implementations | `tool.New(tool.Options{})` | Reference capability gateway |
| `policy` | `toolsbox.policy.v1.PolicyService` | Evaluate simple allow/approval rules by policy ID | `policy.New(policy.Options{})` | Reference evaluator |

## Platform subsystems

| Subsystem | Service contract | Current responsibility | Programmatic entrypoint | Maturity |
|---|---|---|---|---|
| `registry` | `toolsbox.registry.v1.RegistryService` | In-memory registration, leases, lookup, filtering, and resolver adapter | `registry.New(registry.Options{})` | Reference control plane |
| `health` | `toolsbox.health.v1.HealthService` | Report serving/not-serving state for a component | `health.New(health.Options{})` | Reference health service |
| `documentation` | `toolsbox.documentation.v1.DocumentationService` | Serve neutral documentation extracted from protobuf descriptors | `documentation.New(documentation.Options{})` | Reference documentation service |
| `testecho` | `toolsbox.testecho.v1.EchoService` | Integration fixture for reflection, typed calls, docs, and CLI | `testecho.New(testecho.Options{})` | Test fixture |

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
- A feature subsystem must not import another feature subsystem.
- The combined host may import all built-in modules to compose them.
- Runtime dependencies are represented by registry capabilities and service
  references, not Go imports.
- A third-party service can replace a built-in service if it exposes the same
  contract and metadata through ConnectRPC/reflection/registry.
- `pkg/cliapp` adds an `mcp` command to each standalone subsystem automatically.
  The combined host adds `toolsbox mcp` for an aggregated MCP.
- MCP feature exposure is separate from reflection: only policy-allowed unary
  methods can become generated tools or be reached through `call_rpc`.
