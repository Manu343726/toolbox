package apiopenapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	"github.com/Manu343726/toolbox/pkg/openapi"
)

// This file is the RPC layer of the invoker contract. The call itself is
// [openapi.Invoker].

// InvokerOptions configures the invoker.
type InvokerOptions struct {
	// HTTPClient performs the calls. The zero value uses a bounded client.
	HTTPClient *http.Client
	// RequestTimeout bounds one invocation. It defaults to 30 seconds.
	RequestTimeout time.Duration
	// Credentials supplies the credential for a security scheme a description
	// requires.
	Credentials CredentialSource
}

// Invoker serves the framework's invoker contract for APIs described by an
// OpenAPI document.
type Invoker struct {
	invoker *openapi.Invoker
}

// NewInvoker creates the HTTP invoker.
func NewInvoker(options InvokerOptions) *Invoker {
	return &Invoker{invoker: openapi.NewInvoker(openapi.InvokerOptions{
		HTTPClient:     options.HTTPClient,
		RequestTimeout: options.RequestTimeout,
		Credentials:    options.Credentials,
	})}
}

// Invoke implements api.Invoker, so a host with this provider in process calls an
// operation without a round trip through the contract.
func (i *Invoker) Invoke(ctx context.Context, call api.Call) (api.Result, error) {
	return i.invoker.Invoke(ctx, call)
}

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
	described, err := api.APIFromProto(request.Msg.GetApi())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("registered api: %w", err))
	}
	result, err := i.Invoke(ctx, api.Call{
		Server:    server,
		API:       described,
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

// providerError maps a classified failure from the implementation package onto a
// ConnectRPC code, so the classification survives the transport instead of being
// re-derived from a message.
func providerError(err error) error {
	return api.ConnectError(err)
}
