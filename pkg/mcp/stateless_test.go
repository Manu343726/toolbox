package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The HTTP endpoint is stateless so that it can serve protocol revision 2026-07-28,
// which is the revision the MCP Skills extension is specified against. A stateful
// handler cannot serve that revision over streamable HTTP at all — it negotiates down
// to 2025-11-25, and the extension specifies a minimum. These tests pin the two facts
// that depend on, because both are invisible from the outside: a handler that quietly
// negotiated down answers requests just as successfully.
//
// The revision is not declared in a header alone or in the request body alone. Under
// 2026-07-28 there is no initialize handshake, so every request carries its revision
// in its own metadata; the transport additionally requires the header to accompany it.
// A request missing either is refused with a message naming the other, which is what
// these tests exercise first — a request shaped for the wrong revision is refused
// rather than served, and a refusal that names the fix is what a client follows.
func TestTheHTTPEndpointServesTheRevisionSkillsAreSpecifiedAgainst(t *testing.T) {
	bridge, err := New(t.Context(), newFakeSource(t), Options{Policy: APIPolicy()})
	require.NoError(t, err)
	handler := bridge.HTTPHandler()

	// server/discover is how a 2026-07-28 client asks what a server speaks. It does
	// not exist on 2025-11-25, so answering it at all means the endpoint is on the
	// revision the extension requires.
	status, envelope, header := postJSON(t, handler, discover)
	require.Equal(t, http.StatusOK, status,
		"server/discover is answered; the response was %v", envelope)

	assert.Empty(t, header.Get("Mcp-Session-Id"),
		"a stateless endpoint issues no session id, so a client that needed one would be using a revision this does not serve")

	result, _ := envelope["result"].(map[string]any)
	versions, _ := result["supportedVersions"].([]any)
	offered := make([]string, 0, len(versions))
	for _, version := range versions {
		if text, ok := version.(string); ok {
			offered = append(offered, text)
		}
	}
	assert.Contains(t, offered, ProtocolRevision,
		"the revision the MCP Skills extension requires is offered, or the extension cannot be served over HTTP at all")
}

// A stateless endpoint answers each request on its own. Two requests with no session
// between them are two independent requests, which is what the sessionless revision
// means and what lets an endpoint behind a load balancer serve a client that never
// returns to the same process.
func TestTheHTTPEndpointServesEachRequestIndependently(t *testing.T) {
	bridge, err := New(t.Context(), newFakeSource(t), Options{Policy: APIPolicy()})
	require.NoError(t, err)
	handler := bridge.HTTPHandler()

	firstStatus, first, _ := postJSON(t, handler, listToolSurface)
	secondStatus, second, _ := postJSON(t, handler, listToolSurface)
	assert.Equal(t, http.StatusOK, firstStatus)
	assert.Equal(t, http.StatusOK, secondStatus)
	assert.Equal(t, first, second,
		"a second request is answered the same, with nothing carried from the first")
}

// jsonRPC is one request, as a client of the sessionless revision builds it: the
// revision, the method and anything the method names appear both in the body and in
// headers, because the body may be opaque to an intermediary and a header may not be.
type jsonRPC struct {
	method string
	// name is what the method names — a tool, a prompt, a resource URI. The revision
	// requires it in a header for the three methods that take one.
	name string
	// params is the request's own parameters, excluding the revision metadata.
	params map[string]any
}

// request builds the body. Everything a client used to declare once, at initialize,
// it now declares on every request, because this revision has no handshake and so no
// place to remember a declaration: the revision itself, who the client is, and what
// it can do. A server that served a session would remember those; a sessionless one
// reads them off the request, which is why a skill's content can depend on the client
// it is being served to without a connection in hand.
func (r jsonRPC) request() map[string]any {
	params := map[string]any{"_meta": map[string]any{
		"io.modelcontextprotocol/protocolVersion": ProtocolRevision,
		"io.modelcontextprotocol/clientInfo": map[string]any{
			"name":    "toolbox-test-client",
			"version": "1.0.0",
		},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{
			"tools": map[string]any{},
		},
	}}
	for key, value := range r.params {
		params[key] = value
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  r.method,
		"params":  params,
	}
}

// header returns the headers a request of this shape must carry. The method is
// required on every request at this revision; the name is required on the three
// methods that name something, and the SDK refuses a mismatch with the body rather
// than trusting whichever it reads first.
func (r jsonRPC) header() http.Header {
	header := http.Header{}
	header.Set("Mcp-Protocol-Version", ProtocolRevision)
	header.Set("Mcp-Method", r.method)
	if r.name != "" {
		header.Set("Mcp-Name", r.name)
	}
	return header
}

// postJSON sends one request the way a stateless client of this revision does, and
// returns the status, the decoded envelope and the response headers.
func postJSON(t *testing.T, handler http.Handler, call jsonRPC) (int, map[string]any, http.Header) {
	t.Helper()
	body, err := json.Marshal(call.request())
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	// Both media types: the streamable transport requires a client to be willing to
	// take either, one for a single response and one for a stream.
	request.Header.Set("Accept", "application/json, text/event-stream")
	for key, values := range call.header() {
		request.Header[key] = values
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	raw := recorder.Body.String()
	// A response may arrive as one event per data: line rather than as a bare object,
	// so the first data line is taken rather than the whole body.
	for _, line := range bytes.Split([]byte(raw), []byte("\n")) {
		if payload, found := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); found {
			raw = string(bytes.TrimSpace(payload))
			break
		}
	}
	decoded := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &decoded)
	return recorder.Code, decoded, recorder.Header()
}

// discover asks what a server speaks. The method does not exist on 2025-11-25, so
// answering it at all means the endpoint is on the revision the extension requires.
var discover = jsonRPC{method: "server/discover"}

// listTools asks for the tool surface, with nothing named, so no name header.
var listToolSurface = jsonRPC{method: "tools/list"}
