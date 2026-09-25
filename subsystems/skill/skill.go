// Package skill implements an independent, versioned skill service.
package skill

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	skillv1 "github.com/Manu343726/toolsbox/subsystems/skill/skillv1"
	"github.com/Manu343726/toolsbox/subsystems/skill/skillv1/skillv1connect"
	"google.golang.org/protobuf/proto"
)

const (
	// Name is the stable subsystem name.
	Name = "skill"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the skill subsystem.
type Options struct {
	// Store optionally supplies an existing skill store.
	Store *Store
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Store is a concurrency-safe skill store.
type Store struct {
	mu     sync.RWMutex
	skills map[string]*skillv1.Skill
}

// NewStore creates an empty skill store.
func NewStore() *Store { return &Store{skills: make(map[string]*skillv1.Skill)} }

func skillKey(id, version string) string { return id + "\x00" + version }

// Put stores a skill.
func (s *Store) Put(value *skillv1.Skill, failIfExists bool) (*skillv1.Skill, error) {
	if err := validateSkill(value); err != nil {
		return nil, err
	}
	key := skillKey(value.GetId(), value.GetVersion())
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.skills[key]; exists && failIfExists {
		return nil, fmt.Errorf("skill %s@%s already exists", value.GetId(), value.GetVersion())
	}
	copy := proto.Clone(value).(*skillv1.Skill)
	s.skills[key] = copy
	return proto.Clone(copy).(*skillv1.Skill), nil
}

// Get returns a skill by identifier and optional version.
func (s *Store) Get(id, version string) (*skillv1.Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if version != "" {
		value, ok := s.skills[skillKey(id, version)]
		if !ok {
			return nil, fmt.Errorf("skill %s@%s not found", id, version)
		}
		return proto.Clone(value).(*skillv1.Skill), nil
	}
	var latest *skillv1.Skill
	for _, value := range s.skills {
		if value.GetId() == id && (latest == nil || value.GetVersion() > latest.GetVersion()) {
			latest = value
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("skill %s not found", id)
	}
	return proto.Clone(latest).(*skillv1.Skill), nil
}

// List returns skills sorted by identifier and version.
func (s *Store) List(prefix string) []*skillv1.Skill {
	s.mu.RLock()
	result := make([]*skillv1.Skill, 0)
	for _, value := range s.skills {
		if prefix == "" || strings.HasPrefix(value.GetId(), prefix) {
			result = append(result, proto.Clone(value).(*skillv1.Skill))
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

func validateSkill(value *skillv1.Skill) error {
	if value == nil {
		return fmt.Errorf("skill is required")
	}
	if strings.TrimSpace(value.GetId()) == "" || strings.TrimSpace(value.GetVersion()) == "" || strings.TrimSpace(value.GetName()) == "" || strings.TrimSpace(value.GetInstructions()) == "" {
		return fmt.Errorf("id, name, version, and instructions are required")
	}
	return nil
}

// Handler implements SkillService.
type Handler struct{ store *Store }

// NewHandler creates a skill handler.
func NewHandler(store *Store) *Handler {
	if store == nil {
		store = NewStore()
	}
	return &Handler{store: store}
}

// New is the programmatic in-process entrypoint for the skill subsystem.
func New(options Options) (*subsystem.Server, error) {
	store := options.Store
	if store == nil {
		store = NewStore()
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := skillv1connect.NewSkillServiceHandler(NewHandler(store))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Stores reusable, versioned agent skills.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:         skillv1connect.SkillServiceName,
			Path:         path,
			Handler:      handler,
			Capabilities: []string{"skill.read", "skill.write"},
		}},
	})
}

// PutSkill stores a skill.
func (h *Handler) PutSkill(_ context.Context, req *connect.Request[skillv1.PutSkillRequest]) (*connect.Response[skillv1.PutSkillResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	value, err := h.store.Put(req.Msg.GetSkill(), req.Msg.GetFailIfExists())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&skillv1.PutSkillResponse{Skill: value}), nil
}

// GetSkill returns one skill.
func (h *Handler) GetSkill(_ context.Context, req *connect.Request[skillv1.GetSkillRequest]) (*connect.Response[skillv1.GetSkillResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	value, err := h.store.Get(req.Msg.GetId(), req.Msg.GetVersion())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&skillv1.GetSkillResponse{Skill: value}), nil
}

// ListSkills returns stored skills.
func (h *Handler) ListSkills(_ context.Context, req *connect.Request[skillv1.ListSkillsRequest]) (*connect.Response[skillv1.ListSkillsResponse], error) {
	prefix := ""
	if req != nil && req.Msg != nil {
		prefix = req.Msg.GetIdPrefix()
	}
	return connect.NewResponse(&skillv1.ListSkillsResponse{Skills: h.store.List(prefix)}), nil
}
