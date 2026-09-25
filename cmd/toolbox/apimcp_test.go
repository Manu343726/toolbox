package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
	apitoolsv1 "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1"
	apitoolsv1connect "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1/apitoolsv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the reason a Model Context Protocol provider exists: a
// third-party MCP server, which serves no contract of the framework's own, is
// registered in the catalog and used like anything else — described, exposed, and
// called.

// startThirdPartyServer runs an MCP server that knows nothing about this framework.
func startThirdPartyServer(t *testing.T) string {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "weather", Version: "2.0.0"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name:        "forecast",
		Description: "Forecast for a city",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"city": map[string]any{"type": "string", "description": "City name"}},
			"required":   []any{"city"},
		},
		Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
		// A server that happens to know the framework's extension declares what its
		// tools grant. One that does not simply omits it.
		Meta: sdkmcp.Meta{"x-toolbox-capabilities": []any{"weather.read"}},
	}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		// A server's handler receives the arguments as the raw JSON the caller sent,
		// because unmarshalling them is the handler's decision.
		arguments := map[string]string{}
		_ = json.Unmarshal(request.Params.Arguments, &arguments)
		city := arguments["city"]
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "sunny in " + city}},
		}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	return endpoint.URL
}

// TestThirdPartyMCPServerBecomesACatalogAPI is the whole feature: a server that
// serves no framework contract is described through a parser, stored, exposed by
// its declared capability, and called through an invoker.
func TestThirdPartyMCPServerBecomesACatalogAPI(t *testing.T) {
	weather := startThirdPartyServer(t)
	h, catalog, err := buildHost()
	require.NoError(t, err)
	require.NoError(t, h.Select("apitools", "apimcp"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { _ = h.Shutdown(context.Background()) }()

	service := catalog.service

	// The server is registered, and the catalog is told it speaks MCP.
	server, _, err := service.Registrar().RegisterServer(context.Background(), api.Server{
		ID:        "weather",
		Name:      "Weather",
		BaseURL:   weather,
		Format:    apimcpFormat,
		Transport: apimcpTransport,
	})
	require.NoError(t, err)
	assert.Equal(t, apimcpTransport, server.Transport)

	// The catalog describes it through a parser provider, the way any registration
	// works, and the description says how its operations are called.
	describable, err := service.Registrar().DescribeAPI(context.Background(), api.DescribeRequest{
		Format:  apimcpFormat,
		BaseURL: weather,
		APIID:   "weather",
	})
	require.NoError(t, err)
	require.NotEmpty(t, describable.ProviderID, "the parser that read it is reported")
	assert.Equal(t, apimcpTransport, describable.API.Transport)

	operation, found := describable.API.Operation("weather/mcp/forecast")
	require.True(t, found)
	assert.Equal(t, "tools/call", operation.Method)
	assert.Equal(t, []string{"weather.read"}, operation.Capabilities)

	stored, _, err := service.Registrar().RegisterAPI(context.Background(), describable.API, server.ID)
	require.NoError(t, err)

	// The operation is exposed because a declared capability covers it.
	changed, err := service.Registrar().SetExposed(context.Background(), stored.ID, operation.ID, true)
	require.NoError(t, err)
	assert.True(t, changed, "a declared capability authorizes the operation")

	// And it is called, through an invoker provider, against the live server.
	result, err := service.Invoker().Invoke(context.Background(), api.Call{
		Server:    server,
		API:       stored,
		Operation: operation,
		Arguments: json.RawMessage(`{"city":"Porto"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, 200, result.Status)
	assert.Contains(t, string(result.Body), "sunny in Porto")
}

func TestAnMCPServerWithNoDeclaredCapabilityIsNotExposed(t *testing.T) {
	// A server that declares no capabilities produces operations a policy refuses,
	// which is the whole point: reading a tool list is not authorization.
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "bare", Version: "1.0.0"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name:        "do_thing",
		Description: "Does a thing",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "done"}}}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	bare := httptest.NewServer(handler)
	defer bare.Close()

	h, catalog, err := buildHost()
	require.NoError(t, err)
	require.NoError(t, h.Select("apitools", "apimcp"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { _ = h.Shutdown(context.Background()) }()

	service := catalog.service
	describable, err := service.Registrar().DescribeAPI(context.Background(), api.DescribeRequest{
		Format:  apimcpFormat,
		BaseURL: bare.URL,
		APIID:   "bare",
	})
	require.NoError(t, err)
	operation, found := describable.API.Operation("bare/mcp/do_thing")
	require.True(t, found)
	assert.Empty(t, operation.Capabilities, "a tool manifest states no authorization facts, so none are invented")

	_, _, err = service.Registrar().RegisterAPI(context.Background(), describable.API, "")
	require.NoError(t, err)
	_, err = service.Registrar().SetExposed(context.Background(), "bare", operation.ID, true)
	require.Error(t, err, "an operation no capability covers cannot be exposed")
}

func TestMCPIsAnIndexedFormatAndTransport(t *testing.T) {
	h, _, err := buildHost()
	require.NoError(t, err)
	require.NoError(t, h.Select("apitools", "apimcp"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { _ = h.Shutdown(context.Background()) }()

	client := apitoolsv1connect.NewApiToolsServiceClient(http.DefaultClient, h.Servers()["apitools"].Endpoint())
	providers, err := client.ListProviders(context.Background(), connect.NewRequest(&apitoolsv1.ListProvidersRequest{}))
	require.NoError(t, err)

	roles := map[string][]string{}
	for _, provider := range providers.Msg.GetProviders() {
		if provider.GetSubsystem() != "apimcp" {
			continue
		}
		roles[provider.GetRole()] = append(roles[provider.GetRole()], provider.GetFormats()...)
		roles[provider.GetRole()] = append(roles[provider.GetRole()], provider.GetTargets()...)
		roles[provider.GetRole()] = append(roles[provider.GetRole()], provider.GetTransports()...)
	}
	assert.Contains(t, roles[api.ProviderParser], "mcp", "the format is discoverable without a registry entry")
	assert.Contains(t, roles[api.ProviderAdapter], "mcp")
	assert.Contains(t, roles[api.ProviderInvoker], "mcp")

	// The index reports what the deployment can do, from the capabilities the
	// running providers declared — no registration step.
	formats, err := client.ListApiFormats(context.Background(), connect.NewRequest(&apitoolsv1.ListApiFormatsRequest{}))
	require.NoError(t, err)
	var entry *apitoolsv1.ApiFormatEntry
	for _, format := range formats.Msg.GetFormats() {
		if format.GetId() == "mcp" {
			entry = format
		}
	}
	require.NotNil(t, entry, "the format index knows the deployment can read and write MCP")
	assert.True(t, entry.GetParserAvailable(), "a deployment with this provider can read an MCP server")
	assert.Contains(t, entry.GetParserIds(), "apimcp-parser", "and says which provider does it")
	assert.False(t, entry.GetDescribed(), "a descriptor is learned when a description is read, not declared in advance")
}

func TestGatewayServesADescriptionAsToolsWithoutAnMCPServer(t *testing.T) {
	// The publisher and the gateway agree, so an API registered from a third-party
	// MCP server can be offered to an agent through the in-process gateway — with no
	// second protocol hop, because the description is already a description.
	weather := startThirdPartyServer(t)
	h, catalog, err := buildHost()
	require.NoError(t, err)
	require.NoError(t, h.Select("apitools", "apimcp"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { _ = h.Shutdown(context.Background()) }()

	service := catalog.service
	server, _, err := service.Registrar().RegisterServer(context.Background(), api.Server{
		ID: "weather", Name: "Weather", BaseURL: weather, Format: apimcpFormat, Transport: apimcpTransport,
	})
	require.NoError(t, err)
	describable, err := service.Registrar().DescribeAPI(context.Background(), api.DescribeRequest{
		Format: apimcpFormat, BaseURL: weather, APIID: "weather",
	})
	require.NoError(t, err)
	stored, _, err := service.Registrar().RegisterAPI(context.Background(), describable.API, server.ID)
	require.NoError(t, err)
	for _, operation := range stored.Operations() {
		_, err := service.Registrar().SetExposed(context.Background(), stored.ID, operation.ID, true)
		require.NoError(t, err)
	}

	gateway, err := toolboxmcp.NewFromAPICatalog(
		context.Background(),
		service.Catalog(),
		service.Invoker(),
		toolboxmcp.APICatalogOptions{},
	)
	require.NoError(t, err)
	features := gateway.Features()
	require.Len(t, features, 1)
	assert.Equal(t, "mcp__forecast", features[0].ToolName,
		"the tool is named the way the server named it, so a client already knows it")
	assert.True(t, features[0].Exposed, "a declared capability exposed it")
	assert.True(t, features[0].Callable)

	// Calling it goes through the catalog's invoker, to the third-party server.
	answered, err := gateway.CallToolForTest(context.Background(), features[0].ID, json.RawMessage(`{"city":"Porto"}`))
	require.NoError(t, err)
	assert.Contains(t, string(answered), "sunny in Porto")
}

// apimcpFormat and apimcpTransport name what this test registers, so the assertions
// read as the identifiers a deployment would use.
const (
	apimcpFormat    api.Format    = "mcp"
	apimcpTransport api.Transport = "mcp"
)
