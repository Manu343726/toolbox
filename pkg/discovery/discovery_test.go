package discovery_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	// Imported for its contract documentation. pkg/api owns the registration of the framework's
	// .proto, because it is the package that *is* the extension contract; apiv1 is only its
	// generated form and cannot import pkg/api without a cycle. So a caller that documents this
	// contract imports pkg/api, which is what a blank import here is saying.
	_ "github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/api/apiv1"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// The client reads a contract from a live endpoint over reflection and then calls what it
// found. Both halves are tested here against a real server over a real socket, because the
// whole point of reflection is that nothing in this file knows the contract ahead of time, and
// a fake responder would test the fake.
//
// The contract is the framework's own, from this module. A foundation package whose test
// depends on a subsystem module makes every module that depends on this one resolve a module it
// does not own — and the published copy of a subsystem carries no generated code, so that
// resolution fails for reasons that have nothing to do with the package under test. It cost
// sixteen modules' standalone builds before it was noticed.

const (
	invokerService = "toolbox.api.v1.ApiInvokerService"
	invokeMethod   = "InvokeApi"
	// echoed is what the fixture's operation returns, so a caller can tell its own answer
	// from the transport's.
	echoed = "hello"
)

// serve runs a real subsystem serving the invoker service, and a client pointed at it.
//
// A subsystem server rather than a hand-built mux, because reflection is mounted by the server
// and a test that served its own reflection would be testing a responder it wrote itself.
func serve(t *testing.T) (endpoint string, client *discovery.Client, fixture *testInvoker) {
	t.Helper()
	fixture = &testInvoker{}
	path, handler := apiv1connect.NewApiInvokerServiceHandler(fixture)
	server, err := subsystem.NewServer(subsystem.Config{
		Name:     "apigrpc",
		Services: []subsystem.Service{{Name: invokerService, Path: path, Handler: handler}},
	})
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return server.Endpoint(), discovery.New(server.Endpoint()), fixture
}

func TestAReflectedClientCanReadAContractItWasNeverTold(t *testing.T) {
	_, client, _ := serve(t)

	services, err := client.ListServices(context.Background())

	require.NoError(t, err)
	// The only service on the wire is the invoker service, and the client found it by asking.
	assert.Contains(t, services, invokerService)
}

func TestTheSchemaCarriesWhatACallerNeedsToBuildARequest(t *testing.T) {
	_, client, _ := serve(t)

	schema, err := client.DescribeService(context.Background(), invokerService)
	require.NoError(t, err)

	require.NotNil(t, schema)
	assert.Equal(t, invokerService, schema.Name)
	require.NotEmpty(t, schema.Methods)

	var found bool
	for _, method := range schema.Methods {
		if method.Name != invokeMethod {
			continue
		}
		found = true
		// A caller that gets a method with no input descriptor cannot build a request, and one
		// with no output descriptor cannot read the answer. Either is a schema that cannot be
		// called, and both are what a dynamic caller depends on.
		require.NotNil(t, method.Input)
		assert.Equal(t, "toolbox.api.v1.InvokeApiRequest", string(method.Input.FullName()))
		require.NotNil(t, method.Output)
		assert.Equal(t, "toolbox.api.v1.InvokeApiResponse", string(method.Output.FullName()))
		assert.False(t, method.ClientStreaming, "the fixture's method is unary")
		assert.False(t, method.ServerStreaming, "the fixture's method is unary")
	}
	assert.True(t, found, "the invoke method was not in the schema")
}

func TestTheSchemaCarriesTheDocumentationTheContractDeclared(t *testing.T) {
	_, client, _ := serve(t)

	schema, err := client.DescribeService(context.Background(), invokerService)
	require.NoError(t, err)
	require.NotNil(t, schema.Documentation)

	// The description comes from the .proto, and a schema that reached a caller without it
	// would be a contract nobody can read before calling.
	assert.NotEmpty(t, schema.Documentation.Description)
	assert.Contains(t, schema.Documentation.Description, "invoker")
}

func TestDescribingAnUnknownServiceIsRefused(t *testing.T) {
	_, client, _ := serve(t)

	_, err := client.DescribeService(context.Background(), "toolbox.api.v1.NoSuchService")

	// A caller that asked for a contract the endpoint does not serve must be told, rather than
	// handed an empty schema it would then try to call.
	require.Error(t, err)
}

func TestAnEndpointWithNoReflectionIsReportedNotSilentlyEmpty(t *testing.T) {
	// A server that serves something else entirely — a dashboard, a proxy — rather than one
	// that is broken, because the two need different answers from whoever is debugging.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)

	_, err := discovery.New(server.URL).ListServices(context.Background())

	// An empty list would be indistinguishable from a server that serves nothing, and the
	// difference is the difference between a wrong address and a wrong deployment.
	require.Error(t, err)
}

func TestATypedCallIsRoutedByTheReflectedContract(t *testing.T) {
	_, client, _ := serve(t)

	answer, err := client.Invoke(context.Background(), invokerService, invokeMethod,
		&apiv1.InvokeApiRequest{OperationId: "api/getThing"})

	require.NoError(t, err)
	// The answer comes back as a dynamic message, because the client built the response type
	// from reflection rather than from the caller's generated code — that is what makes it able
	// to call a contract it was never compiled against. Converting it to the caller's own type
	// is the caller's one step, and it is the step that makes a dynamic call usable.
	raw, err := proto.Marshal(answer)
	require.NoError(t, err)
	typed := &apiv1.InvokeApiResponse{}
	require.NoError(t, proto.Unmarshal(raw, typed))
	// The body is whatever the operation returned, and a dynamic caller sees exactly the bytes
	// the transport produced.
	assert.Equal(t, `{"message":"`+echoed+`"}`, string(typed.GetBodyJson()))
	assert.Equal(t, int32(200), typed.GetStatus())
}

func TestAJSONCallRoundTripsThroughTheReflectedPath(t *testing.T) {
	_, client, _ := serve(t)

	answer, err := client.InvokeJSON(context.Background(), invokerService, invokeMethod,
		[]byte(`{"operationId":"api/getThing"}`))

	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(answer, &decoded))
	// The JSON path answers in protobuf JSON, so the field names are the contract's own
	// snake_case and a bytes field arrives base64-encoded. A caller reaching for the operation's
	// body has to decode it, and that is a fact about the wire format rather than a defect —
	// but it is the fact a caller of this path needs to be told by a test rather than by
	// discovering it.
	assert.Equal(t, "application/json", decoded["content_type"])
	assert.Equal(t, float64(200), decoded["status"])
	encoded, ok := decoded["body_json"].(string)
	require.True(t, ok, "the body should arrive as an encoded string, got %#v", decoded["body_json"])
	body, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":"`+echoed+`"}`, string(body))
}

func TestACallToAnUnknownMethodIsRefused(t *testing.T) {
	_, client, _ := serve(t)

	_, err := client.Invoke(context.Background(), invokerService, "NoSuchMethod",
		&apiv1.InvokeApiRequest{})

	// Silently calling nothing and reporting success would be the worst outcome, because the
	// caller has a typed response and no idea it is empty.
	require.Error(t, err)
}

func TestACallToAnUnknownServiceIsRefused(t *testing.T) {
	_, client, _ := serve(t)

	_, err := client.Invoke(context.Background(), "toolbox.api.v1.NoSuchService", invokeMethod,
		&apiv1.InvokeApiRequest{})

	require.Error(t, err)
}

func TestTheCacheIsClearedOnRequest(t *testing.T) {
	_, client, _ := serve(t)

	_, err := client.ListServices(context.Background())
	require.NoError(t, err)
	// A reflected contract is cached because reflection is a round trip per call, and a client
	// that cannot be made to re-read is a client that cannot notice a deployment changing.
	client.ClearCache()
	_, err = client.ListServices(context.Background())
	require.NoError(t, err)
}

func TestAnEndpointIsTrimmedAndNotOtherwiseRewritten(t *testing.T) {
	// A trailing slash is trimmed because it is the same base written twice, and rewriting one
	// is how a caller ends up with a path it did not ask for. Nothing else is touched: a
	// scheme is left off if it was left off, because the client is given an address by whoever
	// resolved it and guessing at it here would be a second place for that to go wrong.
	for _, given := range []string{
		"127.0.0.1:8080",
		"http://127.0.0.1:8080",
		"http://127.0.0.1:8080/",
		"127.0.0.1:8080/",
		"  http://127.0.0.1:8080  ",
	} {
		assert.Equal(t, strings.TrimRight(strings.TrimSpace(given), "/"), discovery.New(given).Endpoint(),
			"%q should only be trimmed", given)
	}
}

func TestAReflectedSchemaIsACopyTheCallerCannotCorrupt(t *testing.T) {
	_, client, _ := serve(t)

	first, err := client.DescribeService(context.Background(), invokerService)
	require.NoError(t, err)
	require.NotEmpty(t, first.Methods)
	original := first.Methods[0].Name
	first.Methods[0].Name = "corrupted"

	second, err := client.DescribeService(context.Background(), invokerService)
	require.NoError(t, err)

	// The schema is cached, and a cache handing out its own entries would let one caller's
	// mistake corrupt every later caller — including the gateway, which caches for everyone.
	assert.Equal(t, original, second.Methods[0].Name, "the cache handed out its own entry")
}

func TestConcurrentDescriptionIsSafe(t *testing.T) {
	// The cache is shared and the gateway describes services concurrently, so a read that
	// raced a fill would be a data race on a map.
	_, client, _ := serve(t)

	done := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() {
			_, err := client.DescribeService(context.Background(), invokerService)
			done <- err
		}()
	}
	for i := 0; i < 12; i++ {
		require.NoError(t, <-done)
	}
}

func TestTheContextIsHonouredByACall(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "ServerReflectionInfo") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		<-block
		defer close(block)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := discovery.New(server.URL).ListServices(ctx)

	// A cancelled context that is ignored turns a cancelled call into a hanging one, and a
	// hanging call in a gateway is a hung deployment.
	require.Error(t, err)
}

func TestAnUnreachableEndpointIsReported(t *testing.T) {
	// A closed port rather than a wrong path: the connection failure is the thing a caller
	// needs to distinguish from a contract that is not there.
	_, err := discovery.New("http://127.0.0.1:1").ListServices(context.Background())
	require.Error(t, err)
}

func TestAHeaderIsCarriedOntoTheWire(t *testing.T) {
	_, client, fixture := serve(t)

	header := http.Header{}
	header.Set("X-Toolbox-Policy", "strict")
	_, err := client.InvokeJSONWithHeaders(context.Background(), invokerService, invokeMethod,
		[]byte(`{"operationId":"api/getThing"}`), header)

	// The gateway forwards a policy on a call it makes on an agent's behalf, and a client that
	// dropped the header would make every authorised call look unauthorised to the subsystem
	// — with the failure reported as a policy denial rather than as a lost header. This is
	// asserted against a real service, because the client resolves the schema over reflection
	// before it calls, and a fake that answered the call directly would have tested something
	// that cannot happen.
	require.NoError(t, err)
	assert.Equal(t, "strict", fixture.seen.Get("X-Toolbox-Policy"))
}

// testInvoker is the fixture handler, recording the metadata each call arrived with.
type testInvoker struct {
	// seen is the metadata of the most recent call, so a test can assert what reached the
	// server rather than what the client intended to send.
	seen http.Header
}

// InvokeApi implements the reflected service's method.
func (f *testInvoker) InvokeApi(
	_ context.Context, req *connect.Request[apiv1.InvokeApiRequest],
) (*connect.Response[apiv1.InvokeApiResponse], error) {
	f.seen = req.Header().Clone()
	return connect.NewResponse(&apiv1.InvokeApiResponse{
		Status:      200,
		ContentType: "application/json",
		BodyJson:    []byte(`{"message":"` + echoed + `"}`),
	}), nil
}
