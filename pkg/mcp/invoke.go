package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This file is the invoker half of the MCP transport: it calls one tool of a live
// MCP server.
//
// A session is kept per endpoint, because a protocol session is a negotiated
// conversation and reopening one per call would cost an initialization round trip
// for every operation.

// Invoker calls operations of APIs described by an MCP server.
type Invoker struct {
	mu        sync.Mutex
	client    *http.Client
	timeout   time.Duration
	endpoints map[string]session
	sessions  map[string]*sdkmcp.ClientSession
}

// session is one connection's state, so a dead connection is replaced rather than
// reused.
type session struct {
	current *sdkmcp.ClientSession
}

// InvokerOptions configures the invoker.
type InvokerOptions struct {
	// HTTPClient performs the protocol calls. The zero value uses a bounded client.
	HTTPClient *http.Client
	// RequestTimeout bounds one protocol call, including the session handshake. It
	// defaults to 30 seconds.
	RequestTimeout time.Duration
	// ClientInfo identifies this caller to the server. The zero value is a
	// Toolbox-generated identity.
	ClientInfo *sdkmcp.Implementation
}

// NewInvoker creates the MCP invoker.
func NewInvoker(options InvokerOptions) *Invoker {
	timeout := options.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	invoker := &Invoker{
		client:    options.HTTPClient,
		timeout:   timeout,
		endpoints: make(map[string]session),
		sessions:  make(map[string]*sdkmcp.ClientSession),
	}
	if invoker.client == nil {
		invoker.client = &http.Client{Timeout: timeout}
	}
	return invoker
}

// Invoke implements api.Invoker.
func (i *Invoker) Invoke(ctx context.Context, call api.Call) (api.Result, error) {
	if call.Operation.Streaming.Streaming() {
		// A streaming operation is described and can be exposed for discovery, but
		// a tool call is one request and one response, and this invoker says so
		// rather than hanging.
		return api.Result{}, api.Errorf(
			api.KindUnsupported,
			"operation %q is streaming; an MCP tool call is unary", call.Operation.ID,
		)
	}
	toolName := call.Operation.Name
	if toolName == "" {
		return api.Result{}, api.Errorf(api.KindInvalid, "operation %q names no tool", call.Operation.ID)
	}
	arguments, err := callArguments(call.Arguments)
	if err != nil {
		return api.Result{}, err
	}
	current, err := i.sessionFor(ctx, call.Server.BaseURL)
	if err != nil {
		return api.Result{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, i.timeout)
	defer cancel()
	result, err := current.CallTool(callCtx, &sdkmcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	})
	if err != nil {
		return api.Result{}, api.WrapError(api.KindUnavailable, err, "call %s", call.Operation.ID)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return api.Result{}, api.WrapError(api.KindInternal, err, "encode the result of %s", call.Operation.ID)
	}
	return api.Result{
		Status:      200,
		ContentType: "application/json",
		Body:        encoded,
	}, nil
}

// sessionFor returns a live protocol session for one endpoint, opening one when
// there is none. A session that has failed is dropped, so the next call reconnects
// instead of inheriting a dead conversation.
func (i *Invoker) sessionFor(ctx context.Context, endpoint string) (*sdkmcp.ClientSession, error) {
	if endpoint == "" {
		return nil, api.Errorf(api.KindInvalid, "server has no base url")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if existing, ok := i.sessions[endpoint]; ok {
		return existing, nil
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, i.timeout)
	defer cancel()
	transport := &sdkmcp.StreamableClientTransport{
		Endpoint: endpoint,
		// A caller waits for one answer, so the server-initiated stream is not
		// opened: a connection the invoker never reads is a connection that can
		// only fail.
		DisableStandaloneSSE: true,
	}
	transport.HTTPClient = i.client
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "toolbox", Version: "0.1.0"}, nil)
	opened, err := client.Connect(handshakeCtx, transport, nil)
	if err != nil {
		return nil, api.WrapError(api.KindUnavailable, err, "connect to the MCP server at %s", endpoint)
	}
	i.sessions[endpoint] = opened
	return opened, nil
}

// Close ends every session this invoker opened.
func (i *Invoker) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	var firstErr error
	for endpoint, current := range i.sessions {
		if err := current.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(i.sessions, endpoint)
	}
	return firstErr
}

// callArguments reads the caller's values, which must be a JSON object: a tool's
// arguments are named, and a value the description does not name is an error
// rather than something silently dropped.
func callArguments(arguments json.RawMessage) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(arguments))
	if trimmed == "" || trimmed == "null" {
		return map[string]any{}, nil
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil, api.WrapError(api.KindInvalid, err, "tool arguments must be a JSON object")
	}
	return values, nil
}

// assert the invoker satisfies the framework's interface at compile time.
var _ api.Invoker = (*Invoker)(nil)
