package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func catalogAPI() api.API {
	described := api.API{
		ID:        "shop",
		Name:      "shop",
		Version:   "1.0.0",
		Title:     "Shop",
		Format:    "openapi",
		ServerIDs: []string{"shop-host"},
		Services: []api.Service{{
			Name:        "pets",
			Description: "Pet operations",
			Operations: []api.Operation{{
				Name:         "getPet",
				Method:       "get",
				Path:         "/pets/{petId}",
				Summary:      "Fetch one pet",
				Capabilities: []string{"pet.read"},
				SideEffects:  []api.SideEffect{api.SideEffectReadOnly},
				Parameters: []api.Parameter{{
					Name:        "petId",
					In:          api.ParameterInPath,
					Description: "Pet identifier",
					Required:    true,
					Schema:      api.StringSchema(),
				}},
				Response: api.ObjectSchema(api.Property{Name: "name", Schema: api.StringSchema()}),
			}},
		}},
	}
	normalized, err := described.Normalize()
	if err != nil {
		panic(err)
	}
	return normalized
}

func catalogServer() api.Server {
	return api.Server{
		ID:        "shop-host",
		Name:      "Shop host",
		BaseURL:   "http://shop.test",
		Format:    "openapi",
		Transport: "http",
	}
}

func staticCatalogForTest() *api.StaticCatalog {
	return &api.StaticCatalog{
		RegisteredServers: []api.Server{catalogServer()},
		RegisteredAPIs:    []api.API{catalogAPI()},
	}
}

func TestNewFromAPICatalogGeneratesToolsFromDescriptions(t *testing.T) {
	server, err := NewFromAPICatalog(context.Background(), staticCatalogForTest(), api.InvokerFunc(
		func(_ context.Context, call api.Call) (api.Result, error) {
			return api.Result{Status: 200, Body: json.RawMessage(`{"name":"rex"}`)}, nil
		},
	), APICatalogOptions{Options: Options{Policy: APIPolicy()}})
	require.NoError(t, err)

	features := server.Features()
	require.Len(t, features, 1)
	feature := features[0]
	assert.Equal(t, "shop.pets/getPet", feature.ID)
	assert.Equal(t, "shop.pets", feature.Service)
	assert.Equal(t, "getPet", feature.Method)
	assert.Equal(t, "pets__get_pet", feature.ToolName)
	assert.Equal(t, "Fetch one pet", feature.Description)
	assert.Equal(t, []api.SideEffect{api.SideEffectReadOnly}, feature.SideEffects,
		"the feature reports what the description says invoking it does")
	assert.True(t, feature.Allowed)
	assert.True(t, feature.Callable)
	assert.True(t, feature.Exposed, "the default starts with allowed operations exposed")

	summaries := server.Services()
	require.Len(t, summaries, 1)
	assert.Equal(t, "Pet operations", summaries[0].Description)
}

func TestNewFromAPICatalogBuildsArgumentSchemaFromParameters(t *testing.T) {
	server, err := NewFromAPICatalog(context.Background(), staticCatalogForTest(), nil, APICatalogOptions{Options: Options{Policy: APIPolicy()}})
	require.NoError(t, err)

	entry, err := server.lookupFeature("shop.pets/getPet")
	require.NoError(t, err)
	schema := entry.inputSchema
	assert.Equal(t, "object", schema["type"])
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	petID, ok := properties["petId"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", petID["type"])
	assert.Equal(t, "Pet identifier", petID["description"])
	assert.Equal(t, []string{"petId"}, schema["required"])
}

func TestNewFromAPICatalogWithoutInvokerIsIntrospectableNotCallable(t *testing.T) {
	server, err := NewFromAPICatalog(context.Background(), staticCatalogForTest(), nil, APICatalogOptions{Options: Options{Policy: APIPolicy()}})
	require.NoError(t, err)

	features := server.Features()
	require.Len(t, features, 1)
	assert.False(t, features[0].Callable, "with no invocation path the operation is described, not called")
	assert.False(t, features[0].Exposed, "an operation that cannot be called is not exposed as a tool")
}

func TestNewFromAPICatalogInvokesRegisteredOperation(t *testing.T) {
	var received api.Call
	server, err := NewFromAPICatalog(context.Background(), staticCatalogForTest(), api.InvokerFunc(
		func(_ context.Context, call api.Call) (api.Result, error) {
			received = call
			return api.Result{Status: 200, ContentType: "application/json", Body: json.RawMessage(`{"name":"rex"}`)}, nil
		},
	), APICatalogOptions{Options: Options{Policy: APIPolicy()}})
	require.NoError(t, err)

	result, err := server.callFeature(context.Background(), "shop.pets/getPet", json.RawMessage(`{"petId":"7"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Content)
	assert.JSONEq(t, `{"name":"rex"}`, textOf(t, result))

	// The call carries everything the provider needs: the server to reach, the
	// description, and the operation.
	assert.Equal(t, "shop-host", received.Server.ID)
	assert.Equal(t, "http://shop.test", received.Server.BaseURL)
	assert.Equal(t, "shop", received.API.ID)
	assert.Equal(t, "getPet", received.Operation.Name)
	assert.JSONEq(t, `{"petId":"7"}`, string(received.Arguments))
}

func TestNewFromAPICatalogRespectsExposureLifecycle(t *testing.T) {
	server, err := NewFromAPICatalog(context.Background(), staticCatalogForTest(), api.InvokerFunc(
		func(context.Context, api.Call) (api.Result, error) {
			return api.Result{Body: json.RawMessage(`{}`)}, nil
		},
	), APICatalogOptions{Options: Options{Policy: APIPolicy(), InitialExposure: ExposeNoFeatures}})
	require.NoError(t, err)

	assert.False(t, server.Features()[0].Exposed)
	// A refused call is reported as a tool error rather than a transport error,
	// because the model is the one that has to see what went wrong.
	refused, err := server.callFeature(context.Background(), "shop.pets/getPet", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, refused.IsError)
	assert.Contains(t, textOf(t, refused), "hidden")

	require.NoError(t, server.Expose("shop.pets/getPet"))
	assert.True(t, server.IsExposed("shop.pets/getPet"))
	_, err = server.callFeature(context.Background(), "shop.pets/getPet", json.RawMessage(`{}`))
	require.NoError(t, err)

	require.NoError(t, server.Hide("shop.pets/getPet"))
	assert.False(t, server.IsExposed("shop.pets/getPet"))
}

func TestNewFromAPICatalogDeniesWhatPolicyRejects(t *testing.T) {
	server, err := NewFromAPICatalog(
		context.Background(),
		staticCatalogForTest(),
		api.InvokerFunc(func(context.Context, api.Call) (api.Result, error) { return api.Result{}, nil }),
		APICatalogOptions{Options: Options{Policy: api.DenyAll()}},
	)
	require.NoError(t, err)

	features := server.Features()
	require.Len(t, features, 1)
	assert.False(t, features[0].Allowed)
	assert.False(t, features[0].Exposed)
	assert.Error(t, server.Expose("shop.pets/getPet"), "a denied operation cannot be exposed")
}

func TestNewFromAPICatalogRejectsStreamingAndUnboundOperations(t *testing.T) {
	described := catalogAPI()
	described.Services[0].Operations = append(described.Services[0].Operations, api.Operation{
		Name:        "streamPets",
		Method:      "get",
		Path:        "/pets/stream",
		Streaming:   api.Streaming{Server: true},
		SideEffects: []api.SideEffect{api.SideEffectReadOnly},
	})
	normalized, err := described.Normalize()
	require.NoError(t, err)
	// The API is described but not bound to a server, so its operations can be
	// read and not called.
	normalized.ServerIDs = nil

	catalog := &api.StaticCatalog{RegisteredAPIs: []api.API{normalized}}
	server, err := NewFromAPICatalog(context.Background(), catalog, api.InvokerFunc(
		func(context.Context, api.Call) (api.Result, error) { return api.Result{}, nil },
	), APICatalogOptions{Options: Options{Policy: APIPolicy()}})
	require.NoError(t, err)

	features := server.Features()
	require.Len(t, features, 2)
	byID := map[string]Feature{}
	for _, feature := range features {
		byID[feature.ID] = feature
	}
	assert.False(t, byID["shop.pets/streamPets"].Callable, "a streaming operation is described but not callable")
	assert.False(t, byID["shop.pets/getPet"].Callable, "an API with no registered server is not callable")
}

func TestNewFromAPICatalogRejectsMissingCatalog(t *testing.T) {
	_, err := NewFromAPICatalog(context.Background(), nil, nil, APICatalogOptions{Options: Options{Policy: APIPolicy()}})
	require.Error(t, err)
}

func TestAPICatalogViewSummarizesCatalog(t *testing.T) {
	summaries, err := APICatalogView(context.Background(), staticCatalogForTest())
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "shop", summaries[0].GetId())
	assert.Equal(t, int32(1), summaries[0].GetOperations())
	assert.Equal(t, []string{"shop-host"}, summaries[0].GetServerIds())
}

// textOf returns the text of a tool result, whichever content type the SDK used.
func textOf(t *testing.T, result *sdkmcp.CallToolResult) string {
	t.Helper()
	for _, content := range result.Content {
		if text, ok := content.(*sdkmcp.TextContent); ok {
			return text.Text
		}
	}
	t.Fatalf("tool result carried no text content: %#v", result.Content)
	return ""
}
