// Package workflow implements an independent, provider-neutral workflow
// definition service. It intentionally does not import agent, skill, or
// model implementations; future execution steps are connected through RPC.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	workflowv1 "github.com/Manu343726/toolsbox/subsystems/workflow/workflowv1"
	"github.com/Manu343726/toolsbox/subsystems/workflow/workflowv1/workflowv1connect"
	"google.golang.org/protobuf/proto"
)

const (
	// Name is the stable subsystem name.
	Name = "workflow"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the workflow subsystem.
type Options struct {
	// Store optionally supplies an existing definition store.
	Store *Store
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Store is the concurrency-safe workflow definition store.
type Store struct {
	mu          sync.RWMutex
	definitions map[string]*workflowv1.WorkflowDefinition
}

// NewStore creates an empty workflow store.
func NewStore() *Store {
	return &Store{definitions: make(map[string]*workflowv1.WorkflowDefinition)}
}

func definitionKey(id, version string) string { return id + "\x00" + version }

// Put stores a definition.
func (s *Store) Put(definition *workflowv1.WorkflowDefinition, failIfExists bool) (*workflowv1.WorkflowDefinition, error) {
	if err := ValidateDefinition(definition); err != nil {
		return nil, err
	}
	key := definitionKey(definition.GetId(), definition.GetVersion())
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.definitions[key]; exists && failIfExists {
		return nil, fmt.Errorf("workflow %s@%s already exists", definition.GetId(), definition.GetVersion())
	}
	copy := proto.Clone(definition).(*workflowv1.WorkflowDefinition)
	s.definitions[key] = copy
	return proto.Clone(copy).(*workflowv1.WorkflowDefinition), nil
}

// Get returns a definition by identifier and optional version.
func (s *Store) Get(id, version string) (*workflowv1.WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if version != "" {
		definition, ok := s.definitions[definitionKey(id, version)]
		if !ok {
			return nil, fmt.Errorf("workflow %s@%s not found", id, version)
		}
		return proto.Clone(definition).(*workflowv1.WorkflowDefinition), nil
	}
	var latest *workflowv1.WorkflowDefinition
	for _, definition := range s.definitions {
		if definition.GetId() != id {
			continue
		}
		if latest == nil || definition.GetVersion() > latest.GetVersion() {
			latest = definition
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("workflow %s not found", id)
	}
	return proto.Clone(latest).(*workflowv1.WorkflowDefinition), nil
}

// List returns definitions sorted by identifier and version.
func (s *Store) List(prefix string) []*workflowv1.WorkflowDefinition {
	s.mu.RLock()
	result := make([]*workflowv1.WorkflowDefinition, 0)
	for _, definition := range s.definitions {
		if prefix == "" || strings.HasPrefix(definition.GetId(), prefix) {
			result = append(result, proto.Clone(definition).(*workflowv1.WorkflowDefinition))
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

// ValidateDefinition checks the portable workflow envelope and graph syntax.
func ValidateDefinition(definition *workflowv1.WorkflowDefinition) error {
	if definition == nil {
		return fmt.Errorf("definition is required")
	}
	if strings.TrimSpace(definition.GetId()) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.TrimSpace(definition.GetName()) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(definition.GetVersion()) == "" {
		return fmt.Errorf("version is required")
	}
	var graph any
	if err := json.Unmarshal([]byte(definition.GetGraphJson()), &graph); err != nil {
		return fmt.Errorf("graph_json is invalid: %w", err)
	}
	if _, ok := graph.(map[string]any); !ok {
		return fmt.Errorf("graph_json must contain a JSON object")
	}
	return nil
}

// Handler implements WorkflowService.
type Handler struct {
	store *Store
}

// NewHandler creates a workflow handler.
func NewHandler(store *Store) *Handler {
	if store == nil {
		store = NewStore()
	}
	return &Handler{store: store}
}

// New is the programmatic in-process entrypoint for the workflow subsystem.
func New(options Options) (*subsystem.Server, error) {
	store := options.Store
	if store == nil {
		store = NewStore()
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := workflowv1connect.NewWorkflowServiceHandler(NewHandler(store))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Stores and validates provider-neutral workflow definitions.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:         workflowv1connect.WorkflowServiceName,
			Path:         path,
			Handler:      handler,
			Capabilities: []string{"workflow.definition.read", "workflow.definition.write", "workflow.validate"},
		}},
	})
}

// PutWorkflow stores a workflow definition.
func (h *Handler) PutWorkflow(_ context.Context, req *connect.Request[workflowv1.PutWorkflowRequest]) (*connect.Response[workflowv1.PutWorkflowResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	definition, err := h.store.Put(req.Msg.GetDefinition(), req.Msg.GetFailIfExists())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&workflowv1.PutWorkflowResponse{Definition: definition}), nil
}

// GetWorkflow returns a stored workflow definition.
func (h *Handler) GetWorkflow(_ context.Context, req *connect.Request[workflowv1.GetWorkflowRequest]) (*connect.Response[workflowv1.GetWorkflowResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	definition, err := h.store.Get(req.Msg.GetId(), req.Msg.GetVersion())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&workflowv1.GetWorkflowResponse{Definition: definition}), nil
}

// ListWorkflows returns stored definitions.
func (h *Handler) ListWorkflows(_ context.Context, req *connect.Request[workflowv1.ListWorkflowsRequest]) (*connect.Response[workflowv1.ListWorkflowsResponse], error) {
	prefix := ""
	if req != nil && req.Msg != nil {
		prefix = req.Msg.GetIdPrefix()
	}
	return connect.NewResponse(&workflowv1.ListWorkflowsResponse{Workflows: h.store.List(prefix)}), nil
}

// ValidateWorkflow validates a definition without storing it.
func (h *Handler) ValidateWorkflow(_ context.Context, req *connect.Request[workflowv1.ValidateWorkflowRequest]) (*connect.Response[workflowv1.ValidateWorkflowResponse], error) {
	if req == nil || req.Msg == nil {
		return connect.NewResponse(&workflowv1.ValidateWorkflowResponse{Valid: false, Issues: []*workflowv1.ValidationIssue{{Path: "definition", Message: "request is required", Fatal: true}}}), nil
	}
	definition := req.Msg.GetDefinition()
	if err := ValidateDefinition(definition); err != nil {
		return connect.NewResponse(&workflowv1.ValidateWorkflowResponse{Valid: false, Issues: []*workflowv1.ValidationIssue{{Path: "definition", Message: err.Error(), Fatal: true}}}), nil
	}
	return connect.NewResponse(&workflowv1.ValidateWorkflowResponse{Valid: true}), nil
}
