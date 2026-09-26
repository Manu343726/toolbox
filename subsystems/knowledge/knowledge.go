// Package knowledge implements an independent, provider-neutral knowledge
// source and retrieval service. The reference store is intentionally small;
// production indexing and embedding implementations can replace it behind the
// same RPC contract.
package knowledge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledge/knowledgev1"
	"github.com/Manu343726/toolbox/subsystems/knowledge/knowledgev1/knowledgev1connect"
	"google.golang.org/protobuf/proto"
)

const (
	// Name is the stable subsystem name.
	Name = "knowledge"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the knowledge subsystem.
type Options struct {
	// Store optionally supplies an existing source store.
	Store *Store
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Store is a concurrency-safe in-memory source store.
type Store struct {
	mu      sync.RWMutex
	sources map[string]*knowledgev1.KnowledgeSource
}

// NewStore creates an empty source store.
func NewStore() *Store { return &Store{sources: make(map[string]*knowledgev1.KnowledgeSource)} }

// Put stores a source.
func (s *Store) Put(source *knowledgev1.KnowledgeSource) (*knowledgev1.KnowledgeSource, error) {
	if source == nil || strings.TrimSpace(source.GetId()) == "" {
		return nil, fmt.Errorf("source id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := proto.Clone(source).(*knowledgev1.KnowledgeSource)
	s.sources[copy.GetId()] = copy
	return proto.Clone(copy).(*knowledgev1.KnowledgeSource), nil
}

// Get returns one source.
func (s *Store) Get(id string) (*knowledgev1.KnowledgeSource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	source, ok := s.sources[id]
	if !ok {
		return nil, fmt.Errorf("source %s not found", id)
	}
	return proto.Clone(source).(*knowledgev1.KnowledgeSource), nil
}

// Search performs a deterministic case-insensitive metadata search.
func (s *Store) Search(query string, limit int32, tags []string) []*knowledgev1.SearchPassage {
	if limit <= 0 {
		limit = 10
	}
	query = strings.ToLower(strings.TrimSpace(query))
	s.mu.RLock()
	sources := make([]*knowledgev1.KnowledgeSource, 0, len(s.sources))
	for _, source := range s.sources {
		sources = append(sources, proto.Clone(source).(*knowledgev1.KnowledgeSource))
	}
	s.mu.RUnlock()
	sort.Slice(sources, func(i, j int) bool { return sources[i].GetId() < sources[j].GetId() })
	result := make([]*knowledgev1.SearchPassage, 0)
	for _, source := range sources {
		if !hasAllTags(source.GetTags(), tags) {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{source.GetName(), source.GetType(), source.GetLocation(), strings.Join(source.GetTags(), " ")}, " "))
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		result = append(result, &knowledgev1.SearchPassage{
			SourceId: source.GetId(),
			Text:     source.GetLocation(),
			Score:    1,
		})
		if int32(len(result)) >= limit {
			break
		}
	}
	return result
}

func hasAllTags(available, required []string) bool {
	set := make(map[string]struct{}, len(available))
	for _, tag := range available {
		set[tag] = struct{}{}
	}
	for _, tag := range required {
		if _, ok := set[tag]; !ok {
			return false
		}
	}
	return true
}

// Handler implements KnowledgeService.
type Handler struct{ store *Store }

// NewHandler creates a knowledge handler.
func NewHandler(store *Store) *Handler {
	if store == nil {
		store = NewStore()
	}
	return &Handler{store: store}
}

// New is the programmatic in-process entrypoint for the knowledge subsystem.
func New(options Options) (*subsystem.Server, error) {
	store := options.Store
	if store == nil {
		store = NewStore()
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := knowledgev1connect.NewKnowledgeServiceHandler(NewHandler(store))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Provides provider-neutral knowledge source storage and retrieval.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:    knowledgev1connect.KnowledgeServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
}

// PutSource stores a knowledge source.
func (h *Handler) PutSource(_ context.Context, req *connect.Request[knowledgev1.PutSourceRequest]) (*connect.Response[knowledgev1.PutSourceRequestResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	source, err := h.store.Put(req.Msg.GetSource())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&knowledgev1.PutSourceRequestResponse{Source: source}), nil
}

// GetSource returns one source.
func (h *Handler) GetSource(_ context.Context, req *connect.Request[knowledgev1.GetSourceRequest]) (*connect.Response[knowledgev1.GetSourceResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	source, err := h.store.Get(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&knowledgev1.GetSourceResponse{Source: source}), nil
}

// Search retrieves matching passages.
func (h *Handler) Search(_ context.Context, req *connect.Request[knowledgev1.SearchRequest]) (*connect.Response[knowledgev1.SearchResponse], error) {
	if req == nil || req.Msg == nil {
		return connect.NewResponse(&knowledgev1.SearchResponse{}), nil
	}
	return connect.NewResponse(&knowledgev1.SearchResponse{Passages: h.store.Search(req.Msg.GetQuery(), req.Msg.GetLimit(), req.Msg.GetTags())}), nil
}
