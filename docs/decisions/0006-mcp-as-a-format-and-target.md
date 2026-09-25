# 6. Model Context Protocol as a format and a target

Status: accepted<br>
Date: 2026-09-25
Extends [ADR-0004](0004-api-introspection-and-providers.md) and
[ADR-0005](0005-reusable-packages-behind-thin-providers.md).

## Context

The standard model was built so that any description language could be read into
it and any representation could be rendered from it. The Model Context Protocol was
the obvious next case, and the obvious one to get wrong, for two reasons.

First, MCP is where most agent tooling already lives. A framework that can read a
service contract but not a tool manifest can serve the APIs a team wrote and none
of the tools an ecosystem published. That is the worse half of the problem: MCP is
the integration surface most users arrive with.

Second, MCP is already this project's own surface. The gateway generates MCP tools
from reflected services, and `NewFromAPICatalog` generates them from a catalog. So
the translation from the standard model to MCP tools already existed — welded to a
live server, fed by a catalog or a reflection source, with no way to say "these are
the tools this description is" and no way to read a manifest back.

## Decision

**MCP is a format like any other, implemented in the same reusable package that
serves it.**

### Reading

`mcp.Describe(ctx, endpoint, options)` reads a live server's own tool list into the
standard description, exactly as `protocontract` reads a gRPC contract out of a
endpoint's reflection. A server describes itself; the catalog never has to be handed
a document it would otherwise have to keep in step.

`mcp.DescribeDocument` reads a published manifest, so a deployment can register an
API it published earlier, or one carried in a configuration file, with no server
running at all.

The three pieces of an MCP manifest map like this:

| Manifest field | Becomes |
| -------------- | ------- |
| `name` | the operation's name, and the tool name a client sees |
| `description` | the operation's summary and description |
| `inputSchema` | the operation's request schema, read as JSON Schema |
| `outputSchema` | the operation's response schema |
| `annotations` | the operation's side effects |
| `x-toolbox-capabilities` | the operation's capabilities |

Two things stay empty on purpose. A tool manifest states no authorization facts, so
**capabilities are empty unless the server declared the extension** — which is what
keeps a third-party MCP server from becoming a fully exposed tool surface the moment
it is registered. And an operation with no declared consequences declares none: a
tool that says nothing about whether it modifies the environment is not read-only,
it is unknown.

### Writing

`mcp.Render(described, options)` is the translation from a description to tool
definitions, and it is the *same* translation the gateway uses. That matters more
than it looks: a tool name is a promise to whatever already learned it, and two
translations that agreed by accident would stop agreeing the first time one of them
changed. `pkg/mcp/features.go` holds the naming, the argument schema, and the
capability resolution, and the gateway and the publisher both read from it. A test
asserts they produce identical names, schemas, and prose for the same description.

`mcp.Manifest(described, options)` writes the target's document, for a reader or a
deployment that wants the tools without a server. A round-trip test reads a live
server, publishes the manifest, reads it back, and asserts the operation keeps its
identity, its capabilities, its declared consequences, and its required arguments —
because a manifest that dropped any of those would publish an API the catalog can no
longer reason about.

### Calling

`mcp.Invoker` calls a tool. A protocol session is a negotiated conversation, so one
is kept per endpoint rather than reopened per call.

### What a description knows about its transport

`api.API` gained a `Transport` field. A protocol that names its own transport is
stating a fact about how its operations are called, and a catalog needs that fact to
choose an invoker: without it, registering a description read from an MCP server
would leave the catalog guessing, and a guess between `http` and `mcp` is a wrong call
at the worst moment.

### The serving face is refused, and says why

The adapter's `ServeApi` returns an error rather than serving a surface. Forwarding
a tool call means reaching the original API over *its* transport, which is not the
adapter's to know — the catalog holds an invoker provider for it and routes through
that. A deployment that wants MCP in front of an API already has it: a catalog-backed
gateway serves the description as tools and calls the original through the invoker it
selected. An adapter that served a surface whose every call failed would be worse
than one that explains the arrangement.

## Consequences

- A third-party MCP server is registered, exposed, and called through the same
  catalog, the same policy, and the same gateway as a service the team wrote. The
  agent does not learn that one arrived through a different door.
- Publishing is symmetric: an API described from any format can be published as an
  MCP manifest, and a manifest can be read back. The round trip is tested, so it stays
  honest.
- The gateway and the publisher share one translation, enforced by a test. Adding a
  naming rule is a one-place change.
- `pkg/mcp` is a provider implementation *and* the framework's own gateway. That is
  the same duality as `pkg/openapi` serving an adapted surface while the catalog
  translates in process, and it is the shape ADR-0005 describes: the package is the
  substance, `subsystems/apimcp` is its addressable form.
- A description read from an MCP server says `mcp` for its transport, so the catalog
  routes calls to the MCP invoker without being told which provider to use.
