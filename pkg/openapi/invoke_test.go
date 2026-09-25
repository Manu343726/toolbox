package openapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the HTTP invoker as a plain Go call, because that is how a
// host uses it: the RPC layer in the provider subsystem adds message conversion
// and nothing else.

func invokerForTest(t *testing.T, credentials CredentialSource) *Invoker {
	t.Helper()
	return NewInvoker(InvokerOptions{Credentials: credentials})
}

func registeredOperation(t *testing.T, method, path string, parameters ...api.Parameter) (api.API, api.Server, api.Operation) {
	t.Helper()
	target := api.API{
		ID:     "shop",
		Name:   "shop",
		Format: Format,
		Services: []api.Service{{
			Name: "pets",
			Operations: []api.Operation{{
				Name:       "call",
				Method:     method,
				Path:       path,
				Parameters: parameters,
				Request:    api.ObjectSchema(api.Property{Name: "name", Schema: api.StringSchema(), Required: true}),
				Response:   api.ObjectSchema(api.Property{Name: "ok", Schema: api.StringSchema()}),
			}},
		}},
	}
	normalized, err := target.Normalize()
	require.NoError(t, err)
	registered := api.Server{
		ID:        "shop-host",
		Name:      "Shop host",
		BaseURL:   "http://shop.test/api",
		Format:    Format,
		Transport: TransportHTTP,
	}
	operation, found := normalized.Operation(normalized.OperationIDs()[0])
	require.True(t, found)
	return normalized, registered, operation
}

func TestAdapterBuildsRequestFromOperationAndArguments(t *testing.T) {
	var received *http.Request
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Clone(context.Background())
		buf := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(buf)
		}
		receivedBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "abc")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":"yes"}`))
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "post", "/pets/{petId}",
		api.Parameter{Name: "petId", In: api.ParameterInPath, Required: true, Schema: api.StringSchema()},
		api.Parameter{Name: "verbose", In: api.ParameterInQuery, Schema: api.StringSchema()},
		api.Parameter{Name: "X-Trace", In: api.ParameterInHeader, Schema: api.StringSchema()},
	)
	server.BaseURL = upstream.URL + "/api"

	response := invoke(t, invokerForTest(t, nil), target, server, operation, `{"petId":"7","verbose":"true","X-Trace":"t-1","name":"Rex"}`)

	assert.Equal(t, http.StatusOK, response.Status)
	assert.JSONEq(t, `{"ok":"yes"}`, string(response.Body))
	assert.Equal(t, "abc", response.Headers["X-Request-Id"][0])
	assert.NotContains(t, response.Headers, "Set-Cookie", "credential-bearing headers are not echoed back")

	require.NotNil(t, received)
	assert.Equal(t, "POST", received.Method, "the method is normalized for the wire")
	assert.Equal(t, "/api/pets/7", received.URL.Path)
	assert.Equal(t, "true", received.URL.Query().Get("verbose"))
	assert.Equal(t, "t-1", received.Header.Get("X-Trace"))
	assert.JSONEq(t, `{"name":"Rex"}`, receivedBody)
}

func TestAdapterRejectsMissingRequiredParameter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request must not be sent when a required parameter is missing")
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets/{petId}",
		api.Parameter{Name: "petId", In: api.ParameterInPath, Required: true, Schema: api.StringSchema()},
	)
	server.BaseURL = upstream.URL

	err := invokeExpectingError(t, invokerForTest(t, nil), target, server, operation, `{}`)
	assert.Equal(t, api.KindInvalid, api.KindOf(err), "a missing argument is the caller's mistake")
	assert.Contains(t, err.Error(), "petId")
}

func TestAdapterRejectsParameterAbsentFromPath(t *testing.T) {
	target, server, operation := registeredOperation(t, "get", "/pets",
		api.Parameter{Name: "petId", In: api.ParameterInPath, Required: true, Schema: api.StringSchema()},
	)
	server.BaseURL = "http://shop.test"

	// A description that declares a path parameter the path does not contain is
	// inconsistent, and building the URL from it would silently drop a value.
	err := invokeExpectingError(t, invokerForTest(t, nil), target, server, operation, `{"petId":"7"}`)
	assert.Contains(t, err.Error(), "does not contain the declared parameter")
}

func TestAdapterRejectsUnknownArgument(t *testing.T) {
	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = "http://shop.test"

	err := invokeExpectingError(t, invokerForTest(t, nil), target, server, operation, `{"nmae":"typo"}`)
	assert.Contains(t, err.Error(), `does not accept the argument "nmae"`,
		"a misspelled argument is reported instead of silently dropped")
}

func TestAdapterAppliesAPIKeyCredential(t *testing.T) {
	var received *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Clone(context.Background())
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = upstream.URL
	target.SecuritySchemes = []api.SecurityScheme{{Name: "apiKeyAuth", Type: "apiKey", In: "header", ParameterName: "X-API-Key"}}
	target.Security = []api.SecurityRequirement{{Scheme: "apiKeyAuth"}}
	normalized, err := target.Normalize()
	require.NoError(t, err)

	invoker := invokerForTest(t, StaticCredentials{"apiKeyAuth": "secret"})
	invoke(t, invoker, normalized, server, operation, `{}`)

	require.NotNil(t, received)
	assert.Equal(t, "secret", received.Header.Get("X-API-Key"))
}

func TestAdapterRefusesOperationWithoutAvailableCredential(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an unauthenticated request must not be sent")
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = upstream.URL
	target.Security = []api.SecurityRequirement{{Scheme: "apiKeyAuth"}}
	target.SecuritySchemes = []api.SecurityScheme{{Name: "apiKeyAuth", Type: "apiKey", In: "header", ParameterName: "X-API-Key"}}
	normalized, err := target.Normalize()
	require.NoError(t, err)

	// A deployment with no credential source at all is reported differently from
	// one that simply lacks this scheme's value, so the cause is obvious.
	err = invokeExpectingError(t, invokerForTest(t, nil), normalized, server, operation, "")
	assert.Contains(t, err.Error(), "supplies no credentials")

	err = invokeExpectingError(t, invokerForTest(t, StaticCredentials{}), normalized, server, operation, "")
	assert.Contains(t, err.Error(), "no credential is available")
}

func TestAdapterReportsUpstreamErrorStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = upstream.URL

	response := invoke(t, invokerForTest(t, nil), target, server, operation, `{}`)
	assert.Equal(t, http.StatusTeapot, response.Status)
	assert.JSONEq(t, `{"error":"nope"}`, string(response.Body))
}

func TestAdapterWrapsNonJSONResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("plain text body"))
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = upstream.URL

	response := invoke(t, invokerForTest(t, nil), target, server, operation, `{}`)
	// The value stays valid JSON so a tool result is never invalid JSON.
	assert.JSONEq(t, `"plain text body"`, string(response.Body))
}

// invoke performs one call through the invoker, which is what a transport layer
// does with an invocation request.
func invoke(t *testing.T, invoker *Invoker, target api.API, server api.Server, operation api.Operation, arguments string) api.Result {
	t.Helper()
	result, err := invoker.Invoke(context.Background(), api.Call{
		Server:    server,
		API:       target,
		Operation: operation,
		Arguments: json.RawMessage(arguments),
	})
	require.NoError(t, err)
	return result
}

// invokeExpectingError performs a call that must fail before or during the call.
func invokeExpectingError(
	t *testing.T,
	invoker *Invoker,
	target api.API,
	server api.Server,
	operation api.Operation,
	arguments string,
) error {
	t.Helper()
	_, err := invoker.Invoke(context.Background(), api.Call{
		Server:    server,
		API:       target,
		Operation: operation,
		Arguments: json.RawMessage(arguments),
	})
	require.Error(t, err)
	return err
}
