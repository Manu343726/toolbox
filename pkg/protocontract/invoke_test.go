package protocontract

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
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture here is a real server: the invoker is tested against an endpoint
// that serves the framework's own contract over the transport the framework
// serves, reflection included. A fake would not exercise the part that is
// difficult to get right.

type parserStub struct {
	apiv1connect.UnimplementedApiParserServiceHandler
	received []*apiv1.ParseApiRequest
}

// ParseApi records the request and answers with a fixed description, so the test
// can prove what reached the server and what came back.
func (s *parserStub) ParseApi(
	_ context.Context,
	request *connect.Request[apiv1.ParseApiRequest],
) (*connect.Response[apiv1.ParseApiResponse], error) {
	s.received = append(s.received, request.Msg)
	return connect.NewResponse(&apiv1.ParseApiResponse{
		Api: &apiv1.Api{
			Id:     "answered",
			Name:   "answered",
			Format: string(Format),
			Source: &apiv1.ApiSource{Kind: SourceKindReflection, Location: request.Msg.GetBaseUrl()},
		},
	}), nil
}

// startEndpoint runs a real subsystem serving one contract, reflection included,
// because that is the shape of every endpoint this package is pointed at.
func startEndpoint(t *testing.T) (*parserStub, string) {
	t.Helper()
	stub := &parserStub{}
	path, handler := apiv1connect.NewApiParserServiceHandler(stub)
	server, err := subsystem.NewServer(subsystem.Config{
		Name: "fixture",
		Services: []subsystem.Service{{
			Name:    apiv1connect.ApiParserServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return stub, server.Endpoint()
}

func TestInvokerDescribesALiveEndpointAndCallsIt(t *testing.T) {
	stub, endpoint := startEndpoint(t)
	descriptor := NewDescriptor(Descriptor{})

	described, warnings, err := descriptor.FromEndpoint(t.Context(), endpoint)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.NotEmpty(t, described.Services)
	require.Len(t, described.DeclaredServers, 1)
	assert.Equal(t, endpoint, described.DeclaredServers[0].URL, "a described endpoint declares itself")

	operation, ok := described.Operation(operationID(t, described, "toolbox.api.v1.ApiParserService", "ParseApi"))
	require.True(t, ok)

	result, err := NewInvoker(InvokerOptions{}).Invoke(t.Context(), api.Call{
		Server:    api.Server{ID: "framework", Name: "Framework", BaseURL: endpoint, Format: Format, Transport: "connectrpc"},
		API:       described,
		Operation: operation,
		Arguments: json.RawMessage(`{"format":"grpc","document":"AA=="}`),
	})
	require.NoError(t, err)
	assert.Equal(t, 200, result.Status)
	assert.Equal(t, "application/json", result.ContentType)

	// The call reaches the server with the values the caller supplied, and the
	// response is the protobuf JSON the server actually returned — the whole
	// response message, unflattened.
	require.Len(t, stub.received, 1)
	assert.Equal(t, "grpc", stub.received[0].GetFormat())
	assert.Equal(t, []byte{0x00}, stub.received[0].GetDocument())
	var payload struct {
		API struct {
			ID string `json:"id"`
		} `json:"api"`
	}
	require.NoError(t, json.Unmarshal(result.Body, &payload))
	assert.Equal(t, "answered", payload.API.ID)
}

func TestInvokerUnwrapsABodyArgument(t *testing.T) {
	stub, endpoint := startEndpoint(t)
	described, _, err := NewDescriptor(Descriptor{}).FromEndpoint(t.Context(), endpoint)
	require.NoError(t, err)
	operation, _ := described.Operation(operationID(t, described, "toolbox.api.v1.ApiParserService", "ParseApi"))

	_, err = NewInvoker(InvokerOptions{}).Invoke(t.Context(), api.Call{
		Server:    api.Server{ID: "framework", BaseURL: endpoint},
		API:       described,
		Operation: operation,
		Arguments: json.RawMessage(`{"body":{"format":"grpc"}}`),
	})
	require.NoError(t, err)
	require.Len(t, stub.received, 1)
	assert.Equal(t, "grpc", stub.received[0].GetFormat(), "a wrapped body is unwrapped before the call")
}

func TestInvokerRefusesAStreamingOperation(t *testing.T) {
	_, err := NewInvoker(InvokerOptions{}).Invoke(t.Context(), api.Call{
		Server: api.Server{ID: "framework", BaseURL: "http://framework.test"},
		Operation: api.Operation{
			ID:        "contract/stream/stream",
			Service:   "toolbox.streaming.v1.StreamService",
			Name:      "Stream",
			Streaming: api.Streaming{Server: true},
		},
	})
	require.Error(t, err)
	assert.Equal(t, api.KindUnsupported, api.KindOf(err),
		"a streaming method is described, and this invoker says it does not call it")
}

func TestInvokerReportsAnUnknownMethod(t *testing.T) {
	_, endpoint := startEndpoint(t)
	_, err := NewInvoker(InvokerOptions{}).Invoke(t.Context(), api.Call{
		Server:    api.Server{ID: "framework", BaseURL: endpoint},
		Operation: api.Operation{ID: "contract/api-parser-service/nope", Service: "toolbox.api.v1.ApiParserService", Name: "Nope"},
	})
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
}

func TestInvokerReportsAMalformedArgumentDocument(t *testing.T) {
	_, endpoint := startEndpoint(t)
	described, _, err := NewDescriptor(Descriptor{}).FromEndpoint(t.Context(), endpoint)
	require.NoError(t, err)
	operation, _ := described.Operation(operationID(t, described, "toolbox.api.v1.ApiParserService", "ParseApi"))

	_, err = NewInvoker(InvokerOptions{}).Invoke(t.Context(), api.Call{
		Server:    api.Server{ID: "framework", BaseURL: endpoint},
		API:       described,
		Operation: operation,
		Arguments: json.RawMessage(`"not an object"`),
	})
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
}

func TestInvokerRequiresABaseURL(t *testing.T) {
	_, err := NewInvoker(InvokerOptions{}).Invoke(t.Context(), api.Call{
		Server:    api.Server{ID: "framework"},
		Operation: api.Operation{ID: "contract/api-parser-service/parse-api", Service: "toolbox.api.v1.ApiParserService", Name: "ParseApi"},
	})
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
}

func TestInvokerReportsAnUnreachableEndpoint(t *testing.T) {
	// An endpoint that is not listening is a deployment problem, not a bad
	// request, and the classification says so.
	unreachable := httptest.NewServer(http.NotFoundHandler())
	endpoint := unreachable.URL
	unreachable.Close()

	_, err := NewInvoker(InvokerOptions{}).Invoke(t.Context(), api.Call{
		Server:    api.Server{ID: "gone", BaseURL: endpoint},
		Operation: api.Operation{ID: "contract/api-parser-service/parse-api", Service: "toolbox.api.v1.ApiParserService", Name: "ParseApi"},
	})
	require.Error(t, err)
	assert.Equal(t, api.KindUnavailable, api.KindOf(err))
}

func TestRequestPayloadUnwrapsBody(t *testing.T) {
	payload, err := RequestPayload(nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(payload))

	payload, err = RequestPayload(json.RawMessage(`{"body":{"name":"Rex"}}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"Rex"}`, string(payload))

	payload, err = RequestPayload(json.RawMessage(`{"name":"Rex"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"Rex"}`, string(payload), "an unwrapped argument object is the request itself")

	_, err = RequestPayload(json.RawMessage(`[1,2]`))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
}

func TestApplyCapabilitiesJoinsAContractToItsManifest(t *testing.T) {
	parsed := parseForTest(t, contractDescriptorSet(t))
	assert.Empty(t, parsed.Services[0].Capabilities, "a contract declares no capabilities")

	enriched := ApplyCapabilities(parsed, CapabilitiesFor(map[string][]string{
		"toolbox.api.v1.ApiParserService": {" api.parse.grpc ", ""},
	}))
	var parser *api.Service
	for i := range enriched.Services {
		if enriched.Services[i].Name == "toolbox.api.v1.ApiParserService" {
			parser = &enriched.Services[i]
		}
	}
	require.NotNil(t, parser)
	assert.Equal(t, []string{"api.parse.grpc"}, parser.Capabilities)
	// A service nobody declared a capability for stays empty, so it stays
	// unexposable.
	for _, service := range enriched.Services {
		if service.Name != "toolbox.api.v1.ApiParserService" {
			assert.Empty(t, service.Capabilities)
		}
	}
}

func TestApplyCapabilitiesLeavesADeclaredCapabilityAlone(t *testing.T) {
	parsed := parseForTest(t, contractDescriptorSet(t))
	parsed.Services[0].Capabilities = []string{"declared.by.the.contract"}
	enriched := ApplyCapabilities(parsed, CapabilitiesFor(map[string][]string{
		parsed.Services[0].Name: {"declared.by.the.manifest"},
	}))
	assert.Equal(t, []string{"declared.by.the.contract"}, enriched.Services[0].Capabilities,
		"a hand-written contract knows more than the manifest it was parsed from")
}

func TestParseWithCapabilitiesDescribesAndEnriches(t *testing.T) {
	_, endpoint := startEndpoint(t)
	described, _, err := NewDescriptor(Descriptor{}).ParseWithCapabilities(t.Context(), endpoint, map[string][]string{
		"toolbox.api.v1.ApiParserService": {"api.parse.grpc"},
	})
	require.NoError(t, err)
	for _, service := range described.Services {
		if service.Name == "toolbox.api.v1.ApiParserService" {
			assert.Equal(t, []string{"api.parse.grpc"}, service.Capabilities)
			return
		}
	}
	t.Fatal("the endpoint's contract declares no parser service")
}
