# 7. One rule for every feature

Status: accepted<br>
Date: 2026-09-25
Extends [ADR-0004](0004-api-introspection-and-providers.md),
[ADR-0005](0005-reusable-packages-behind-thin-providers.md), and
[ADR-0006](0006-mcp-as-a-format-and-target.md).

## Context

ADR-0004 introduced the extension contracts, and three subsystems serve all three
of them on purpose. The seeder, the catalog gateway, and the manifest publisher
each had to decide what to do with a service whose name was in a list, and each
decided to skip it:

- `pkg/discovery.IsInfrastructureService` matched a health check, the registry, the
  documentation service, and the three extension contracts.
- `pkg/mcp/source.go` exempted the extension contracts from its duplicate-service
  check, so a contract several subsystems serve did not collide.
- `pkg/host/seed.go` skipped a subsystem whose services were all on that list,
  reporting "the subsystem serves no service a user adopted".
- `pkg/cli` and `pkg/mcp` skipped the same names from the generated command tree and
  the generated tool surface unless `--include-infrastructure` was passed.

The consequence was that the framework decided, from a list of names, that the
three subsystems whose entire purpose is to serve those contracts contributed
nothing a user could see or call. A provider subsystem could not be exposed, however
a deployment asked, because whether it was in the catalog was decided before the
deployment was consulted.

Three costs, in increasing order of how long they take to notice.

1. The rule lived in five places, so it drifted. The CLI and the gateway each
   skipped a slightly different set.
2. A new service family — a tracing contract, a policy contract — needed a code
   change in each of them, and a deployment that wanted it exposed had to patch
   the framework.
3. The most expensive one: the platform's own capabilities were withheld by
   default. `health`, `registry`, and `documentation` all declare capabilities, so
   the framework's own rules made them features, and then a name list overrode it.

## Decision

**Nothing is excluded from a surface by its name. What a subsystem declares is
what it offers.**

### What is left out, and why

Only the protocol's reflection services, which exist so a client can discover a
contract and provide no capability. They are not a feature because there is nothing
to authorize, not because of how they are named. `Options.IncludeReflection` asks
for them.

Every other service — a health check, the registry, a documentation service, an
extension contract — is an operation like any other. It becomes a tool when it
declared a capability and the deployment exposed it, and it is hidden when the
deployment says so.

### Every started subsystem is registered whole

`RegisterInto` no longer drops services by name. A deployment that wants one out
passes `ExcludeServices`, which is its own list. A subsystem is skipped only when
every service it serves was excluded, and it says so.

### A name more than one operation claims is qualified

Removing the collision exemption exposed a real problem: three providers serving
`ApiParserService` reduce to the same short tool name. The surface used to be built
by refusing that, which meant `--all` could not start.

`pkg/mcp/naming.go` assigns names over the whole surface before any tool is named:

- a name exactly one operation claims is used as it always was, so a name an agent
  already learned does not change;
- a name several operations claim is qualified with the API, or the endpoint, that
  tells them apart — `apimcp__api_parser__parse_api`;
- a name still shared after qualifying falls back to the full service name, because
  a longer name is better than a name that reaches the wrong operation.

The result does not depend on the order operations were discovered in, and a denied
operation still occupies its name: a name that moved because a hidden operation was
present would change what an exposed tool is called.

### What the two surfaces can each tell apart

The catalog names each provider separately, so each is a tool an agent can call:
`apigrpc__api_parser__parse_api`, `apimcp__api_parser__parse_api`,
`apiopenapi__api_parser__parse_api`.

The reflection path reaches a service by name, and a name reaches one endpoint, so
it offers one tool per contract — the endpoint that registered it. That is the
limit of what a service name addresses, and it is why providers are told apart in
the catalog: there each one is a separate API with its own identifier. The
reflection path is no longer a special case that fails to start.

## Consequences

- `--include-infrastructure` is gone; `--include-reflection` says what it now
  means. The old flag still parses, so an existing command line keeps working.
- `apimcp.RenderApi` refuses `include-infrastructure` as an unknown option rather
  than ignoring it, so a caller that passes it learns that it is not doing what it
  thinks.
- A default deployment's tool surface grows: `health__check`,
  `registry__list_services`, and `documentation__read_service` are offered,
  because the manifests that declare those capabilities say they should be. A
  deployment that does not want them hides them, which it can do per operation.
- `discovery.IsInfrastructureService` is deleted rather than deprecated. It had no
  callers left, and leaving it would leave a trap for the next one.
- The exemption for extension contracts in `EndpointSource` is gone too. Every
  endpoint serving a shared contract is remembered, so the owners are not lost
  when the first registration stands.
