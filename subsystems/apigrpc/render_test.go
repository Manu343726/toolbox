package apigrpc

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	apiopenapi "github.com/Manu343726/toolbox/subsystems/apiopenapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderOpenAPIFromContract is the whole point of the standard model, exercised
// end to end: a protobuf contract is parsed into the standard description, and
// an adapter that has never heard of protobuf renders that description into an
// OpenAPI document. The renderer is not told where the description came from.
func renderOpenAPIFromContract(t *testing.T, document []byte) map[string]any {
	t.Helper()

	parser := NewParser(ParserOptions{})
	parsed, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatGRPC,
		Document: document,
		ApiId:    "echo",
	}))
	require.NoError(t, err)
	assert.Equal(t, FormatGRPC, FormatFromDescriptor(parsed.Msg.GetFormats()[0]))

	rendered, err := apiopenapi.NewRenderer().RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:    parsed.Msg.GetApi(),
		Target: apiopenapi.TargetOpenAPI,
	}))
	require.NoError(t, err)
	assert.Equal(t, "application/json", rendered.Msg.GetMediaType())
	assert.Equal(t, "json", rendered.Msg.GetFileExtension())
	require.Len(t, rendered.Msg.GetTargets(), 1)
	assert.Equal(t, apiopenapi.TargetOpenAPI, rendered.Msg.GetTargets()[0].GetId())

	// The target's schema comes back as a file set, with one primary file a
	// client can start from.
	require.Len(t, rendered.Msg.GetFiles(), 1)
	primary := rendered.Msg.GetFiles()[0]
	assert.True(t, primary.GetPrimary())
	assert.Equal(t, "openapi.json", primary.GetName())

	var output map[string]any
	require.NoError(t, json.Unmarshal(primary.GetContent(), &output))
	return output
}

func TestRendererTurnsProtobufContractIntoOpenAPIDocument(t *testing.T) {
	document := renderOpenAPIFromContract(t, echoDescriptorSet(t))

	assert.Equal(t, "3.1.0", document["openapi"])
	info, ok := document["info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "toolbox.testecho.v1.EchoService", info["title"])

	paths, ok := document["paths"].(map[string]any)
	require.True(t, ok)

	// A protobuf contract declares a method and no path, so the renderer applies
	// the conventional mapping: a remote procedure becomes a POST under a
	// derived path. Both derivations are reported, because a rendered document
	// must not present a derived path as a declared one.
	entry, ok := paths["/echo/Echo"].(map[string]any)
	require.True(t, ok, "paths: %v", paths)
	post, ok := entry["post"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Echo", post["operationId"])
}

// FormatFromDescriptor reads the id a provider reported, for the assertions
// above.
func FormatFromDescriptor(descriptor *apiv1.ApiFormatDescriptor) string { return descriptor.GetId() }

func TestRendererReportsOperationsOpenAPICannotRepresent(t *testing.T) {
	// With the conventional mapping disabled, a description whose operations have
	// no path and no HTTP method cannot become an OpenAPI document; the adapter
	// says so instead of producing something misleading.
	described := api.API{
		ID:     "pathless",
		Name:   "pathless",
		Format: "grpc",
		Services: []api.Service{{
			Name:       "svc",
			Operations: []api.Operation{{Name: "Unary", Method: "Unary"}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)

	_, err = apiopenapi.NewRenderer().RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:     normalized.ToProto(),
		Target:  apiopenapi.TargetOpenAPI,
		Options: map[string]string{"derive-http-mapping": "false"},
	}))
	require.Error(t, err, "a description with nothing representable is refused, not rendered as something empty")
	assert.Contains(t, err.Error(), "no operation that OpenAPI can represent")
}

func TestRendererRejectsForeignTarget(t *testing.T) {
	described := api.API{
		ID:     "svc",
		Name:   "svc",
		Format: "grpc",
		Services: []api.Service{{
			Name:       "s",
			Operations: []api.Operation{{Name: "Get", Method: "get", Path: "/x"}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)

	_, err = apiopenapi.NewRenderer().RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:    normalized.ToProto(),
		Target: "smithy",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "openapi")
}

func TestRenderedDocumentCanBeParsedBack(t *testing.T) {
	// A rendered document must be readable by the parser that owns the target,
	// otherwise publishing an API in a second format loses the declarations the
	// catalog needs.
	described := api.API{
		ID:     "shop",
		Name:   "shop",
		Format: "grpc",
		Services: []api.Service{{
			Name: "pets",
			Operations: []api.Operation{{
				Name:         "getPet",
				Method:       "get",
				Path:         "/pets/{petId}",
				Summary:      "Fetch one pet",
				Capabilities: []string{"pet.read"},
				SideEffects:  []api.SideEffect{api.SideEffectReadOnly},
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

	rendered, err := apiopenapi.NewRenderer().RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:    normalized.ToProto(),
		Target: apiopenapi.TargetOpenAPI,
	}))
	require.NoError(t, err)

	parsed, err := apiopenapi.NewParser(apiopenapi.ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   apiopenapi.FormatOpenAPI,
		Document: rendered.Msg.GetFiles()[0].GetContent(),
		ApiId:    "shop",
	}))
	require.NoError(t, err)

	reparsed, err := api.APIFromProto(parsed.Msg.GetApi())
	require.NoError(t, err)
	operation, found := reparsed.Operation("shop/pets/getPet")
	require.True(t, found)
	assert.Equal(t, []string{"pet.read"}, operation.Capabilities, "capabilities survive the round trip")
	assert.Equal(t, []api.SideEffect{api.SideEffectReadOnly}, operation.SideEffects)
	assert.Len(t, operation.Parameters, 1)
	assert.Equal(t, api.ParameterInPath, operation.Parameters[0].In)
}

func TestRendererYAMLOutput(t *testing.T) {
	described := api.API{
		ID:     "shop",
		Name:   "shop",
		Format: "grpc",
		Services: []api.Service{{
			Name:       "pets",
			Operations: []api.Operation{{Name: "listPets", Method: "get", Path: "/pets"}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)

	rendered, err := apiopenapi.NewRenderer().RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:       normalized.ToProto(),
		Target:    apiopenapi.TargetOpenAPI,
		MediaType: "application/yaml",
	}))
	require.NoError(t, err)
	yamlFile := rendered.Msg.GetFiles()[0]
	assert.Contains(t, string(yamlFile.GetContent()), "openapi: 3.1.0")

	// YAML the renderer produced is readable by the parser that owns the target.
	_, err = apiopenapi.NewParser(apiopenapi.ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   apiopenapi.FormatOpenAPI,
		Document: yamlFile.GetContent(),
	}))
	require.NoError(t, err)
}

func TestProvidersDescribeEveryContractThisSubsystemImplements(t *testing.T) {
	// This subsystem reads a protobuf contract and calls one. It does not render,
	// so it claims no target: a deployment gets that from a rendering provider.
	byRole := make(map[string]api.Provider)
	for _, provider := range Providers("http://127.0.0.1:0") {
		if _, exists := byRole[provider.Role]; !exists {
			byRole[provider.Role] = provider
		}
	}
	require.Contains(t, byRole, api.ProviderParser)
	require.Contains(t, byRole, api.ProviderInvoker)
	assert.NotContains(t, byRole, api.ProviderAdapter)

	assert.True(t, byRole[api.ProviderParser].HandlesFormat(FormatGRPC))
	assert.True(t, byRole[api.ProviderInvoker].HandlesTransport(TransportConnectRPC))
	assert.Contains(t, byRole[api.ProviderInvoker].ServiceNames, apiv1connect.ApiInvokerServiceName)

	// The OpenAPI provider is the one that renders, and it declares a target.
	openapiRoles := make(map[string]bool)
	for _, provider := range apiopenapi.Providers("http://127.0.0.1:0") {
		if provider.HandlesTarget("openapi") {
			openapiRoles[provider.Role] = true
			assert.Contains(t, provider.ServiceNames, apiv1connect.ApiAdapterServiceName)
		}
	}
	assert.True(t, openapiRoles[api.ProviderAdapter])
}
