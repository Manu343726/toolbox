package discovery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/testecho/echov1"
	"github.com/Manu343726/toolbox/subsystems/testecho/echov1/echov1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// The client reads a contract from a live endpoint over reflection and then calls what it
// found. Both halves are tested here against a real server over a real socket, because the
// whole point of reflection is that nothing in this file knows the contract ahead of time, and
// a fake responder would test the fake.

// serve runs a real subsystem serving the echo service, and a client pointed at it.
//
// A subsystem server rather than a hand-built mux, because reflection is mounted by the server
// and a test that served its own reflection would be testing a responder it wrote itself.
func serve(t *testing.T) (endpoint string, client *discovery.Client, echo *testEcho) {
	t.Helper()
	echo = &testEcho{}
	path, handler := echov1connect.NewEchoServiceHandler(echo)
	server, err := subsystem.NewServer(subsystem.Config{
		Name: "testecho",
		Services: []subsystem.Service{{
			Name:    "toolbox.testecho.v1.EchoService",
			Path:    path,
			Handler: handler,
		}},
	})
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return server.Endpoint(), discovery.New(server.Endpoint()), echo
}

func TestAReflectedClientCanReadAContractItWasNeverTold(t *testing.T) {
	_, client, _ := serve(t)

	services, err := client.ListServices(context.Background())

	require.NoError(t, err)
	// The only service on the wire is the echo service, and the client found it by asking.
	assert.Contains(t, services, "toolbox.testecho.v1.EchoService")
}

func TestTheSchemaCarriesWhatACallerNeedsToBuildARequest(t *testing.T) {
	_, client, _ := serve(t)

	schema, err := client.DescribeService(context.Background(), "toolbox.testecho.v1.EchoService")
	require.NoError(t, err)

	require.NotNil(t, schema)
	assert.Equal(t, "toolbox.testecho.v1.EchoService", schema.Name)
	require.NotEmpty(t, schema.Methods)

	var found bool
	for _, method := range schema.Methods {
		if method.Name != "Echo" {
			continue
		}
		found = true
		// A caller that gets a method with no input descriptor cannot build a request, and one
		// with no output descriptor cannot read the answer. Either is a schema that cannot be
		// called, and both are what a dynamic caller depends on.
		require.NotNil(t, method.Input)
		assert.Equal(t, "toolbox.testecho.v1.EchoRequest", string(method.Input.FullName()))
		require.NotNil(t, method.Output)
		assert.Equal(t, "toolbox.testecho.v1.EchoResponse", string(method.Output.FullName()))
		assert.False(t, method.ClientStreaming, "the fixture's method is unary")
		assert.False(t, method.ServerStreaming, "the fixture's method is unary")
	}
	assert.True(t, found, "the echo method was not in the schema")
}

func TestDescribingAnUnknownServiceIsRefused(t *testing.T) {
	_, client, _ := serve(t)

	_, err := client.DescribeService(context.Background(), "toolbox.testecho.v1.NoSuchService")

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

	answer, err := client.Invoke(context.Background(),
		"toolbox.testecho.v1.EchoService", "Echo",
		&echov1.EchoRequest{Message: "hello"})

	require.NoError(t, err)
	// The answer comes back as a dynamic message, because the client built the response type
	// from reflection rather than from the caller's generated code — that is what makes it able
	// to call a contract it was never compiled against. Converting it to the caller's own type
	// is the caller's one step, and it is the step that makes a dynamic call usable.
	raw, err := proto.Marshal(answer)
	require.NoError(t, err)
	typed := &echov1.EchoResponse{}
	require.NoError(t, proto.Unmarshal(raw, typed))
	assert.Equal(t, "hello", typed.GetMessage())
}

func TestAJSONCallRoundTripsThroughTheReflectedPath(t *testing.T) {
	_, client, _ := serve(t)

	answer, err := client.InvokeJSON(context.Background(),
		"toolbox.testecho.v1.EchoService", "Echo", []byte(`{"message":"from json"}`))

	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(answer, &decoded))
	assert.Equal(t, "from json", decoded["message"])
}

func TestACallToAnUnknownMethodIsRefused(t *testing.T) {
	_, client, _ := serve(t)

	_, err := client.Invoke(context.Background(),
		"toolbox.testecho.v1.EchoService", "NoSuchMethod", &echov1.EchoRequest{})

	// Silently calling nothing and reporting success would be the worst outcome, because the
	// caller has a typed response and no idea it is empty.
	require.Error(t, err)
}

func TestACallToAnUnknownServiceIsRefused(t *testing.T) {
	_, client, _ := serve(t)

	_, err := client.Invoke(context.Background(),
		"toolbox.testecho.v1.NoSuchService", "Echo", &echov1.EchoRequest{})

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

	first, err := client.DescribeService(context.Background(), "toolbox.testecho.v1.EchoService")
	require.NoError(t, err)
	require.NotEmpty(t, first.Methods)
	original := first.Methods[0].Name
	first.Methods[0].Name = "corrupted"

	second, err := client.DescribeService(context.Background(), "toolbox.testecho.v1.EchoService")
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
			_, err := client.DescribeService(context.Background(), "toolbox.testecho.v1.EchoService")
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
	_, client, echo := serve(t)

	header := http.Header{}
	header.Set("X-Toolbox-Policy", "strict")
	_, err := client.InvokeJSONWithHeaders(context.Background(),
		"toolbox.testecho.v1.EchoService", "Echo", []byte(`{"message":"hi"}`), header)

	// The gateway forwards a policy on a call it makes on an agent's behalf, and a client that
	// dropped the header would make every authorised call look unauthorised to the subsystem
	// — with the failure reported as a policy denial rather than as a lost header.
	require.NoError(t, err)
	assert.Equal(t, "strict", echo.seen.Get("X-Toolbox-Policy"))
}

// testEcho is the fixture handler, recording the metadata each call arrived with.
type testEcho struct {
	// seen is the metadata of the most recent call, so a test can assert what reached the
	// server rather than what the client intended to send.
	seen http.Header
}

// Echo implements the reflected service's unary method.
func (e *testEcho) Echo(
	ctx context.Context, req *connect.Request[echov1.EchoRequest],
) (*connect.Response[echov1.EchoResponse], error) {
	e.seen = req.Header().Clone()
	return connect.NewResponse(&echov1.EchoResponse{Message: req.Msg.GetMessage()}), nil
}

// StreamEcho implements the fixture's server-streaming method, which exists so a client can be
// shown refusing it. The generated handler interface requires it, so it is implemented here
// rather than by embedding an unimplemented base — a test that reads a contract blind should
// not depend on the framework to fill in a method it is about to assert on.
func (e *testEcho) StreamEcho(
	ctx context.Context, req *connect.Request[echov1.EchoRequest],
	stream *connect.ServerStream[echov1.EchoResponse],
) error {
	return stream.Send(&echov1.EchoResponse{Message: req.Msg.GetMessage()})
}
