// Package testecho provides a small independent RPC service used by discovery,
// documentation, and CLI integration tests.
package testecho

import (
	"context"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	echov1 "github.com/Manu343726/toolbox/subsystems/testecho/echov1"
	"github.com/Manu343726/toolbox/subsystems/testecho/echov1/echov1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "testecho"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the test service.
type Options struct {
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Handler implements EchoService.
type Handler struct{}

// NewHandler creates an echo handler.
func NewHandler() *Handler { return &Handler{} }

// New is the programmatic in-process entrypoint for the test service.
func New(options Options) (*subsystem.Server, error) {
	if err := shareddocs.RegisterEmbeddedFile(echoDescriptorSet); err != nil {
		return nil, err
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := echov1connect.NewEchoServiceHandler(NewHandler())
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Reference echo service for framework integration tests.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:    echov1connect.EchoServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
}

// Echo returns the input message, optionally transformed.
func (h *Handler) Echo(_ context.Context, req *connect.Request[echov1.EchoRequest]) (*connect.Response[echov1.EchoResponse], error) {
	if req == nil || req.Msg == nil {
		return connect.NewResponse(&echov1.EchoResponse{}), nil
	}
	message := req.Msg.GetMessage()
	if req.Msg.GetUppercase() || req.Msg.GetMode() == echov1.EchoMode_ECHO_MODE_UPPER {
		message = strings.ToUpper(message)
	}
	return connect.NewResponse(&echov1.EchoResponse{
		Message: message,
		Length:  int32(utf8.RuneCountInString(message)),
		Tags:    append([]string(nil), req.Msg.GetTags()...),
	}), nil
}

// StreamEcho emits one response for each requested repetition.
func (h *Handler) StreamEcho(_ context.Context, req *connect.Request[echov1.EchoRequest], stream *connect.ServerStream[echov1.EchoResponse]) error {
	if req == nil || req.Msg == nil {
		return stream.Send(&echov1.EchoResponse{})
	}
	repeat := req.Msg.GetRepeat()
	if repeat < 1 {
		repeat = 1
	}
	for i := int32(0); i < repeat; i++ {
		response, err := h.Echo(context.Background(), connect.NewRequest(req.Msg))
		if err != nil {
			return err
		}
		if err := stream.Send(response.Msg); err != nil {
			return err
		}
	}
	return nil
}
