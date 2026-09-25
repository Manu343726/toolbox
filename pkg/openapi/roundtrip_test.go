package openapi_test

import (
	"encoding/json"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	"github.com/Manu343726/toolbox/pkg/openapi"
	"github.com/Manu343726/toolbox/pkg/protocontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

// These tests are the point of the standard model, exercised end to end: a
// protobuf contract is read into the standard description, and this package —
// which has never heard of protobuf — renders that description into an OpenAPI
// document and reads it back.
//
// The packages meet in process, with no provider subsystem and no transport
// between them. That is what the split into reusable packages buys: the
// composition is a function call, so it can be tested as one.

func contractDescriptorSet(t *testing.T) []byte {
	t.Helper()
	encoded, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(apiv1.File_toolbox_api_v1_api_proto),
		},
	})
	require.NoError(t, err)
	return encoded
}

// renderFromContract reads a contract and renders it, which is the whole chain.
func renderFromContract(t *testing.T, document []byte) map[string]any {
	t.Helper()
	described, _, err := protocontract.NewDescriptor(protocontract.Descriptor{APIID: "framework"}).FromDescriptorSet(document)
	require.NoError(t, err)
	assert.Equal(t, protocontract.Format, described.Format)

	rendered, err := openapi.Render(described, openapi.RenderOptions{})
	require.NoError(t, err)
	assert.Equal(t, "application/json", rendered.MediaType)
	assert.Equal(t, "json", rendered.FileExtension)
	require.Len(t, rendered.Files, 1)
	assert.True(t, rendered.Files[0].Primary, "the file a client should start from is marked")
	assert.Equal(t, "openapi.json", rendered.Files[0].Name)

	var output map[string]any
	require.NoError(t, json.Unmarshal(rendered.Files[0].Content, &output))
	return output
}

func TestRendersAProtobufContractAsAnOpenAPIDocument(t *testing.T) {
	document := renderFromContract(t, contractDescriptorSet(t))

	assert.Equal(t, "3.1.0", document["openapi"])
	info, ok := document["info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "toolbox.api.v1.ApiAdapterService", info["title"])

	paths, ok := document["paths"].(map[string]any)
	require.True(t, ok)

	// A protobuf contract declares a method and no path, so the renderer applies
	// the conventional mapping: a remote procedure becomes a POST under a derived
	// path. Both derivations are reported, because a rendered document must not
	// present a derived value as a declared one.
	// The derived path uses the service's short name, because a package prefix is
	// path noise rather than information.
	entry, ok := paths["/apiadapter/RenderApi"].(map[string]any)
	require.True(t, ok, "paths: %v", paths)
	post, ok := entry["post"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "RenderApi", post["operationId"])
}

func TestReportsOperationsOpenAPICannotRepresent(t *testing.T) {
	// With the conventional mapping disabled, a description whose operations have
	// no path and no HTTP method cannot become an OpenAPI document; the renderer
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

	_, err = openapi.Render(normalized, openapi.RenderOptions{
		Options: map[string]string{"derive-http-mapping": "false"},
	})
	require.Error(t, err, "a description with nothing representable is refused, not rendered as something empty")
	assert.Contains(t, err.Error(), "no operation that OpenAPI can represent")
}

func TestRenderedDocumentCanBeReadBack(t *testing.T) {
	// A rendered document must be readable by the reader that owns the target,
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

	rendered, err := openapi.Render(normalized, openapi.RenderOptions{})
	require.NoError(t, err)

	reparsed, _, err := openapi.Parse(rendered.Files[0].Content, openapi.ParseOptions{APIID: "shop"})
	require.NoError(t, err)

	operation, found := reparsed.Operation("shop/pets/getPet")
	require.True(t, found)
	assert.Equal(t, []string{"pet.read"}, operation.Capabilities, "capabilities survive the round trip")
	assert.Equal(t, []api.SideEffect{api.SideEffectReadOnly}, operation.SideEffects)
	assert.Len(t, operation.Parameters, 1)
	assert.Equal(t, api.ParameterInPath, operation.Parameters[0].In)
}

func TestRenderedYAMLIsReadableAndNamedForItsContent(t *testing.T) {
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

	rendered, err := openapi.Render(normalized, openapi.RenderOptions{MediaType: "application/yaml"})
	require.NoError(t, err)
	assert.Equal(t, "openapi.yaml", rendered.Files[0].Name, "the file is named for the content it holds")
	assert.Equal(t, "yaml", rendered.FileExtension)
	assert.Contains(t, string(rendered.Files[0].Content), "openapi: 3.1.0")

	// YAML this package produced is readable by its own reader.
	_, _, err = openapi.Parse(rendered.Files[0].Content, openapi.ParseOptions{})
	require.NoError(t, err)
}

func TestTargetAndFormatClaimsAreCaseInsensitive(t *testing.T) {
	assert.True(t, openapi.HandlesTarget("OpenAPI"))
	assert.True(t, openapi.HandlesTarget(" openapi "))
	assert.True(t, openapi.HandlesFormat("OPENAPI"))
	assert.False(t, openapi.HandlesTarget("smithy"), "a target this package does not produce is not claimed")
	assert.False(t, openapi.HandlesFormat("grpc"))
	assert.Equal(t, []string{"openapi"}, openapi.Targets())
	assert.Equal(t, []api.Format{"openapi"}, openapi.Formats())
}

func TestDescriptorsDescribeTheFormatAndTheTarget(t *testing.T) {
	format := openapi.FormatDescriptor()
	assert.Equal(t, "openapi", format.ID)
	assert.Contains(t, format.FileExtensions, "yaml")
	assert.Empty(t, format.Provider, "a package is not a subsystem, so it names no provider")

	target := openapi.TargetDescriptor()
	assert.Equal(t, "openapi", target.ID)
	assert.Equal(t, "3.1.0", target.SpecificationVersion)

	transports := openapi.TransportDescriptors()
	require.Len(t, transports, 2)
	assert.Equal(t, "http", transports[0].ID)
	assert.Equal(t, "https", transports[1].ID)
}
