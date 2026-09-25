package apimcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the RPC layer: the claims, the message conversion, and the
// code a caller sees. Reading a server, rendering a manifest, and calling a tool
// are tested in the package this subsystem serves, and repeating them here would
// only test the conversion twice.

// startServer runs a real MCP server with one tool, so the parser and the invoker
// are tested against something that actually speaks the protocol.
func startServer(t *testing.T) string {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "petstore", Version: "1.0.0"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name:        "get_pet",
		Description: "Fetch one pet",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"petId": map[string]any{"type": "string"}},
			"required":   []any{"petId"},
		},
		Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
		Meta:        sdkmcp.Meta{"x-toolbox-capabilities": []any{"pet.read"}},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"name":"rex"}`}},
		}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	return endpoint.URL
}

func TestParserClaimsTheFormat(t *testing.T) {
	assert.Equal(t, []api.Format{FormatMCP}, NewParser(ParserOptions{}).Formats())
	assert.Equal(
		t,
		[]api.Format{"mcp"},
		NewParser(ParserOptions{Formats: []api.Format{" MCP "}}).Formats(),
		"a claim is compared without regard to case or padding",
	)
}

func TestParserReadsALiveServer(t *testing.T) {
	endpoint := startServer(t)
	response, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:  FormatMCP,
		BaseUrl: endpoint,
		ApiId:   "shop",
	}))
	require.NoError(t, err)

	described, err := api.APIFromProto(response.Msg.GetApi())
	require.NoError(t, err)
	assert.Equal(t, "shop", described.ID)
	assert.Equal(t, FormatMCP, described.Format)
	assert.Equal(t, TransportMCP, described.Transport,
		"a description knows how its operations are called")
	require.Len(t, response.Msg.GetFormats(), 1)
	assert.Equal(t, string(FormatMCP), response.Msg.GetFormats()[0].GetId())
	assert.Equal(t, Name, response.Msg.GetFormats()[0].GetProvider())

	operation, found := described.Operation("shop/mcp/get_pet")
	require.True(t, found)
	assert.Equal(t, "tools/call", operation.Method)
	require.Len(t, described.DeclaredServers, 1)
	assert.Equal(t, endpoint, described.DeclaredServers[0].URL)
}

func TestParserReadsAPublishedManifest(t *testing.T) {
	// A manifest is the same format as a live server, so a deployment can register
	// an API it published earlier, with no server running.
	manifest := `{
	  "protocolVersion": "2025-06-18",
	  "server": {"name": "shop", "description": "The shop tools"},
	  "tools": [{
	    "name": "mcp__get_pet",
	    "description": "Fetch one pet",
	    "inputSchema": {"type": "object", "properties": {"petId": {"type": "string"}}},
	    "x-toolbox-operation": "shop/mcp/get_pet",
	    "x-toolbox-capabilities": ["pet.read"]
	  }]
	}`
	response, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatMCP,
		Document: []byte(manifest),
		ApiId:    "shop",
	}))
	require.NoError(t, err)
	described, err := api.APIFromProto(response.Msg.GetApi())
	require.NoError(t, err)
	_, found := described.Operation("shop/mcp/get_pet")
	require.True(t, found)
}

func TestParserRejectsForeignFormatAndUnusableInput(t *testing.T) {
	parser := NewParser(ParserOptions{})

	_, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   "openapi",
		Document: []byte(`{"tools":[]}`),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "mcp", "a parser says which formats it handles")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{Document: []byte(`{}`)}))
	require.Error(t, err, "a document with no declared format is not guessed at")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{Format: FormatMCP}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "document or base_url")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatMCP,
		Document: []byte("{not json"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestParserReportsAnUnreachableServer(t *testing.T) {
	unreachable := httptest.NewServer(http.NotFoundHandler())
	closed := unreachable.URL
	unreachable.Close()

	_, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:  FormatMCP,
		BaseUrl: closed,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err),
		"a server that cannot be reached is unavailable, which says what to fix")
}

func TestAdapterRendersAToolManifest(t *testing.T) {
	described := petAPI(t)
	adapter := NewAdapter(AdapterOptions{})
	response, err := adapter.RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:    described.ToProto(),
		Target: TargetMCP,
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetFiles(), 1)
	assert.True(t, response.Msg.GetFiles()[0].GetPrimary())
	assert.Equal(t, "mcp-tools.json", response.Msg.GetFiles()[0].GetName())
	assert.Equal(t, "application/json", response.Msg.GetMediaType())
	require.Len(t, response.Msg.GetTargets(), 1)
	assert.Equal(t, TargetMCP, response.Msg.GetTargets()[0].GetId())

	var manifest map[string]any
	require.NoError(t, json.Unmarshal(response.Msg.GetFiles()[0].GetContent(), &manifest))
	tools, ok := manifest["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "pets__get_pet", tool["name"], "the manifest names tools the way a client sees them")
	assert.Equal(t, "shop/pets/getPet", tool["x-toolbox-operation"])
	assert.Contains(t, tool, "annotations", "declared consequences travel, so a reader does not lose them")
}

func TestAdapterHonoursItsOptions(t *testing.T) {
	described := petAPI(t)
	described.Services = append(described.Services, api.Service{
		Name: "toolbox.shop.v1.HealthService",
		Operations: []api.Operation{{
			Name: "Check",
		}},
	})
	normalized, err := described.Normalize()
	require.NoError(t, err)
	adapter := NewAdapter(AdapterOptions{})

	without, err := adapter.RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:    normalized.ToProto(),
		Target: TargetMCP,
	}))
	require.NoError(t, err)
	var plain map[string]any
	require.NoError(t, json.Unmarshal(without.Msg.GetFiles()[0].GetContent(), &plain))
	assert.Len(t, plain["tools"], 2, "every operation the description declares is published")

	with, err := adapter.RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:     normalized.ToProto(),
		Target:  TargetMCP,
		Options: map[string]string{"instructions": "Use sparingly."},
	}))
	require.NoError(t, err)
	var inclusive map[string]any
	require.NoError(t, json.Unmarshal(with.Msg.GetFiles()[0].GetContent(), &inclusive))
	assert.Len(t, inclusive["tools"], 2)
	server, ok := inclusive["server"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Use sparingly.", server["description"])
}

func TestAdapterRefusesASwitchThatNoLongerExists(t *testing.T) {
	// The switch this replaces withheld operations by service name. Nothing is
	// withheld by name any more, so it is refused rather than silently ignored:
	// a caller that passes it should learn that it is not doing what it thinks.
	_, err := NewAdapter(AdapterOptions{}).RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:     petAPI(t).ToProto(),
		Target:  TargetMCP,
		Options: map[string]string{"include-infrastructure": "true"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "unknown option")
}

func TestAdapterRefusesAnUnknownOption(t *testing.T) {
	// A misspelled switch would otherwise produce a document that quietly differs
	// from what the caller asked for.
	_, err := NewAdapter(AdapterOptions{}).RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:     petAPI(t).ToProto(),
		Target:  TargetMCP,
		Options: map[string]string{"include-infrastructre": "true"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "unknown option")
}

func TestAdapterRejectsForeignTarget(t *testing.T) {
	_, err := NewAdapter(AdapterOptions{}).RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:    petAPI(t).ToProto(),
		Target: "protoset",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "mcp")
}

func TestAdapterRefusesToServeAndSaysWhy(t *testing.T) {
	// Forwarding a tool call needs an invoker for the original API's transport, which
	// the catalog selects. Saying that is more useful than a surface whose calls
	// would all fail.
	_, err := NewAdapter(AdapterOptions{}).ServeApi(context.Background(), connect.NewRequest(&apiv1.ServeApiRequest{
		Api:    petAPI(t).ToProto(),
		Server: api.Server{ID: "shop", Name: "Shop", BaseURL: "http://shop.test", Transport: "connectrpc"}.ToProto(),
		Target: TargetMCP,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "invoker")
	assert.Contains(t, err.Error(), "connectrpc", "the message names the transport that would be needed")
}

func TestAdapterStopReportsNothingToStop(t *testing.T) {
	stopped, err := NewAdapter(AdapterOptions{}).StopApi(context.Background(), connect.NewRequest(&apiv1.StopApiRequest{
		InstanceId: "anything",
	}))
	require.NoError(t, err)
	assert.False(t, stopped.Msg.GetStopped(), "this adapter serves no surface, so there is nothing to stop")

	_, err = NewAdapter(AdapterOptions{}).StopApi(context.Background(), connect.NewRequest(&apiv1.StopApiRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestInvokerCallsATool(t *testing.T) {
	endpoint := startServer(t)
	described, err := api.APIFromProto(mustParse(t, endpoint).GetApi())
	require.NoError(t, err)
	operation, found := described.Operation("shop/mcp/get_pet")
	require.True(t, found)

	invoker := NewInvoker(InvokerOptions{})
	t.Cleanup(func() { _ = invoker.Close() })
	response, err := invoker.InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		Server:        api.Server{ID: "petstore", Name: "Pet store", BaseURL: endpoint, Transport: TransportMCP}.ToProto(),
		Api:           described.ToProto(),
		Operation:     operation.ToProto(),
		ArgumentsJson: []byte(`{"petId":"7"}`),
	}))
	require.NoError(t, err)
	assert.Equal(t, int32(200), response.Msg.GetStatus())
	assert.Contains(t, string(response.Msg.GetBodyJson()), "rex", "the server's own answer comes back")
}

func TestInvokerRejectsMalformedArguments(t *testing.T) {
	endpoint := startServer(t)
	described, err := api.APIFromProto(mustParse(t, endpoint).GetApi())
	require.NoError(t, err)
	operation, _ := described.Operation("shop/mcp/get_pet")

	_, err = NewInvoker(InvokerOptions{}).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		Server:        api.Server{ID: "petstore", Name: "Pet store", BaseURL: endpoint}.ToProto(),
		Api:           described.ToProto(),
		Operation:     operation.ToProto(),
		ArgumentsJson: []byte(`["not an object"]`),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestProvidersDescribeEveryContractThisSubsystemImplements(t *testing.T) {
	byRole := make(map[string]api.Provider)
	for _, provider := range Providers("http://127.0.0.1:0") {
		if _, exists := byRole[provider.Role]; !exists {
			byRole[provider.Role] = provider
		}
	}
	require.Contains(t, byRole, api.ProviderParser)
	require.Contains(t, byRole, api.ProviderAdapter)
	require.Contains(t, byRole, api.ProviderInvoker)

	assert.True(t, byRole[api.ProviderParser].HandlesFormat(FormatMCP))
	assert.True(t, byRole[api.ProviderAdapter].HandlesTarget(TargetMCP))
	assert.True(t, byRole[api.ProviderInvoker].HandlesTransport(TransportMCP))
	assert.Contains(t, byRole[api.ProviderAdapter].ServiceNames, apiv1connect.ApiAdapterServiceName)
}

func TestNewServesAllThreeContracts(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)
	descriptor := server.Descriptor()
	assert.Equal(t, Name, descriptor.SubsystemName)
	names := map[string]bool{}
	for _, name := range descriptor.ServiceNames {
		names[name] = true
	}
	assert.True(t, names[apiv1connect.ApiParserServiceName])
	assert.True(t, names[apiv1connect.ApiAdapterServiceName])
	assert.True(t, names[apiv1connect.ApiInvokerServiceName])
}

func TestDescriptorsAreStampedWithTheSubsystem(t *testing.T) {
	format := FormatDescriptor()
	assert.Equal(t, string(FormatMCP), format.ID)
	assert.Equal(t, Name, format.Provider, "a catalog indexes the subsystem that contributed a format")

	target := TargetDescriptor()
	assert.Equal(t, TargetMCP, target.ID)
	assert.Equal(t, Name, target.Provider)

	transports := TransportDescriptors()
	require.Len(t, transports, 1)
	assert.Equal(t, string(TransportMCP), transports[0].ID)
	assert.Equal(t, Name, transports[0].Provider)
}

// mustParse reads a live server through the parser, for tests that need a
// description without repeating the request.
func mustParse(t *testing.T, endpoint string) *apiv1.ParseApiResponse {
	t.Helper()
	response, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:  FormatMCP,
		BaseUrl: endpoint,
		ApiId:   "shop",
	}))
	require.NoError(t, err)
	return response.Msg
}

// petAPI is a small described API the adapter can render.
func petAPI(t *testing.T) api.API {
	t.Helper()
	described := api.API{
		ID:     "shop",
		Name:   "shop",
		Title:  "Shop",
		Format: "openapi",
		Services: []api.Service{{
			Name: "pets",
			Operations: []api.Operation{{
				Name:        "getPet",
				Method:      "get",
				Path:        "/pets/{petId}",
				Summary:     "Fetch one pet",
				SideEffects: []api.SideEffect{api.SideEffectReadOnly},
				Parameters: []api.Parameter{{
					Name:     "petId",
					In:       api.ParameterInPath,
					Required: true,
					Schema:   api.StringSchema(),
				}},
				Response: api.ObjectSchema(api.Property{Name: "name", Schema: api.StringSchema()}),
			}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)
	return normalized
}
