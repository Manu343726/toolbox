# 8. Authorization: names and side effects

Status: accepted<br>
Date: 2026-09-25
Replaces the capability vocabulary described in
[ADR-0004](0004-api-introspection-and-providers.md) and
[ADR-0005](0005-reusable-packages-behind-thin-providers.md).
Extends [ADR-0007](0007-one-rule-for-every-feature.md).

## Context

A capability was one string doing two jobs. `knowledge.source.read` named a unique
operation *and* stated that the operation is a read, and declaring it was the grant
to call it. Everything downstream inherited the consequences.

The first was that no policy could be coarse. Because the grant was baked into the
name, saying "every read in this deployment" meant enumerating every read. A
deployment could allow a service or refuse one method, and nothing in between.

The second was worse and quieter. A capability belonged to a service, and the
registry handshake flattens capabilities into one list per subsystem because a
registry and a discovery client both want a single list. The MCP gateway read that
flattened list and copied it onto every service the subsystem advertised, so a
service that had declared nothing arrived authorized by a sibling's declaration. It
was reachable in three deployments before anyone noticed, and there is no test in
the world that catches it by looking at the description.

The third was conceptual. `api.parse.<format>` was a claim about what a subsystem
implements, and it silently doubled as an authorization grant — so a provider's
extension-point advertisement authorized an agent to call it, and nothing said so.

## Decision

**A contract states what invoking a method does. A policy states who may. They are
separate facts, in separate places, and neither is derived from the other.**

### What a contract states

A method says so in its own comment, in the vocabulary the OpenAPI parser already
produced from HTTP verbs:

```proto
// Fetch one pet.
//
// @toolbox.side-effects read_only
rpc GetPet(GetPetRequest) returns (Pet);
```

A service may declare once and a method overrides rather than adds to it. The
annotation is stripped from the prose, because the prose is what generated help and
an agent's tool description are built from. An unrecognised key or value is a
reported finding, never silence: a misspelt key leaves an operation unclassified,
and an unclassified operation is one no policy can expose.

A method that declares nothing is unclassified, and unclassified is its own class
rather than a read. A rule naming the read class does not cover it; a rule naming
no class does, which is how a deployment deliberately trusts one.

`pkg/docs` reads the syntax and nothing else. `pkg/api` owns the vocabulary, so the
protobuf spelling and the OpenAPI extension provably mean the same thing.

### What a policy states

A rule is a pattern over operation identifiers, a predicate over effect classes,
and a decision. The first matching rule decides, most specific first, and both
decisions exist because the only way to narrow a grant without restating it is to
write a refusal inside it.

```
allow *                                read
deny  registry/**                      write read unclassified
allow  knowledge/**                    write read unclassified
```

The identifier includes the API, because it has to stay unique when three
subsystems serve one contract. `apimcp/**` names one provider;
`*/toolbox.api.v1.ApiParserService/*` names the contract in any of them. `*` matches
one segment and a trailing `**` matches the rest, so "everything in this API" does
not have to know the arity of an identifier.

Nothing matched is a refusal, and the zero policy refuses everything. `AllowAll` is
written down explicitly, because "no policy" and "every policy" must never be the
same value.

A policy is a document, so the default one is a file: `cmd/toolbox/policy/toolbox.policy`
grants every read and nothing that changes state. `--policy` replaces it, and a path
that cannot be read is an error rather than a fallback.

### What is deleted

The capability fields on `api.API`, `Service`, `Operation`, and `Server`; on
`subsystem.Service`, `subsystem.Descriptor`, and `core.Endpoint`; the
`api.parse`/`api.render`/`api.invoke` prefixes and their constructors;
`IdentifierFromCapability`; `CapabilitiesFor`; `DeclaresCapability`;
`WithCapabilities`; `ExposableOperations`; `mcp.FeaturePolicy` and
`apitools.OperationPolicy` with all six implementations; `x-toolbox-capabilities`
everywhere; and `ListApis`' `required_capability` filter.

Two of those are not deprecated but removed, because leaving them would leave the
mechanism that produced the defect:

- `subsystem.Service.Capabilities` had nowhere to survive a handshake, so the
  flattening had nowhere to stop being necessary. With the field gone, the
  flattening has nowhere to happen.
- A provider's claim to implement a contract was silently the grant to call it. A
  provider now declares itself: each provider subsystem exports `[]api.Provider`
  records — which already existed and which the catalog already read by field —
  and the composition hands them to the directory.

## Consequences

- The three consumers of "may this be exposed" — the catalog's store, the seeder,
  and the gateway — read one `api.Policy` from one document. Three policies that
  could disagree is the failure this removes.
- A gateway with no policy exposes introspection only. That is a real change from
  a default that exposed everything an operation was not streaming, and it is the
  direction rule 7 of the architecture requires.
- A third-party MCP server needs nothing new. `readOnlyHint` was always a
  side-effect declaration; now it is the only one that is read.
- Authoring cost moved rather than disappeared. A subsystem used to declare one
  capability per operation; it now declares one annotation per operation. What it
  gains is that the *intent* moved to the policy, where a wildcard can be coarse.
- A policy can no longer be written in terms of "what this deployment is for". It
  is written in terms of names and classes. A deployment that wants a semantic
  grouping names it, once, as a set of patterns.
