// Package tool implements an independent capability/tool service. Reflection
// exposes its RPC contract, while individual tool capabilities remain
// explicitly registered and policy-visible.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	toolv1 "github.com/Manu343726/toolbox/subsystems/tool/toolv1"
	"github.com/Manu343726/toolbox/subsystems/tool/toolv1/toolv1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "tool"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Invoker executes one registered tool.
type Invoker func(context.Context, json.RawMessage) (any, error)

// Options configures the tool subsystem.
type Options struct {
	// Tools are descriptors exposed by the service.
	Tools []*toolv1.ToolDescriptor
	// Invokers maps tool names to local implementations.
	Invokers map[string]Invoker
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Handler implements ToolService.
type Handler struct {
	mu       sync.RWMutex
	tools    map[string]*toolv1.ToolDescriptor
	invokers map[string]Invoker
}

// NewHandler creates a tool handler.
func NewHandler(options Options) *Handler {
	handler := &Handler{tools: make(map[string]*toolv1.ToolDescriptor), invokers: make(map[string]Invoker)}
	for _, descriptor := range options.Tools {
		if descriptor != nil && descriptor.GetName() != "" {
			handler.tools[descriptor.GetName()] = cloneTool(descriptor)
		}
	}
	for name, invoker := range options.Invokers {
		if name != "" && invoker != nil {
			handler.invokers[name] = invoker
		}
	}
	return handler
}

// Register adds or replaces a tool descriptor and optional implementation.
func (h *Handler) Register(descriptor *toolv1.ToolDescriptor, invoker Invoker) error {
	if descriptor == nil || strings.TrimSpace(descriptor.GetName()) == "" {
		return fmt.Errorf("tool name is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tools[descriptor.GetName()] = cloneTool(descriptor)
	if invoker != nil {
		h.invokers[descriptor.GetName()] = invoker
	}
	return nil
}

// New is the programmatic in-process entrypoint for the tool subsystem.
func New(options Options) (*subsystem.Server, error) {
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := toolv1connect.NewToolServiceHandler(NewHandler(options))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Exposes explicitly declared tools and their invocations.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:         toolv1connect.ToolServiceName,
			Path:         path,
			Handler:      handler,
			Capabilities: []string{"tool.list", "tool.invoke"},
		}},
	})
}

// ListTools returns registered tool descriptors.
func (h *Handler) ListTools(_ context.Context, req *connect.Request[toolv1.ListToolsRequest]) (*connect.Response[toolv1.ListToolsResponse], error) {
	prefix := ""
	if req != nil && req.Msg != nil {
		prefix = req.Msg.GetNamePrefix()
	}
	h.mu.RLock()
	result := make([]*toolv1.ToolDescriptor, 0)
	for _, descriptor := range h.tools {
		if prefix == "" || strings.HasPrefix(descriptor.GetName(), prefix) {
			result = append(result, cloneTool(descriptor))
		}
	}
	h.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].GetName() < result[j].GetName() })
	return connect.NewResponse(&toolv1.ListToolsResponse{Tools: result}), nil
}

// InvokeTool invokes a registered tool.
func (h *Handler) InvokeTool(ctx context.Context, req *connect.Request[toolv1.InvokeToolRequest]) (*connect.Response[toolv1.InvokeToolResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}
	h.mu.RLock()
	descriptor, exists := h.tools[req.Msg.GetName()]
	invoker := h.invokers[req.Msg.GetName()]
	h.mu.RUnlock()
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tool %q not found", req.Msg.GetName()))
	}
	if invoker == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("tool %q has no local invoker", descriptor.GetName()))
	}
	var args any
	if strings.TrimSpace(req.Msg.GetArgumentsJson()) != "" {
		if err := json.Unmarshal([]byte(req.Msg.GetArgumentsJson()), &args); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("arguments_json is invalid: %w", err))
		}
	}
	result, err := invoker(ctx, json.RawMessage(req.Msg.GetArgumentsJson()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal tool result: %w", err))
	}
	return connect.NewResponse(&toolv1.InvokeToolResponse{ResultJson: string(encoded)}), nil
}

func cloneTool(value *toolv1.ToolDescriptor) *toolv1.ToolDescriptor {
	if value == nil {
		return nil
	}
	return &toolv1.ToolDescriptor{
		Name: value.GetName(), Description: value.GetDescription(), InputSchemaJson: value.GetInputSchemaJson(),
		Mutating: value.GetMutating(), RequiredPermission: value.GetRequiredPermission(),
	}
}
