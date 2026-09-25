# Policy

A policy decides which operations a deployment's agents may call. It is a short
text document, and it is the whole of the framework's authorization vocabulary.

The default is [`cmd/toolbox/policy/toolbox.policy`](../cmd/toolbox/policy/toolbox.policy),
which grants every read in the deployment and nothing that changes state. Every
command that exposes operations takes `--policy` to replace it.

## The format

One rule per line, and nothing else:

```text
# Comments start with a hash. Blank lines are ignored.
allow *                              read
deny  registry/**                    write read unclassified
allow  knowledge/**                  write read unclassified
allow  apimcp/**                     read
```

A line is three things: a **decision** (`allow` or `deny`), a **pattern** over
operation identifiers, and optionally the **effect classes** it applies to. A rule
with no class is a statement about every operation, which is how a deployment
deliberately trusts an operation whose contract classified nothing.

A line that cannot be read is an error naming the line number. A policy that
silently skipped half its rules would authorize less than its author believes,
which is the direction that gets noticed last.

## Patterns

An operation identifier is `<api>/<service>/<method>`:

```text
knowledge/toolbox.knowledge.v1.KnowledgeService/Search
```

The API is part of it because three subsystems serve the same contract, and naming
one provider has to be possible: `apimcp/**` is `apimcp`, while
`*/toolbox.api.v1.ApiParserService/*` is that contract in any of them.

| token | matches |
|---|---|
| `*` | exactly one segment |
| `**` | every remaining segment, and only as the last segment |
| anything else | that segment, compared exactly |

A segment is matched whole, so a service name is one segment however many dots it
has, and a prefix does not match. A wildcard mixed with other text inside one
segment is refused rather than treated as a prefix match, because it does not look
like what it would do.

`knowledge/**` is every operation of the knowledge API. It is written that way
rather than `knowledge/*/*` because the second form knows how many segments an
identifier has, and that is easy to get subtly wrong.

## Evaluation

Rules are consulted most specific first, measured in segments named exactly, and
the first match decides. A pattern naming more of the identifier matches fewer
operations, so it goes first.

That is what lets a broad grant and a narrow refusal coexist:

```text
allow *         read
deny  registry/**   read
allow registry/toolbox.registry.v1.RegistryService/Deregister  read
```

The second line is consulted before the first, so the registry's reads are refused
except the one operation named; the third is consulted before the second, so that
one is permitted. **The order the rules were written in does not matter**, which is
the property that makes a readable document safe to edit. Two rules matching
exactly the same operations are decided by the order a person wrote them.

**Nothing matched is a refusal.** The empty policy permits nothing, and permitting
everything is written down explicitly. "No policy" and "every policy" are different
values, and a deployment that has written nothing down has authorized nothing.

## Effect classes

| class | covers |
|---|---|
| `read` | every declared effect is a read |
| `write` | at least one declared effect is not a read |
| `unclassified` | the contract declared no effect at all |

An operation nobody classified is **not** a read. A rule naming the `read` class
does not cover it, and that is deliberate: a rule that permits reads must not
quietly authorize an operation whose contract says nothing about what it does. A
rule naming no class covers everything, so a deployment that trusts it says so
deliberately.

## Where the classification comes from

A contract states it in its own comment:

```proto
// Fetch one pet.
//
// @toolbox.side-effects read_only
rpc GetPet(GetPetRequest) returns (Pet);
```

A service may declare once and a method overrides rather than adds to it, so a
read-only service says so once and a read-only service with one writing method is
still classified correctly. A method that says nothing is unclassified.

Three other sources state it, all in the same vocabulary:

- an **OpenAPI document** gets the effect its HTTP method implies — `GET` is a
  read, `POST` a create, `PUT` and `PATCH` an update, `DELETE` a delete — and
  `x-toolbox-side-effects` adds to it.
- a **third-party MCP server** states it in the protocol's own annotations:
  `readOnlyHint` is a read, `destructiveHint` is irreversible, `idempotentHint` is
  a repeat with no further effect.
- a **description written by hand** sets `side_effects` on the operation.

## What a policy does not decide

A policy decides what a deployment exposes. It is not per-actor authorization: it
does not know who is calling. A decision about *whether this caller may do this
now*, with the actor and the resource, is the policy subsystem's `Evaluate`, which
is not wired into the gateway yet.

Exposure remains a separate, mutable decision on top of a policy. An agent can
expose and hide operations at runtime through the gateway's `expose_feature` and
`hide_feature` tools, and through the catalog's `ExposeOperation`; neither can
exceed what the policy permits, so growing a footprint is always a subset of what a
deployment already allowed.
