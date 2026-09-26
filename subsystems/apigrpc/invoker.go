package apigrpc

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	"github.com/Manu343726/toolbox/pkg/protocontract"
)

// This file is the RPC layer of the invoker contract. The call itself is
// [protocontract.Invoker].

// InvokerOptions configures the invoker.
type InvokerOptions struct {
	// HTTPClient performs the calls. The zero value uses a bounded client.
	HTTPClient *http.Client
	// RequestTimeout bounds one invocation. It defaults to 30 seconds.
	RequestTimeout time.Duration
	// Clients optionally supplies pre-built discovery clients, keyed by base URL.
	Clients map[string]*discoveryClient
}

// discoveryClient is the reflection client the implementation package keeps per
// endpoint. It is named here so the option stays readable.
type discoveryClient = discoveryClientAlias

// Invoker serves the framework's invoker contract for APIs described by a
// protobuf contract.
type Invoker struct {
	invoker *protocontract.Invoker
}

// NewInvoker creates the gRPC invocation provider.
func NewInvoker(options InvokerOptions) *Invoker {
	return &Invoker{invoker: protocontract.NewInvoker(protocontract.InvokerOptions{
		HTTPClient:     options.HTTPClient,
		RequestTimeout: options.RequestTimeout,
	})}
}

// Invoke implements api.Invoker, so a host with this provider in process calls
// operations without a round trip through the contract.
func (i *Invoker) Invoke(ctx context.Context, call api.Call) (api.Result, error) {
	return i.invoker.Invoke(ctx, call)
}

// InvokeApi implements the framework's ApiInvokerService.
//
// The request is read and the response written by the framework's own
// translation, which is the same one a deployment that performs a call in process
// uses. Two paths that agreed by accident would stop agreeing the first time one
// of them changed.
func (i *Invoker) InvokeApi(ctx context.Context, request *connect.Request[apiv1.InvokeApiRequest]) (*connect.Response[apiv1.InvokeApiResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	response, err := api.ServeInvoke(ctx, i, request)
	if err != nil {
		return nil, providerError(err)
	}
	return response, nil
}
