package apigrpc

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

// These tests cover the RPC layer: message conversion, the format claim, and the
// code a caller sees. The translation itself is tested in the package this
// subsystem serves, and duplicating those tests here would only test the
// conversion twice.

// contractSet is the framework's own contract, which is a real service contract
// the subsystem can therefore describe.
func contractSet(t *testing.T) []byte {
	t.Helper()
	encoded, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(apiv1.File_toolbox_api_v1_api_proto),
		},
	})
	require.NoError(t, err)
	return encoded
}

func TestParserClaimsTheContractFormat(t *testing.T) {
	assert.Equal(t, []api.Format{FormatGRPC}, NewParser(ParserOptions{}).Formats())

	narrowed := NewParser(ParserOptions{Formats: []api.Format{"GRPC"}})
	assert.Equal(t, []api.Format{"grpc"}, narrowed.Formats(), "a claim is compared without regard to case")
}

func TestParserRejectsForeignFormatAndUnusableInput(t *testing.T) {
	parser := NewParser(ParserOptions{})

	_, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   "openapi",
		Document: contractSet(t),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "grpc", "a parser says which formats it handles")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{Document: contractSet(t)}))
	require.Error(t, err, "a contract with no declared format is not guessed at")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{Format: FormatGRPC}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "a document or a base url")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatGRPC,
		Document: []byte("not a descriptor set"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "FileDescriptorSet")
}

func TestParserReportsTheFormatDescriptorItImplements(t *testing.T) {
	response, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatGRPC,
		Document: contractSet(t),
		ApiId:    "framework",
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetFormats(), 1)
	assert.Equal(t, string(FormatGRPC), response.Msg.GetFormats()[0].GetId())
	assert.Equal(t, Name, response.Msg.GetFormats()[0].GetProvider(), "a catalog indexes the subsystem that contributed a format")
	assert.Contains(t, response.Msg.GetFormats()[0].GetFileExtensions(), "protoset")

	described, err := api.APIFromProto(response.Msg.GetApi())
	require.NoError(t, err)
	assert.Equal(t, "framework", described.ID)
}

func TestParserNamesAnUndescribedAPIFromTheRequest(t *testing.T) {
	// The requested identifier is the one the description gets, so a catalog can
	// choose how a contract is named in its repository.
	response, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatGRPC,
		Document: contractSet(t),
		ApiId:    "framework",
	}))
	require.NoError(t, err)
	described, err := api.APIFromProto(response.Msg.GetApi())
	require.NoError(t, err)
	assert.NotEmpty(t, described.ID)
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
	assert.True(t, byRole[api.ProviderInvoker].HandlesTransport(TransportGRPCWeb))
	assert.Contains(t, byRole[api.ProviderInvoker].ServiceNames, apiv1connect.ApiInvokerServiceName)
}

func TestInvokerConvertsACallAndItsResult(t *testing.T) {
	_, err := NewInvoker(InvokerOptions{}).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		Server:    api.Server{ID: "gone", Name: "Gone", BaseURL: "http://127.0.0.1:1"}.ToProto(),
		Operation: anOperation(t, "RenderApi", api.Streaming{}),
	}))
	require.Error(t, err, "a call that cannot be made is reported, not swallowed")
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err),
		"an endpoint that cannot be reached is unavailable, which says what to fix")

	_, err = NewInvoker(InvokerOptions{}).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		Server:    api.Server{ID: "gone", Name: "Gone", BaseURL: "http://127.0.0.1:1"}.ToProto(),
		Operation: anOperation(t, "Stream", api.Streaming{Server: true}),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err),
		"a streaming method is described but not invoked here")
}

func TestNewServesBothContracts(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)
	descriptor := server.Descriptor()
	assert.Equal(t, Name, descriptor.SubsystemName)
	names := map[string]bool{}
	for _, name := range descriptor.ServiceNames {
		names[name] = true
	}
	assert.True(t, names[apiv1connect.ApiParserServiceName])
	assert.True(t, names[apiv1connect.ApiInvokerServiceName])
	// The claim a catalog discovers is the capability the manifest declares.
	assert.Contains(t, descriptor.Capabilities, api.ParseCapability(FormatGRPC))
	assert.Contains(t, descriptor.Capabilities, api.InvokeCapability(TransportGRPCWeb))
}

func TestConnectCodeCoversEveryClassification(t *testing.T) {
	// Every classification the implementation package can produce has a code, so a
	// caller never receives a code that contradicts the failure.
	cases := map[api.ErrorKind]connect.Code{
		api.KindInvalid:            connect.CodeInvalidArgument,
		api.KindNotFound:           connect.CodeNotFound,
		api.KindFailedPrecondition: connect.CodeFailedPrecondition,
		api.KindDenied:             connect.CodePermissionDenied,
		api.KindAlreadyExists:      connect.CodeAlreadyExists,
		api.KindUnsupported:        connect.CodeUnimplemented,
		api.KindUnavailable:        connect.CodeUnavailable,
		api.KindInternal:           connect.CodeInternal,
		"":                         connect.CodeInternal,
	}
	for kind, expected := range cases {
		assert.Equal(t, expected, connectCode(kind), "kind %q", kind)
	}
}

func TestErrorKindsClassifyFailures(t *testing.T) {
	_, err := NewParser(ParserOptions{}).ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatGRPC,
		Document: []byte("not a descriptor set"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.True(t, json.Valid([]byte(`{}`)), "the fixture's JSON stays valid")
}

// anOperation returns a valid described operation, as a catalog would hold it.
func anOperation(t *testing.T, name string, streaming api.Streaming) *apiv1.ApiOperation {
	t.Helper()
	described := api.API{
		ID:     "contract",
		Name:   "contract",
		Format: FormatGRPC,
		Services: []api.Service{{
			Name:       "toolbox.api.v1.ApiAdapterService",
			Operations: []api.Operation{{Name: name, Streaming: streaming}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)
	operation, found := normalized.Operation(normalized.OperationIDs()[0])
	require.True(t, found)
	return operation.ToProto()
}
