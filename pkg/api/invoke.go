package api

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
)

// This file is the one place the framework's invoke request and its call are the
// same thing. An invoke request is how a catalog addresses an invoker provider;
// a Call is what the invocation itself receives. A deployment that performs the
// call in process and a deployment that hands it to a provider must not disagree
// about what the request carried, so both read it through here.

// CallFromInvokeRequest reads the framework's invoke request as a call.
//
// The server and the operation are required: the two together are what make a
// call addressable, and a call missing either is not a call. The API is not — an
// invoker reaches the operation through the server, so a request that carries
// only those two is a complete call, and requiring the third would refuse a
// request that names everything needed to perform one. When it is present it is
// read, because a caller that sent it expects it to be the API being called.
//
// The arguments travel as written, including a body-wrapped request, which the
// invoker itself unwraps — that shape is the caller's, not this function's to
// change.
func CallFromInvokeRequest(request *apiv1.InvokeApiRequest) (Call, error) {
	if request == nil {
		return Call{}, Errorf(KindInvalid, "an invoke request is required")
	}
	server, err := ServerFromProto(request.GetServer())
	if err != nil {
		return Call{}, err
	}
	operation, err := OperationFromProto(request.GetOperation())
	if err != nil {
		return Call{}, err
	}
	call := Call{
		Server:    server,
		Operation: operation,
		Arguments: request.GetArgumentsJson(),
	}
	if request.GetApi() != nil {
		described, err := APIFromProto(request.GetApi())
		if err != nil {
			return Call{}, err
		}
		call.API = described
	}
	return call, nil
}

// InvokeResponseFromResult renders a result as the response an invoker provider
// answers with.
//
// The two invocations a deployment can perform — in process, or by handing the
// request to a provider — are reported in the same shape, so a caller cannot
// tell from the response which one happened. That is the point: a deployment
// adding or removing an invoker provider changes how a call travels, not what the
// caller receives.
func InvokeResponseFromResult(result Result) *apiv1.InvokeApiResponse {
	response := &apiv1.InvokeApiResponse{
		Status:      int32(result.Status),
		ContentType: result.ContentType,
		BodyJson:    result.Body,
	}
	// Headers are only carried when there are some, because an empty map in the
	// response is a claim that the transport sent no headers rather than that it
	// sent none.
	//
	// A call may produce a header more than once and the response contract holds
	// one value per name, so repeated values are combined the way HTTP combines
	// them: joined in order with a comma. A single-valued header — which is what
	// every header a call actually produces — passes through unchanged, and a
	// repeated one is not silently dropped for want of a field to hold it.
	if len(result.Headers) > 0 {
		response.Headers = make(map[string]string, len(result.Headers))
		for name, values := range result.Headers {
			response.Headers[name] = strings.Join(values, ", ")
		}
	}
	return response
}

// ServeInvoke performs a call from an invoke request and answers in the response
// shape, which is what an implementation of the invoker contract does.
//
// The two functions above are the whole translation. A provider that implements
// the contract over ConnectRPC and a deployment that implements it in process
// produce the same response from the same request, and a test asserts it.
//
// A failure is returned with its kind and not with a code. This package is
// transport-independent, so it says what went wrong and the transport layer
// decides what that is: a ConnectRPC handler maps KindInvalid to InvalidArgument
// and KindNotFound to NotFound, and a caller speaking neither transport can still
// read the kind.
func ServeInvoke(ctx context.Context, invoker Invoker, request *connect.Request[apiv1.InvokeApiRequest]) (*connect.Response[apiv1.InvokeApiResponse], error) {
	if invoker == nil {
		return nil, Errorf(KindUnsupported, "no invoker is configured to perform this call")
	}
	call, err := CallFromInvokeRequest(request.Msg)
	if err != nil {
		return nil, WrapError(KindInvalid, err, "read the invoke request")
	}
	result, err := invoker.Invoke(ctx, call)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(InvokeResponseFromResult(result)), nil
}
