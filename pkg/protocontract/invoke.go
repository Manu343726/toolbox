package protocontract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Invoker calls operations of APIs described by a protobuf contract.
//
// The standard model addresses an operation by service and method name, so the
// invoker resolves the method through the endpoint's own reflection and then
// performs a dynamic unary call. Reflection is cached per endpoint: a call must
// not re-describe a service that has not changed.
type Invoker struct {
	mu      sync.Mutex
	client  *http.Client
	clients map[string]*discovery.Client
}

// InvokerOptions configures the invoker.
type InvokerOptions struct {
	// HTTPClient performs the calls. The zero value uses a bounded client.
	HTTPClient *http.Client
	// RequestTimeout bounds one invocation. It defaults to 30 seconds.
	RequestTimeout time.Duration
	// Clients optionally supplies pre-built discovery clients, keyed by base URL,
	// for a deployment that shares one client per endpoint.
	Clients map[string]*discovery.Client
}

// NewInvoker creates the contract invoker.
func NewInvoker(options InvokerOptions) *Invoker {
	client := options.HTTPClient
	if client == nil {
		client = BoundedClient(options.RequestTimeout)
	}
	invoker := &Invoker{client: client, clients: make(map[string]*discovery.Client, len(options.Clients))}
	for endpoint, existing := range options.Clients {
		invoker.clients[endpoint] = existing
	}
	return invoker
}

// Invoke implements api.Invoker.
func (i *Invoker) Invoke(ctx context.Context, call api.Call) (api.Result, error) {
	server := call.Server
	operation := call.Operation
	if strings.TrimSpace(server.BaseURL) == "" {
		return api.Result{}, api.Errorf(api.KindInvalid, "server %q has no base url", server.ID)
	}
	if operation.Streaming.Streaming() {
		// A streaming method is described and can be exposed for discovery, but
		// this invoker calls unary methods only, and says so rather than hanging
		// on a stream.
		return api.Result{}, api.Errorf(
			api.KindUnsupported,
			"operation %q is streaming; this invoker calls unary methods only", operation.ID,
		)
	}
	schema, err := i.clientFor(server.BaseURL).DescribeService(ctx, operation.Service)
	if err != nil {
		// An endpoint that cannot be reached is a deployment problem; an endpoint
		// that answers but does not describe the service is a description problem.
		// The two are reported differently, because the fix differs.
		kind := api.KindFailedPrecondition
		if unreachable(err) {
			kind = api.KindUnavailable
		}
		return api.Result{}, api.WrapError(
			kind, err, "describe service %q at %q", operation.Service, server.BaseURL,
		)
	}
	method, found := findMethod(schema, operation.Name)
	if !found {
		return api.Result{}, api.Errorf(
			api.KindNotFound,
			"service %q at %q has no method %q", operation.Service, server.BaseURL, operation.Name,
		)
	}
	payload, err := RequestPayload(call.Arguments)
	if err != nil {
		return api.Result{}, err
	}
	request := dynamicpb.NewMessage(method.Input)
	if len(payload) > 0 {
		if err := (protojson.UnmarshalOptions{}).Unmarshal(payload, request); err != nil {
			return api.Result{}, api.WrapError(api.KindInvalid, err, "decode the request for %s", operation.ID)
		}
	}
	response, err := i.clientFor(server.BaseURL).Invoke(ctx, operation.Service, operation.Name, request)
	if err != nil {
		return api.Result{}, api.WrapError(api.KindUnavailable, err, "call %s", operation.ID)
	}
	encoded, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(response)
	if err != nil {
		return api.Result{}, api.WrapError(api.KindInternal, err, "encode the response for %s", operation.ID)
	}
	return api.Result{
		Status:      200,
		ContentType: "application/json",
		Body:        encoded,
	}, nil
}

// HandlesTransport reports whether this package can perform a call over one
// transport itself.
//
// The answer is read from [TransportDescriptors] rather than restated, because the
// list of transports a deployment may describe and the list this package can call
// over are the same fact written down twice. A catalog uses it to decide whether
// an operation needs an invoker provider deployed alongside it: it does not for a
// transport the framework speaks, and it does for one the framework does not.
func HandlesTransport(transport api.Transport) bool {
	for _, descriptor := range TransportDescriptors() {
		if api.Transport(descriptor.ID) == transport {
			return true
		}
	}
	return false
}

// unreachable reports whether a failure is a transport failure — the endpoint
// could not be reached at all — rather than a description the endpoint refused to
// give.
func unreachable(err error) bool {
	if err == nil {
		return false
	}
	if api.KindOf(err) == api.KindUnavailable {
		return true
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return true
	default:
		return false
	}
}

func (i *Invoker) clientFor(endpoint string) *discovery.Client {
	i.mu.Lock()
	defer i.mu.Unlock()
	if existing, ok := i.clients[endpoint]; ok {
		return existing
	}
	client := discovery.New(endpoint, DiscoveryOptions(i.client)...)
	i.clients[endpoint] = client
	return client
}

func findMethod(schema *discovery.ServiceSchema, name string) (discovery.MethodSchema, bool) {
	if schema == nil {
		return discovery.MethodSchema{}, false
	}
	for _, method := range schema.Methods {
		if method.Name == name {
			return method, true
		}
	}
	return discovery.MethodSchema{}, false
}

// RequestPayload returns the request bytes for a call. A protobuf request message
// is supplied as the argument object itself, or under a "body" key when the
// caller wraps it the way a description with a named body would.
func RequestPayload(arguments json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(arguments))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage("{}"), nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &values); err != nil {
		return nil, api.WrapError(api.KindInvalid, err, "arguments must be a JSON object")
	}
	if raw, ok := values["body"]; ok {
		if len(raw) == 0 || string(raw) == "null" {
			return json.RawMessage("{}"), nil
		}
		return raw, nil
	}
	return arguments, nil
}

// assert the invoker satisfies the framework's interface at compile time.
var _ api.Invoker = (*Invoker)(nil)

// FormatString renders a format identifier for messages.
func FormatString(format api.Format) string { return fmt.Sprintf("%s", string(format)) }
