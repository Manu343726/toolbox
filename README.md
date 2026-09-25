# Toolbox

**The foundation for a fully AI-assisted working environment.**

An assistant can only do real work if it knows your rules, can reach your tools,
can hand work to other assistants, and can follow the process you defined. Those
four things are hard, and most teams rebuild them for every agent, every
project, and every model.

Toolbox provides them once, as a foundation you build on:

| Foundation                   | What you get                                                                   |
| ---------------------------- | ------------------------------------------------------------------------------ |
| **Knowledge base and retrieval** | A shared knowledge base an assistant can search and cite, instead of a per-agent pile of pasted documents |
| **External tool calls**      | A governed way for an assistant to act on your systems, not just describe them |
| **Multi-agent definition and coordination** | Define who does what, and let assistants hand work to each other under your rules |
| **Workflow definition**      | The process written down as a versioned, reviewable plan rather than improvised each run |
| **API integration**          | Point Toolbox at an existing API — OpenAPI, a service contract, or the Model Context Protocol tools an ecosystem already published — and use its operations as tools, published back in a format you did not write it in |

Write your rules, skills, prompts and knowledge once. Every agent, workflow and
model you add afterwards reuses them. And take all of it or just the part you
need — each capability is usable on its own.

## The goal

To let people work in an environment where the assistant is a participant
rather than a suggestion box:

- it **knows** the domain — the rules, conventions and reference material your
  team already wrote down;
- it **acts** — through your tools, inside limits you set, with approval where
  you want it;
- it **coordinates** — several assistants dividing real work, each with a
  defined role and reach;
- it **follows** the process you defined, repeatably and reviewably.

And to make that possible without rewriting the world each time. Today an
assistant's behaviour lives in a prompt; the next assistant, the next project
and the next model start from zero. Toolbox treats rules, skills, prompts,
knowledge, tools and policies as shared, versioned assets — authored once,
improved once, reused everywhere.

## Without a foundation

Building an AI-assisted workflow by hand usually means:

- a large prompt per assistant, which is the only place its behaviour is
  defined and nobody can review it;
- knowledge re-pasted into every prompt, drifting out of date immediately;
- tools wired per project, so the same capability exists in five places and
  behaves five ways;
- no answer to "what is this assistant allowed to do?" or "why did it do
  that?";
- new assistants or new models starting over from nothing.

The result is assistants that are hard to reuse, hard to change and hard to
trust.

## The four foundations

### A knowledge base with retrieval

Your reference material lives in the toolbox, not in a prompt. Assistants
search what they need and work from sources they can point to, so knowledge is
curated once and shared by every assistant and workflow. Access to sources is
governed like everything else, so a restricted document stays restricted.

*Removes:* re-pasting and re-curating context for every assistant.

### Governed external tool calls

An assistant that cannot act is a suggestion engine. Toolbox gives assistants a
declared set of actions against your real systems — each one described, each one
subject to policy, and the consequential ones able to require approval before
they run. Capabilities are added once and become available to every assistant
that is allowed to use them.

*Removes:* bespoke tool wiring per project, and the ambiguity of what an
assistant is allowed to do.

### Multi-agent definition and coordination

Real work needs more than one assistant: a researcher, an author, a reviewer, an
operator. Toolbox lets you define each role, what it can reach, and how work
moves between them — including handoffs, escalation for approval, and shared
context that all of them work from. Adding a role is a new definition, not a new
prompt for everyone.

*Removes:* duplicated roles and hand-rolled message passing between assistants.

### Workflow definition

A workflow is the process written down: the steps, the order, the branching, the
approvals, and which assistant or capability each step uses. It is versioned and
reviewable like code, validated before it runs, and reused by every assistant
that needs it. Changing how work happens becomes a reviewed change, not a
rewrite of everyone's instructions.

*Removes:* improvisation, and the silent drift of "how we do things" into
whoever prompted last.

## Use it as a whole, or take one piece

Toolbox is a complete environment you can adopt as-is, but it is not a
take-it-or-leave-it bundle. Each capability stands on its own, so you can start
with the one that solves a real problem and add the others when you need them.

Common ways people start:

| You want                        | You take                                            | What you get                                                             |
| ------------------------------- | --------------------------------------------------- | ------------------------------------------------------------------------ |
| A knowledge base for your agents| Just the knowledge capability                        | Shared, searchable, governed knowledge — nothing else to run or maintain |
| Your tools available to agents  | Just the tools capability, plus policies             | Declared actions with approval boundaries, no other machinery            |
| A process assistants follow     | Workflows, plus whichever capabilities the steps use | A versioned plan instead of per-agent instructions                      |
| Coordination between agents     | Agents, workflows, and the capabilities they need   | Defined roles and handoffs without adopting the whole environment       |
| The full environment            | Everything                                            | One toolbox where rules, knowledge, tools, and process stay in one place  |

Whatever you choose, the capability you adopt keeps the same properties as in the
full environment: it is versioned, governed by policy, documented, and
reachable by your agent runtime over the Model Context Protocol. Adopting more
later is additive — it does not replace or invalidate what you already run, and
nothing you adopted has to be rewritten when you do.

This is why each capability is an independent service rather than a module
inside a monolith: a capability you did not want should cost you nothing to
leave out. See [`docs/architecture.md`](docs/architecture.md) for how the
composition modes implement this.

## What is in the toolbox

| Building block | What it holds                                              | Why it matters                                                    |
| -------------- | ---------------------------------------------------------- | ----------------------------------------------------------------- |
| Workflows      | Versioned plans: steps, order, branching, approvals       | The process, written down and reviewable                           |
| Agents         | Roles referencing the skills, knowledge, tools they may use | Who does what, defined once and reused                            |
| Skills         | Reusable units of know-how bound to what they require      | Know-how that improves once for everyone                           |
| Prompts        | Parameterised templates                                    | Consistent instructions without hand-editing each time             |
| Knowledge      | The sources assistants may search                          | One current body of reference material                            |
| Models         | The models available to the team                           | Work stays portable across providers and budgets                  |
| Tools          | Declared actions, each with its own requirements            | What an assistant can actually do                                 |
| Policies       | What is allowed, and what needs approval                   | Limits enforced by the system, not requested in a prompt          |
| Health         | Whether each part is ready                                 | Assistants and people do not rely on something unavailable        |

## A day in the environment

> Someone asks for the weekly report. An assistant loads the report workflow,
> which names the prompt template, the knowledge sources, and the review step.
> The assistant searches the sources, drafts with the template, and reaches the
> publish step — which requires approval, so it hands off to a reviewer instead
> of sending anything itself. The workflow records which versions of the
> template, sources and policy it used, so the result can be explained and
> repeated.

Next month, a second assistant does the same thing. It reuses every one of
those assets. Nobody rewrote a rule.

> An assistant is also given the APIs you already have. It looks one up, reads
> what it can do, asks for the operations the job needs, and calls them. When the
> job is done it lets them go again.

## Getting started

Build the toolbox:

```sh
make build
```

Run it:

```sh
./bin/toolbox --all
```

Connect an assistant to it. Any agent runtime that speaks the Model Context
Protocol can use the toolbox directly:

```sh
./bin/toolbox mcp --all
```

Start with a smaller surface when you want the assistant to request what it
needs as it goes:

```sh
./bin/toolbox mcp --all --minimal
```

Or adopt just the capabilities you want — either from the host:

```sh
./bin/toolbox mcp --component knowledge
./bin/toolbox mcp --component knowledge --component policy
```

…or run a single capability on its own, with no host at all:

```sh
./bin/knowledge mcp
```

Then read [`docs/feature-spec.md`](docs/feature-spec.md) — it describes what
each part of the product must do, and where the current implementation stands
against it.

## Who it is for

- **Platform and AI engineers** building an assistant that can do the work
  rather than describe it.
- **Agent developers** who want reusable, versioned behaviour instead of
  prompt-by-prompt tuning.
- **Teams** that need to know, at any moment, what an assistant can do and what
  it actually did.

## Status

Early and actively developed. The foundation works end to end today; the
included knowledge, tool, agent and workflow capabilities are working reference
implementations rather than finished products. The current state is written up
in [`docs/status.md`](docs/status.md) and the plan in
[`docs/todos.md`](docs/todos.md).

## Documentation

[`docs/README.md`](docs/README.md) is the map.

Product and design:

- [Feature specification](docs/feature-spec.md) — what the product is and what each part must do
- [Subsystem catalog](docs/subsystems.md) — every capability and its contract
- [Current status](docs/status.md) and [roadmap](docs/todos.md)

Engineering detail:

- [Architecture](docs/architecture.md) — boundaries, runtime layers, composition, constraints
- [MCP gateway](docs/mcp.md) — agent tooling, exposure control, client setup
- [Development guide](docs/development.md) — build, contracts, code generation
- [Testing guide](docs/testing.md) — test layers and required checks
- [Protocol conventions](docs/protocol.md) and [decisions](docs/decisions/README.md)

Contributors and agents should read [`AGENTS.md`](AGENTS.md).
