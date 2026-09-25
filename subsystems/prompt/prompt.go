// Package prompt implements an independent, provider-neutral prompt template
// service.
package prompt

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	promptv1 "github.com/Manu343726/toolbox/subsystems/prompt/promptv1"
	"github.com/Manu343726/toolbox/subsystems/prompt/promptv1/promptv1connect"
	"google.golang.org/protobuf/proto"
)

const (
	// Name is the stable subsystem name.
	Name = "prompt"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the prompt subsystem.
type Options struct {
	// Store optionally supplies an existing prompt store.
	Store *Store
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Store is a concurrency-safe prompt template store.
type Store struct {
	mu      sync.RWMutex
	prompts map[string]*promptv1.PromptTemplate
}

// NewStore creates an empty prompt store.
func NewStore() *Store { return &Store{prompts: make(map[string]*promptv1.PromptTemplate)} }

func promptKey(id, version string) string { return id + "\x00" + version }

// Put stores a prompt template.
func (s *Store) Put(value *promptv1.PromptTemplate, failIfExists bool) (*promptv1.PromptTemplate, error) {
	if err := validatePrompt(value); err != nil {
		return nil, err
	}
	key := promptKey(value.GetId(), value.GetVersion())
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.prompts[key]; exists && failIfExists {
		return nil, fmt.Errorf("prompt %s@%s already exists", value.GetId(), value.GetVersion())
	}
	copy := proto.Clone(value).(*promptv1.PromptTemplate)
	s.prompts[key] = copy
	return proto.Clone(copy).(*promptv1.PromptTemplate), nil
}

// Get returns a prompt by identifier and optional version.
func (s *Store) Get(id, version string) (*promptv1.PromptTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if version != "" {
		value, ok := s.prompts[promptKey(id, version)]
		if !ok {
			return nil, fmt.Errorf("prompt %s@%s not found", id, version)
		}
		return proto.Clone(value).(*promptv1.PromptTemplate), nil
	}
	var latest *promptv1.PromptTemplate
	for _, value := range s.prompts {
		if value.GetId() == id && (latest == nil || value.GetVersion() > latest.GetVersion()) {
			latest = value
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("prompt %s not found", id)
	}
	return proto.Clone(latest).(*promptv1.PromptTemplate), nil
}

// List returns prompts sorted by identifier and version.
func (s *Store) List(prefix string) []*promptv1.PromptTemplate {
	s.mu.RLock()
	result := make([]*promptv1.PromptTemplate, 0)
	for _, value := range s.prompts {
		if prefix == "" || strings.HasPrefix(value.GetId(), prefix) {
			result = append(result, proto.Clone(value).(*promptv1.PromptTemplate))
		}
	}
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].GetId() == result[j].GetId() {
			return result[i].GetVersion() < result[j].GetVersion()
		}
		return result[i].GetId() < result[j].GetId()
	})
	return result
}

func validatePrompt(value *promptv1.PromptTemplate) error {
	if value == nil {
		return fmt.Errorf("prompt is required")
	}
	if strings.TrimSpace(value.GetId()) == "" || strings.TrimSpace(value.GetVersion()) == "" || strings.TrimSpace(value.GetName()) == "" || strings.TrimSpace(value.GetTemplate()) == "" {
		return fmt.Errorf("id, name, version, and template are required")
	}
	return nil
}

// Handler implements PromptService.
type Handler struct{ store *Store }

// NewHandler creates a prompt handler.
func NewHandler(store *Store) *Handler {
	if store == nil {
		store = NewStore()
	}
	return &Handler{store: store}
}

// New is the programmatic in-process entrypoint for the prompt subsystem.
func New(options Options) (*subsystem.Server, error) {
	store := options.Store
	if store == nil {
		store = NewStore()
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := promptv1connect.NewPromptServiceHandler(NewHandler(store))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Stores and renders provider-neutral prompt templates.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:    promptv1connect.PromptServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
}

// PutPrompt stores a prompt template.
func (h *Handler) PutPrompt(_ context.Context, req *connect.Request[promptv1.PutPromptRequest]) (*connect.Response[promptv1.PutPromptResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	value, err := h.store.Put(req.Msg.GetPrompt(), req.Msg.GetFailIfExists())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&promptv1.PutPromptResponse{Prompt: value}), nil
}

// GetPrompt returns one prompt template.
func (h *Handler) GetPrompt(_ context.Context, req *connect.Request[promptv1.GetPromptRequest]) (*connect.Response[promptv1.GetPromptResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	value, err := h.store.Get(req.Msg.GetId(), req.Msg.GetVersion())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&promptv1.GetPromptResponse{Prompt: value}), nil
}

// ListPrompts returns stored prompt templates.
func (h *Handler) ListPrompts(_ context.Context, req *connect.Request[promptv1.ListPromptsRequest]) (*connect.Response[promptv1.ListPromptsResponse], error) {
	prefix := ""
	if req != nil && req.Msg != nil {
		prefix = req.Msg.GetIdPrefix()
	}
	return connect.NewResponse(&promptv1.ListPromptsResponse{Prompts: h.store.List(prefix)}), nil
}

// RenderPrompt renders a stored template.
func (h *Handler) RenderPrompt(_ context.Context, req *connect.Request[promptv1.RenderPromptRequest]) (*connect.Response[promptv1.RenderPromptResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	value, err := h.store.Get(req.Msg.GetId(), req.Msg.GetVersion())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	text := value.GetTemplate()
	for name, replacement := range req.Msg.GetVariables() {
		text = strings.ReplaceAll(text, "{{"+name+"}}", replacement)
	}
	return connect.NewResponse(&promptv1.RenderPromptResponse{Text: text}), nil
}
