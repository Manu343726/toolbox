package knowledgehindsight

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
	knowledgev1connect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

// pageService creates, configures and refreshes the projection.
//
// It has no reads of its own, and that is the point. `GetPageTree` became `GetContentTree`, which
// walks authored and derived content together, and `SearchPages` became `Search` with a scope. What
// is left is what is genuinely page-specific: creating a folder, creating a page, changing how it is
// built, refreshing it, and removing it. A service whose reads were a subset of a wider surface would
// make a caller choose where to look before it knows what it is looking for.
//
// The body is the thing worth understanding about this service. A page *is* a mental model, and its
// body is always synthesized by a model from the question it is given — the backend has no writable
// body field anywhere in its public surface. So none of these methods takes prose. Authored prose
// becomes a document; a page is what the system believes, rebuilt on its own schedule.
type pageService struct {
	knowledgev1connect.UnimplementedPageServiceHandler
	p *Provider
}

func (p *Provider) pageHandler() *pageService { return &pageService{p: p} }

func (s *pageService) CreatePageFolder(ctx context.Context, req *connect.Request[knowledgev1.CreatePageFolderRequest]) (*connect.Response[knowledgev1.CreatePageFolderResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	parentID, err := s.p.resolveFolder(ctx, baseID, req.Msg.GetTreePath(), req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	// A folder carries a name and a parent and nothing else. Tags and a description are in the
	// request because a node in a tree ought to be describable, and the backend refuses both
	// — so the refusal is forwarded rather than the arguments being dropped, because a create
	// that reported success while discarding half its input leaves a folder that cannot be
	// found again by the thing that described it.
	if len(req.Msg.GetTags()) > 0 || req.Msg.GetDescription() != "" {
		return nil, unsupportedFields(
			"a folder carries only a name and a parent",
			map[string]bool{"tags": len(req.Msg.GetTags()) > 0, "description": req.Msg.GetDescription() != ""},
		)
	}
	node, err := s.p.client.CreateFolder(ctx, baseID, req.Msg.GetName(), parentID, nil, "")
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.CreatePageFolderResponse{Node: pageNodeMessage(baseID, node)}), nil
}

// pageByID finds one node in the page tree.
//
// The tree rather than the single-page endpoint, because the single-page read does not report
// which mental model maintains the page — and the model's own trigger is what decides whether a
// refresh is full or incremental. A caller that read the page and not the tree would be
// configuring a refresh against a page whose model it has never seen.
func (p *Provider) pageByID(ctx context.Context, baseID, nodeID string) (kh.Page, error) {
	pages, err := p.client.ListPages(ctx, baseID)
	if err != nil {
		return kh.Page{}, err
	}
	for _, page := range pages {
		if page.ID == nodeID {
			return page, nil
		}
	}
	return kh.Page{}, &api.Error{
		Kind:    api.KindNotFound,
		Message: "no node called " + nodeID + " in base " + baseID,
	}
}

// resolveFolder turns a slash-separated folder path into a node identifier.
//
// Every segment has to exist. A path is not created on the way through, because a typo in segment
// three would otherwise make two folders instead of one, and the one it made would be the one the
// caller got a page in — a tree nobody asked for, containing something, which is the hardest kind of
// mistake to notice later.
func (p *Provider) resolveFolder(ctx context.Context, baseID, treePath, name string) (string, error) {
	if treePath == "" {
		return "", nil
	}
	pages, err := p.client.ListPages(ctx, baseID)
	if err != nil {
		return "", err
	}
	byPath := make(map[string]string, len(pages))
	for _, page := range pages {
		byPath[page.Path] = page.ID
	}
	full := treePath
	if name != "" {
		full = treePath + "/" + name
	}
	id, ok := byPath[full]
	if !ok {
		return "", &api.Error{
			Kind: api.KindNotFound,
			Message: fmt.Sprintf(
				"no folder at %q in base %s. A path is not created on the way through: a typo in one segment would otherwise make folders where none was asked for and put the result somewhere nobody is looking",
				full, baseID),
		}
	}
	return id, nil
}

func (s *pageService) CreatePage(ctx context.Context, req *connect.Request[knowledgev1.CreatePageRequest]) (*connect.Response[knowledgev1.CreatePageResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetSourceQuery() == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a page needs the question it answers; a page's body is synthesized from it, so a page with no question is a page with no content and no visible error",
		}
	}
	parentID, err := s.p.resolveFolder(ctx, baseID, req.Msg.GetParentPath(), "")
	if err != nil {
		return nil, err
	}
	var trigger *knowledge.Trigger
	if msg := req.Msg.GetTrigger(); msg != nil {
		resolved, terr := triggerFromMessage(baseID, msg)
		if terr != nil {
			return nil, terr
		}
		trigger = &resolved
	}
	created, err := s.p.client.CreatePage(ctx, baseID, req.Msg.GetName(), req.Msg.GetSourceQuery(), parentID, req.Msg.GetTags(), trigger)
	if err != nil {
		return nil, err
	}
	// The created node is read back through the tree, which is the only read that reports a
	// page's place and the model behind it. The create reported both identifiers, so a tree
	// that has not caught up is a possibility and the identifiers are used to fill in what
	// the tree does not have yet rather than leaving the caller with no node at all.
	node, err := s.p.pageByID(ctx, baseID, created.PageID)
	if err != nil {
		return nil, err
	}
	if node.ID == "" {
		node = kh.Page{ID: created.PageID, Kind: "page", Name: req.Msg.GetName(), BackingModel: created.ModelID, Tags: req.Msg.GetTags()}
	}
	return connect.NewResponse(&knowledgev1.CreatePageResponse{Node: pageNodeMessage(baseID, node)}), nil
}

func (s *pageService) UpdatePageNode(ctx context.Context, req *connect.Request[knowledgev1.UpdatePageNodeRequest]) (*connect.Response[knowledgev1.UpdatePageNodeResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	update := kh.PageUpdate{Tags: req.Msg.GetTags()}
	// `optional` in the contract and a pointer here: absent leaves a name, present sets it, and
	// a name is not optional. Conflating the two would empty a name the caller never mentioned.
	update.Name = req.Msg.Name
	update.Description = req.Msg.Description
	if msg := req.Msg.GetTrigger(); msg != nil {
		resolved, terr := triggerFromMessage(baseID, msg)
		if terr != nil {
			return nil, terr
		}
		update.Trigger = &resolved
	}
	node, err := s.p.client.UpdatePageNode(ctx, baseID, req.Msg.GetNodeId(), update)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.UpdatePageNodeResponse{Node: pageNodeMessage(baseID, node)}), nil
}

func (s *pageService) RefreshPage(ctx context.Context, req *connect.Request[knowledgev1.RefreshPageRequest]) (*connect.Response[knowledgev1.RefreshPageResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	page, err := s.p.pageByID(ctx, baseID, req.Msg.GetPageId())
	if err != nil {
		return nil, err
	}
	// A refresh rebuilds the model behind the page, and the backend takes no parameters for it:
	// whether it is full or incremental is a property of the model's own trigger. A flag here
	// would be a second place to set the same thing and a way for the two to disagree — so the
	// request's flag is checked against the model's configuration and the disagreement is named.
	full := req.Msg.GetFull()
	if model := page.BackingModel; model != "" {
		if m, merr := s.p.client.GetModel(ctx, baseID, model); merr == nil {
			if want := m.Trigger.Mode; want != "" && (want == "all") != full {
				return nil, &api.Error{
					Kind: api.KindInvalid,
					Message: fmt.Sprintf(
						"this page's trigger says its mode is %q, so a refresh cannot be %s. The flag would be a second place to set what the trigger already says, and the two disagreeing is why it is refused rather than honoured",
						want, modeWord(full)),
				}
			}
		}
	}
	opID, err := s.p.client.RefreshModel(ctx, baseID, page.BackingModel, full)
	if err != nil {
		return nil, err
	}
	refreshed, err := s.p.client.GetPage(ctx, baseID, req.Msg.GetPageId())
	if err != nil {
		return nil, err
	}
	if refreshed.ID == "" {
		refreshed = page
	}
	return connect.NewResponse(&knowledgev1.RefreshPageResponse{
		Node:        pageNodeMessage(baseID, refreshed),
		OperationId: opID,
		Notes: []string{
			"a refresh is asynchronous: it returns the operation doing the work, and the page's new body appears when that operation finishes",
		},
	}), nil
}

func modeWord(full bool) string {
	if full {
		return "full"
	}
	return "incremental"
}

func (s *pageService) PreviewPageRefresh(ctx context.Context, req *connect.Request[knowledgev1.PreviewPageRefreshRequest]) (*connect.Response[knowledgev1.PreviewPageRefreshResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	page, err := s.p.pageByID(ctx, baseID, req.Msg.GetPageId())
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.PreviewPageRefreshResponse{IsStale: page.IsStale}
	if page.BackingModel == "" {
		// A folder is a position and has no model, so a folder refreshes nothing. Saying so is
		// the answer; reporting "not stale" would let a caller conclude the folder is current.
		out.Notes = append(out.Notes, "this node is a folder: a folder has no mental model behind it and refreshing it would do nothing")
		return connect.NewResponse(out), nil
	}
	tags, factTypes, would, err := s.p.client.PreviewModelRefresh(ctx, baseID, page.BackingModel)
	if err != nil {
		return nil, err
	}
	out.WouldRefresh = would
	out.InputTags = tags
	out.InputFactTypes = factTypes
	if len(tags) == 0 && len(factTypes) == 0 {
		// The dry run reports an outcome and not its inputs, so an empty pair is the backend
		// not reporting them rather than the page having none. It is worth saying, because a
		// page whose trigger matches nothing stays empty however many times it is refreshed and
		// has no visible error.
		out.Notes = append(out.Notes,
			"the backend's dry run reports an outcome rather than the facts it would use, so no inputs are listed here; if this page stays empty, read the tags on its trigger — a page tagged with terms it was not given matches nothing")
	}
	return connect.NewResponse(out), nil
}

func (s *pageService) ExportPageBundle(ctx context.Context, req *connect.Request[knowledgev1.ExportPageBundleRequest]) (*connect.Response[knowledgev1.ExportPageBundleResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	files, err := s.p.client.ExportPages(ctx, baseID)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ExportPageBundleResponse{}
	subtree := knowledge.NormalizePath(req.Msg.GetSubtree())
	for _, f := range files {
		if subtree != "" && subtree != "." && !withinSubtree(f.Path, subtree) {
			continue
		}
		out.Files = append(out.Files, &knowledgev1.ProjectedFile{Path: f.Path, Content: f.Content, Origin: "generated"})
	}
	return connect.NewResponse(out), nil
}

// withinSubtree reports whether a path is under a subtree root.
func withinSubtree(path, root string) bool {
	return path == root || len(path) > len(root) && path[:len(root)] == root && path[len(root)] == '/'
}

func (s *pageService) DeletePage(ctx context.Context, req *connect.Request[knowledgev1.DeletePageRequest]) (*connect.Response[knowledgev1.DeletePageResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	// What is under the node is read first, because the deletion cannot report it afterwards: a
	// folder takes its whole subtree and the backend does not enumerate what went. A caller that
	// wants to keep a page has to know it is there.
	pages, err := s.p.client.ListPages(ctx, baseID)
	if err != nil {
		return nil, err
	}
	removed := subtreeIDs(pages, req.Msg.GetNodeId())
	if len(removed) == 0 {
		return nil, &api.Error{Kind: api.KindNotFound, Message: "no node called " + req.Msg.GetNodeId() + " in base " + baseID}
	}
	if err := s.p.client.DeletePageNode(ctx, baseID, req.Msg.GetNodeId()); err != nil {
		return nil, err
	}
	notes := []string{fmt.Sprintf("%d node(s) removed", len(removed))}
	for _, id := range removed {
		notes = append(notes, "removed "+id)
	}
	return connect.NewResponse(&knowledgev1.DeletePageResponse{Deleted: true, Notes: notes}), nil
}

// subtreeIDs returns a node and everything under it.
func subtreeIDs(pages []kh.Page, root string) []string {
	var out []string
	for _, page := range pages {
		if page.ID == root {
			out = append(out, page.ID)
		}
	}
	if len(out) == 0 {
		return nil
	}
	found := true
	for found {
		found = false
		for _, page := range pages {
			if hasString(out, page.ID) {
				continue
			}
			if hasString(out, page.Parent) && page.Parent != "" {
				out = append(out, page.ID)
				found = true
			}
		}
	}
	return out
}

func pageNodeMessage(baseID string, p kh.Page) *knowledgev1.PageNode {
	return &knowledgev1.PageNode{
		Id: p.ID, BaseId: baseID, Kind: p.Kind, Name: p.Name, ParentId: p.Parent,
		BackingModelId: p.BackingModel, Tags: p.Tags, IsStale: p.IsStale,
		Timestamp: timestampOrNil(p.Timestamp), LastRefreshFailedAt: timestampOrNil(p.RefreshFailed),
		Description: p.Description, Trigger: triggerMessage(p.Trigger),
	}
}

func triggerMessage(t knowledge.Trigger) *knowledgev1.PageTrigger {
	out := &knowledgev1.PageTrigger{
		Mode: t.Mode, FactTypes: t.FactTypes, ExcludeMentalModels: t.ExcludeMentalModels,
		RefreshAfterConsolidation: t.RefreshAfterConsolidation, Tags: t.Tags,
		TagsMatch: t.TagsMatch, Cron: t.Cron,
	}
	for _, g := range t.TagGroups {
		if msg := tagFilterMessage(g); msg != nil {
			out.TagGroups = append(out.TagGroups, msg)
		}
	}
	return out
}

// triggerFromMessage reads a trigger out of the contract and checks it against what the backend
// accepts.
//
// The check is here rather than in the adapter because the contract is where a caller's mistake is
// cheapest to name: a mode the backend does not have is not a mode this build declined to send, it
// is a mode that does not exist, and the list belongs with the fields it constrains.
func triggerFromMessage(baseID string, msg *knowledgev1.PageTrigger) (knowledge.Trigger, error) {
	out := knowledge.Trigger{
		Mode: msg.GetMode(), FactTypes: msg.GetFactTypes(),
		ExcludeMentalModels:       msg.GetExcludeMentalModels(),
		RefreshAfterConsolidation: msg.GetRefreshAfterConsolidation(),
		Tags:                      msg.GetTags(), TagsMatch: msg.GetTagsMatch(), Cron: msg.GetCron(),
	}
	if m := msg.GetMode(); m != "" && m != "delta" && m != "all" {
		return out, &api.Error{
			Kind:    api.KindInvalid,
			Message: fmt.Sprintf("a trigger's mode is %q, not %q; the accepted values are delta (rewrite what changed) and all (regenerate from scratch)", m, m),
		}
	}
	if tm := msg.GetTagsMatch(); tm != "" {
		switch tm {
		case "any", "all", "any_strict", "all_strict":
		default:
			return out, &api.Error{
				Kind:    api.KindInvalid,
				Message: fmt.Sprintf("a trigger's tag match mode is %q, not one of any, all, any_strict, all_strict", tm),
			}
		}
	}
	if out.Cron != "" && out.RefreshAfterConsolidation {
		// Both were named. They are not two settings for one thing: a document refreshes
		// either when consolidation produces something or on a schedule, and a trigger saying
		// both means it refreshes twice, in two ways, for reasons that will not be visible in
		// the resulting document.
		return out, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a trigger cannot both refresh after every consolidation and refresh on a schedule; a document refreshes either when consolidation produces something or on a cron, and naming both means it refreshes twice for reasons nothing in the document will show",
		}
	}
	// The shorthand and the tree are two ways of saying the same thing, so carrying both is
	// refused rather than merged. A merged filter is one whose meaning depends on which field
	// a reader looked at — and it would read back differently again, which is worse.
	if len(msg.GetTagGroups()) > 0 && (len(msg.GetTags()) > 0 || msg.GetTagsMatch() != "") {
		return out, &api.Error{
			Kind: api.KindInvalid,
			Message: "a trigger carries either `tags` — a flat list, shorthand for one leaf — " +
				"or `tag_groups`, the expression, and not both. Two filters over the same " +
				"input with no rule for combining them is a filter whose meaning depends on " +
				"which one a reader looked at",
		}
	}
	for i, group := range msg.GetTagGroups() {
		filter, err := tagFilterFrom(group, fmt.Sprintf("tag_groups[%d]", i))
		if err != nil {
			return out, &api.Error{Kind: api.KindInvalid, Message: err.Error()}
		}
		out.TagGroups = append(out.TagGroups, filter)
	}
	return out, nil
}

// tagFilterFrom converts the contract's recursive tag expression into the domain's.
//
// The conversion is here rather than in the adapter because the contract's `TagGroup` and the
// domain's `TagFilter` are two declarations of one idea, and the *rules* — a leaf needs tags, a
// group needs members, a match mode is one of four — are the framework's, not the backend's. The
// adapter's job is the wire form; this is the shape.
func tagFilterFrom(group *knowledgev1.TagGroup, path string) (knowledge.TagFilter, error) {
	if group == nil {
		return knowledge.TagFilter{}, &api.Error{Kind: api.KindInvalid, Message: "a tag group cannot be absent"}
	}
	switch g := group.GetGroup().(type) {
	case *knowledgev1.TagGroup_Leaf:
		match := ""
		if g.Leaf.GetMatch() != knowledgev1.TagMatch_TAG_MATCH_UNSPECIFIED {
			match = tagMatchString(g.Leaf.GetMatch())
		}
		leaf := knowledge.TagFilter{Leaf: &knowledge.TagLeaf{Tags: g.Leaf.GetTags(), Match: match}}
		if err := leaf.Validate(path + ".leaf"); err != nil {
			return knowledge.TagFilter{}, &api.Error{Kind: api.KindInvalid, Message: err.Error()}
		}
		return leaf, nil
	case *knowledgev1.TagGroup_GroupsAnd:
		out := knowledge.TagFilter{}
		for i, sub := range g.GroupsAnd.GetGroups() {
			child, err := tagFilterFrom(sub, fmt.Sprintf("%s.and[%d]", path, i))
			if err != nil {
				return out, err
			}
			out.And = append(out.And, child)
		}
		return out, out.Validate(path)
	case *knowledgev1.TagGroup_GroupsOr:
		out := knowledge.TagFilter{}
		for i, sub := range g.GroupsOr.GetGroups() {
			child, err := tagFilterFrom(sub, fmt.Sprintf("%s.or[%d]", path, i))
			if err != nil {
				return out, err
			}
			out.Or = append(out.Or, child)
		}
		return out, out.Validate(path)
	case *knowledgev1.TagGroup_GroupNot:
		inner, err := tagFilterFrom(g.GroupNot.GetGroup(), path+".not")
		if err != nil {
			return knowledge.TagFilter{}, err
		}
		negated := knowledge.TagFilter{Not: &inner}
		return negated, negated.Validate(path + ".not")
	}
	return knowledge.TagFilter{}, &api.Error{
		Kind:    api.KindInvalid,
		Message: "a tag group is a leaf or one of the three compounds; an empty one constrains nothing, and a filter that looks present and matches nothing is the one shape a reader cannot diagnose",
	}
}

func tagMatchString(m knowledgev1.TagMatch) string {
	switch m {
	case knowledgev1.TagMatch_TAG_MATCH_ANY:
		return "any"
	case knowledgev1.TagMatch_TAG_MATCH_ALL:
		return "all"
	case knowledgev1.TagMatch_TAG_MATCH_ANY_STRICT:
		return "any_strict"
	case knowledgev1.TagMatch_TAG_MATCH_ALL_STRICT:
		return "all_strict"
	}
	return ""
}

// tagFilterMessage converts a domain filter back to the contract, for a response that reports what
// a trigger actually is.
func tagFilterMessage(f knowledge.TagFilter) *knowledgev1.TagGroup {
	switch {
	case f.Leaf != nil:
		leaf := &knowledgev1.TagGroupLeaf{Tags: f.Leaf.Tags}
		if f.Leaf.Match != "" {
			leaf.Match = tagMatchValue(f.Leaf.Match)
		}
		return &knowledgev1.TagGroup{Group: &knowledgev1.TagGroup_Leaf{Leaf: leaf}}
	case len(f.And) > 0:
		groups := make([]*knowledgev1.TagGroup, 0, len(f.And))
		for _, sub := range f.And {
			groups = append(groups, tagFilterMessage(sub))
		}
		return &knowledgev1.TagGroup{Group: &knowledgev1.TagGroup_GroupsAnd{GroupsAnd: &knowledgev1.TagGroupAnd{Groups: groups}}}
	case len(f.Or) > 0:
		groups := make([]*knowledgev1.TagGroup, 0, len(f.Or))
		for _, sub := range f.Or {
			groups = append(groups, tagFilterMessage(sub))
		}
		return &knowledgev1.TagGroup{Group: &knowledgev1.TagGroup_GroupsOr{GroupsOr: &knowledgev1.TagGroupOr{Groups: groups}}}
	case f.Not != nil:
		return &knowledgev1.TagGroup{Group: &knowledgev1.TagGroup_GroupNot{GroupNot: &knowledgev1.TagGroupNot{Group: tagFilterMessage(*f.Not)}}}
	}
	return nil
}

func tagMatchValue(m string) knowledgev1.TagMatch {
	switch m {
	case "any":
		return knowledgev1.TagMatch_TAG_MATCH_ANY
	case "all":
		return knowledgev1.TagMatch_TAG_MATCH_ALL
	case "any_strict":
		return knowledgev1.TagMatch_TAG_MATCH_ANY_STRICT
	case "all_strict":
		return knowledgev1.TagMatch_TAG_MATCH_ALL_STRICT
	}
	return knowledgev1.TagMatch_TAG_MATCH_UNSPECIFIED
}

// unsupportedFields names what a backend refused, rather than accepting a request and dropping half
// of it.
//
// A caller that gets a success for a call that applied nothing has no way to find out, and the thing
// it asked for is still missing. The error is `unsupported` rather than `invalid` because the
// arguments are well formed; the backend simply has no field for them.
func unsupportedFields(what string, present map[string]bool) error {
	var names []string
	for name, isSet := range present {
		if isSet {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return &api.Error{
		Kind: api.KindUnsupported,
		Message: fmt.Sprintf(
			"the backend's %s; this build refuses %s rather than accepting them and dropping them, because a call that reported success while applying nothing leaves the caller believing it had",
			what, quoteList(names)),
	}
}

// mentalModelService manages the synthesized documents themselves.
//
// It is separate from the page tree because a model is not a page: a model is the document, a page is
// a position in a tree that a model maintains. A deployment that never uses the tree still wants to
// reason and to see what it reasoned.
type mentalModelService struct {
	knowledgev1connect.UnimplementedMentalModelServiceHandler
	p *Provider
}

func (p *Provider) mentalModelHandler() *mentalModelService { return &mentalModelService{p: p} }

func (s *mentalModelService) ListMentalModels(ctx context.Context, req *connect.Request[knowledgev1.ListMentalModelsRequest]) (*connect.Response[knowledgev1.ListMentalModelsResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if err := checkToken(req.Msg.GetPageToken()); err != nil {
		return nil, err
	}
	limit, offset := pageWindow(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	models, total, err := s.p.client.ListModels(ctx, baseID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ListMentalModelsResponse{NextPageToken: nextToken(offset, int32(len(models)), total)}
	for _, m := range models {
		out.Models = append(out.Models, modelMessage(baseID, m))
	}
	return connect.NewResponse(out), nil
}

func (s *mentalModelService) CreateMentalModel(ctx context.Context, req *connect.Request[knowledgev1.CreateMentalModelRequest]) (*connect.Response[knowledgev1.CreateMentalModelResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetSourceQuery() == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a mental model needs the question it answers; its body is synthesized from that, so a model with no question is a model with no body and no visible error",
		}
	}
	var trigger *knowledge.Trigger
	if msg := req.Msg.GetTrigger(); msg != nil {
		resolved, terr := triggerFromMessage(baseID, msg)
		if terr != nil {
			return nil, terr
		}
		trigger = &resolved
	}
	created, err := s.p.client.CreateModel(ctx, baseID, req.Msg.GetName(), req.Msg.GetSourceQuery(), req.Msg.GetTags(), trigger)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.CreateMentalModelResponse{Model: modelMessage(baseID, created)}), nil
}

func (s *mentalModelService) UpdateMentalModel(ctx context.Context, req *connect.Request[knowledgev1.UpdateMentalModelRequest]) (*connect.Response[knowledgev1.UpdateMentalModelResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	// `optional` generates a pointer, so absent is nil and there is no pair to destructure.
	name, query := req.Msg.Name, req.Msg.SourceQuery
	var trigger *knowledge.Trigger
	if msg := req.Msg.GetTrigger(); msg != nil {
		resolved, terr := triggerFromMessage(baseID, msg)
		if terr != nil {
			return nil, terr
		}
		trigger = &resolved
	}
	model, err := s.p.client.UpdateModel(ctx, baseID, req.Msg.GetModelId(), name, query, req.Msg.GetTags(), trigger)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.UpdateMentalModelResponse{Model: modelMessage(baseID, model)}), nil
}

func (s *mentalModelService) DeleteMentalModel(ctx context.Context, req *connect.Request[knowledgev1.DeleteMentalModelRequest]) (*connect.Response[knowledgev1.DeleteMentalModelResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	model, err := s.p.client.GetModel(ctx, baseID, req.Msg.GetModelId())
	if err != nil {
		return nil, err
	}
	notes := []string{"the model and the document it maintains are both removed; a document that was there will not come back, and the next refresh of a page behind it would rebuild it from its question"}
	if model.ID == "" {
		notes = append(notes, "the model read as absent and the delete is a no-op, so nothing was there to lose")
	}
	if err := s.p.client.DeleteModel(ctx, baseID, req.Msg.GetModelId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.DeleteMentalModelResponse{Deleted: true, Notes: notes}), nil
}

func (s *mentalModelService) GetMentalModelHistory(ctx context.Context, req *connect.Request[knowledgev1.GetMentalModelHistoryRequest]) (*connect.Response[knowledgev1.GetMentalModelHistoryResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	revisions, err := s.p.client.ModelHistory(ctx, baseID, req.Msg.GetModelId(), req.Msg.GetPageSize(), 0)
	if err != nil {
		return nil, err
	}
	// The backend's history endpoint takes no paging parameters, so the whole history comes
	// back and the page size is applied here. Reporting that is the point: a caller asking for
	// five revisions of a model with two hundred of them would otherwise be told it has two.
	out := &knowledgev1.GetMentalModelHistoryResponse{}
	if limit := int(req.Msg.GetPageSize()); limit > 0 && len(revisions) > limit {
		out.NextPageToken = fmt.Sprintf("%d", limit)
		revisions = revisions[:limit]
	}
	for _, r := range revisions {
		out.Revisions = append(out.Revisions, &knowledgev1.ModelRevision{
			Text: r.Text, At: timestampOrNil(r.At), Trigger: r.Trigger, Failed: r.Failed, Failure: r.Failure,
		})
	}
	return connect.NewResponse(out), nil
}

func (s *mentalModelService) ClearMentalModel(ctx context.Context, req *connect.Request[knowledgev1.ClearMentalModelRequest]) (*connect.Response[knowledgev1.ClearMentalModelResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	model, err := s.p.client.GetModel(ctx, baseID, req.Msg.GetModelId())
	if err != nil {
		return nil, err
	}
	if model.ID == "" {
		return nil, &api.Error{Kind: api.KindNotFound, Message: "no mental model called " + req.Msg.GetModelId() + " in base " + baseID}
	}
	if err := s.p.client.ClearModel(ctx, baseID, req.Msg.GetModelId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.ClearMentalModelResponse{
		Cleared: true,
		Notes: []string{
			"the model and its configuration are still there; only its body was emptied",
			"an empty model is not a wrong one — it is a model that has not been refreshed, and refreshing it rebuilds from the question rather than from what was there",
		},
	}), nil
}

func modelMessage(baseID string, m kh.Model) *knowledgev1.MentalModel {
	return &knowledgev1.MentalModel{
		Id: m.ID, BaseId: baseID, Name: m.Name, SourceQuery: m.SourceQuery,
		Text: m.Text, HasText: m.HasText, Tags: m.Tags,
		UpdatedAt: timestampOrNil(m.UpdatedAt), LastRefreshFailedAt: timestampOrNil(m.RefreshFailed),
	}
}

func quoteList(in []string) string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fmt.Sprintf("%q", s)
	}
	return joinWithAnd(out)
}

func joinWithAnd(in []string) string {
	switch len(in) {
	case 0:
		return ""
	case 1:
		return in[0]
	default:
		result := ""
		for i, s := range in {
			if i == 0 {
				result = s
			} else if i == len(in)-1 {
				result += " or " + s
			} else {
				result += ", " + s
			}
		}
		return result
	}
}
