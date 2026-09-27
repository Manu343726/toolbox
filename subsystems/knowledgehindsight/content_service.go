package knowledgehindsight

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
	knowledgev1connect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

// contentService is the unified read and write surface over one base.
//
// There is no per-origin service under it, and that is the requirement rather than a convenience:
// the reads that used to be a document read, a page read and a corpus read are one method, and a
// consumer does not ask where something came from before it can read it.
type contentService struct {
	knowledgev1connect.UnimplementedContentServiceHandler
	p *Provider
}

func (p *Provider) contentHandler() *contentService { return &contentService{p: p} }

// ListContent returns content of the requested origins from one base.
//
// `origins` is a filter and not a partition. Asking for two origins returns both, each result
// carrying its own, and every other option behaves the same either way — which is what makes the
// surface one surface rather than three behind a switch.
func (s *contentService) ListContent(ctx context.Context, req *connect.Request[knowledgev1.ListContentRequest]) (*connect.Response[knowledgev1.ListContentResponse], error) {
	msg := req.Msg
	if _, err := scopeOrigins(msg.GetOrigins()); err != nil {
		return nil, err
	}
	// The base is resolved so that an unaddressable base fails here rather than returning an
	// empty list: a caller who mistyped a base name should be told so, not handed a
	// confident "you have no documentation". A listing is a read, so it does not need the
	// backend to already hold the base.
	if _, err := s.p.resolveBaseForRead(ctx, msg.GetBaseId()); err != nil {
		return nil, err
	}
	corpus, err := s.p.bases.corpus(nil)
	if err != nil {
		return nil, err
	}
	walk, err := corpus.Walk(ctx, knowledge.WalkOptions{})
	if err != nil {
		return nil, err
	}

	wanted := originSet(msg.GetOrigins())
	prefix := knowledge.NormalizePath(msg.GetLocationPrefix())
	tags := knowledge.SortedTags(msg.GetTags())
	kind := docKindMessage(knowledge.DocKind(kindString(msg.GetKind())))
	status := docStatusMessage(knowledge.DocStatus(statusString(msg.GetStatus())))

	var out []*knowledgev1.Content
	for _, f := range walk.Files {
		if !wanted[knowledgev1.Origin_ORIGIN_AUTHORED] {
			break
		}
		if prefix != "" && f.Path != prefix && !strings.HasPrefix(f.Path, prefix+"/") {
			continue
		}
		if kind != knowledgev1.DocKind_DOC_KIND_UNSPECIFIED && docKindMessage(f.Front.Kind) != kind {
			continue
		}
		if status != knowledgev1.DocStatus_DOC_STATUS_UNSPECIFIED && docStatusMessage(f.Front.Status) != status {
			continue
		}
		fileTags := knowledge.TagsFor(f, knowledge.TagOptions{RootName: f.Root, Owner: s.p.Owner()})
		if !hasAllTags(fileTags, tags) {
			continue
		}
		if !f.Read {
			// A listing that skipped a file's bytes can still describe it, because the
			// record carries its digest, title and declared metadata. Saying so is better
			// than reporting a file with an empty body that looks like an empty document.
			out = append(out, s.describeRecord(f.Root, f.Path, f.ID, fileTags, knowledge.OwnershipEntry{
				ID: f.ID, DeclaredID: f.DeclaredID, Root: f.Root, Path: f.Path,
				Digest: f.Digest, ModTime: f.ModTime, Size: f.Size, Title: f.Title,
				Front: f.Front, Tags: fileTags,
			}, false))
			continue
		}
		out = append(out, s.contentFromFile(f, fileTags, msg.GetIncludeBodies()))
	}
	return connect.NewResponse(&knowledgev1.ListContentResponse{Content: out}), nil
}

// GetContent reads one piece of content by identifier, whatever its origin.
//
// It resolves an authored identifier to a corpus file and a retained or derived one to the
// backend, and the answer says which. That is the whole of the unification: one method, and the
// origin is on the result rather than something the caller had to supply.
func (s *contentService) GetContent(ctx context.Context, req *connect.Request[knowledgev1.GetContentRequest]) (*connect.Response[knowledgev1.GetContentResponse], error) {
	msg := req.Msg
	baseID, err := s.p.resolveBaseForRead(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(msg.GetContentId())
	if id == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a content identifier is required"}
	}
	if c := s.findAuthored(ctx, baseID, id); c != nil {
		return connect.NewResponse(&knowledgev1.GetContentResponse{Content: c}), nil
	}
	doc, err := s.p.client.GetDocument(ctx, baseID, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.GetContentResponse{Content: documentMessage(doc, msg.GetIncludeBody())}), nil
}

// findAuthored resolves an identifier to a corpus file, or returns nil.
//
// The search is over the local corpus because authored content is *on this host* in a directory the
// reconciler walks; the backend holds the facts extracted from it, not the file. A declared
// identifier is matched against the frontmatter and a derived one against the namespaced path,
// which is the same rule the reconciler used and therefore cannot disagree with it.
func (s *contentService) findAuthored(ctx context.Context, baseID, id string) *knowledgev1.Content {
	corpus, err := s.p.bases.corpus(nil)
	if err != nil {
		return nil
	}
	walk, err := corpus.Walk(ctx, knowledge.WalkOptions{})
	if err != nil {
		return nil
	}
	for _, f := range walk.Files {
		if f.ID != id {
			continue
		}
		tags := knowledge.TagsFor(f, knowledge.TagOptions{RootName: f.Root, Owner: s.p.Owner()})
		if !f.Read {
			return s.describeRecord(f.Root, f.Path, f.ID, tags, knowledge.OwnershipEntry{
				ID: f.ID, DeclaredID: f.DeclaredID, Root: f.Root, Path: f.Path,
				Digest: f.Digest, ModTime: f.ModTime, Size: f.Size, Title: f.Title,
				Front: f.Front, Tags: tags,
			}, false)
		}
		return s.contentFromFile(f, tags, true)
	}
	return nil
}

// WriteContent writes content, and the three origins answer it differently.
//
// Retained content is written and curated in place. Authored and derived are refused with a
// pointer to where the change belongs, because accepting either would create something that is
// silently overwritten later — and a refusal that says what to do instead is a better answer than a
// success that is discarded.
func (s *contentService) WriteContent(ctx context.Context, req *connect.Request[knowledgev1.WriteContentRequest]) (*connect.Response[knowledgev1.WriteContentResponse], error) {
	msg := req.Msg
	in := msg.GetContent()
	if in == nil {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "content is required"}
	}
	if err := refuseOriginWrite(in.GetId(), in.GetOrigin()); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.GetId()) == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "retained content needs an identifier"}
	}
	baseID, err := s.p.resolveBase(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}

	eventTime := knowledge.TimelessTimestamp()
	if t := msg.GetEventTime(); t != nil && t.IsValid() {
		eventTime = t.AsTime().UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	res, err := s.p.client.Retain(ctx, knowledge.RetainRequest{
		BaseID:         baseID,
		IdempotencyKey: s.p.Owner() + ":" + in.GetId() + ":" + knowledge.Digest([]byte(in.GetBody())),
		Async:          true,
		Items: []knowledge.RetainItem{{
			ID:        in.GetId(),
			Content:   in.GetBody(),
			Tags:      knowledge.SortedTags(in.GetTags()),
			Context:   "toolbox-knowledge",
			EventTime: eventTime,
			// Replace, always. An edited document must stop being retrievable by the
			// sentence it used to contain: an assistant answering from a superseded claim
			// is worse than one that answers from nothing.
			UpdateMode: knowledge.UpdateModeReplace,
			// Entity resolution stays on for a retained write. It is off for the corpus
			// because there a name is a person's deliberate word, and on for retained
			// content because there the caller is naming an entity it means to attach to.
			ResolveEntities: true,
			Metadata:        map[string]string{"origin": "retained"},
		}},
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.WriteContentResponse{
		Content:      in,
		OperationIds: res.OperationIDs,
	}), nil
}

// CurateContent corrects retained content in place.
func (s *contentService) CurateContent(ctx context.Context, req *connect.Request[knowledgev1.CurateContentRequest]) (*connect.Response[knowledgev1.CurateContentResponse], error) {
	msg := req.Msg
	if strings.TrimSpace(msg.GetReason()) == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a correction needs a reason: it is recorded on the content's provenance, because a corrected fact with no stated reason is indistinguishable from one somebody changed by accident",
		}
	}
	baseID, err := s.p.resolveBase(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if _, err := s.p.client.Retain(ctx, knowledge.RetainRequest{
		BaseID:         baseID,
		IdempotencyKey: s.p.Owner() + ":curate:" + msg.GetContentId() + ":" + knowledge.Digest([]byte(msg.GetBody())),
		Async:          true,
		Items: []knowledge.RetainItem{{
			ID:         msg.GetContentId(),
			Content:    msg.GetBody(),
			Tags:       msg.GetMetadata().GetTags(),
			Context:    "toolbox-knowledge-curation",
			EventTime:  knowledge.TimelessTimestamp(),
			UpdateMode: knowledge.UpdateModeReplace,
			Metadata: map[string]string{
				"origin": "retained",
				"reason": msg.GetReason(),
			},
			ResolveEntities: true,
		}},
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.CurateContentResponse{
		Content: &knowledgev1.Content{
			Id: msg.GetContentId(), BaseId: baseID,
			Origin: knowledgev1.Origin_ORIGIN_RETAINED, Body: msg.GetBody(), HasBody: true,
			Mutability: knowledgev1.Mutability_MUTABILITY_CURATED,
			Provenance: &knowledgev1.Provenance{
				OriginTag:      knowledge.OriginTag(knowledge.OriginRetained),
				Source:         "curation",
				SourceRevision: msg.GetReason(),
			},
		},
	}), nil
}

// DeleteContent removes content, and what removal means differs by origin.
func (s *contentService) DeleteContent(ctx context.Context, req *connect.Request[knowledgev1.DeleteContentRequest]) (*connect.Response[knowledgev1.DeleteContentResponse], error) {
	msg := req.Msg
	if msg.GetOrigin() == knowledgev1.Origin_ORIGIN_AUTHORED {
		return nil, &api.Error{
			Kind: api.KindFailedPrecondition,
			Message: "authored content is not deleted here: removing a corpus file's content from the base removes what a person wrote " +
				"from the only place the deployment can act on it, and the file is the deletion. Delete the file in your repository and run ApplyReconcile",
		}
	}
	baseID, err := s.p.resolveBase(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if err := s.p.client.DeleteContent(ctx, baseID, msg.GetContentId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.DeleteContentResponse{
		Deleted: true,
		Cascade: &knowledgev1.CascadeReport{
			Notes: []string{
				"the document was removed and the facts extracted from it are gone with it; observations and pages consolidated from those facts are re-derived, and a citation naming one of those facts is now dangling",
			},
		},
	}), nil
}

// GetContentTree returns one tree over the base, authored and derived content together.
func (s *contentService) GetContentTree(ctx context.Context, req *connect.Request[knowledgev1.GetContentTreeRequest]) (*connect.Response[knowledgev1.GetContentTreeResponse], error) {
	msg := req.Msg
	baseID, err := s.p.resolveBaseForRead(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	corpus, err := s.p.bases.corpus(nil)
	if err != nil {
		return nil, err
	}
	walk, err := corpus.Walk(ctx, knowledge.WalkOptions{})
	if err != nil {
		return nil, err
	}

	var nodes []*knowledgev1.ContentNode
	if msg.GetOrigin() == knowledgev1.Origin_ORIGIN_UNSPECIFIED || msg.GetOrigin() == knowledgev1.Origin_ORIGIN_AUTHORED {
		prefix := knowledge.NormalizePath(msg.GetTreePath())
		for _, f := range walk.Files {
			if prefix != "" && f.Path != prefix && !strings.HasPrefix(f.Path, prefix+"/") {
				continue
			}
			nodes = append(nodes, &knowledgev1.ContentNode{
				Id:         f.ID,
				Path:       "file/" + f.Path,
				Name:       f.DerivedTitle(),
				Origin:     knowledgev1.Origin_ORIGIN_AUTHORED,
				CorpusPath: f.Path,
			})
		}
	}
	if msg.GetOrigin() == knowledgev1.Origin_ORIGIN_UNSPECIFIED || msg.GetOrigin() == knowledgev1.Origin_ORIGIN_DERIVED {
		tree, terr := s.p.client.PageTree(ctx, baseID)
		if terr != nil {
			return nil, terr
		}
		prefix := knowledge.NormalizePath(msg.GetTreePath())
		for _, n := range tree {
			if prefix != "" && n.TreePath != prefix && !strings.HasPrefix(n.TreePath, prefix+"/") {
				continue
			}
			if n.IsStale && !msg.GetIncludeStale() {
				continue
			}
			nodes = append(nodes, &knowledgev1.ContentNode{
				Id:      n.ID,
				Path:    "generated/" + n.TreePath,
				Name:    lastSegment(n.TreePath, n.ID),
				Origin:  knowledgev1.Origin_ORIGIN_DERIVED,
				IsStale: n.IsStale,
			})
		}
	}
	return connect.NewResponse(&knowledgev1.GetContentTreeResponse{Nodes: nodes}), nil
}

// ListContentChunks returns the segments a retained document was split into.
func (s *contentService) ListContentChunks(ctx context.Context, req *connect.Request[knowledgev1.ListContentChunksRequest]) (*connect.Response[knowledgev1.ListContentChunksResponse], error) {
	msg := req.Msg
	if msg.GetDocumentId() == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a document identifier is required"}
	}
	baseID, err := s.p.resolveBase(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	chunks, err := s.p.client.ListDocumentChunks(ctx, baseID, msg.GetDocumentId())
	if err != nil {
		return nil, err
	}
	var out []*knowledgev1.ContentChunk
	for _, c := range chunks {
		out = append(out, &knowledgev1.ContentChunk{
			Id: c.ID, Index: c.Index, Text: c.Text, HasText: c.Text != "",
		})
	}
	return connect.NewResponse(&knowledgev1.ListContentChunksResponse{Chunks: out}), nil
}

// ReprocessContent re-extracts a retained document.
func (s *contentService) ReprocessContent(ctx context.Context, req *connect.Request[knowledgev1.ReprocessContentRequest]) (*connect.Response[knowledgev1.ReprocessContentResponse], error) {
	msg := req.Msg
	baseID, err := s.p.resolveBase(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	cascade, err := s.p.client.ReprocessDocument(ctx, baseID, msg.GetDocumentId(), msg.GetDryRun())
	if err != nil {
		return nil, err
	}
	doc, derr := s.p.client.GetDocument(ctx, baseID, msg.GetDocumentId())
	if derr != nil {
		return nil, derr
	}
	return connect.NewResponse(&knowledgev1.ReprocessContentResponse{
		Content: documentMessage(doc, false),
		Cascade: cascadeMessage(cascade),
	}), nil
}

// --- conversion.

func (s *contentService) contentFromFile(f knowledge.File, tags []string, includeBody bool) *knowledgev1.Content {
	return s.describeRecord(f.Root, f.Path, f.ID, tags, knowledge.OwnershipEntry{
		ID: f.ID, DeclaredID: f.DeclaredID, Root: f.Root, Path: f.Path,
		Digest: f.Digest, ModTime: f.ModTime, Size: f.Size, Title: f.DerivedTitle(),
		Front: f.Front, Tags: tags,
	}, includeBody, f.Body)
}

// describeRecord builds a Content message for a corpus file, from the file or from the record that
// stands in for one the walk skipped.
func (s *contentService) describeRecord(root, path, id string, tags []string, e knowledge.OwnershipEntry, includeBody bool, body ...string) *knowledgev1.Content {
	hasBody := includeBody && e.Digest != ""
	text := ""
	if hasBody && len(body) > 0 {
		text = body[0]
	}
	return &knowledgev1.Content{
		Id:         id,
		BaseId:     s.p.ProviderName(),
		Origin:     knowledgev1.Origin_ORIGIN_AUTHORED,
		Title:      e.Title,
		Body:       text,
		HasBody:    hasBody,
		Tags:       tags,
		Location:   &knowledgev1.Location{Kind: knowledgev1.LocationKind_LOCATION_KIND_CORPUS_PATH, Path: path},
		Mutability: mutabilityMessage(knowledge.OriginAuthored),
		Provenance: &knowledgev1.Provenance{
			OriginTag: knowledge.OriginTag(knowledge.OriginAuthored),
			Source:    path,
		},
		Revision: &knowledgev1.Revision{
			Digest:  e.Digest,
			ModTime: timestamppb.New(e.ModTime),
			Size:    e.Size,
		},
		CorpusMeta: corpusMetaMessage(e.Front),
	}
}

func documentMessage(doc kh.Document, includeBody bool) *knowledgev1.Content {
	return &knowledgev1.Content{
		Id:         doc.ID,
		Origin:     knowledgev1.Origin_ORIGIN_RETAINED,
		Title:      doc.Title,
		Body:       doc.Text,
		HasBody:    includeBody && doc.Text != "",
		Tags:       doc.Tags,
		Location:   &knowledgev1.Location{Kind: knowledgev1.LocationKind_LOCATION_KIND_DOCUMENT, Path: doc.ID},
		Mutability: mutabilityMessage(knowledge.OriginRetained),
		Provenance: &knowledgev1.Provenance{OriginTag: knowledge.OriginTag(knowledge.OriginRetained), Source: doc.Source},
		Revision:   &knowledgev1.Revision{Digest: doc.ContentHash, ModTime: parseTimestamp(doc.ModTime)},
	}
}

func cascadeMessage(c kh.Cascade) *knowledgev1.CascadeReport {
	return &knowledgev1.CascadeReport{
		FactsReplaced:         c.FactsReplaced,
		ObservationsRederived: c.ObservationsRederived,
		PagesRederived:        c.PagesRederived,
		Notes:                 c.Notes,
	}
}

func originSet(origins []knowledgev1.Origin) map[knowledgev1.Origin]bool {
	out := map[knowledgev1.Origin]bool{}
	if len(origins) == 0 {
		return map[knowledgev1.Origin]bool{
			knowledgev1.Origin_ORIGIN_AUTHORED: true,
			knowledgev1.Origin_ORIGIN_RETAINED: true,
			knowledgev1.Origin_ORIGIN_DERIVED:  true,
		}
	}
	for _, o := range origins {
		out[o] = true
	}
	return out
}

func hasAllTags(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, t := range have {
		set[t] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}

func lastSegment(path, fallback string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	if path == "" {
		return fallback
	}
	return path
}

// kindString and statusString map the contract's enums back to the domain's, so a filter written
// against the contract is expressed in the same vocabulary the corpus walk uses.
func kindString(k knowledgev1.DocKind) string {
	switch k {
	case knowledgev1.DocKind_DOC_KIND_ARCHITECTURE:
		return "architecture"
	case knowledgev1.DocKind_DOC_KIND_POLICY:
		return "policy"
	case knowledgev1.DocKind_DOC_KIND_DECISION:
		return "decision"
	case knowledgev1.DocKind_DOC_KIND_PROCEDURE:
		return "procedure"
	case knowledgev1.DocKind_DOC_KIND_REFERENCE:
		return "reference"
	case knowledgev1.DocKind_DOC_KIND_RUNBOOK:
		return "runbook"
	case knowledgev1.DocKind_DOC_KIND_GLOSSARY:
		return "glossary"
	case knowledgev1.DocKind_DOC_KIND_INDEX:
		return "index"
	case knowledgev1.DocKind_DOC_KIND_LOG:
		return "log"
	}
	return ""
}

func statusString(s knowledgev1.DocStatus) string {
	switch s {
	case knowledgev1.DocStatus_DOC_STATUS_ACTIVE:
		return "active"
	case knowledgev1.DocStatus_DOC_STATUS_DEPRECATED:
		return "deprecated"
	case knowledgev1.DocStatus_DOC_STATUS_DRAFT:
		return "draft"
	}
	return ""
}

// countReported renders a count for a warning without pretending to more precision than a count
// has.
func countReported(n int) string { return fmt.Sprintf("%d", n) }

// parseTimestamp converts the adapter's string timestamps, which the backend sends as ISO 8601
// strings, into the contract's message.
//
// A value that does not parse is reported as absent rather than as the zero time: a reader
// comparing a zero timestamp against a real one would conclude the content is very old, which is a
// different and wrong claim from "this base does not report when it changed".
func parseTimestamp(s string) *timestamppb.Timestamp {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, s); err == nil {
			return timestamppb.New(parsed.UTC())
		}
	}
	return nil
}
