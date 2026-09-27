package knowledgehindsight

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
	knowledgev1connect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

// queryService is one query over both halves of a base.
//
// Recall and reflect live here rather than on a storage service because they are queries, and a
// query that lives next to storage tends to acquire storage's concerns.
type queryService struct {
	knowledgev1connect.UnimplementedQueryServiceHandler
	p *Provider
}

func (p *Provider) queryHandler() *queryService { return &queryService{p: p} }

// Search returns content, pages and facts matching a query, each carrying its origin.
//
// There is no "search the wiki" versus "search the memory" for a caller to choose between, and
// that is the requirement rather than a convenience: a consumer that had to run two searches and
// merge them would have no shared ranking and no way to ask for "only what we wrote down".
func (s *queryService) Search(ctx context.Context, req *connect.Request[knowledgev1.SearchRequest]) (*connect.Response[knowledgev1.SearchResponse], error) {
	msg := req.Msg
	query := strings.TrimSpace(msg.GetQuery())
	if query == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a query is required; an empty one is refused rather than treated as \"everything\", because a search that returns the whole base is not a search and would look like one",
		}
	}
	origins, err := scopeOrigins(msg.GetScope().GetOrigins())
	if err != nil {
		return nil, err
	}
	baseID, err := s.p.resolveBaseForRead(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	wanted := originSet(origins)
	tagFilter := knowledge.SortedTags(msg.GetScope().GetTags())

	out := &knowledgev1.SearchResponse{}
	contentSearch := s.contentService()
	pageSize := int(msg.GetPageSize())
	if pageSize <= 0 {
		pageSize = 50
	}

	if wanted[knowledgev1.Origin_ORIGIN_AUTHORED] {
		// The authored half is a local directory, so it is searched here rather than
		// through the backend: the backend holds the facts extracted from those files, not
		// the files, and searching for a phrase somebody wrote is a question about the file.
		corpus, cerr := s.p.bases.corpus(msg.GetScope().GetTags())
		if cerr != nil {
			return nil, cerr
		}
		walk, werr := corpus.Walk(ctx, knowledge.WalkOptions{})
		if werr != nil {
			return nil, werr
		}
		needle := strings.ToLower(query)
		for _, f := range walk.Files {
			if len(out.Content) >= pageSize {
				break
			}
			if !f.Read || !strings.Contains(strings.ToLower(f.Body), needle) {
				continue
			}
			tags := knowledge.TagsFor(f, knowledge.TagOptions{RootName: f.Root, Owner: s.p.Owner()})
			if !hasAllTags(tags, tagFilter) {
				continue
			}
			out.Content = append(out.Content, contentSearch.contentFromFile(f, tags, msg.GetIncludeBodies()))
			out.Coverage = bumpContentCoverage(out.Coverage)
		}
	}

	if wanted[knowledgev1.Origin_ORIGIN_RETAINED] || wanted[knowledgev1.Origin_ORIGIN_DERIVED] {
		results, rerr := s.p.client.Recall(ctx, baseID, query, budget(msg.GetBudget(), false), len(tagFilter) == 0)
		if rerr != nil {
			// A recall that cannot run is not a search that found nothing, and reporting
			// it as one would be the worst possible answer to "why is my documentation
			// missing from these results".
			return nil, rerr
		}
		for _, r := range results {
			if len(out.Facts) >= pageSize {
				break
			}
			msg := recallResultMessage(r)
			if !hasAllTags(msg.GetTags(), tagFilter) {
				continue
			}
			if r.Origin == knowledge.OriginRetained && !wanted[knowledgev1.Origin_ORIGIN_RETAINED] {
				continue
			}
			if r.Origin == knowledge.OriginDerived && !wanted[knowledgev1.Origin_ORIGIN_DERIVED] {
				continue
			}
			out.Facts = append(out.Facts, msg)
			out.Coverage = bumpFactCoverage(out.Coverage)
		}
	}

	out.Coverage.OriginsSearched = origins
	if len(origins) == 0 {
		out.Coverage.OriginsSearched = []knowledgev1.Origin{
			knowledgev1.Origin_ORIGIN_AUTHORED,
			knowledgev1.Origin_ORIGIN_RETAINED,
			knowledgev1.Origin_ORIGIN_DERIVED,
		}
	}
	return connect.NewResponse(out), nil
}

func bumpContentCoverage(c *knowledgev1.SearchCoverage) *knowledgev1.SearchCoverage {
	if c == nil {
		c = &knowledgev1.SearchCoverage{}
	}
	c.ContentConsidered++
	return c
}

func bumpFactCoverage(c *knowledgev1.SearchCoverage) *knowledgev1.SearchCoverage {
	if c == nil {
		c = &knowledgev1.SearchCoverage{}
	}
	c.FactsConsidered++
	return c
}

func (s *queryService) contentService() *contentService { return s.p.contentHandler() }

// Recall retrieves what the base holds about a question.
func (s *queryService) Recall(ctx context.Context, req *connect.Request[knowledgev1.RecallRequest]) (*connect.Response[knowledgev1.RecallResponse], error) {
	msg := req.Msg
	if strings.TrimSpace(msg.GetQuery()) == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a query is required"}
	}
	if _, err := scopeOrigins(msg.GetScope().GetOrigins()); err != nil {
		return nil, err
	}
	baseID, err := s.p.resolveBaseForRead(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	results, err := s.p.client.Recall(ctx, baseID, msg.GetQuery(), budget(msg.GetBudget(), false), msg.GetIncludeSources())
	if err != nil {
		return nil, err
	}
	limit := int(msg.GetLimit())
	out := &knowledgev1.RecallResponse{}
	for i, r := range results {
		if limit > 0 && i >= limit {
			break
		}
		out.Results = append(out.Results, recallResultMessage(r))
	}
	// The authored sources are resolved from the local corpus rather than from the
	// backend, for the same reason search does: the backend holds the facts, and the file a
	// person can open is on this host.
	if msg.GetIncludeSources() {
		cs := s.contentService()
		seen := map[string]bool{}
		for _, r := range out.Results {
			for _, src := range r.GetDerivedFrom() {
				if seen[src] {
					continue
				}
				seen[src] = true
				if c := cs.findAuthored(ctx, baseID, src); c != nil {
					out.Sources = append(out.Sources, c)
				}
			}
		}
	}
	return connect.NewResponse(out), nil
}

// Reflect answers a question by reasoning over the base, and reports what it relied on.
//
// The citation resolution is the part worth reading. A fact cited by a reasoning answer comes back
// as an identifier and its text and **usually without the document it came from**; the document is
// on the retrieval results inside the reasoning trace. So resolving a citation to a file takes a
// join against the trace, which this surface performs and a caller would otherwise have to
// reconstruct.
func (s *queryService) Reflect(ctx context.Context, req *connect.Request[knowledgev1.ReflectRequest]) (*connect.Response[knowledgev1.ReflectResponse], error) {
	msg := req.Msg
	if strings.TrimSpace(msg.GetQuery()) == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a question is required"}
	}
	baseID, err := s.p.resolveBaseForRead(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	// Three-valued, and the default is the expensive one. `resolve_citations` is documented as
	// on unless the caller declines, and a plain `bool` cannot say that: absent and `false` both
	// read as false, so a caller relying on the documented default would get unresolved
	// citations and conclude the base has no sources. An explicit `false` is a decision and is
	// honoured; absent is the default and resolves.
	resolve := true
	if msg.ResolveCitations != nil {
		resolve = *msg.ResolveCitations
	}
	// Sent only when asked for, because the backend already applies directives scoped by tag and
	// this ignores the scope. Sending `true` by default would widen every answer past what the
	// base's own configuration says.
	applyAll := msg.ApplyAllDirectives != nil && *msg.ApplyAllDirectives
	reflected, err := s.p.client.Reflect(ctx, baseID, msg.GetQuery(), budget(msg.GetBudget(), true), resolve, applyAll)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ReflectResponse{
		Text:           reflected.Text,
		BackingModelId: reflected.BackingModelID,
		TraceDigest:    reflected.TraceDigest,
	}
	if !resolve {
		// With resolution off, the raw cited identifiers are returned and the caller is
		// told why it cannot open them, rather than being handed a citation with an
		// empty location that looks like a page that does not exist.
		for _, id := range reflected.RawFactIDs {
			out.Citations = append(out.Citations, &knowledgev1.Citation{
				FactIds: []string{id},
			})
		}
		return connect.NewResponse(out), nil
	}

	cs := s.contentService()
	for _, c := range reflected.Citations {
		citation := &knowledgev1.Citation{
			ContentId: c.ContentID,
			Origin:    originMessage(c.Origin),
			FactIds:   c.FactIDs,
			Location:  locationMessage(c.Location),
		}
		if c.Excerpt != "" {
			citation.Excerpt, citation.HasExcerpt = c.Excerpt, true
		}
		// A citation with no location is worse than none: it reads as a page that does
		// not exist. It is kept, because the claim is still what the answer relied on,
		// and it is left unresolved on purpose — the trace join did not resolve it, and
		// inventing a source would be the one thing a citation must never do.
		out.Citations = append(out.Citations, citation)
	}
	_ = cs
	return connect.NewResponse(out), nil
}

// ListTags returns the tags in use, separated by origin so a caller can tell the framework's
// namespace from a person's.
func (s *queryService) ListTags(ctx context.Context, req *connect.Request[knowledgev1.ListTagsRequest]) (*connect.Response[knowledgev1.ListTagsResponse], error) {
	msg := req.Msg
	baseID, err := s.p.resolveBase(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	counts := map[string]int32{}
	originOf := map[string]knowledgev1.Origin{}

	corpus, cerr := s.p.bases.corpus(nil)
	if cerr != nil {
		return nil, cerr
	}
	walk, werr := corpus.Walk(ctx, knowledge.WalkOptions{})
	if werr != nil {
		return nil, werr
	}
	for _, f := range walk.Files {
		for _, t := range knowledge.TagsFor(f, knowledge.TagOptions{RootName: f.Root, Owner: s.p.Owner()}) {
			counts[t]++
			originOf[t] = knowledgev1.Origin_ORIGIN_AUTHORED
		}
	}
	for tag, n := range s.p.client.BackendTags(ctx, baseID) {
		counts[tag] = n
		originOf[tag] = knowledgev1.Origin_ORIGIN_RETAINED
	}

	prefix := strings.TrimSpace(msg.GetPrefix())
	var out []*knowledgev1.TagCount
	for tag, n := range counts {
		if prefix != "" && !strings.HasPrefix(tag, prefix) {
			continue
		}
		if o := originOf[tag]; msg.GetOrigin() != knowledgev1.Origin_ORIGIN_UNSPECIFIED && o != msg.GetOrigin() {
			continue
		}
		out = append(out, &knowledgev1.TagCount{Tag: tag, Count: n, Origin: originOf[tag]})
	}
	sortTagCounts(out)
	return connect.NewResponse(&knowledgev1.ListTagsResponse{Tags: out}), nil
}

// PreviewExtraction reports what a write would extract, without extracting it.
func (s *queryService) PreviewExtraction(ctx context.Context, req *connect.Request[knowledgev1.PreviewExtractionRequest]) (*connect.Response[knowledgev1.PreviewExtractionResponse], error) {
	msg := req.Msg
	if strings.TrimSpace(msg.GetContent()) == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "content is required; a preview of nothing tells you nothing, and an empty document is more often a mistake in the call than a real input",
		}
	}
	baseID, err := s.p.resolveBase(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	facts, settings, err := s.p.client.DryRunExtract(ctx, baseID, msg.GetContent(), msg.GetDocumentId())
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.PreviewExtractionResponse{
		Settings: &knowledgev1.ExtractionSettings{
			Mode:             settings.Mode,
			ResolveEntities:  settings.ResolveEntities,
			ObservationScope: settings.ObservationScope,
		},
	}
	for _, f := range facts {
		out.Facts = append(out.Facts, &knowledgev1.PreviewFact{Text: f.Text, FactType: f.FactType, Entities: f.Entities})
	}
	if len(facts) == 0 {
		out.Warnings = append(out.Warnings,
			"extraction produced no facts for this text; an empty body after frontmatter is skipped by the corpus path, and a document with nothing to ground on is not worth ingesting")
	}
	if settings.ResolveEntities {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("entity resolution is on for this base, so a name close to one already in it may resolve to that one; the corpus path turns it off, and this preview is how a caller confirms which is in force"))
	}
	return connect.NewResponse(out), nil
}

// PreviewPrompts reports the prompts a configuration would use.
func (s *queryService) PreviewPrompts(ctx context.Context, req *connect.Request[knowledgev1.PreviewPromptsRequest]) (*connect.Response[knowledgev1.PreviewPromptsResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	prompts, err := s.p.client.PreviewPrompts(ctx, baseID, req.Msg.GetOperation(), req.Msg.GetStrategy())
	if err != nil {
		return nil, err
	}
	var out []*knowledgev1.Prompt
	for _, p := range prompts {
		out = append(out, &knowledgev1.Prompt{Stage: p.Stage, System: p.System, Template: p.Template})
	}
	return connect.NewResponse(&knowledgev1.PreviewPromptsResponse{Prompts: out}), nil
}

func recallResultMessage(r knowledge.RecallResult) *knowledgev1.RecallResult {
	out := &knowledgev1.RecallResult{
		Id:          r.ID,
		Text:        r.Text,
		FactType:    r.FactType,
		Origin:      originMessage(r.Origin),
		Location:    locationMessage(r.Location),
		DerivedFrom: r.DerivedFrom,
		Tags:        r.Tags,
	}
	if !r.ObservedAt.IsZero() {
		out.ObservedAt = timestamppb.New(r.ObservedAt)
	}
	for _, src := range r.Sources {
		out.Sources = append(out.Sources, &knowledgev1.RecallSource{Strategy: src.Strategy, Score: src.Score, Excerpt: src.Excerpt})
	}
	return out
}

func locationMessage(l knowledge.Location) *knowledgev1.Location {
	switch l.Kind {
	case knowledge.LocationKindCorpusPath:
		return &knowledgev1.Location{Kind: knowledgev1.LocationKind_LOCATION_KIND_CORPUS_PATH, Path: l.Path}
	case knowledge.LocationKindDocument:
		return &knowledgev1.Location{Kind: knowledgev1.LocationKind_LOCATION_KIND_DOCUMENT, Path: l.Path}
	case knowledge.LocationKindPage:
		return &knowledgev1.Location{
			Kind:           knowledgev1.LocationKind_LOCATION_KIND_PAGE,
			TreePath:       l.TreePath,
			PageId:         l.PageID,
			BackingModelId: l.BackingModelID,
		}
	default:
		return &knowledgev1.Location{}
	}
}

func sortTagCounts(in []*knowledgev1.TagCount) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j].Tag < in[j-1].Tag; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

var _ = kh.ExpectedAPIVersion
