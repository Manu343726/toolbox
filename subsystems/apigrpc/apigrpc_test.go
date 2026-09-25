package apigrpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	testecho "github.com/Manu343726/toolbox/subsystems/testecho"
	testechov1 "github.com/Manu343726/toolbox/subsystems/testecho/echov1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// descriptorSet renders a contract's FileDescriptorSet, which is what a parser
// receives when a caller hands it a compiled contract rather than an endpoint.
func descriptorSetFor(t *testing.T, file protoreflect.FileDescriptor) *descriptorpb.FileDescriptorSet {
	t.Helper()
	descriptor := protodesc.ToFileDescriptorProto(file)
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{descriptor}}
}

func echoDescriptorSet(t *testing.T) []byte {
	t.Helper()
	file := testechov1.File_proto_echo_proto
	encoded, err := proto.Marshal(descriptorSetFor(t, file))
	require.NoError(t, err)
	return encoded
}

func parseForTest(t *testing.T, document []byte) api.API {
	t.Helper()
	parser := NewParser(ParserOptions{})
	response, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatGRPC,
		Document: document,
		ApiId:    "echo",
	}))
	require.NoError(t, err)
	parsed, err := api.APIFromProto(response.Msg.GetApi())
	require.NoError(t, err)
	return parsed
}

func TestParserReadsContractFromDescriptorSet(t *testing.T) {
	parsed := parseForTest(t, echoDescriptorSet(t))

	assert.Equal(t, "echo", parsed.ID)
	assert.Equal(t, FormatGRPC, parsed.Format)
	require.Len(t, parsed.Services, 1)
	service := parsed.Services[0]
	assert.Equal(t, "toolbox.testecho.v1.EchoService", service.Name)
	assert.Equal(t, "echo/toolbox.testecho.v1.EchoService", service.ID)
	assert.Equal(t, []string{"Echo", "StreamEcho"}, operationNames(service))
	assert.Equal(t, "descriptor_set", parsed.Source.Kind)
}

func TestParserDescribesRequestAndResponse(t *testing.T) {
	parsed := parseForTest(t, echoDescriptorSet(t))
	echo, ok := parsed.Operation(echoOperationID(t, parsed, "Echo"))
	require.True(t, ok)

	assert.Equal(t, "Echo", echo.Method)
	require.NotNil(t, echo.Request)
	message, ok := echo.Request.Property("message")
	require.True(t, ok)
	assert.Equal(t, api.TypeString, message.Schema.Type)

	require.NotNil(t, echo.Response)
	result, ok := echo.Response.Property("message")
	require.True(t, ok)
	assert.Equal(t, api.TypeString, result.Schema.Type)
}

func TestParserMarksStreamingWithoutInvokingIt(t *testing.T) {
	parsed := parseForTest(t, echoDescriptorSet(t))
	stream, ok := parsed.Operation(echoOperationID(t, parsed, "StreamEcho"))
	require.True(t, ok)

	assert.True(t, stream.Streaming.Server)
	assert.False(t, stream.Streaming.Client)

	unary, _ := parsed.Operation(echoOperationID(t, parsed, "Echo"))
	assert.False(t, unary.Streaming.Streaming())
}

func TestParserDeclaresNoCapabilitiesForAProtobufContract(t *testing.T) {
	// A protobuf contract states no authorization facts, so the parser must not
	// invent any: the catalog's policy refuses the operations until a deployment
	// says what it authorizes.
	parsed := parseForTest(t, echoDescriptorSet(t))
	for _, service := range parsed.Services {
		for _, operation := range service.Operations {
			assert.Empty(t, operation.Capabilities)
			assert.Empty(t, operation.SideEffects)
		}
	}
}

func TestParserRejectsForeignFormatAndUnusableInput(t *testing.T) {
	parser := NewParser(ParserOptions{})
	assert.Equal(t, []api.Format{FormatGRPC}, parser.Formats())

	_, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   "openapi",
		Document: echoDescriptorSet(t),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "grpc")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{Document: echoDescriptorSet(t)}))
	require.Error(t, err, "a contract with no declared format is not guessed at")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{Format: FormatGRPC}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "document or base_url")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatGRPC,
		Document: []byte("not a descriptor set"),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FileDescriptorSet")
}

func TestParserReportsFormatDescriptor(t *testing.T) {
	parser := NewParser(ParserOptions{})
	response, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatGRPC,
		Document: echoDescriptorSet(t),
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetFormats(), 1)
	assert.Equal(t, FormatGRPC, response.Msg.GetFormats()[0].GetId())
	assert.Equal(t, Name, response.Msg.GetFormats()[0].GetProvider())
	assert.Contains(t, response.Msg.GetFormats()[0].GetFileExtensions(), "protoset")
}

// startEchoEndpoint runs the echo fixture as a real subsystem, so the adapter is
// tested against a live endpoint speaking the transport the framework serves,
// including the reflection a gRPC client needs to describe the contract.
func startEchoEndpoint(t *testing.T) (*testecho.Handler, string) {
	t.Helper()
	server, err := testecho.New(testecho.Options{})
	require.NoError(t, err)
	require.NoError(t, server.Start(context.Background()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return testecho.NewHandler(), server.Endpoint()
}

func TestAdapterInvokesLiveUnaryMethod(t *testing.T) {
	service, endpoint := startEchoEndpoint(t)

	parsed := parseForTest(t, echoDescriptorSet(t))
	operation, ok := parsed.Operation(echoOperationID(t, parsed, "Echo"))
	require.True(t, ok)
	server := api.Server{
		ID:        "echo-host",
		Name:      "Echo host",
		BaseURL:   endpoint,
		Format:    FormatGRPC,
		Transport: TransportConnectRPC,
	}

	adapter := NewAdapter(AdapterOptions{})
	response, err := adapter.InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		ServerId:      server.ID,
		ApiId:         parsed.ID,
		OperationId:   operation.ID,
		ArgumentsJson: json.RawMessage(`{"message":"ping"}`),
		Server:        server.ToProto(),
		Api:           parsed.ToProto(),
		Operation:     operation.ToProto(),
	}))
	require.NoError(t, err)
	assert.Equal(t, int32(200), response.Msg.GetStatus())
	// The response is the protobuf JSON the real service returned, computed
	// fields included, so the assertion checks the echoed value rather than the
	// whole document.
	var payload map[string]any
	require.NoError(t, json.Unmarshal(response.Msg.GetBodyJson(), &payload))
	assert.Equal(t, "ping", payload["message"])
	_ = service
}

func TestAdapterRefusesStreamingOperation(t *testing.T) {
	_, endpoint := startEchoEndpoint(t)
	parsed := parseForTest(t, echoDescriptorSet(t))
	operation, _ := parsed.Operation(echoOperationID(t, parsed, "StreamEcho"))
	server := api.Server{ID: "echo-host", Name: "Echo", BaseURL: endpoint, Format: FormatGRPC, Transport: TransportConnectRPC}

	_, err := NewAdapter(AdapterOptions{}).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		OperationId: operation.ID,
		Server:      server.ToProto(),
		Api:         parsed.ToProto(),
		Operation:   operation.ToProto(),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "streaming")
}

func TestAdapterReportsUnknownMethodAndService(t *testing.T) {
	_, endpoint := startEchoEndpoint(t)
	parsed := parseForTest(t, echoDescriptorSet(t))
	server := api.Server{ID: "echo-host", Name: "Echo", BaseURL: endpoint, Format: FormatGRPC, Transport: TransportConnectRPC}
	adapter := NewAdapter(AdapterOptions{})

	ghost := api.Operation{ID: "ghost/Method", APIID: parsed.ID, Service: "toolbox.testecho.v1.EchoService", Name: "NoSuchMethod"}
	_, err := adapter.InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		Server:    server.ToProto(),
		Api:       parsed.ToProto(),
		Operation: ghost.ToProto(),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	absent := api.Operation{ID: "ghost/Method", APIID: parsed.ID, Service: "toolbox.testecho.v1.MissingService", Name: "Echo"}
	_, err = adapter.InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		Server:    server.ToProto(),
		Api:       parsed.ToProto(),
		Operation: absent.ToProto(),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestAdapterRejectsMalformedRequestArguments(t *testing.T) {
	_, endpoint := startEchoEndpoint(t)
	parsed := parseForTest(t, echoDescriptorSet(t))
	operation, _ := parsed.Operation(echoOperationID(t, parsed, "Echo"))
	server := api.Server{ID: "echo-host", Name: "Echo", BaseURL: endpoint, Format: FormatGRPC, Transport: TransportConnectRPC}

	_, err := NewAdapter(AdapterOptions{}).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		ServerId:      server.ID,
		OperationId:   operation.ID,
		ArgumentsJson: json.RawMessage(`{"message": 42}`),
		Server:        server.ToProto(),
		Api:           parsed.ToProto(),
		Operation:     operation.ToProto(),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestRequestPayloadUnwrapsBody(t *testing.T) {
	payload, err := requestPayload(json.RawMessage(`{"body":{"message":"x"}}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":"x"}`, string(payload))

	payload, err = requestPayload(json.RawMessage(`{"message":"x"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":"x"}`, string(payload))

	payload, err = requestPayload(nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(payload))

	_, err = requestPayload(json.RawMessage(`[]`))
	require.Error(t, err, "a non-object argument document is refused")
}

// echoOperationID resolves one operation by its method name, so the tests do not
// depend on how the identifier is composed.
func echoOperationID(t *testing.T, parsed api.API, method string) string {
	t.Helper()
	for _, id := range parsed.OperationIDs() {
		if strings.HasSuffix(id, "/"+method) {
			return id
		}
	}
	t.Fatalf("operation %q not found in %v", method, parsed.OperationIDs())
	return ""
}

func operationNames(service api.Service) []string {
	names := make([]string, 0, len(service.Operations))
	for _, operation := range service.Operations {
		names = append(names, operation.Name)
	}
	return names
}
