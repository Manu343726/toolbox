# toolsbox

Toolsbox is a provider-neutral framework for composable AI-assisted workflows.

The repository is organized as a set of independent Go subsystems. A subsystem owns its implementation, its protobuf contract, its generated clients, its tests, and its standalone command:

```text
subsystems/workflow/
├── proto/workflow.proto
├── workflow.go
├── workflow_test.go
├── docs_embed.go
├── workflowv1/              # generated Go (ignored)
├── workflowv1connect/       # generated ConnectRPC (ignored)
└── cmd/workflow/main.go
```

Each subsystem is a separate Go module and can be built independently:

```sh
cd subsystems/workflow
make test
make build
```

The root workspace uses `go.work` and a top-level Makefile to build/test all subsystems and the combined host.

## Documentation

See [`docs/README.md`](docs/README.md) for the complete project documentation map,
including the feature specification, architecture, subsystem catalog,
development and testing guides, protocol conventions, current development status,
roadmap, and architecture decisions.

## Public foundational packages

- `pkg/subsystem` — transport/lifecycle SDK for serving ConnectRPC services and exposing gRPC reflection.
- `pkg/discovery` — reflection-based service/schema discovery and dynamic unary invocation.
- `pkg/core` — endpoint resolver plus service-to-service calls. Known services can use generated, type-safe Connect clients through `core.Bind`; unknown services use dynamic calls.
- `pkg/docs` — public protobuf documentation parser. Descriptor sets generated with source information preserve comments for CLI help and documentation services.
- `pkg/cli` — public Cobra command generator driven by reflected protobuf schemas.
- `pkg/mcp` — MCP gateway generated from reflected ConnectRPC services, with introspection and runtime feature exposure.
- `pkg/cliapp` — shared standalone-command runner used by every subsystem command.
- `pkg/host` — composes independently-built subsystem factories in one process.

## Type-safe service-to-service calls

The core package follows the typed plugin-to-plugin pattern: resolve the target endpoint first, then construct the target's generated client. If the target cannot be resolved, construction fails before any RPC is attempted.

```go
resolver := core.NewStaticResolver(core.Endpoint{
    Name:         "weather",
    URL:          "http://127.0.0.1:9000",
    ServiceNames: []string{"weather.v1.WeatherService"},
})
client := core.NewClient(core.ClientOptions{Resolver: resolver})

weatherClient, err := core.Bind(
    ctx,
    client,
    "weather.v1.WeatherService",
    weatherconnect.NewWeatherServiceClient,
)
if err != nil {
    return err
}

forecast, err := weatherClient.Forecast(ctx, connect.NewRequest(&weatherv1.ForecastRequest{
    Location: "Madrid",
}))
```

The same `core.Client` can dynamically call a service whose generated contract is not linked into the caller.

## Combined host

Build the single host binary with:

```sh
make host
```

Run one subsystem:

```sh
./bin/toolsbox --component workflow
```

Run all built-in subsystems and register their endpoints in the in-process registry:

```sh
./bin/toolsbox --all
```

The host is only a composition layer. Subsystem packages do not import one another; cross-subsystem calls go through ConnectRPC, discovery, and the resolver/client packages.

## MCP gateway

Every standalone subsystem command automatically includes an `mcp` subcommand:

```sh
./bin/workflow mcp
./bin/workflow mcp --minimal
```

The generated MCP reflects the subsystem's RPC services, turns allowed unary methods into tools, and provides `list_features`, `describe_feature`, `expose_feature`, `hide_feature`, and a generic `call_rpc` tool. Reflection supplies schemas; an explicit feature policy controls authorization. Use `--minimal` to start with only the introspection tools and grow the tool footprint on demand.

The combined host exposes all selected built-in subsystems as one MCP:

```sh
./bin/toolsbox mcp --all
./bin/toolsbox mcp --component workflow --component agent
```

See [`docs/mcp.md`](docs/mcp.md) for the feature model, tool schemas, exposure semantics, and programmatic adapters.

## Protocol generation

Each subsystem Makefile:

1. Compiles the local `proto/*.proto` file.
2. Generates Go protobuf and ConnectRPC code.
3. Generates a descriptor set with source information.
4. Embeds that descriptor set through the subsystem's `docs_embed.go` file.

Generated Go, ConnectRPC, and descriptor-set files are ignored. Run the subsystem's `make proto` (or any build/test target) after changing a proto file.
