# Toolbox

**The AI agent workflow toolbox.**

Toolbox gives AI agents a shared, governed set of capabilities — workflows,
prompts, knowledge, skills, models, tools and policies — instead of a pile of
hardcoded prompts and one-off tool wrappers.

An agent opens the toolbox, sees what it is allowed to use, pulls in what the
job needs, and works inside explicit limits. Humans, scripts and agents use the
same toolbox, so nothing an agent can do is a secret from the rest of your
team.

## The problem

Most agent setups are assembled by hand:

- every agent gets its own giant prompt, and the prompt is the only place its
  behaviour is defined;
- tools are scattered across repositories, duplicated per project, and drift
  from the services that actually implement them;
- nobody can answer "what is this agent allowed to do?" or "which agent used
  that tool last Tuesday?";
- adding a capability means touching every agent that might need it.

The result is agents that are hard to reuse, hard to change, and hard to trust.

## What Toolbox gives you

### One toolbox, many agents

Workflows, prompts, knowledge sources, skills, models, tools and policies live
in the toolbox once. Any agent can use them. Adding a capability makes it
available everywhere immediately, instead of to the agents you remember to
update.

### Workflows as first-class, reviewable artifacts

A workflow is a named, versioned plan: which steps run, in what order, using
which capabilities. Workflows are validated before they are used, reviewed like
code, and reused across agents. Changing how work happens is a version bump, not
a prompt rewrite.

### Agents defined by what they can reach

An agent profile references the skills, knowledge, tools and policies it uses
rather than embedding them in text. The same profile works with a different
model, a different team, or a different environment, because the profile
describes intent, not implementation.

### Governance instead of trust

Every capability declares what it does, and policies decide what each action
requires: allowed, or allowed with approval. The toolbox refuses actions its
policies do not permit, so limits are enforced by the system rather than
requested in a prompt. You can always answer what an agent could have done.

### Agents that ask for what they need

Agents start by seeing the toolbox's catalogue, not by loading every tool
forever. They request the specific capabilities a task needs, and release them
when the task is done. Context stays small, behaviour stays legible, and the
footprint an agent used is inspectable.

### Model freedom

Workflows and agent profiles are provider-neutral. Bring the model that fits
the task — or the budget, or the region — without rewriting the work itself.
Model access is a capability, not a hardcoded dependency.

### Bring your own capabilities

The included capabilities are a starting point, not a ceiling. Add your own
services, and the toolbox treats them the same way it treats the built-ins:
discoverable, documented and governed.

### One toolbox for humans and agents

The same definitions back the command line, your scripts and your agents.
There is no agent-only surface that drifts, and no privileged path that only
works for the framework.

### Works where your agents already are

The toolbox plugs into the agent tools and IDEs your team already uses, through
the Model Context Protocol that agent runtimes speak. No new agent client to
adopt, no prompt conventions to teach.

## What is in the toolbox

| Capability      | What it is                                                            | What an agent does with it                              |
| --------------- | --------------------------------------------------------------------- | -------------------------------------------------------- |
| **Workflows**   | Versioned plans made of steps                                        | Look up a plan, validate it, follow it, improve it        |
| **Agents**      | Versioned profiles that reference capabilities                       | Act as a configured role with a defined reach              |
| **Skills**      | Reusable units of know-how bound to the capabilities they need        | Load a skill when the task matches it                     |
| **Prompts**     | Parameterised templates                                               | Render a template instead of improvising wording          |
| **Knowledge**   | Sources an agent is allowed to consult                               | Search only the sources it is entitled to                 |
| **Models**      | The models available to the team                                     | Choose or be given a model for the step it is running     |
| **Tools**       | Declared actions, each with its own requirements                     | Invoke an action, subject to that action's policy         |
| **Policies**    | The rules that decide what needs approval                            | Check what a step requires before taking it               |
| **Health**      | Whether each part of the toolbox is ready                            | Avoid relying on something that is not available         |

Every item is versioned, so behaviour is reproducible and changes are visible.

## A day with the toolbox

> An agent is asked to prepare the weekly report. It finds the report workflow,
> loads the prompt template it names, searches the knowledge sources it is
> allowed to consult, and reaches the publish step. The publish step requires
> approval, so the agent prepares the draft and requests sign-off instead of
> sending anything itself. The whole exchange is recorded against the
> workflow version it used.

## Getting started

Build the toolbox:

```sh
make build
```

Run everything it contains:

```sh
./bin/toolbox --all
```

Hand it to your agents:

```sh
./bin/toolbox mcp --all
```

Any MCP-capable agent runtime can then connect to that command and use the
toolbox. To give an agent a smaller, quieter toolbox, start it with
`--minimal` and let it request capabilities as it needs them.

From there, the fastest way to understand the product is to read
[`docs/feature-spec.md`](docs/feature-spec.md) and then look at the workflows,
agents and prompts it describes.

## Who it is for

- **AI and platform engineers** standardising how agents get tools, and how
  those tools are governed.
- **Agent developers** who want reusable, versioned behaviour instead of
  prompt-by-prompt hand-tuning.
- **Teams** that need to answer, at any moment, what an agent is capable of and
  what it actually did.

## Status

Early and actively developed. The framework works end to end today and the
included capabilities are working reference implementations rather than
finished products — the current state is written up in
[`docs/status.md`](docs/status.md) and the plan in
[`docs/todos.md`](docs/todos.md).

## Documentation

[`docs/README.md`](docs/README.md) is the map. Product and design reading
first:

- [Feature specification](docs/feature-spec.md) — what the product is and what it must do
- [Subsystem catalog](docs/subsystems.md) — every capability and its contract
- [Current status](docs/status.md) and [roadmap](docs/todos.md)

Engineering detail lives here:

- [Architecture](docs/architecture.md) — boundaries, runtime layers, composition, constraints
- [MCP gateway](docs/mcp.md) — agent tooling, exposure control, client setup
- [Development guide](docs/development.md) — build, contracts, code generation
- [Testing guide](docs/testing.md) — test layers and required checks
- [Protocol conventions](docs/protocol.md) and [decisions](docs/decisions/README.md)

Contributors and agents should read [`AGENTS.md`](AGENTS.md).
