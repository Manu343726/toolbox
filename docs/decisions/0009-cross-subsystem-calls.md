# 9. Cross-subsystem calls

Status: accepted<br>
Date: 2026-09-25
Amends [ADR-0001](0001-independent-subsystem-modules.md).
Amends rule 3 of `AGENTS.md`, which previously read "Feature packages must not
import one another."

## Context

Rule 1 makes each subsystem an independent Go module. Rule 3 then said feature
packages must not import one another, and gave two sentences that contradicted each
other:

> A feature subsystem must not import another feature subsystem. If it needs a
> runtime dependency, inject or resolve a `core.Resolver` and call the generated
> client after resolution.

The first sentence forbids the thing the second one describes. The intended
mechanism was always the second; the first was an over-statement of it, and it was
wrong for a reason nobody had checked.

The rule's actual purpose is stated in `docs/architecture.md`, in the section on
additive adoption: *"Adding a subsystem to a running deployment does not change the
contract, endpoint, or behavior of the subsystems already deployed... This is why a
subsystem may not import another subsystem: doing so would make the cost of leaving
one out non-zero."*

So the concern was never that calling another subsystem is coupling. It is that
**link-time coupling makes a subsystem mandatory**. If subsystem A cannot be built
without subsystem B present in A's module graph, then a deployment that wants A
without B pays for it — and the property additive adoption exists to protect is
gone.

That distinction is the whole decision.

## What dotfilesd does

Checked, because the mechanism already exists and works. A dotfilesd plugin is its
own Go module. Two of them call each other:

```text
// ~/.config/dotfilesd/plugins/tmuxbar/go.mod
module plugins/tmuxbar
require plugins/resources v0.0.0
replace plugins/resources => ../resources
```

```go
// tmuxbar/main.go
import "plugins/resources/proto/resources/resourcesconnect"

func initResourcesClient() resourcesconnect.ResourcesServiceClient {
    // The registry is the resolver. The daemon is not a proxy.
    regClient := dotfilesdv1connect.NewPluginRegistryServiceClient(httpClient, "http://127.0.0.1:9105")
    regResp, err := regClient.GetPlugin(ctx, connect.NewRequest(&dotfilesdv1.RegistryGetPluginRequest{
        PluginName: "resources",
    }))
    if err != nil { return nil }
    return resourcesconnect.NewResourcesServiceClient(httpClient, regResp.Msg.Url)
}
```

Three properties worth copying:

1. **The import is the contract, not the implementation.** `resourcesconnect` is
   generated code. The caller gets a typed client and never a `Resources` struct.
2. **The endpoint is resolved at runtime**, from the registry. The plugin dials the
   *other plugin* directly at the URL it was given; the daemon is the resolver,
   not a hop in the call path.
3. **The plugin SDK says so in its own words.** `plugin/context.go`:

   > // Context is the interface plugins use to interact with the daemon.
   > // Plugins call each other DIRECTLY via generated Connect clients,
   > // NOT through this Context.

   The `Context` is for *daemon* capabilities — exec, sudo, secrets, TTY, stdio.
   Peer calls do not go through it.

Two more mechanisms, one used and one declared: `Service.PluginAccessible` marks a
service as reachable by other plugins, and `PluginRegistryService.LoadPlugin` loads
"a plugin by name, including its dependencies" — a declared dependency graph that
the daemon walks.

Note what dotfilesd does *not* solve: `plugins/tmuxbar` has a real module
dependency on `plugins/resources`, and `replace ../resources` couples them at build
time. The callee's *contract* is the coupling, and a contract change forces
rebuilds. That cost is real, and dotfilesd accepts it.

## Decision

**A subsystem may depend on another and call it. It must not link another's
implementation.**

Three separate things, previously conflated:

| | allowed | why |
|---|---|---|
| Import the callee's **generated contract** | yes | gives a typed client; no state, no storage, no lifecycle |
| Import the callee's **implementation** | **no** | links its state, storage, and lifecycle into the caller's process; neither is separately deployable |
| Depend on the callee's **module** at build time | yes, with a known cost | a contract change forces callers to rebuild |

A cross-subsystem call is an RPC resolved at runtime. The caller loads and starts
whether or not the callee is present, and a deployment may leave the callee out —
which is the property additive adoption needs, and which the old rule got by
prohibiting something it did not need to prohibit.

The sanctioned path is already the one rule 6 describes: resolve first, then
construct the generated client with `core.Bind`, and fail before calling if
resolution failed.

## Consequences

- `core.Resolver` stops being optional. `registry.Resolver` exists, resolves a
  subsystem name or a fully-qualified service name to an endpoint, and was never
  constructed by anything. A subsystem that calls a peer needs a resolver, so
  wiring the registry-backed one is no longer a convenience.
- A subsystem that resolves a peer at startup should say so when the peer is
  missing, and distinguish "not deployed" from "not answering", because a deployment
  may legitimately run without it.
- The build-time module dependency is the residue. Two ways to remove it, neither
  required now: extract a contract into its own module, or depend on a shared
  contract module. Toolbox contracts are already independently importable
  (`subsystems/<name>/<name>v1`), so this is a packaging choice, not a redesign.
- `docs/development.md` now shows the working example rather than the
  contradiction, and `AGENTS.md` rule 3 says what is actually forbidden.
- This record does not decide how a peer is *found*. A resolver chain — in-process
  endpoints first, then a daemon — is the separate question in
  `docs/decisions/0010-core-and-daemon.md`.
