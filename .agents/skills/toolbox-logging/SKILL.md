---
name: toolbox-logging
description: Add or change a logging backend, a route, or the slog fanout. Use when touching pkg/log, a loghandler provider subsystem, the logger subsystem, or a deployment's logging: section.
---

# Toolbox logging

Use this skill whenever logging is involved. Read
`docs/decisions/0012-logging.md` and `docs/logging.md` first.

## The one rule

Use `slog` and its ecosystem. Do not write a logging facade.

| Need | Use |
| --- | --- |
| A caller logging | `slog.Info("msg", "k", v)` |
| A component's name | `slog.Default().With(slog.String("logger", name))` |
| A project's tag, deep in a call | `log.WithWorkspace(ctx, "acme")` |
| Fanout within a route | `slog.NewMultiHandler(...)` |
| A sink's minimum | `slog.HandlerOptions.Level` |
| Attribute binding and nesting | the sink's own `WithAttrs` / `WithGroup` |
| Text and JSON lines | `slog.NewTextHandler` / `slog.NewJSONHandler` |
| A rotating file | `gopkg.in/natefinch/lumberjack.v2` |
| syslog, Loki, colour, capture | the handler already written for it |

Forbidden in this repository: a framework `Entry`, a `Handler` interface, `Level`
constants, a line format, a filtering wrapper, a rotating file. If you find
yourself writing one, the standard library already has it.

## Adding a backend

```go
var Provider = log.ProviderFunc{ID: "loki", BuildFn: NewHandler}

func NewHandler(options map[string]any) (slog.Handler, error) { /* ... */ }
```

- Honour the `level` option through `slog.HandlerOptions`, so filtering is your
  own `Enabled` and not a filter wrapped around it.
- Refuse a setting you do not understand, naming it and listing what you accept.
- Declare the subsystem with the role `loghandler` and `Providers(endpoint)`.
- A backend with no operations to address gets **no proto, no command and no
  binary**. The root `build` skips a subsystem without `cmd/`; do not add one to
  make the build pass.

Entries go to handlers **in process**. Never route a log line over ConnectRPC.

## Changing `pkg/log`

`pkg/log` holds the routing decision and nothing else. If a change adds a
concept `slog` already has, it belongs in the sink, not here.

Two traps that have already cost a day each:

- **A `slog.Level` is not a sentinel.** `Debug -4, Info 0, Warn 4, Error 8` means
  an unset level cannot be the zero value and mean "no opinion". Use
  `*slog.Level`. The same applies to `Match.Level` and `HandlerConfig.Level`.
- **`slog.With` attributes live in the handler chain, not the record.** A route
  cannot see them through `record.Attrs`, so `Router.WithAttrs` keeps them as
  well as delegating them to the sinks.

## Changing a route

- **Every matching route contributes.** First-match-wins cannot express
  "everything to the file and this project's problems also to the pager", and
  that is not a corner case.
- A handler named by two routes receives an entry **once**, attributed to the
  route that claimed it first.
- A route that `add`s attributes and sends them nowhere is **refused**. Make it
  an error rather than letting a project's tags silently vanish.
- Conditions and tags are **text and must be quoted**. A bare number is refused,
  because a project whose directory is `001` would otherwise have a route that
  could never match itself.

## A client configuring its own fanout

`SetConfig` may state a fanout for the caller's own workspace. Three rules, and they are the
safety of the feature rather than a style:

- **A fanout must name a workspace.** Unscoped would mean deployment-wide, and there is
  deliberately no request that changes the deployment's fanout — that is the file's.
- **The router is looked up by workspace.** Another workspace's entry finds nothing and
  reaches the deployment's routes. A single global slot for a caller's configuration is the
  bug to look for: it makes every configured workspace capture every other one.
- **Only a backend the installation has may be named.**

A second configuration replaces the first; a fanout is a whole plan, not a patch set.

A workspace name asserts which project a caller is serving, not who it is. Two callers
asserting the same workspace share its configuration, and that belongs in the docs rather than
in a comment nobody reads.

## Testing

Read a real file rather than asserting on a fake handler: a test about routing
should fail on routing, not on a path. `slog.NewJSONHandler` over a
`sync.Mutex`-guarded buffer reads as the lines a sink would have written, so a
routing assertion and a formatting assertion are the same assertion.

- Inject a clock (`logger.Options.Clock`) instead of asserting a time is roughly
  now.
- lumberjack compresses a rotated file on a background goroutine, so a test that
  watches for a compressed backup has to wait for a *readable* one, with a
  bound. A sleep afterwards tests nothing.
- A deployment composed only to be described gets a **discarding** fanout.
  Describing a deployment must not create its log files.
