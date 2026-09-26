// Package model implements a provider-neutral model catalog and a deterministic
// reference provider. Real providers can be added as independent services.
package model

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	modelv1 "github.com/Manu343726/toolbox/subsystems/model/modelv1"
	"github.com/Manu343726/toolbox/subsystems/model/modelv1/modelv1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "model"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the model subsystem.
type Options struct {
	// Models optionally supplies the reference catalog.
	Models []*modelv1.ModelDescriptor
	// Generate optionally supplies a provider implementation.
	Generate func(context.Context, *modelv1.GenerateRequest) (string, error)
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Handler implements ModelService.
type Handler struct {
	mu       sync.RWMutex
	models   []*modelv1.ModelDescriptor
	generate func(context.Context, *modelv1.GenerateRequest) (string, error)
}

// NewHandler creates a model handler.
func NewHandler(options Options) *Handler {
	models := options.Models
	if len(models) == 0 {
		models = []*modelv1.ModelDescriptor{{
			Id:            "reference/echo",
			Provider:      "reference",
			DisplayName:   "Reference deterministic model",
			ContextWindow: 8192,
			SupportsTools: false,
		}}
	}
	generate := options.Generate
	if generate == nil {
		// The reference provider classifies its own failure, so the code a caller
		// sees comes from what went wrong rather than from a guess made here.
		generate = func(_ context.Context, req *modelv1.GenerateRequest) (string, error) {
			if req.GetModelId() != "reference/echo" {
				return "", api.Errorf(api.KindNotFound, "model %q is not available", req.GetModelId())
			}
			return req.GetPrompt(), nil
		}
	}
	return &Handler{models: cloneModels(models), generate: generate}
}

// New is the programmatic in-process entrypoint for the model subsystem.
func New(options Options) (*subsystem.Server, error) {
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := modelv1connect.NewModelServiceHandler(NewHandler(options))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Exposes provider-neutral model capabilities and generation.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:    modelv1connect.ModelServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
}

// ListModels returns available models.
func (h *Handler) ListModels(_ context.Context, req *connect.Request[modelv1.ListModelsRequest]) (*connect.Response[modelv1.ListModelsResponse], error) {
	provider := ""
	if req != nil && req.Msg != nil {
		// A filter of only whitespace is no filter, the same as an absent one, so a
		// client that built the field from an unset variable is not answered with
		// an empty catalog.
		provider = strings.TrimSpace(req.Msg.GetProvider())
	}
	h.mu.RLock()
	result := make([]*modelv1.ModelDescriptor, 0)
	for _, descriptor := range h.models {
		if provider == "" || descriptor.GetProvider() == provider {
			result = append(result, cloneModel(descriptor))
		}
	}
	h.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].GetId() < result[j].GetId() })
	return connect.NewResponse(&modelv1.ListModelsResponse{Models: result}), nil
}

// Generate invokes the configured provider implementation.
func (h *Handler) Generate(ctx context.Context, req *connect.Request[modelv1.GenerateRequest]) (*connect.Response[modelv1.GenerateResponse], error) {
	// A model identifier that is only whitespace names nothing, and accepting it
	// would hand the provider a request it cannot match — which answers not-found
	// and sends the caller looking for a model that is right there in the catalog.
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetModelId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("model_id is required"))
	}
	text, err := h.generate(ctx, req.Msg)
	if err != nil {
		// A provider says what went wrong and the mapping turns that into a code.
		// Reporting every failure as not-found sent a caller looking for a model
		// that exists, and told a caller that cancelled to investigate a request it
		// had already abandoned.
		return nil, api.ConnectError(err)
	}
	return connect.NewResponse(&modelv1.GenerateResponse{ModelId: req.Msg.GetModelId(), Text: text}), nil
}

func cloneModels(values []*modelv1.ModelDescriptor) []*modelv1.ModelDescriptor {
	result := make([]*modelv1.ModelDescriptor, 0, len(values))
	for _, value := range values {
		result = append(result, cloneModel(value))
	}
	return result
}

func cloneModel(value *modelv1.ModelDescriptor) *modelv1.ModelDescriptor {
	if value == nil {
		return nil
	}
	return &modelv1.ModelDescriptor{
		Id: value.GetId(), Provider: value.GetProvider(), DisplayName: value.GetDisplayName(),
		ContextWindow: value.GetContextWindow(), SupportsTools: value.GetSupportsTools(),
	}
}
