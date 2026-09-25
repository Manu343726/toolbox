# Toolbox documentation

This directory is the canonical project documentation for Toolbox.

## Start here

1. [Feature specification](feature-spec.md) — product goals, domain model, requirements, and acceptance criteria.
2. [Architecture](architecture.md) — subsystem boundaries, runtime composition, discovery, and call flows.
3. [Subsystem catalog](subsystems.md) — current services, entrypoints, dependencies, and maturity.
4. [Development guide](development.md) — prerequisites, module workflow, protobuf generation, and build commands.
5. [Testing guide](testing.md) — unit, integration, race, and independent-module testing rules.
6. [Current development status](status.md) — implemented work, validation, limitations, and next milestone.
7. [TODO and roadmap](todos.md) — prioritized work with acceptance criteria.
8. [Protocol conventions](protocol.md) — RPC naming, metadata, errors, versioning, and documentation conventions.
9. [MCP gateway](mcp.md) — generated MCP tools, introspection, exposure control, and deployment commands.
10. [Policy](policy.md) — the document that decides which operations a deployment's agents may call.
11. [Architecture decisions](decisions/README.md) — accepted decisions and their consequences.

## Documentation ownership

- `AGENTS.md` contains agent constraints and required workflows.
- `.agents/skills/` contains task-specific operating instructions.
- `README.md` is the short project introduction and quick start.
- `docs/` is the detailed design and project record.

Update the relevant document in the same change as the behavior it describes. Generated Go, ConnectRPC code, descriptor sets, binaries, and databases are not documentation artifacts and remain ignored.
