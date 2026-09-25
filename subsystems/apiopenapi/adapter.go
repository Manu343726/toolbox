package apiopenapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/openapi"
)

// This file is the RPC layer of the adapter contract: it translates a request into
// the three calls the implementation package offers — render, serve, and stop —
// and converts the results back.

var _ apiv1connect.ApiAdapterServiceHandler = (*Adapter)(nil)

// AdapterOptions configures the adapter.
type AdapterOptions struct {
	// SwaggerUI is an OpenAPI documentation UI, such as the official swagger-ui
	// bundle. When empty, each adapted surface serves a documentation page
	// generated from its own schema.
	SwaggerUI []byte
	// RequestTimeout bounds one tunnelled request. It defaults to 30 seconds.
	RequestTimeout time.Duration
}

// Adapter serves the framework's adapter contract by combining the two faces of
// adaptation: it returns the target's schema files, and it serves the target's
// surface while tunnelling to the original server.
//
// Keeping both in one service is deliberate. A schema that a client reads and a
// surface the same client calls have to agree, and the only way to guarantee they
// do is to produce both from the same translation.
type Adapter struct {
	surfaces *openapi.Surfaces
}

// NewAdapter creates the combined OpenAPI adapter.
func NewAdapter(options AdapterOptions) *Adapter {
	return &Adapter{
		surfaces: openapi.NewSurfaces(openapi.SurfaceOptions{
			SwaggerUI:      options.SwaggerUI,
			RequestTimeout: options.RequestTimeout,
		}),
	}
}

// RenderApi implements the framework's ApiAdapterService.
func (a *Adapter) RenderApi(_ context.Context, request *connect.Request[apiv1.RenderApiRequest]) (*connect.Response[apiv1.RenderApiResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	target := strings.TrimSpace(request.Msg.GetTarget())
	if target == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("target is required"))
	}
	if !openapi.HandlesTarget(target) {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("this adapter produces %s, not %q", strings.Join(openapi.Targets(), ", "), target),
		)
	}
	described, err := api.APIFromProto(request.Msg.GetApi())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source description: %w", err))
	}
	rendered, err := openapi.Render(described, openapi.RenderOptions{
		MediaType: request.Msg.GetMediaType(),
		Options:   request.Msg.GetOptions(),
	})
	if err != nil {
		return nil, providerError(err)
	}
	files := make([]*apiv1.ApiSchemaFile, 0, len(rendered.Files))
	for _, file := range rendered.Files {
		files = append(files, &apiv1.ApiSchemaFile{
			Name:      file.Name,
			Content:   file.Content,
			MediaType: file.MediaType,
			Primary:   file.Primary,
		})
	}
	return connect.NewResponse(&apiv1.RenderApiResponse{
		Files:         files,
		MediaType:     rendered.MediaType,
		FileExtension: rendered.FileExtension,
		Warnings:      rendered.Warnings,
		Targets:       []*apiv1.ApiFormatDescriptor{TargetDescriptor().ToProto()},
	}), nil
}

// ServeApi implements the framework's ApiAdapterService.
func (a *Adapter) ServeApi(_ context.Context, request *connect.Request[apiv1.ServeApiRequest]) (*connect.Response[apiv1.ServeApiResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	target := strings.TrimSpace(request.Msg.GetTarget())
	if target == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("target is required"))
	}
	if !openapi.HandlesTarget(target) {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("this adapter serves %s, not %q", strings.Join(openapi.Targets(), ", "), target),
		)
	}
	described, err := api.APIFromProto(request.Msg.GetApi())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source description: %w", err))
	}
	original, err := api.ServerFromProto(request.Msg.GetServer())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	served, err := a.surfaces.Serve(described, original, openapi.ServeOptions{
		ListenAddress: request.Msg.GetListenAddress(),
		BasePath:      request.Msg.GetBasePath(),
		Options:       request.Msg.GetOptions(),
	})
	if err != nil {
		return nil, providerError(err)
	}
	return connect.NewResponse(&apiv1.ServeApiResponse{
		Target:                served.Target,
		Endpoint:              served.Endpoint,
		BasePath:              served.BasePath,
		Paths:                 served.Paths,
		InstanceId:            served.InstanceID,
		DocumentationEndpoint: served.DocumentationEndpoint,
		SchemaEndpoint:        served.SchemaEndpoint,
		Warnings:              served.Warnings,
	}), nil
}

// StopApi implements the framework's ApiAdapterService.
func (a *Adapter) StopApi(_ context.Context, request *connect.Request[apiv1.StopApiRequest]) (*connect.Response[apiv1.StopApiResponse], error) {
	if request == nil || request.Msg == nil || strings.TrimSpace(request.Msg.GetInstanceId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance_id is required"))
	}
	return connect.NewResponse(&apiv1.StopApiResponse{Stopped: a.surfaces.Stop(request.Msg.GetInstanceId())}), nil
}
