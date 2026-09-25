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

// CapabilitiesFor returns the capabilities a deployment declared for each of a
// contract's services, keyed by service name.
//
// It is the one place that joins a parsed contract to what a server says it does:
// a contract knows which services exist, and a server's manifest knows what those
// services are for. Neither knows about the other, so a deployment that exposes
// its own subsystems supplies the manifest's answer here rather than encoding it
// into a contract that is not allowed to declare it.
func CapabilitiesFor(declared map[string][]string) map[string][]string {
	byService := make(map[string][]string, len(declared))
	for service, capabilities := range declared {
		if trimmed := api.CapabilitiesFor(capabilities); len(trimmed) > 0 {
			byService[service] = trimmed
		}
	}
	if len(byService) == 0 {
		return nil
	}
	return byService
}

// ApplyCapabilities attaches the capabilities a server declared to the services of
// a described API. The join itself belongs to the model, so a description read here
// and one read by a host come out the same.
func ApplyCapabilities(described api.API, declared map[string][]string) api.API {
	return described.WithCapabilities(declared)
}

// ParseWithCapabilities describes a live endpoint and attaches the capabilities
// its server declared, which is the shape a composition needs: one call, one
// description, ready to register.
func (d *Descriptor) ParseWithCapabilities(
	ctx context.Context,
	endpoint string,
	declared map[string][]string,
) (api.API, []string, error) {
	described, warnings, err := d.FromEndpoint(ctx, endpoint)
	if err != nil {
		return api.API{}, warnings, err
	}
	enriched := described.WithCapabilities(CapabilitiesFor(declared))
	normalized, err := enriched.Normalize()
	if err != nil {
		return api.API{}, warnings, api.WrapError(api.KindInvalid, err, "normalize the described API")
	}
	return normalized, warnings, nil
}

// assert the invoker satisfies the framework's interface at compile time.
var _ api.Invoker = (*Invoker)(nil)

// FormatString renders a format identifier for messages.
func FormatString(format api.Format) string { return fmt.Sprintf("%s", string(format)) }
