package apiopenapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the RPC layer: message conversion, the claims, and the code a
// caller sees. Reading a document, rendering one, serving one, and calling one are
// tested in the package this subsystem serves, and repeating them here would only
// test the conversion twice.

const sampleDocument = `{
  "openapi": "3.1.0",
  "info": {"title": "Pet Store", "version": "1.0.0"},
  "paths": {
    "/pets/{petId}": {
      "parameters": [{"name": "petId", "in": "path", "required": true, "schema": {"type": "string"}}],
      "get": {
        "operationId": "getPet",
        "summary": "Fetch one pet",
        "tags": ["pets"],
        "x-toolbox-capabilities": ["pet.read"],
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"type": "object", "properties": {"name": {"type": "string"}}}}}}}
      }
    }
  }
}`

func TestParserClaimsTheDocumentFormat(t *testing.T) {
	parser := NewParser(ParserOptions{})
	assert.Equal(t, []api.Format{FormatOpenAPI}, parser.Formats())

	narrowed := NewParser(ParserOptions{Formats: []api.Format{" OpenAPI "}})
	assert.Equal(t, []api.Format{"openapi"}, narrowed.Formats(), "a claim is compared without regard to case or padding")
}

func TestParserRejectsForeignFormatAndUnusableInput(t *testing.T) {
	parser := NewParser(ParserOptions{})

	_, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   "grpc",
		Document: []byte(sampleDocument),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "openapi", "a parser says which formats it handles")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{Document: []byte(sampleDocument)}))
	require.Error(t, err, "a document with no declared format is not guessed at")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{Format: FormatOpenAPI}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "document")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatOpenAPI,
		Document: []byte("{not json"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestParserReportsTheFormatDescriptorItImplements(t *testing.T) {
	response, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatOpenAPI,
		Document: []byte(sampleDocument),
		ApiId:    "shop",
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetFormats(), 1)
	assert.Equal(t, string(FormatOpenAPI), response.Msg.GetFormats()[0].GetId())
	assert.Equal(t, Name, response.Msg.GetFormats()[0].GetProvider(),
		"a catalog indexes the subsystem that contributed a format")

	described, err := api.APIFromProto(response.Msg.GetApi())
	require.NoError(t, err)
	assert.Equal(t, "shop", described.ID)
	require.Len(t, described.Services, 1)
}

func TestParserAppliesTheCallersBaseURLWhenTheDocumentDeclaresNone(t *testing.T) {
	response, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatOpenAPI,
		Document: []byte(sampleDocument),
		BaseUrl:  "http://localhost:9090",
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetApi().GetDeclaredServers(), 1)
	assert.Equal(t, "http://localhost:9090", response.Msg.GetApi().GetDeclaredServers()[0].GetUrl(),
		"a caller that knows the endpoint binds the document to it")
}

func TestAdapterRejectsForeignTarget(t *testing.T) {
	adapter := NewAdapter(AdapterOptions{})
	described, warnings, err := openapiParse(t)
	require.NoError(t, err)
	assert.NotNil(t, described)
	assert.NotNil(t, warnings)

	_, err = adapter.RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:    aPetAPI(t).ToProto(),
		Target: "smithy",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "openapi", "an adapter says which targets it produces")

	_, err = adapter.ServeApi(context.Background(), connect.NewRequest(&apiv1.ServeApiRequest{
		Api:    aPetAPI(t).ToProto(),
		Server: api.Server{ID: "shop", Name: "Shop", BaseURL: "http://shop.test"}.ToProto(),
		Target: "smithy",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "openapi", "an adapter says which targets it serves")
}

func TestAdapterRendersAServedDescription(t *testing.T) {
	adapter := NewAdapter(AdapterOptions{})
	response, err := adapter.RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:    aPetAPI(t).ToProto(),
		Target: TargetOpenAPI,
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetFiles(), 1)
	assert.True(t, response.Msg.GetFiles()[0].GetPrimary())
	assert.Equal(t, "openapi.json", response.Msg.GetFiles()[0].GetName())
	require.Len(t, response.Msg.GetTargets(), 1)
	assert.Equal(t, TargetOpenAPI, response.Msg.GetTargets()[0].GetId())
	assert.Equal(t, Name, response.Msg.GetTargets()[0].GetProvider())
}

func TestAdapterRendersYAMLWhenAsked(t *testing.T) {
	adapter := NewAdapter(AdapterOptions{})
	response, err := adapter.RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:       aPetAPI(t).ToProto(),
		Target:    TargetOpenAPI,
		MediaType: "application/yaml",
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetFiles(), 1)
	assert.Equal(t, "openapi.yaml", response.Msg.GetFiles()[0].GetName(),
		"a file is named for the content it holds, so a download is never mislabelled")
	assert.Contains(t, string(response.Msg.GetFiles()[0].GetContent()), "openapi: 3.1.0")
}

func TestAdapterServesAndStopsASurface(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"rex"}`))
	}))
	defer upstream.Close()
	adapter := NewAdapter(AdapterOptions{})

	response, err := adapter.ServeApi(context.Background(), connect.NewRequest(&apiv1.ServeApiRequest{
		Api:    aPetAPI(t).ToProto(),
		Server: api.Server{ID: "shop", Name: "Shop", BaseURL: upstream.URL}.ToProto(),
		Target: TargetOpenAPI,
	}))
	require.NoError(t, err)
	assert.Equal(t, TargetOpenAPI, response.Msg.GetTarget())
	assert.NotEmpty(t, response.Msg.GetEndpoint())
	assert.NotEmpty(t, response.Msg.GetSchemaEndpoint())
	assert.NotEmpty(t, response.Msg.GetDocumentationEndpoint())

	stopped, err := adapter.StopApi(context.Background(), connect.NewRequest(&apiv1.StopApiRequest{
		InstanceId: response.Msg.GetInstanceId(),
	}))
	require.NoError(t, err)
	assert.True(t, stopped.Msg.GetStopped())

	again, err := adapter.StopApi(context.Background(), connect.NewRequest(&apiv1.StopApiRequest{
		InstanceId: response.Msg.GetInstanceId(),
	}))
	require.NoError(t, err)
	assert.False(t, again.Msg.GetStopped(), "a stop for an unknown surface is answered honestly")

	_, err = adapter.StopApi(context.Background(), connect.NewRequest(&apiv1.StopApiRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdapterRequiresAnOriginalServerToForwardTo(t *testing.T) {
	adapter := NewAdapter(AdapterOptions{})
	_, err := adapter.ServeApi(context.Background(), connect.NewRequest(&apiv1.ServeApiRequest{
		Api:    aPetAPI(t).ToProto(),
		Server: api.Server{ID: "shop", Name: "Shop"}.ToProto(),
		Target: TargetOpenAPI,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "base url")
}

func TestInvokerRejectsAnUnusableRequest(t *testing.T) {
	_, err := NewInvoker(InvokerOptions{}).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		Server:    api.Server{ID: "shop", Name: "Shop", BaseURL: "http://shop.test"}.ToProto(),
		Operation: aPetAPI(t).Services[0].Operations[0].ToProto(),
		Api:       aPetAPI(t).ToProto(),
		// A value the description does not declare is refused rather than dropped.
		ArgumentsJson: []byte(`{"petId":"7","missing":"value"}`),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "does not accept the argument",
		"a misspelled argument is reported instead of silently dropped")
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

	assert.True(t, byRole[api.ProviderParser].HandlesFormat(FormatOpenAPI))
	assert.True(t, byRole[api.ProviderAdapter].HandlesTarget(TargetOpenAPI))
	assert.Contains(t, byRole[api.ProviderAdapter].ServiceNames, apiv1connect.ApiAdapterServiceName)
	assert.True(t, byRole[api.ProviderInvoker].HandlesTransport(TransportHTTP))
	assert.True(t, byRole[api.ProviderInvoker].HandlesTransport(TransportHTTPS))
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

func TestCredentialTypesAreThePackagesOwn(t *testing.T) {
	// The subsystem re-exports the credential types rather than defining its own,
	// so a deployment configures the implementation package's contract.
	var source CredentialSource = StaticCredentials{"apiKey": "secret"}
	value, ok := source.Credential(api.SecurityScheme{Name: "apiKey"}, api.SecurityRequirement{Scheme: "apiKey"})
	assert.True(t, ok)
	assert.Equal(t, "secret", value)
}

// aPetAPI is a small described API the adapter can render and serve.
func aPetAPI(t *testing.T) api.API {
	t.Helper()
	described := api.API{
		ID:     "shop",
		Name:   "shop",
		Title:  "Shop",
		Format: FormatOpenAPI,
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

// openapiParse reads the sample document, so a test can reach a described API
// without repeating the document.
func openapiParse(t *testing.T) (*apiv1.Api, []string, error) {
	t.Helper()
	response, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatOpenAPI,
		Document: []byte(sampleDocument),
		ApiId:    "shop",
	}))
	if err != nil {
		return nil, nil, err
	}
	return response.Msg.GetApi(), response.Msg.GetWarnings(), nil
}
