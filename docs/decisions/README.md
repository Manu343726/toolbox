# Architecture decisions

These records capture the foundational decisions that should not be changed
implicitly while implementing features.

- [ADR-0001: Independent subsystem modules](0001-independent-subsystem-modules.md)
- [ADR-0002: Registry plus reflection](0002-registry-and-reflection.md)
- [ADR-0003: Typed and dynamic service calls](0003-typed-and-dynamic-calls.md)
- [ADR-0004: API introspection and provider subsystems](0004-api-introspection-and-providers.md)
- [ADR-0005: Reusable packages behind thin provider subsystems](0005-reusable-packages-behind-thin-providers.md)
- [ADR-0006: Model Context Protocol as a format and a target](0006-mcp-as-a-format-and-target.md)
- [ADR-0007: One rule for every feature](0007-one-rule-for-every-feature.md)
- [ADR-0008: Authorization by name and side effect](0008-authorization-by-name-and-side-effect.md)
- [ADR-0009: Cross-subsystem calls](0009-cross-subsystem-calls.md)
- [ADR-0010: The core, and running it as a daemon](0010-core-and-daemon.md) — *proposed; four decisions outstanding*

An ADR is *proposed* until its outstanding decisions are answered; only then is it
accepted. An ADR is accepted when the decision is implemented, documented, and
covered by tests. A proposal that changes one of these boundaries should add a new ADR and
update the affected subsystem and feature documents.
