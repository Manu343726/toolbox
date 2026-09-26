package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The translation from the framework's invoke request to a call, and from a
// result back to the invoke response, is the one place a call's shape is decided.
// Two deployments that reach it by different routes — one handing the request to
// a provider, one performing the call in process — must produce the same call and
// the same response, and these tests are what holds them to it.

func anInvokeRequest(t *testing.T) *apiv1.InvokeApiRequest {
	t.Helper()
	described := api.API{
		ID:      "shop",
		Name:    "shop",
		Version: "1.0.0",
		Format:  "openapi",
		Services: []api.Service{{
			Name: "pets",
			Operations: []api.Operation{{
				Name:   "getPetById",
				Method: "get",
				Path:   "/pets/{petId}",
			}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)
	operation := normalized.Services[0].Operations[0]
	return &apiv1.InvokeApiRequest{
		ServerId:      "shop-server",
		ApiId:         "shop",
		OperationId:   operation.ID,
		ArgumentsJson: json.RawMessage(`{"petId":"7"}`),
		Server:        api.Server{ID: "shop-server", Name: "Shop", BaseURL: "http://shop.test", Transport: "http"}.ToProto(),
		Api:           normalized.ToProto(),
		Operation:     operation.ToProto(),
	}
}

func TestCallFromInvokeRequestReadsEveryValueTheRequestCarries(t *testing.T) {
	request := anInvokeRequest(t)

	call, err := api.CallFromInvokeRequest(request)
	require.NoError(t, err)
	assert.Equal(t, "shop-server", call.Server.ID)
	assert.Equal(t, "shop", call.API.ID)
	assert.Equal(t, "pets", call.Operation.Service)
	assert.Equal(t, "getPetById", call.Operation.Name)
	assert.JSONEq(t, `{"petId":"7"}`, string(call.Arguments),
		"the arguments travel as written: unwrapping a body is the invoker's decision, not this one's")
}

// The API is context, not address. A request naming only the server and the
// operation is a complete call, so requiring the third would refuse a request that
// says everything needed to perform one.
func TestCallFromInvokeRequestAcceptsARequestWithoutItsAPI(t *testing.T) {
	request := anInvokeRequest(t)
	request.Api = nil

	call, err := api.CallFromInvokeRequest(request)
	require.NoError(t, err)
	assert.Empty(t, call.API.ID)
	assert.Equal(t, "getPetById", call.Operation.Name)

	// An API that is present but malformed is still reported, because a caller
	// that sent one expects it to be the API being called.
	request.Api = &apiv1.Api{}
	_, err = api.CallFromInvokeRequest(request)
	require.Error(t, err)
}

func TestCallFromInvokeRequestRefusesAnAbsentOrIncompleteRequest(t *testing.T) {
	_, err := api.CallFromInvokeRequest(nil)
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))

	_, err = api.CallFromInvokeRequest(&apiv1.InvokeApiRequest{})
	require.Error(t, err, "a request with neither a server nor an operation is not a call")
}

func TestInvokeResponseFromResultCarriesTheResult(t *testing.T) {
	response := api.InvokeResponseFromResult(api.Result{
		Status:      200,
		ContentType: "application/json",
		Body:        json.RawMessage(`{"name":"rex"}`),
	})
	assert.Equal(t, int32(200), response.GetStatus())
	assert.Equal(t, "application/json", response.GetContentType())
	assert.JSONEq(t, `{"name":"rex"}`, string(response.GetBodyJson()))
	assert.Empty(t, response.GetHeaders(),
		"no headers is left unset rather than claimed as an empty set")
}

// A call may produce a header more than once and the response holds one value per
// name, so repeated values are combined the way HTTP combines them. Dropping the
// extras for want of a field would lose a header the call actually sent.
func TestInvokeResponseFromResultCombinesRepeatedHeaders(t *testing.T) {
	response := api.InvokeResponseFromResult(api.Result{
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
			"Warning":      {"199 deprecated", "299 also deprecated"},
		},
	})
	assert.Equal(t, "application/json", response.GetHeaders()["Content-Type"],
		"a single-valued header passes through unchanged")
	assert.Equal(t, "199 deprecated, 299 also deprecated", response.GetHeaders()["Warning"])
}

func TestServeInvokePerformsTheCallAndAnswersInTheProvidersShape(t *testing.T) {
	request := anInvokeRequest(t)
	var received api.Call
	invoker := api.InvokerFunc(func(_ context.Context, call api.Call) (api.Result, error) {
		received = call
		return api.Result{Status: 200, ContentType: "application/json", Body: json.RawMessage(`{"name":"rex"}`)}, nil
	})

	response, err := api.ServeInvoke(context.Background(), invoker, connect.NewRequest(request))
	require.NoError(t, err)
	assert.Equal(t, "shop-server", received.Server.ID)
	assert.JSONEq(t, `{"name":"rex"}`, string(response.Msg.GetBodyJson()))
}

func TestServeInvokeReportsAMalformedRequestAndAFailedCall(t *testing.T) {
	_, err := api.ServeInvoke(context.Background(), api.InvokerFunc(
		func(context.Context, api.Call) (api.Result, error) { return api.Result{}, nil },
	), connect.NewRequest(&apiv1.InvokeApiRequest{}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err),
		"a request that is not a call is the caller's mistake")

	// A failure the invoker reports keeps its own kind rather than being folded
	// into whatever this function was doing, so the transport still decides what
	// it maps to.
	_, err = api.ServeInvoke(context.Background(), api.InvokerFunc(
		func(context.Context, api.Call) (api.Result, error) {
			return api.Result{}, api.Errorf(api.KindNotFound, "no such pet")
		},
	), connect.NewRequest(anInvokeRequest(t)))
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
}

func TestServeInvokeRefusesAnAbsentInvoker(t *testing.T) {
	_, err := api.ServeInvoke(context.Background(), nil, connect.NewRequest(anInvokeRequest(t)))
	require.Error(t, err)
	assert.Equal(t, api.KindUnsupported, api.KindOf(err))
}

// Every subsystem links contracts; every one of them is registered from init by a
// docs_embed.go. A registration that reached only the proto-source tunnel and not
// the default catalog would be invisible: the process starts, help works, and
// every contract simply has no description. This is the assertion that the
// default catalog is not empty in a process that links the framework's own.
func TestALinkedContractIsInTheDefaultCatalog(t *testing.T) {
	// This binary links pkg/api's own contract, registered by its docs_embed.go,
	// so the default catalog must describe it without any test registering it.
	documented, err := shareddocs.DefaultCatalog().Get("toolbox.api.v1.ApiInvokerService")
	require.NoError(t, err, "a linked contract is in the default catalog")
	assert.NotEmpty(t, documented.Description)
	assert.NotEmpty(t, documented.Methods)

	// The prose came from the .proto, not from the generated descriptor: a
	// description read from structure alone would be empty, and an operation with
	// no @toolbox.side-effects line is one a policy cannot grant by naming read or
	// write.
	method := documented.Methods[0]
	assert.NotEmpty(t, method.Description)
	assert.NotEmpty(t, method.Annotations,
		"an operation whose classification was lost cannot be authorized")
}
