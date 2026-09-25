package apimcp

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
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
)

// This file is the RPC layer of the adapter contract. The translation is
// [toolboxmcp.Render] and [toolboxmcp.Manifest]; this converts requests and results.

var _ apiv1connect.ApiAdapterServiceHandler = (*Adapter)(nil)

// AdapterOptions configures the adapter.
type AdapterOptions struct {
	// HTTPClient is reserved for a target whose rendering needs the network. The
	// MCP target is a pure translation, so it is currently unused and named for
	// the shape a deployment configures.
	HTTPClient *http.Client
	// RequestTimeout bounds any work the target does. It defaults to 30 seconds.
	RequestTimeout time.Duration
}

// Adapter serves the framework's adapter contract, rendering a standard
// description into a Model Context Protocol tool manifest.
type Adapter struct {
	options AdapterOptions
}

// NewAdapter creates the Model Context Protocol adapter.
func NewAdapter(options AdapterOptions) *Adapter {
	return &Adapter{options: options}
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
	if !toolboxmcp.HandlesTarget(target) {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("this adapter produces %s, not %q", strings.Join(toolboxmcp.Targets(), ", "), target),
		)
	}
	described, err := api.APIFromProto(request.Msg.GetApi())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source description: %w", err))
	}
	options, err := renderOptions(request.Msg.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	rendered, err := toolboxmcp.Render(described, options)
	if err != nil {
		return nil, providerError(err)
	}
	document, err := toolboxmcp.Manifest(described, options)
	if err != nil {
		return nil, providerError(err)
	}
	// The target's schema is a file set, because a target may be described by more
	// than one file, and one of them is marked primary.
	return connect.NewResponse(&apiv1.RenderApiResponse{
		Files: []*apiv1.ApiSchemaFile{{
			Name:      toolboxmcp.ToolManifestFile,
			Content:   document,
			MediaType: "application/json",
			Primary:   true,
		}},
		MediaType:     "application/json",
		FileExtension: "json",
		Warnings:      rendered.Warnings,
		Targets:       []*apiv1.ApiFormatDescriptor{TargetDescriptor().ToProto()},
	}), nil
}

// ServeApi implements the framework's ApiAdapterService.
//
// Serving an adapted MCP surface means forwarding each tool call to the original
// API, and the original API's transport is not this adapter's to know: a catalog
// holds an invoker provider for it and routes through that. So this face reports
// what it cannot do and why, rather than serving a surface whose calls would fail.
//
// A deployment that wants MCP in front of an API has it already: a catalog-backed
// gateway serves a description as tools and calls the original through the
// invoker it selected.
func (a *Adapter) ServeApi(_ context.Context, request *connect.Request[apiv1.ServeApiRequest]) (*connect.Response[apiv1.ServeApiResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	target := strings.TrimSpace(request.Msg.GetTarget())
	if target != "" && !toolboxmcp.HandlesTarget(target) {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("this adapter serves %s, not %q", strings.Join(toolboxmcp.Targets(), ", "), target),
		)
	}
	original, err := api.ServerFromProto(request.Msg.GetServer())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil, connect.NewError(
		connect.CodeFailedPrecondition,
		fmt.Errorf(
			"cannot serve %s in front of %q: forwarding a tool call needs an invoker for %q, which the catalog selects; serve the description through a catalog instead",
			targetOrMCP(target), original.ID, original.Transport,
		),
	)
}

func targetOrMCP(target string) string {
	if target == "" {
		return toolboxmcp.ToolCallMethod
	}
	return target
}

// StopApi implements the framework's ApiAdapterService. This adapter serves no
// surface of its own, so there is nothing to stop.
func (a *Adapter) StopApi(_ context.Context, request *connect.Request[apiv1.StopApiRequest]) (*connect.Response[apiv1.StopApiResponse], error) {
	if request == nil || request.Msg == nil || strings.TrimSpace(request.Msg.GetInstanceId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance_id is required"))
	}
	return connect.NewResponse(&apiv1.StopApiResponse{Stopped: false}), nil
}

// renderOptions reads the target's switches. A switch this target does not know is
// refused rather than ignored, because a caller that misspelled one would otherwise
// get a document that quietly differs from what it asked for.
//
// Every operation a description declares is published, so there is no switch for
// keeping a service out: an operation is withheld by not being described, or by the
// deployment that reads the manifest, not by a name matched here.
func renderOptions(values map[string]string) (toolboxmcp.RenderOptions, error) {
	options := toolboxmcp.RenderOptions{}
	for name, value := range values {
		switch api.NormalizeIdentifier(name) {
		case "instructions":
			options.Instructions = value
		case "only":
			options.Only = splitList(value)
		case "exclude-tools":
			options.ExcludeTools = splitList(value)
		default:
			return toolboxmcp.RenderOptions{}, fmt.Errorf("unknown option %q for the %s target", name, TargetMCP)
		}
	}
	return options, nil
}

func booleanOption(name, value string) (bool, error) {
	switch api.NormalizeIdentifier(value) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("option %q must be true or false, not %q", name, value)
	}
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// manifestBytes is the rendered document, for a caller that wants it without the
// contract types in the way.
func manifestBytes(document []byte) string {
	if len(document) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(document, &decoded); err != nil {
		return string(document)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return string(document)
	}
	return string(encoded)
}
