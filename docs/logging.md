# Logging

A Toolbox deployment has one log fanout, configured in its configuration file, and every
piece of code in the process logs into it through Go's standard `log/slog`. An agent or a
service that is not in the process logs into it through the `logger` subsystem.

```text
   slog.Info("stored a source", "id", 42)          a dependency's slog.Info(...)
              │                                              │
              └──────────────┬───────────────────────────────┘
                             ▼
                    log.Router  (a slog.Handler)
                             │  which routes match this entry?
                             ▼
        ┌────────────┬───────────────┬──────────────┐
        ▼            ▼               ▼              ▼
   logs/machine.log  logs/pager.json  syslog      discarded
```

## The Go API is `slog`

There is no logging facade. A subsystem logs the ordinary way:

```go
slog.Info("stored a source", "id", id, "bytes", len(content))
```

A deployment installs its fanout as the process-wide default with `slog.SetDefault`, so
anything in the process that logs through `slog` — including a dependency the framework has
never heard of — lands in it. A caller that wants a component name on its lines uses the
standard library's own convention:

```go
logger := slog.Default().With(slog.String("logger", "knowledge"))
logger.Info("stored a source", "id", id)
```

The `logger` attribute is what routes match on to answer "which subsystem said this".

`pkg/log` contributes one thing the standard library does not have: deciding which handlers
an entry reaches, and what it is tagged with on the way. Everything else is `slog` or the
ecosystem — `slog.MultiHandler` for the fanout, `slog.HandlerOptions` for levels and
formatting, each sink's own `WithAttrs` and `WithGroup` for nesting, and `lumberjack` for
rotation.

## Configuring a fanout

The `logging` section of the configuration file is the whole fanout.

```yaml
daemon:
  host: 127.0.0.1
  port: 9180

logging:
  level: debug

  handlers:
    machine:
      provider: json
      options:
        path: logs/machine.log

    pager:
      provider: logfile
      level: warn
      options:
        path: logs/deployment.log
        max_backups: 5
        max_age_days: 30
        compress: true

  routes:
    - name: everything
      handlers: [machine]

    - name: and problems also to the pager
      match:
        level: warn
      handlers: [pager]

    - name: one project's own file
      match:
        workspace: acme
      handlers: [acme]
      add:
        project: acme
```

### `level`

The minimum an entry must reach to be routed at all. One of `debug`, `info`, `warn`,
`error`. Anything else is refused by name.

### `handlers`

Each handler names a **provider** — the backend that writes to it — its own `level`, and that
provider's `options`.

| Provider | What it writes | Options |
| --- | --- | --- |
| `text` | the standard library's text handler | `path`, `level`, `source`, `replace_attr` |
| `json` | the standard library's JSON handler | `path`, `level`, `source`, `replace_attr` |
| `null` | nothing, so a destination can be switched off without deleting the entry naming it | `level` |
| `logfile` | a rotating file, through lumberjack | everything above plus `format`, `max_size`, `max_backups`, `max_age_days`, `compress`, `local_time` |

`path` is optional on `text` and `json`; without one they write to standard error. A
**relative path resolves against the project the configuration file belongs to**, so
`path: logs/project.log` in a project's own file is a file inside that project.

`max_size` is in megabytes, `max_age_days` in days, and `max_backups` is how many rotated
files are kept. A number written in a configuration file may be a number or its digits as
text, because a configuration read from an environment is strings all the way down.

### `routes`

A route is a `match`, the `handlers` it sends to, and the attributes it `add`s.

**Every route that matches contributes.** "Everything to the file, and this project's problems
also to the pager" is exactly two routes. A handler named by two routes receives an entry
once, attributed to the first route that claimed it — a line duplicated because two routes
overlapped is a line nobody can count.

A `match` may set `level` and any number of attributes that must be present. A route with no
`match` is the default. A route that `add`s attributes and sends them nowhere is **refused**,
because the only sign it was broken would be a project whose logs could not be found.

Conditions and tags are text and must be quoted. A bare number is refused rather than read as
a number, because a project whose directory is called `001` would otherwise end up with a
route that could never match itself.

### Tagging a project

`add` is what makes one project's lines findable among a machine's:

```yaml
routes:
  - name: one project's own
    match:
      workspace: acme
    handlers: [acme]
    add:
      project: acme
```

An in-process caller carries its project on the context, so nothing deep in a call has to
thread it through every signature to log which project it was serving:

```go
ctx = log.WithWorkspace(ctx, "acme")
slog.InfoContext(ctx, "stored a source")
```

The workspace is put on the entry before any sink sees it, so a collector filtering by
project reads the line and not the context.

## Per-project fanouts

A project may carry its own `.toolbox/config.yaml` with its own `logging` section, found by
walking up from a directory the way every other project configuration is. A machine-wide file
is the fallback; a project's own file is the more specific statement.

```text
machine/
├── .toolbox/config.yaml            the deployment: everything to logs/machine.log
└── projects/
    └── acme/
        ├── .toolbox/config.yaml    the project: only acme, into logs/acme.log
        └── logs/acme.log
```

## Logging from another process

The `logger` subsystem is the RPC boundary. An agent, or a service it started, is not in the
daemon and cannot call `slog.New` against a router in there.

```sh
toolbox logger log --message "advanced a run" --logger agent --level LOG_LEVEL_INFO
toolbox logger get-config
toolbox logger set-config --workspace acme
```

`Log` answers with the sinks the entry reached:

```json
{ "routed": true, "handlers": ["machine", "pager"] }
```

`routed: false` is not an error: the entry matched no route, so nothing asked for it. That is
the one logging failure a log cannot report, so the deployment counts it and `GetConfig`
reports the count as `unrouted`.

### A client configuring its own fanout

A client can state where **its own** entries go, and the scope is enforced rather than
documented:

```sh
toolbox logger set-config --workspace acme \
  --level LOG_LEVEL_DEBUG \
  --handlers '{"name":"agent","provider":"json","options":{"path":"logs/agent.log"}}' \
  --routes  '{"name":"the agent'\''s own","handlers":["agent"],"add":{"sent_by":"agent"}}'
```

```json
{
  "workspace": "acme",
  "source": "FANOUT_SOURCE_CALLER",
  "configured": true,
  "handlers": [{ "name": "agent", "provider": "json", "options": { "path": "logs/agent.log" } }],
  "routes":   [{ "name": "the agent's own", "handlers": ["agent"], "add": { "sent_by": "agent" } }]
}
```

`--handlers` and `--routes` take **one JSON object per occurrence**, each the same message
`GetConfig` reports, so a client that read its fanout can send it back.

Three rules make this safe rather than merely convenient:

- **A fanout must name a workspace.** An unscoped configuration would be a deployment-wide
  one, and that is the one thing a caller cannot configure. There is no request that changes
  the deployment's fanout; that is the configuration file's.
- **It applies only to entries carrying that workspace.** Another workspace's entry looks up a
  different slot and reaches the deployment's own routes. A caller that redirects its own
  logging cannot redirect anyone else's.
- **It may only name a backend the installation has.** A caller cannot use the service to
  reach a sink that was never part of this deployment.

A second configuration for a workspace **replaces** the first, because a fanout is a whole
plan rather than a set of patches.

`GetConfig` and `SetConfig` report `source`, so a caller can tell its own configuration from
its project's file and from the deployment's:

| `source` | The fanout came from |
| --- | --- |
| `FANOUT_SOURCE_DEPLOYMENT` | the deployment's configuration file |
| `FANOUT_SOURCE_PROJECT_FILE` | the named project's own configuration file |
| `FANOUT_SOURCE_CALLER` | a `SetConfig` from a caller, for that workspace |

A workspace name asserts which project a caller is serving; it does not establish identity.
Two callers asserting the same workspace share its configuration. The boundary is *entries
carrying this workspace*, not *entries from this process*.

A relative path in a caller's configuration resolves against the **deployment's**
configuration directory, because the caller is not in this process and cannot know where it
runs. A caller that wants its logs somewhere particular says an absolute path.

### A project's name and a project's location

`workspace` on a request is one field doing a reader's job, and it is resolved as either a
**label** or a **location**:

- a value that **names a directory** is a location: that project's configuration file is
  read, its fanout is built, and the entry is tagged with the directory's name. A route
  written as `match: {workspace: acme}` matches whether the caller said `acme` or said where
  `acme` lives.
- a value that is **only a name** is a label: it is matched against the deployment's own
  routes and no file is looked up. A name says which project a caller is serving, not where
  that project lives, and guessing a directory from a name would put one project's log in
  another's.

`GetConfig` answers with the same resolution `Log` uses, so what a caller is told is the
fanout its entries actually go through.

## What a deployment can see

| Where | What it tells you |
| --- | --- |
| `logs/machine.log` | the first line says which fanout is in use and which file it was read from |
| `GetConfig.failures` | entries a sink refused to write |
| `GetConfig.unrouted` | entries that matched no route and reached no sink |
| `GetConfig.source` | whether a workspace's fanout is the deployment's, its project's, or its own |
| a sink's error on standard error | which route could not write, and why |

`unrouted` is the number worth watching on a quiet deployment: a fanout whose routes no
longer match what it logs has an empty log and no error anywhere.

## Writing a backend

A provider is one method returning a `slog.Handler`:

```go
var Provider = log.ProviderFunc{
    ID:      "loki",
    BuildFn: NewHandler,
}

func NewHandler(options map[string]any) (slog.Handler, error) {
    endpoint, _ := options["endpoint"].(string)
    return slogloki.NewHandler(endpoint, slogloki.WithLevel(slog.LevelDebug)), nil
}
```

Register it where the deployment composes its fanout, and a configuration can name `loki` like
any other backend. Two rules:

- **Honour the `level` option** by passing it to your `slog.HandlerOptions`, so filtering
  happens inside your own `Enabled` rather than in a filter wrapped around it.
- **Refuse a setting you do not understand**, naming it and listing what you accept.

Declare the subsystem in the catalog the way every other provider subsystem does, with the
role `loghandler`, so a deployment can list which logging backends it has through the same
call that lists its parsers and adapters.

## See also

- `docs/decisions/0012-logging.md` — why the API is `slog` and the rest is the ecosystem.
- `docs/configuration.md` — where the `logging` section is read from.
- `docs/subsystems.md` — `logger` and `logfile`.
