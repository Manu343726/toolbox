---
name: documentation-cli
description: Maintain protobuf documentation extraction, descriptor-set embedding, reflection metadata, and schema-driven CLI generation.
---

# Documentation and CLI generation

Use this skill for changes to `pkg/docs`, `pkg/discovery`, `pkg/cli`, generated
help text, descriptor-set files, or subsystem `docs_embed.go` files.

## Documentation flow

```text
.proto comments
  -> FileDescriptorSet with --include_source_info
  -> pkg/docs parser
  -> neutral docs.Service model
  -> DocumentationService or CLI help
```

- Keep comments next to the protobuf definitions; do not duplicate API
  descriptions in hand-written Go.
- Generate descriptor sets through the subsystem Makefile.
- Keep `docs_embed.go` small and mechanical.
- Do not let empty generated-descriptor documentation overwrite richer embedded
  source documentation in a shared catalog.
- Preserve service, method, field, enum, streaming, and presence information.

## CLI flow

- Discover methods and request fields through `pkg/discovery`.
- Use the neutral documentation model to populate command and flag help.
- Generate method flags and requests in `pkg/cli`; do not hand-code flags in a
  subsystem command.
- Reject invalid enum values and report malformed numbers/maps clearly.
- Reject streaming methods explicitly in the unary generator until streaming
  invocation is implemented.
- Test generated command names, flags, help text, request construction, JSON
  output, and error paths.

## Validation

After changing documentation or CLI behavior:

```sh
make test
GOWORK=off make -C subsystems/testecho test
```

The testecho subsystem is the executable vertical slice for reflection,
dynamic calls, typed calls, embedded documentation, and generated CLI behavior.
