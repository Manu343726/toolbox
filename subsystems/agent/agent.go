// Package agent implements independent storage for provider-neutral agent
// profiles. Model execution is intentionally left to the model subsystem.
package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	agentv1 "github.com/Manu343726/toolsbox/subsystems/agent/agentv1"
	"github.com/Manu343726/toolsbox/subsystems/agent/agentv1/agentv1connect"
	"google.golang.org/protobuf/proto"
)

const (
	// Name is the stable subsystem name.
	Name = "agent"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the agent subsystem.
type Options struct {
	// Store optionally supplies an existing profile store.
	Store *Store
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Store is a concurrency-safe agent profile store.
type Store struct {
	mu       sync.RWMutex
	profiles map[string]*agentv1.AgentProfile
}

// NewStore creates an empty profile store.
func NewStore() *Store { return &Store{profiles: make(map[string]*agentv1.AgentProfile)} }

func profileKey(id, version string) string { return id + "\x00" + version }

// Put stores a profile.
func (s *Store) Put(profile *agentv1.AgentProfile, failIfExists bool) (*agentv1.AgentProfile, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	key := profileKey(profile.GetId(), profile.GetVersion())
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.profiles[key]; exists && failIfExists {
		return nil, fmt.Errorf("agent %s@%s already exists", profile.GetId(), profile.GetVersion())
	}
	copy := proto.Clone(profile).(*agentv1.AgentProfile)
	s.profiles[key] = copy
	return proto.Clone(copy).(*agentv1.AgentProfile), nil
}

// Get returns a profile by identifier and optional version.
func (s *Store) Get(id, version string) (*agentv1.AgentProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if version != "" {
		profile, ok := s.profiles[profileKey(id, version)]
		if !ok {
			return nil, fmt.Errorf("agent %s@%s not found", id, version)
		}
		return proto.Clone(profile).(*agentv1.AgentProfile), nil
	}
	var latest *agentv1.AgentProfile
	for _, profile := range s.profiles {
		if profile.GetId() == id && (latest == nil || profile.GetVersion() > latest.GetVersion()) {
			latest = profile
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("agent %s not found", id)
	}
	return proto.Clone(latest).(*agentv1.AgentProfile), nil
}

// List returns profiles sorted by identifier and version.
func (s *Store) List(prefix string) []*agentv1.AgentProfile {
	s.mu.RLock()
	result := make([]*agentv1.AgentProfile, 0)
	for _, profile := range s.profiles {
		if prefix == "" || strings.HasPrefix(profile.GetId(), prefix) {
			result = append(result, proto.Clone(profile).(*agentv1.AgentProfile))
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

func validateProfile(profile *agentv1.AgentProfile) error {
	if profile == nil {
		return fmt.Errorf("profile is required")
	}
	if strings.TrimSpace(profile.GetId()) == "" || strings.TrimSpace(profile.GetVersion()) == "" || strings.TrimSpace(profile.GetName()) == "" {
		return fmt.Errorf("id, name, and version are required")
	}
	return nil
}

// Handler implements AgentService.
type Handler struct{ store *Store }

// NewHandler creates an agent handler.
func NewHandler(store *Store) *Handler {
	if store == nil {
		store = NewStore()
	}
	return &Handler{store: store}
}

// New is the programmatic in-process entrypoint for the agent subsystem.
func New(options Options) (*subsystem.Server, error) {
	store := options.Store
	if store == nil {
		store = NewStore()
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := agentv1connect.NewAgentServiceHandler(NewHandler(store))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Stores provider-neutral agent profiles and capability references.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:         agentv1connect.AgentServiceName,
			Path:         path,
			Handler:      handler,
			Capabilities: []string{"agent.profile.read", "agent.profile.write"},
		}},
	})
}

// PutAgent stores an agent profile.
func (h *Handler) PutAgent(_ context.Context, req *connect.Request[agentv1.PutAgentRequest]) (*connect.Response[agentv1.PutAgentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	profile, err := h.store.Put(req.Msg.GetProfile(), req.Msg.GetFailIfExists())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentv1.PutAgentResponse{Profile: profile}), nil
}

// GetAgent returns one profile.
func (h *Handler) GetAgent(_ context.Context, req *connect.Request[agentv1.GetAgentRequest]) (*connect.Response[agentv1.GetAgentResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	profile, err := h.store.Get(req.Msg.GetId(), req.Msg.GetVersion())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&agentv1.GetAgentResponse{Profile: profile}), nil
}

// ListAgents returns stored profiles.
func (h *Handler) ListAgents(_ context.Context, req *connect.Request[agentv1.ListAgentsRequest]) (*connect.Response[agentv1.ListAgentsResponse], error) {
	prefix := ""
	if req != nil && req.Msg != nil {
		prefix = req.Msg.GetIdPrefix()
	}
	return connect.NewResponse(&agentv1.ListAgentsResponse{Agents: h.store.List(prefix)}), nil
}
