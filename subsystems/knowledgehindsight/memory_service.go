package knowledgehindsight

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Manu343726/toolbox/pkg/api"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
	knowledgev1connect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

// memoryService reads and corrects the facts a base holds.
//
// It is the storage half of the extraction layer, and it exists because reading a fact and reading
// the document it came from are different questions at different fidelities. `ListContent` answers
// the second with the text somebody wrote; this answers the first with a claim and its provenance.
// A caller correcting an answer needs the first, and a caller citing the base needs the second.
type memoryService struct {
	knowledgev1connect.UnimplementedMemoryServiceHandler
	p *Provider
}

func (p *Provider) memoryHandler() *memoryService { return &memoryService{p: p} }

func (s *memoryService) GetMemory(ctx context.Context, req *connect.Request[knowledgev1.GetMemoryRequest]) (*connect.Response[knowledgev1.GetMemoryResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	fact, err := s.p.client.GetFact(ctx, baseID, req.Msg.GetMemoryId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.GetMemoryResponse{Memory: factMessage(baseID, fact)}), nil
}

// CurateMemory corrects a fact in place, and reports what the correction cost downstream.
//
// The cascade is reported as *not* measured rather than as zero when the backend does not say. The
// backend re-derives what was consolidated from a fact, and it does not report how much — so a
// caller that trusted a zero here would believe a correction changed nothing else, which is the one
// answer that would be wrong. The note says so.
func (s *memoryService) CurateMemory(ctx context.Context, req *connect.Request[knowledgev1.CurateMemoryRequest]) (*connect.Response[knowledgev1.CurateMemoryResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if text := req.Msg.GetText(); text == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a correction needs the text the fact should now say"}
	}
	if err := s.p.client.UpdateFact(ctx, baseID, req.Msg.GetMemoryId(), req.Msg.GetText(), req.Msg.GetReason()); err != nil {
		return nil, err
	}
	fact, err := s.p.client.GetFact(ctx, baseID, req.Msg.GetMemoryId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.CurateMemoryResponse{
		Memory:  factMessage(baseID, fact),
		Cascade: unmeasuredCascade(),
	}), nil
}

func (s *memoryService) GetMemoryHistory(ctx context.Context, req *connect.Request[knowledgev1.GetMemoryHistoryRequest]) (*connect.Response[knowledgev1.GetMemoryHistoryResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	revisions, err := s.p.client.FactHistory(ctx, baseID, req.Msg.GetMemoryId())
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.GetMemoryHistoryResponse{}
	for _, r := range revisions {
		out.Revisions = append(out.Revisions, &knowledgev1.MemoryRevision{
			Text: r.Text, At: timestampOrNil(r.At), Source: r.Source, Reason: r.Reason,
		})
	}
	return connect.NewResponse(out), nil
}

func (s *memoryService) GetMemoryGraph(ctx context.Context, req *connect.Request[knowledgev1.GetMemoryGraphRequest]) (*connect.Response[knowledgev1.GetMemoryGraphResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	// The backend's graph is the entity graph and offers no root, depth or limit, so a walk
	// "around a fact" cannot be asked for through it. Rather than return the whole graph under
	// a method that promised a neighbourhood, the response says what was actually walked.
	nodes, links, truncated, err := s.p.client.EntityGraph(ctx, baseID, graphLimit(req.Msg.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.GetMemoryGraphResponse{
		Truncated: truncated,
		Walked:    "the whole entity graph: the backend's graph has no root, depth or limit, and a method that promised a neighbourhood must not silently return a base-sized graph",
	}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, &knowledgev1.GraphNode{Id: n.ID, Kind: n.Kind, Label: n.Label})
	}
	for _, l := range links {
		out.Links = append(out.Links, &knowledgev1.GraphLink{From: l.From, To: l.To, Kind: l.Kind, Weight: l.Weight})
	}
	if truncated {
		out.Links = append(out.Links[:len(out.Links):len(out.Links)], &knowledgev1.GraphLink{
			// A link with no endpoints and a kind saying so, rather than a second boolean
			// field: the caller reading a graph already reads its links, and a marker in
			// that sequence is harder to overlook than a flag beside it.
			Kind: "truncated",
		})
	}
	return connect.NewResponse(out), nil
}

// graphLimit applies a caller's limit, with the default the backend's own page size.
func graphLimit(requested int32) int32 {
	if requested > 0 {
		return requested
	}
	return 200
}

// unmeasuredCascade is what a correction costs when the backend does not say.
//
// The counters are zero and the notes say they are unmeasured, because the alternative is to report
// zeroes as measurements: a caller checking whether a correction disturbed anything would read zero
// facts replaced and conclude it disturbed nothing, when what is true is that nobody counted.
func unmeasuredCascade() *knowledgev1.CascadeReport {
	return &knowledgev1.CascadeReport{Notes: []string{
		"the backend re-derives what was consolidated from a corrected fact and does not report how much, so these counts are unmeasured rather than zero",
		"run a consolidation and compare the observations if you need to know what moved",
	}}
}

func factMessage(baseID string, f kh.Fact) *knowledgev1.Memory {
	return &knowledgev1.Memory{
		Id: f.ID, BaseId: baseID, Text: f.Text, FactType: f.FactType,
		DocumentId: f.DocumentID, Entities: f.Entities, Tags: f.Tags,
		ObservedAt: timestampOrNil(f.ObservedAt), UpdatedAt: timestampOrNil(f.UpdatedAt),
		Curated: f.Curated, SupersededText: f.SupersededText,
	}
}

func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// entityService reads the entity graph.
//
// It is named here rather than imported from the backend's vocabulary, because "entity" is a
// provider-neutral word and the backend's own is not. An entity is a name the extractor found and
// resolved; the graph is how a fact reaches things outside its own text. Worth naming that this is
// structure and not meaning: it answers what is connected, not what is relevant, and a caller
// wanting relevance wants a recall.
type entityService struct {
	knowledgev1connect.UnimplementedEntityServiceHandler
	p *Provider
}

func (p *Provider) entityHandler() *entityService { return &entityService{p: p} }

func (s *entityService) ListEntities(ctx context.Context, req *connect.Request[knowledgev1.ListEntitiesRequest]) (*connect.Response[knowledgev1.ListEntitiesResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if err := checkToken(req.Msg.GetPageToken()); err != nil {
		return nil, err
	}
	limit, offset := pageWindow(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	entities, total, err := s.p.client.ListEntities(ctx, baseID, req.Msg.GetQuery(), limit, offset)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ListEntitiesResponse{NextPageToken: nextToken(offset, int32(len(entities)), total)}
	for _, e := range entities {
		if len(req.Msg.GetTypes()) > 0 && !hasString(req.Msg.GetTypes(), e.Name) {
			continue
		}
		out.Entities = append(out.Entities, &knowledgev1.Entity{
			Id: e.ID, BaseId: baseID, Name: e.Name, MentionCount: e.MentionCount,
			FirstSeen: timestampOrNil(e.FirstSeen), LastSeen: timestampOrNil(e.LastSeen),
			Tags: e.Tags, FactIds: e.FactIDs,
			ObservationCount: e.ObservationCount,
		})
	}
	return connect.NewResponse(out), nil
}

func (s *entityService) GetEntity(ctx context.Context, req *connect.Request[knowledgev1.GetEntityRequest]) (*connect.Response[knowledgev1.GetEntityResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	entity, err := s.p.client.GetEntity(ctx, baseID, req.Msg.GetEntityId())
	if err != nil {
		return nil, err
	}
	if entity.ID == "" {
		return nil, &api.Error{
			Kind:    api.KindNotFound,
			Message: "no entity called " + req.Msg.GetEntityId() + " in base " + baseID,
		}
	}
	return connect.NewResponse(&knowledgev1.GetEntityResponse{Entity: &knowledgev1.Entity{
		Id: entity.ID, BaseId: baseID, Name: entity.Name, MentionCount: entity.MentionCount,
		FirstSeen: timestampOrNil(entity.FirstSeen), LastSeen: timestampOrNil(entity.LastSeen),
		FactIds:          entity.FactIDs,
		ObservationCount: entity.ObservationCount,
	}}), nil
}

func (s *entityService) GetEntityGraph(ctx context.Context, req *connect.Request[knowledgev1.GetEntityGraphRequest]) (*connect.Response[knowledgev1.GetEntityGraphResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	nodes, links, truncated, err := s.p.client.EntityGraph(ctx, baseID, graphLimit(req.Msg.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.GetEntityGraphResponse{Truncated: truncated}
	if root := req.Msg.GetRootEntityId(); root != "" {
		keptNodes, keptLinks := rootOnly(root, nodes, links)
		nodes, links = keptNodes, keptLinks
		// Filtering locally rather than asking the backend to walk from a root: the backend
		// offers no root parameter, so a graph that is not rooted would be a graph that
		// ignored the caller's question.
		if len(nodes) == 0 {
			return nil, &api.Error{
				Kind:    api.KindNotFound,
				Message: "no entity called " + root + " is in base " + baseID + ", or the graph the backend returned does not include it; the endpoint has no root parameter, so the walk is filtered here and a base whose graph is truncated by the server would not contain the entity the caller asked about",
			}
		}
	}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, &knowledgev1.GraphNode{Id: n.ID, Kind: n.Kind, Label: n.Label})
	}
	for _, l := range links {
		out.Links = append(out.Links, &knowledgev1.GraphLink{From: l.From, To: l.To, Kind: l.Kind, Weight: l.Weight})
	}
	return connect.NewResponse(out), nil
}

// rootOneHops keeps the nodes adjacent to a root and the links between them.
func rootOnly(root string, nodes []kh.GraphNode, links []kh.GraphLink) ([]kh.GraphNode, []kh.GraphLink) {
	adjacent := map[string]bool{root: true}
	for _, l := range links {
		if l.From == root {
			adjacent[l.To] = true
		}
		if l.To == root {
			adjacent[l.From] = true
		}
	}
	kept := make([]kh.GraphNode, 0, len(adjacent))
	for _, n := range nodes {
		if adjacent[n.ID] {
			kept = append(kept, n)
		}
	}
	if len(kept) == 0 {
		// A root the walk never mentioned still gets a node, so the response names the thing
		// that was asked about rather than returning an empty graph with no explanation.
		kept = append(kept, kh.GraphNode{ID: root, Kind: "entity", Label: root})
	}
	keptLinks := make([]kh.GraphLink, 0, len(adjacent))
	for _, l := range links {
		if adjacent[l.From] && adjacent[l.To] {
			keptLinks = append(keptLinks, l)
		}
	}
	return kept, keptLinks
}

func hasString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}
