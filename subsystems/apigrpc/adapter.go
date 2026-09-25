package apigrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

// AdapterOptions configures the invocation adapter.
type AdapterOptions struct {
	// HTTPClient performs the calls. The zero value uses a bounded client.
	HTTPClient *http.Client
	// RequestTimeout bounds one invocation. It defaults to 30 seconds.
	RequestTimeout time.Duration
	// Clients optionally supplies pre-built discovery clients, keyed by base URL.
	// It is primarily useful for tests and for a deployment that shares one
	// client per endpoint.
	Clients map[string]*discovery.Client
}

// Adapter implements the framework's invocation contract for APIs described by a
// protobuf contract. The registered operation names a protobuf service and
// method, and the request value is the protobuf JSON the caller supplied.
type Adapter struct {
	client  *http.Client
	clients map[string]*discovery.Client
}

// NewAdapter creates the gRPC invocation adapter.
func NewAdapter(options AdapterOptions) *Adapter {
	client := options.HTTPClient
	if client == nil {
		timeout := options.RequestTimeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	clients := make(map[string]*discovery.Client, len(options.Clients))
	for endpoint, existing := range options.Clients {
		clients[endpoint] = existing
	}
	return &Adapter{client: client, clients: clients}
}

// InvokeApi implements the framework's ApiInvokerService.
func (a *Adapter) InvokeApi(ctx context.Context, request *connect.Request[apiv1.InvokeApiRequest]) (*connect.Response[apiv1.InvokeApiResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	operation, err := api.OperationFromProto(request.Msg.GetOperation())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	server, err := api.ServerFromProto(request.Msg.GetServer())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if operation.Streaming.Streaming() {
		// A streaming method is described and can be exposed for discovery, but
		// this adapter invokes unary methods only, and says so instead of
		// hanging on a stream.
		return nil, connect.NewError(
			connect.CodeUnimplemented,
			fmt.Errorf("operation %q is streaming; this adapter invokes unary methods only", operation.ID),
		)
	}
	serviceName := operation.Service
	methodName := operation.Name
	schema, err := a.clientFor(server.BaseURL).DescribeService(ctx, serviceName)
	if err != nil {
		return nil, connect.NewError(
			connect.CodeFailedPrecondition,
			fmt.Errorf("describe service %q at %q: %w", serviceName, server.BaseURL, err),
		)
	}
	method, found := findMethod(schema, methodName)
	if !found {
		return nil, connect.NewError(
			connect.CodeNotFound,
			fmt.Errorf("service %q at %q has no method %q", serviceName, server.BaseURL, methodName),
		)
	}
	payload, err := requestPayload(request.Msg.GetArgumentsJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	requestMessage := dynamicpb.NewMessage(method.Input)
	if len(payload) > 0 {
		if err := (protojson.UnmarshalOptions{}).Unmarshal(payload, requestMessage); err != nil {
			return nil, connect.NewError(
				connect.CodeInvalidArgument,
				fmt.Errorf("decode request for %s: %w", operation.ID, err),
			)
		}
	}
	response, err := a.clientFor(server.BaseURL).Invoke(ctx, serviceName, methodName, requestMessage)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("call %s: %w", operation.ID, err))
	}
	encoded, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(response)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode response: %w", err))
	}
	return connect.NewResponse(&apiv1.InvokeApiResponse{
		Status:      200,
		ContentType: "application/json",
		BodyJson:    encoded,
	}), nil
}

func (a *Adapter) clientFor(endpoint string) *discovery.Client {
	if existing, ok := a.clients[endpoint]; ok {
		return existing
	}
	client := newDiscoveryClient(endpoint, a.client)
	a.clients[endpoint] = client
	return client
}

// findMethod returns the reflected method schema, which carries the request and
// response message descriptors the call needs.
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

// requestPayload returns the request bytes for a call. A gRPC request message is
// supplied as the argument object itself, or under a "body" key when the caller
// wraps it the way a description with a named body would.
func requestPayload(arguments json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(arguments))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage("{}"), nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &values); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
	}
	if raw, ok := values["body"]; ok {
		if len(raw) == 0 || string(raw) == "null" {
			return json.RawMessage("{}"), nil
		}
		return raw, nil
	}
	return arguments, nil
}
