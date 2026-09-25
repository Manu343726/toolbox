package apimcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
)

// This file is the RPC layer of the invoker contract. The call itself is
// [toolboxmcp.Invoker].

// InvokerOptions configures the invoker.
type InvokerOptions struct {
	// HTTPClient performs the protocol calls. The zero value uses a bounded client.
	HTTPClient *http.Client
	// RequestTimeout bounds one protocol call, including the session handshake. It
	// defaults to 30 seconds.
	RequestTimeout time.Duration
}

// Invoker serves the framework's invoker contract for APIs described by a Model
// Context Protocol server.
type Invoker struct {
	invoker *toolboxmcp.Invoker
}

// NewInvoker creates the Model Context Protocol invoker.
func NewInvoker(options InvokerOptions) *Invoker {
	return &Invoker{invoker: toolboxmcp.NewInvoker(toolboxmcp.InvokerOptions{
		HTTPClient:     boundedClient(options.HTTPClient, options.RequestTimeout),
		RequestTimeout: options.RequestTimeout,
	})}
}

// Invoke implements api.Invoker, so a host with this provider in process calls a
// tool without a round trip through the contract.
func (i *Invoker) Invoke(ctx context.Context, call api.Call) (api.Result, error) {
	return i.invoker.Invoke(ctx, call)
}

// Close ends the protocol sessions this invoker opened.
func (i *Invoker) Close() error { return i.invoker.Close() }

// InvokeApi implements the framework's ApiInvokerService.
func (i *Invoker) InvokeApi(ctx context.Context, request *connect.Request[apiv1.InvokeApiRequest]) (*connect.Response[apiv1.InvokeApiResponse], error) {
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
	result, err := i.Invoke(ctx, api.Call{
		Server:    server,
		Operation: operation,
		Arguments: request.Msg.GetArgumentsJson(),
	})
	if err != nil {
		return nil, providerError(err)
	}
	headers := make(map[string]string, len(result.Headers))
	for name, values := range result.Headers {
		if len(values) > 0 {
			headers[name] = values[0]
		}
	}
	return connect.NewResponse(&apiv1.InvokeApiResponse{
		Status:      int32(result.Status),
		ContentType: result.ContentType,
		Headers:     headers,
		BodyJson:    result.Body,
	}), nil
}
