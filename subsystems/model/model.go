// Package model implements a provider-neutral model catalog and a deterministic
// reference provider. Real providers can be added as independent services.
package model

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	modelv1 "github.com/Manu343726/toolsbox/subsystems/model/modelv1"
	"github.com/Manu343726/toolsbox/subsystems/model/modelv1/modelv1connect"
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
		generate = func(_ context.Context, req *modelv1.GenerateRequest) (string, error) {
			if req.GetModelId() != "reference/echo" {
				return "", fmt.Errorf("model %q is not available", req.GetModelId())
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
			Name:         modelv1connect.ModelServiceName,
			Path:         path,
			Handler:      handler,
			Capabilities: []string{"model.list", "model.generate"},
		}},
	})
}

// ListModels returns available models.
func (h *Handler) ListModels(_ context.Context, req *connect.Request[modelv1.ListModelsRequest]) (*connect.Response[modelv1.ListModelsResponse], error) {
	provider := ""
	if req != nil && req.Msg != nil {
		provider = req.Msg.GetProvider()
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
	if req == nil || req.Msg == nil || req.Msg.GetModelId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("model_id is required"))
	}
	text, err := h.generate(ctx, req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
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
