package protocontract

import (
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The fixture for these tests is the framework's own contract. A package that
// reads contracts should be tested against a real one, and the root module has
// one: it does not need a subsystem module to have something to describe.

// descriptorSetFor renders a file's FileDescriptorSet, which is what a reader
// receives when a caller hands it a compiled contract rather than an endpoint.
func descriptorSetFor(t *testing.T, file protoreflect.FileDescriptor) []byte {
	t.Helper()
	encoded, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{protodesc.ToFileDescriptorProto(file)},
	})
	require.NoError(t, err)
	return encoded
}

// contractDescriptorSet is the framework's own extension contract, as bytes.
func contractDescriptorSet(t *testing.T) []byte {
	t.Helper()
	return descriptorSetFor(t, apiv1.File_toolbox_api_v1_api_proto)
}

// streamingDescriptorSet builds a contract with a streaming method, because the
// framework's own contract has none and the streaming path still has to be
// described honestly.
func streamingDescriptorSet(t *testing.T) []byte {
	t.Helper()
	descriptor := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("toolbox/streaming/v1/stream.proto"),
		Package: proto.String("toolbox.streaming.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Request"),
		}, {
			Name: proto.String("Response"),
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("StreamService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:            proto.String("Stream"),
				InputType:       proto.String(".toolbox.streaming.v1.Request"),
				OutputType:      proto.String(".toolbox.streaming.v1.Response"),
				ServerStreaming: proto.Bool(true),
			}},
		}},
	}
	encoded, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{descriptor},
	})
	require.NoError(t, err)
	return encoded
}

func parseForTest(t *testing.T, document []byte) api.API {
	t.Helper()
	described, _, err := NewDescriptor(Descriptor{APIID: "contract"}).FromDescriptorSet(document)
	require.NoError(t, err)
	return described
}

func TestDescriptorReadsContractFromDescriptorSet(t *testing.T) {
	parsed := parseForTest(t, contractDescriptorSet(t))

	assert.Equal(t, "contract", parsed.ID)
	assert.Equal(t, Format, parsed.Format)
	assert.Equal(t, SourceKindDescriptorSet, parsed.Source.Kind)
	assert.NotEmpty(t, parsed.Source.Digest, "a described contract carries the digest of what was read")
	require.NotEmpty(t, parsed.Services)

	names := make([]string, 0, len(parsed.Services))
	for _, service := range parsed.Services {
		names = append(names, service.Name)
		assert.Equal(t, service.ID, "contract/"+service.Name)
	}
	assert.Contains(t, names, "toolbox.api.v1.ApiParserService")
	assert.Contains(t, names, "toolbox.api.v1.ApiAdapterService")
	assert.Contains(t, names, "toolbox.api.v1.ApiInvokerService")
}

func TestDescriptorDerivesAnIdentifierWhenNoneIsGiven(t *testing.T) {
	described, _, err := NewDescriptor(Descriptor{}).FromDescriptorSet(contractDescriptorSet(t))
	require.NoError(t, err)
	// The identifier comes from the first service in a deterministic order, so the
	// same contract always describes the same API. The contract's services are
	// sorted, and the adapter service comes first alphabetically.
	assert.Equal(t, "api-adapter", described.ID)
}

func TestDescriptorDescribesRequestAndResponse(t *testing.T) {
	parsed := parseForTest(t, contractDescriptorSet(t))
	render, ok := parsed.Operation(operationID(t, parsed, "toolbox.api.v1.ApiAdapterService", "RenderApi"))
	require.True(t, ok)

	assert.Equal(t, "RenderApi", render.Method)
	require.NotNil(t, render.Request)
	target, ok := render.Request.Property("target")
	require.True(t, ok)
	assert.Equal(t, api.TypeString, target.Schema.Type)

	require.NotNil(t, render.Response)
	files, ok := render.Response.Property("files")
	require.True(t, ok)
	assert.Equal(t, api.TypeArray, files.Schema.Type)
}

func TestDescriptorMarksStreamingWithoutInvokingIt(t *testing.T) {
	parsed := parseForTest(t, streamingDescriptorSet(t))
	stream, ok := parsed.Operation(operationID(t, parsed, "toolbox.streaming.v1.StreamService", "Stream"))
	require.True(t, ok)

	assert.True(t, stream.Streaming.Server, "a server-streaming method is described as such")
	assert.False(t, stream.Streaming.Client)
	assert.True(t, stream.Streaming.Streaming())
}

func TestDescriptorStatesWhatAContractSaysAndNothingElse(t *testing.T) {
	// A contract says what invoking a method does, and a contract says nothing
	// about who may invoke it. A reader must not invent the second from the first:
	// the policy answers that question, from the deployment.
	parsed := parseForTest(t, contractDescriptorSet(t))
	for _, service := range parsed.Services {
		for _, operation := range service.Operations {
			assert.Empty(t, operation.SideEffects,
				"this fixture's contract declares none, so its operations are unclassified")
		}
	}
}

func TestDescriptorRejectsUnusableInput(t *testing.T) {
	_, _, err := NewDescriptor(Descriptor{}).FromDescriptorSet(nil)
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))

	_, _, err = NewDescriptor(Descriptor{}).FromDescriptorSet([]byte("not a descriptor set"))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "FileDescriptorSet")

	_, _, err = NewDescriptor(Descriptor{}).FromEndpoint(t.Context(), "")
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
}

func TestDescriptorRejectsAnExcludedService(t *testing.T) {
	described, _, err := NewDescriptor(Descriptor{
		APIID:           "contract",
		ExcludeServices: []string{"toolbox.api.v1.ApiParserService"},
	}).FromDescriptorSet(contractDescriptorSet(t))
	require.NoError(t, err)
	for _, service := range described.Services {
		assert.NotEqual(t, "toolbox.api.v1.ApiParserService", service.Name)
	}
}

func TestFormatDescriptorDescribesTheFormat(t *testing.T) {
	descriptor := FormatDescriptor()
	assert.Equal(t, string(Format), descriptor.ID)
	assert.Contains(t, descriptor.FileExtensions, "protoset")
	assert.NotEmpty(t, descriptor.Description)

	transports := TransportDescriptors()
	require.NotEmpty(t, transports)
	ids := make([]string, 0, len(transports))
	for _, transport := range transports {
		ids = append(ids, transport.ID)
	}
	assert.Equal(t, []string{"connectrpc", "grpc", "grpc-web"}, ids)
}

func TestSlugDerivesReadableIdentifiers(t *testing.T) {
	assert.Equal(t, "echo", Slug("toolbox.testecho.v1.EchoService"), "the service suffix is dropped, since every service carries one")
	assert.Equal(t, "api-parser", Slug("toolbox.api.v1.ApiParserService"))
	assert.Equal(t, "api", Slug("v1."))
}

func operationID(t *testing.T, described api.API, service, method string) string {
	t.Helper()
	for _, candidate := range described.Services {
		if candidate.Name != service {
			continue
		}
		for _, operation := range candidate.Operations {
			if operation.Name == method {
				return operation.ID
			}
		}
	}
	t.Fatalf("contract declares no %s/%s", service, method)
	return ""
}
