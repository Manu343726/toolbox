# Architecture decisions

These records capture the foundational decisions that should not be changed
implicitly while implementing features.

- [ADR-0001: Independent subsystem modules](0001-independent-subsystem-modules.md)
- [ADR-0002: Registry plus reflection](0002-registry-and-reflection.md)
- [ADR-0003: Typed and dynamic service calls](0003-typed-and-dynamic-calls.md)
- [ADR-0004: API introspection and provider subsystems](0004-api-introspection-and-providers.md)
- [ADR-0005: Reusable packages behind thin provider subsystems](0005-reusable-packages-behind-thin-providers.md)
- [ADR-0006: Model Context Protocol as a format and a target](0006-mcp-as-a-format-and-target.md)

An ADR is accepted when the decision is implemented, documented, and covered by
tests. A proposal that changes one of these boundaries should add a new ADR and
update the affected subsystem and feature documents.
