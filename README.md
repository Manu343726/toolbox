# Toolbox

Toolbox is a Go framework for building AI-agent capabilities as independent
services — and for exposing those services to agents through
[Model Context Protocol](https://modelcontextprotocol.io/) without hand-writing
tool wrappers, command layers, or documentation.

Your services keep their own contracts. Toolbox derives the agent-facing
surface from those contracts, so the tools an agent sees always match the API
you actually ship.

## Why

An agent can only use a service it can **discover**, **understand**, and
**safely call**. Teams usually bridge that gap with a bespoke MCP wrapper per
service, plus a hand-written CLI and a separate doc page — three artifacts that
drift from the real contract as soon as the service changes.

Toolbox keeps one source of truth. Change the contract and the agent tools,
their schemas, the CLI, and the documentation all follow.

## Features

### Agents can call your services

Toolbox generates an MCP server from the services you already expose. Every
allowed RPC method becomes a tool with a JSON Schema built from your protobuf
contract, and the descriptions agents read come from the comments you already
write. It works with any MCP client, over stdio or Streamable HTTP.

### Agents start small and grow on demand

A large tool list is expensive and confusing. Toolbox agents can begin with
introspection tools only, then pull in the features they need:

- `list_services` / `list_features` — what is available
- `describe_feature` / `read_feature_documentation` — what it does
- `expose_feature` / `hide_feature` — turn features on and off at runtime
- `feature_exposure` — audit the current footprint
- `call_rpc` — a generic escape hatch for one-off calls

The same gateway can start fully exposed (`--all`) or minimal
(`--minimal`); the agent decides the rest.

### Reflection never grants permission

Discovery and authorization are separate. A reflected method is not callable
until an explicit feature policy allows it, and a policy is declared per
service or subsystem rather than inferred from a schema. Agents see what
exists; your policy decides what runs.

### Services describe themselves

Every service exposes reflection, machine-readable handshake metadata, and can
register itself with a registry using leases. Callers resolve an endpoint first
and construct a client second, so a missing service fails immediately instead
of half-way through a request.

### Documentation cannot go stale

Comments in your contract are extracted into the service documentation, the
generated CLI help, and agent-readable feature documentation. There is no
second place to update.

### The CLI matches the API

Commands are generated from the reflected schema, so flags, arguments, and help
text follow the contract automatically. New methods appear as new commands
without writing command code.

### Capabilities stay independent

Each capability is its own Go module with its own contract, implementation,
tests, and binary. Add, replace, or ship one capability without touching the
others. When you want them together, a single host process composes them — but
no capability imports another.

### Typed where it matters, dynamic where it helps

Calls to services whose contracts you compile against are fully typed.
Services you only know at runtime are still reachable through the same
discovery and client layer, so plugins and external systems integrate without
recompiling.

### Provider-neutral by design

Core contracts — workflows, agents, prompts, knowledge, skills, models, tools,
policies — contain no provider-specific request or response types. Providers
sit behind the model capability; the contracts stay yours.

## Capabilities included

The repository ships reference capabilities that are useful on their own and
double as a worked example of the framework.

| Capability     | What an agent can do with it                                  |
| -------------- | ------------------------------------------------------------- |
| `workflow`     | Store, list, retrieve, and validate versioned workflow definitions |
| `agent`        | Manage provider-neutral agent profiles and their references     |
| `skill`        | Manage reusable skills and the capabilities they require        |
| `prompt`       | Store templates and render them with variables                 |
| `knowledge`    | Store sources and search them                                   |
| `model`        | List available models and invoke a provider                    |
| `tool`         | Declare tools and invoke explicitly registered implementations |
| `policy`       | Evaluate allow/approval rules by policy                        |
| `registry`     | Discover and resolve service endpoints                          |
| `health`       | Report component serving state                                 |
| `documentation`| Read the documentation carried by the contracts themselves      |

## Quick start

Build the capabilities and the host:

```sh
make build
```

Expose everything to an agent as one MCP server:

```sh
./bin/toolbox mcp --all
```

Or start minimal and let the agent discover what it needs:

```sh
./bin/toolbox mcp --all --minimal
```

Expose a single capability, or a single service inside it:

```sh
./bin/toolbox mcp --component workflow
./bin/toolbox mcp --component workflow --service toolbox.workflow.v1.WorkflowService
```

Every capability also ships as its own MCP:

```sh
./bin/workflow mcp
```

Point your MCP client at one of these commands, or wire it into an agent
session — see [`docs/mcp.md`](docs/mcp.md) for the feature model, exposure
semantics, and client setup. OpenCode sessions started in this repository
already have the gateway and the project documentation available.

Run the services themselves the same way:

```sh
./bin/toolbox --component workflow   # one capability
./bin/toolbox --all                  # every capability, composed
```

## Documentation

[`docs/README.md`](docs/README.md) is the documentation map:

- [Feature specification](docs/feature-spec.md) — goals, requirements, acceptance criteria
- [Architecture](docs/architecture.md) — how capabilities, discovery, and calls fit together
- [MCP gateway](docs/mcp.md) — tools, exposure control, client and session setup
- [Subsystem catalog](docs/subsystems.md) — every capability and its contract
- [Development guide](docs/development.md) — build, protobuf, and contribution workflow
- [Testing guide](docs/testing.md) — unit, integration, and race-testing rules
- [Status and roadmap](docs/status.md) · [todos](docs/todos.md) — what is done and what is next

Contributors and agents should read [`AGENTS.md`](AGENTS.md).

## Status

Early and actively developed. The framework foundation — independent
capabilities, discovery, documentation, generated CLI, and the MCP gateway —
works end to end, and the included capabilities are reference
implementations rather than production engines. See
[`docs/status.md`](docs/status.md) for the current state and
[`docs/todos.md`](docs/todos.md) for what is planned.
