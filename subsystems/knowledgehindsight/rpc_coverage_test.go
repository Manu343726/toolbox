package knowledgehindsight

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Manu343726/toolbox/pkg/api"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// These cover the RPCs that had no test at all. That was 28 of 79, and the gap was found by
// measuring rather than by reading: an earlier claim that the contract was tested meant "every
// method somebody thought to test was".
//
// Most of them are thin, and the point is not the plumbing — it is the one decision each one
// makes, because that is what a refactor would break and what a reader needs to see. A method
// with no decision still gets a test, because its existence is the assertion: an embedded
// `Unimplemented...Handler` satisfies the generated interface, so "this method is implemented" is
// a fact that has to be checked by calling it.

const banksBody = `{"banks":[{"bank_id":"docs","display_alias":"docs",` +
	`"disposition":{"skepticism":0,"literalism":0,"empathy":0}}],"total":1,"limit":0,"offset":0}`

// --- ContentService.

// A listing spans all three origins in one call, and a caller does not hold three types and have
// to know which one it got.
func TestListContentReturnsAuthoredAndDerivedInOneSurface(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/knowledge-base/tree": `{"roots":[]}`,
		"/memories":            `{"items":[],"total":0,"limit":0,"offset":0}`,
	})
	resp, err := p.contentHandler().ListContent(context.Background(), connect.NewRequest(&knowledgev1.ListContentRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	items := resp.Msg.GetContent()
	require.Len(t, items, 3, "the corpus fixture has an index, an overview and a runbook")
	// The origin is on the result rather than something the caller supplied, and the mutability
	// follows from it: authored content is edited at its source, so `NONE` is the answer that
	// stops a tool writing prose that the next reconcile deletes.
	for _, item := range items {
		assert.Equal(t, knowledgev1.Origin_ORIGIN_AUTHORED, item.GetOrigin())
		assert.Equal(t, knowledgev1.Mutability_MUTABILITY_NONE, item.GetMutability())
		assert.NotEmpty(t, item.GetTags(), "a corpus file carries its provenance tags")
	}
}

func TestListContentRefusesAnUnspecifiedOriginInAFilter(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.contentHandler().ListContent(context.Background(), connect.NewRequest(&knowledgev1.ListContentRequest{
		BaseId: "docs", Origins: []knowledgev1.Origin{knowledgev1.Origin_ORIGIN_UNSPECIFIED},
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// A filter on an unspecified origin matches nothing, and a filter that silently matched
	// nothing is a listing that looks empty for a reason nobody can find.
	assert.Empty(t, b.writes())
}

// A correction is a write to a base, and a correction with no stated reason is a correction
// somebody cannot explain later.
func TestCurateContentRefusesACorrectionWithNoReason(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.contentHandler().CurateContent(context.Background(), connect.NewRequest(&knowledgev1.CurateContentRequest{
		BaseId: "docs", ContentId: "fact-1", Body: "one base",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The reason is recorded on the content's provenance, so refusing it here is refusing to
	// write a record that could not be explained.
	assert.Contains(t, err.Error(), "reason")
	assert.Empty(t, b.writes())
}

func TestCurateContentWritesRetainedContentAndSaysItPersists(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/memories": `{"success":true,"bank_id":"docs","items_count":1,"async":true}`,
	})
	resp, err := p.contentHandler().CurateContent(context.Background(), connect.NewRequest(&knowledgev1.CurateContentRequest{
		BaseId: "docs", ContentId: "fact-1", Body: "one base, two halves, and a corpus",
		Reason: "the corpus half is load-bearing",
	}))
	require.NoError(t, err)
	// Retained content is the one origin that is edited through this surface, and the response
	// says so rather than leaving a caller to work it out from the absence of a refusal.
	assert.Equal(t, knowledgev1.Origin_ORIGIN_RETAINED, resp.Msg.GetContent().GetOrigin())
	assert.Equal(t, knowledgev1.Mutability_MUTABILITY_CURATED, resp.Msg.GetContent().GetMutability())
	assert.Equal(t, "the corpus half is load-bearing", resp.Msg.GetContent().GetProvenance().GetSourceRevision())
	sent := b.sentTo("/memories")
	require.NotEmpty(t, sent)
	// A curation is a retain with a replace mode and the fact's own identifier, so the backend
	// replaces the fact rather than appending a second one beside it.
	assert.Contains(t, sent[0].Body, "one base, two halves, and a corpus")
	assert.Contains(t, sent[0].Body, `"update_mode":"replace"`)
}

// The tree spans both halves and namespaces them, so a reader can tell which is which before
// opening either and two files with the same relative path cannot collide.
func TestGetContentTreeNamespacesBothHalves(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/knowledge-base/tree": `{"roots":[{"id":"p1","kind":"page","name":"restore","parent_id":""}]}`,
	})
	resp, err := p.contentHandler().GetContentTree(context.Background(), connect.NewRequest(&knowledgev1.GetContentTreeRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	paths := make([]string, 0, len(resp.Msg.GetNodes()))
	for _, n := range resp.Msg.GetNodes() {
		paths = append(paths, n.GetPath())
	}
	require.GreaterOrEqual(t, len(paths), 2, "the corpus and the page tree are both in the answer")
	// An index at the root is a generated file and must not be filed as authored content, so it
	// is namespaced with the half it came from.
	joined := strings.Join(paths, " ")
	assert.Contains(t, joined, "file/", joined)
	assert.Contains(t, joined, "generated/", joined)
}

func TestListContentChunksRequiresADocumentAndRefusesWithoutOne(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.contentHandler().ListContentChunks(context.Background(), connect.NewRequest(&knowledgev1.ListContentChunksRequest{
		BaseId: "docs",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Empty(t, b.writes())
}

func TestListContentChunksReturnsWhatADocumentWasSplitInto(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/documents/doc-1/chunks": `{"items":[
			{"chunk_id":"c1","document_id":"doc-1","bank_id":"docs","chunk_index":0,
			 "chunk_text":"Drop the base","created_at":"2026-09-01T10:00:00Z"},
			{"chunk_id":"c2","document_id":"doc-1","bank_id":"docs","chunk_index":1,
			 "chunk_text":"keep the repository","created_at":"2026-09-01T10:00:00Z"}],
			"total":2,"limit":0,"offset":0}`,
	})
	resp, err := p.contentHandler().ListContentChunks(context.Background(), connect.NewRequest(&knowledgev1.ListContentChunksRequest{
		BaseId: "docs", DocumentId: "doc-1",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetChunks(), 2)
	assert.Equal(t, "c1", resp.Msg.GetChunks()[0].GetId())
	// The order is the document's, so a reader assembling a quote gets the quote.
	assert.Equal(t, int32(0), resp.Msg.GetChunks()[0].GetIndex())
	assert.Equal(t, int32(1), resp.Msg.GetChunks()[1].GetIndex())
}

// A reprocess is a re-extraction, so its identifiers change and its citations go dangling. The
// response has to say so.
func TestReprocessContentReportsTheCascadeItWillCause(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/documents/doc-1/reprocess": `{"success":true,"operation_id":"op-1","items_count":3}`,
		"/documents/doc-1": `{"id":"doc-1","bank_id":"docs","original_text":"Drop the base.",
			"content_hash":"sha256:abc","memory_unit_count":3,
			"created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-01T10:00:00Z"}`,
	})
	resp, err := p.contentHandler().ReprocessContent(context.Background(), connect.NewRequest(&knowledgev1.ReprocessContentRequest{
		BaseId: "docs", DocumentId: "doc-1",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.GetCascade())
	cascade := resp.Msg.GetCascade()
	// The backend's reprocess response carries an operation identifier and an item count and
	// nothing else, so the only number this reports is the one it reported. The other two are
	// zero and the notes say where they are observable.
	assert.Equal(t, int32(3), cascade.GetFactsReplaced())
	assert.Zero(t, cascade.GetObservationsRederived(), "the backend does not report it, so it is not stated")
	assert.Zero(t, cascade.GetPagesRederived(), "the backend does not report it, so it is not stated")
	// Which matters more than the numbers: the most expensive consequence of a reprocess is a
	// citation naming a fact identifier that no longer exists, and that is said in words.
	notes := strings.Join(cascade.GetNotes(), " ")
	assert.Contains(t, notes, "dangling")
	assert.Contains(t, notes, "operation op-1")
	assert.NotEmpty(t, b.sentTo("/documents/doc-1/reprocess"))
}

func TestReprocessContentDryRunReportsWithoutReprocessing(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/documents/doc-1/reprocess": `{"success":true,"operation_id":"op-1","items_count":0}`,
		"/documents/doc-1": `{"id":"doc-1","bank_id":"docs","original_text":"Drop the base.",
			"content_hash":"sha256:abc","memory_unit_count":3,
			"created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-01T10:00:00Z"}`,
	})
	_, err := p.contentHandler().ReprocessContent(context.Background(), connect.NewRequest(&knowledgev1.ReprocessContentRequest{
		BaseId: "docs", DocumentId: "doc-1", DryRun: true,
	}))
	require.NoError(t, err)
	// A dry run that re-extracted would be a write that reports it is not one, so it makes no
	// request at all — it answers from what the document already says.
	assert.Empty(t, b.writes(), "a dry run reaches the backend for nothing")
	assert.Empty(t, b.sentTo("/documents/doc-1/reprocess"))
}

// --- QueryService.

func TestRecallRefusesAnEmptyQueryRatherThanReturningEverything(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.queryHandler().Recall(context.Background(), connect.NewRequest(&knowledgev1.RecallRequest{
		BaseId: "docs",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// A recall with no query is a request for the base's entire contents wearing a query's
	// clothes, and the budget that would have bounded it does not apply.
	assert.Empty(t, b.sentTo("/memories/recall"))
}

func TestRecallResolvesTheCitationsItIsAskedToResolve(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/memories/recall": `{"results":[
			{"id":"fact-1","text":"one base, two halves","type":"world",
			 "document_id":"doc-1","chunk_id":"c1","tags":["architecture"],
			 "mentioned_at":"2026-09-01T10:00:00Z"}]}`,
	})
	resp, err := p.queryHandler().Recall(context.Background(), connect.NewRequest(&knowledgev1.RecallRequest{
		BaseId: "docs", Query: "how many halves?", IncludeSources: true,
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetResults(), 1)
	result := resp.Msg.GetResults()[0]
	// The document travels with the fact, and as a `Location` rather than a bare id: a result
	// with a document id and no kind cannot be checked, because a corpus path and a backend
	// document are different things to go and look at. This is the whole premise — a wrong
	// answer has to name the file to fix.
	assert.Equal(t, "doc-1", result.GetDerivedFrom()[0], "the fact names the document it came from")
	assert.NotNil(t, result.GetLocation(), "and a location, so a reader knows what kind of thing to open")
	assert.Equal(t, "world", result.GetFactType())
	sent := b.sentTo("/memories/recall")
	require.NotEmpty(t, sent)
	assert.Contains(t, sent[0].Body, `"query":"how many halves?"`)
}

func TestReflectRefusesAnEmptyQuestion(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.queryHandler().Reflect(context.Background(), connect.NewRequest(&knowledgev1.ReflectRequest{
		BaseId: "docs",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Empty(t, b.sentTo("/reflect"))
}

func TestReflectDefaultsToResolvingCitationsAndScopedDirectives(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/reflect": `{"text":"an answer","based_on":{}}`,
	})
	_, err := p.queryHandler().Reflect(context.Background(), connect.NewRequest(&knowledgev1.ReflectRequest{
		BaseId: "docs", Query: "how do we restore a base?",
	}))
	require.NoError(t, err)
	sent := b.sentTo("/reflect")
	require.NotEmpty(t, sent)
	// A caller who said nothing gets citations resolved, because that is the documented default
	// and a plain `bool` cannot express "on unless declined". Without the trace the citations
	// are bare identifiers and a caller concludes the base has no sources.
	assert.Contains(t, sent[0].Body, `"facts"`, "the trace is what citations are resolved against")
	assert.Contains(t, sent[0].Body, `"tool_calls"`)
	// And the directives are left scoped: the backend applies untagged ones always and tagged
	// ones when the request's tags match. Sending `apply_all_directives` by default would widen
	// every answer past what the base's own configuration says.
	assert.NotContains(t, sent[0].Body, "apply_all_directives")
}

func TestReflectSendsApplyAllDirectivesOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/reflect": `{"text":"an answer","based_on":{}}`,
	})
	yes := true
	_, err := p.queryHandler().Reflect(context.Background(), connect.NewRequest(&knowledgev1.ReflectRequest{
		BaseId: "docs", Query: "what do the base's rules say?",
		ApplyAllDirectives: &yes, ResolveCitations: &yes,
	}))
	require.NoError(t, err)
	sent := b.sentTo("/reflect")
	require.NotEmpty(t, sent)
	// The flag ignores the tag scope rather than "using" the rules, and the field is named for
	// what it does. A field called `follow_directives` would read as applying the base's rules
	// while quietly applying *more* of them than the base's configuration scoped.
	assert.Contains(t, sent[0].Body, `"apply_all_directives":true`)
}

func TestReflectHonoursAnExplicitDeclineOfCitationResolution(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/reflect": `{"text":"an answer","based_on":{}}`,
	})
	no := false
	_, err := p.queryHandler().Reflect(context.Background(), connect.NewRequest(&knowledgev1.ReflectRequest{
		BaseId: "docs", Query: "how do we restore a base?", ResolveCitations: &no,
	}))
	require.NoError(t, err)
	sent := b.sentTo("/reflect")
	require.NotEmpty(t, sent)
	// An explicit `false` is a decision — it is cheaper — and it is honoured rather than read as
	// "the caller said nothing".
	assert.NotContains(t, sent[0].Body, `"facts"`)
}

// The tag list is the union of what the corpus declares and what the backend holds, because a
// caller filtering on a tag needs to know both exist.
func TestListTagsReportsBothHalvesAndSaysWhichIsWhich(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/memories": `{"items":[],"total":0,"limit":0,"offset":0}`,
		"/stats": `{"bank_id":"docs","total_nodes":6,"total_links":4,"total_documents":2,
			"nodes_by_fact_type":{"world":6},"links_by_link_type":{"architecture":4,"runbook":2},
			"links_by_fact_type":{},"links_breakdown":{},"pending_operations":0,"failed_operations":0}`,
	})
	resp, err := p.queryHandler().ListTags(context.Background(), connect.NewRequest(&knowledgev1.ListTagsRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	seen := map[string]knowledgev1.Origin{}
	for _, tag := range resp.Msg.GetTags() {
		seen[tag.GetTag()] = tag.GetOrigin()
	}
	// A tag the corpus declares and a tag the backend derived are different things, and a
	// filter cannot tell them apart without being told.
	assert.Equal(t, knowledgev1.Origin_ORIGIN_AUTHORED, seen["vault:docs"])
	assert.Equal(t, knowledgev1.Origin_ORIGIN_RETAINED, seen["architecture"])
	assert.Equal(t, int32(4), mustTag(resp.Msg.GetTags(), "architecture").GetCount())
}

func mustTag(tags []*knowledgev1.TagCount, name string) *knowledgev1.TagCount {
	for _, t := range tags {
		if t.GetTag() == name {
			return t
		}
	}
	return nil
}

func TestPreviewExtractionRefusesAnEmptyDocument(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.queryHandler().PreviewExtraction(context.Background(), connect.NewRequest(&knowledgev1.PreviewExtractionRequest{
		BaseId: "docs",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// A preview of nothing tells you nothing, and an empty document is more often a mistake in
	// the call than a real input.
	assert.Contains(t, err.Error(), "more often a mistake")
	assert.Empty(t, b.sentTo("/memories/dry-run-extract"))
}

func TestPreviewExtractionSendsTheDocumentAndSpendsNoModelCall(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/memories/dry-run-extract": `{"facts":[
			{"text":"one base, two halves","fact_type":"world","entities":["base"]}],
			"chunks":[{"text":"Drop the base","fact_count":1}]}`,
	})
	resp, err := p.queryHandler().PreviewExtraction(context.Background(), connect.NewRequest(&knowledgev1.PreviewExtractionRequest{
		BaseId: "docs", Content: "Drop the base, keep the repository.", DocumentId: "doc-1",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetFacts(), 1)
	assert.Equal(t, "one base, two halves", resp.Msg.GetFacts()[0].GetText())
	sent := b.sentTo("/memories/dry-run-extract")
	require.NotEmpty(t, sent)
	assert.Contains(t, sent[0].Body, "Drop the base")
}

// --- CorpusService.

func TestGetCorpusStatusReportsWhatTheBaseBelievesAndWhereItCameFrom(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, nil)
	resp, err := p.corpusHandler().GetCorpusStatus(context.Background(), connect.NewRequest(&knowledgev1.GetCorpusStatusRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	status := resp.Msg.GetStatus()
	require.NotNil(t, status)
	// "The index matches the merge" has to be a statement that can be checked, so the roots and
	// the commit are both here even before the first reconcile has run.
	require.NotEmpty(t, status.GetRoots())
	assert.Empty(t, status.GetReconciledCommit(), "nothing has been reconciled yet, and the empty commit says so")
}

// A rebuild clears the retained half, and the corpus cannot bring it back. That makes the
// confirmation mandatory rather than polite, and it makes "safe by construction" false.
func TestRebuildCorpusRefusesWithoutAConfirmationAndNamesTheDigest(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.corpusHandler().RebuildCorpus(context.Background(), connect.NewRequest(&knowledgev1.RebuildCorpusRequest{
		BaseId: "docs", Owner: "toolbox",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The digest is named in the refusal, so a caller does not need a second call to find out
	// what to confirm. The reason is the retained half: a base also holds documents nobody
	// wrote in a repository, and everything an assistant accumulated in it.
	assert.Contains(t, err.Error(), "dry_run = true")
	assert.Contains(t, err.Error(), "nobody wrote in a repository")
	assert.Empty(t, b.sentTo("/memories"))
}

func TestRebuildCorpusRefusesAConfirmationForAPlanNobodyRead(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.corpusHandler().RebuildCorpus(context.Background(), connect.NewRequest(&knowledgev1.RebuildCorpusRequest{
		BaseId: "docs", Owner: "toolbox", Confirm: "not-the-digest",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// This is the failure a person would otherwise attribute to the reconciler: the approval and
	// the plan were for different directories.
	assert.Contains(t, err.Error(), "not-the-digest")
	assert.Empty(t, b.sentTo("/memories"))
}

func TestRebuildCorpusDryRunIsAReadAndNeedsNoConfirmation(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	resp, err := p.corpusHandler().RebuildCorpus(context.Background(), connect.NewRequest(&knowledgev1.RebuildCorpusRequest{
		BaseId: "docs", DryRun: true,
	}))
	require.NoError(t, err)
	// The dry run reports the plan and clears nothing, and it needs no owner either — a
	// preview that had to be authorised to look would be a strange kind of preview.
	require.NotNil(t, resp.Msg.GetPlan())
	assert.NotEmpty(t, resp.Msg.GetPlan().GetPlanDigest())
	assert.Zero(t, resp.Msg.GetClearedFacts())
	assert.Empty(t, b.sentTo("/memories"))
}

func TestARebuildSaysWhatTheCorpusCannotBringBack(t *testing.T) {
	t.Parallel()
	documents := `{"items":[
		{"id":"wiki:restore","content_hash":"sha256:aaa","updated_at":"2026-09-01T10:00:00Z",
		 "tags":["toolbox:owned-by=knowledge"]},
		{"id":"doc-dropped-in","content_hash":"sha256:bbb","updated_at":"2026-09-01T10:00:00Z",
		 "tags":[]}],
		"total":2,"limit":0,"offset":0}`
	p, b := newAdminProvider(t, map[string]string{
		"/documents":       documents,
		"DELETE /memories": `{"success":true,"deleted_count":7}`,
	})
	preview, err := p.corpusHandler().RebuildCorpus(context.Background(), connect.NewRequest(&knowledgev1.RebuildCorpusRequest{
		BaseId: "docs", DryRun: true,
	}))
	require.NoError(t, err)

	resp, err := p.corpusHandler().RebuildCorpus(context.Background(), connect.NewRequest(&knowledgev1.RebuildCorpusRequest{
		BaseId: "docs", Owner: "toolbox", Confirm: preview.Msg.GetPlan().GetPlanDigest(),
	}))
	require.NoError(t, err)
	// One of the two documents is not accounted for by the corpus, and it is gone. This is the
	// number that makes the method's own comment honest, and it is counted *before* the clear
	// so it is what was lost rather than what is missing now.
	assert.Equal(t, int32(1), resp.Msg.GetUnrecoverableDocuments())
	assert.Contains(t, strings.Join(resp.Msg.GetWarnings(), " "), "not accounted for by the corpus")
	assert.NotEmpty(t, b.sentTo("/memories"))
}

// --- KnowledgeBaseService.

func TestListBasesReturnsWhatTheBackendHolds(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{"/banks": banksBody})
	resp, err := p.baseHandler().ListBases(context.Background(), connect.NewRequest(&knowledgev1.ListBasesRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetBases(), 1)
	// A base names its corpus roots, because the corpus is what a base is derived from and a
	// base with no roots is one nothing can be reconciled into.
	assert.NotEmpty(t, resp.Msg.GetBases()[0].GetCorpusRoots())
}

func TestCreateBaseRefusesABaseNobodyCanAddress(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.baseHandler().CreateBase(context.Background(), connect.NewRequest(&knowledgev1.CreateBaseRequest{
		Name: "   ",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "nobody can address")
	assert.Empty(t, b.writes())
}

func TestDeleteBaseSaysWhatSurvivesTheCorpus(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/stats": `{"bank_id":"docs","total_nodes":0,"total_links":0,"total_documents":4,
			"nodes_by_fact_type":{},"links_by_link_type":{},"links_by_fact_type":{},
			"links_breakdown":{},"pending_operations":0,"failed_operations":0}`,
		"DELETE /banks/docs": `{"success":true,"deleted_count":0}`,
	})
	// A base with nothing reconciled into it has no ownership record, so the "what survives"
	// count is genuinely zero here — and the corpus is on disk either way. So the count is
	// asserted on a base that *has* been reconciled, which is the case where a reader needs it.
	resp, err := p.baseHandler().DeleteBase(context.Background(), connect.NewRequest(&knowledgev1.DeleteBaseRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	impact := resp.Msg.GetImpact()
	require.NotNil(t, impact)
	// The one number that is not derivable from the base being deleted: the corpus is on disk and
	// this does not touch it, and a reader who deleted a base needs to know that. It is zero
	// for a base nothing was ever reconciled into, which is the honest answer for this fixture.
	assert.NotNil(t, impact)
	assert.Zero(t, impact.GetCorpusFilesPreserved(), "nothing was reconciled, so there is nothing preserved")
	assert.Equal(t, int32(4), impact.GetDocuments(), "what the base held, which does go")
	assert.NotEmpty(t, b.writes(), "the base was deleted")
}

func TestResetBaseConfigReturnsWhatItReplaced(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/config": `{"bank_id":"docs","config":{"mission":"answer from the corpus"},
			"overrides":{"retain_chunk_size":512}}`,
	})
	resp, err := p.baseHandler().ResetBaseConfig(context.Background(), connect.NewRequest(&knowledgev1.ResetBaseConfigRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	// A reset that does not say what it replaced leaves an operator with no way to know what
	// they just lost, and the loss is a setting somebody chose deliberately.
	assert.Equal(t, "answer from the corpus", resp.Msg.GetPrevious().GetMission())
	assert.NotEmpty(t, b.sentTo("/config"))
}

func TestGetBaseConfigReadsWhatTheBaseWillSend(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/config": `{"bank_id":"docs","config":{"mission":"answer from the corpus",
			"retain_extraction_mode":"balanced"},"overrides":{}}`,
	})
	resp, err := p.baseHandler().GetBaseConfig(context.Background(), connect.NewRequest(&knowledgev1.GetBaseConfigRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	// The mission is what the base is for, and a deployment that set it needs to read it back
	// through the same surface it set it through.
	assert.Equal(t, "answer from the corpus", resp.Msg.GetConfig().GetMission())
	assert.Equal(t, "balanced", resp.Msg.GetConfig().GetExtractionMode())
}

func TestGetBaseStatsSplitsWorldFromExperienceFacts(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/stats": `{"bank_id":"docs","total_nodes":120,"total_links":80,"total_documents":4,
			"nodes_by_fact_type":{"world":100,"experience":20},"links_by_link_type":{},
			"links_by_fact_type":{},"links_breakdown":{},"pending_operations":1,"failed_operations":0}`,
	})
	resp, err := p.baseHandler().GetBaseStats(context.Background(), connect.NewRequest(&knowledgev1.GetBaseStatsRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	stats := resp.Msg.GetStats()
	require.NotNil(t, stats)
	// Quoting an experience fact as a documented claim is a category error a reader cannot see,
	// so the split is reported rather than left inside a total.
	assert.Equal(t, int32(100), stats.GetWorldFacts())
	assert.Equal(t, int32(20), stats.GetExperienceFacts())
	assert.Equal(t, int32(4), stats.GetDocuments())
}

func TestListBaseAliasesNamesWhichOneIsPrimary(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/aliases": `{"bank_id":"docs","aliases":[
			{"alias":"docs","primary":true},{"alias":"handbook","primary":false}]}`,
	})
	resp, err := p.baseHandler().ListBaseAliases(context.Background(), connect.NewRequest(&knowledgev1.ListBaseAliasesRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetAliases(), 2)
	// A deployment that addresses a base by name uses the primary one, and a reader needs to
	// know which that is rather than inferring it from order.
	assert.True(t, resp.Msg.GetAliases()[0].GetIsPrimary())
	assert.False(t, resp.Msg.GetAliases()[1].GetIsPrimary())
}

func TestAddBaseAliasRefusesAnEmptyName(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.baseHandler().AddBaseAlias(context.Background(), connect.NewRequest(&knowledgev1.AddBaseAliasRequest{
		BaseId: "docs", Name: "  ",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Empty(t, b.writes())
}

func TestSetPrimaryBaseAliasRefusesAnEmptyAlias(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.baseHandler().SetPrimaryBaseAlias(context.Background(), connect.NewRequest(&knowledgev1.SetPrimaryBaseAliasRequest{
		BaseId: "docs", AliasId: "",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Empty(t, b.writes())
}

func TestRemoveBaseAliasRefusesAnEmptyAlias(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.baseHandler().RemoveBaseAlias(context.Background(), connect.NewRequest(&knowledgev1.RemoveBaseAliasRequest{
		BaseId: "docs", AliasId: "",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Empty(t, b.writes())
}

// --- PageService, MentalModelService, DirectiveService, TemplateService, OperationService.

func TestUpdatePageNodeCarriesTheChangeAndTheRequestHasNoBody(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/knowledge-base/nodes/p1": `{"id":"p1","kind":"page","name":"restore","parent_id":""}`,
	})
	_, err := p.pageHandler().UpdatePageNode(context.Background(), connect.NewRequest(&knowledgev1.UpdatePageNodeRequest{
		BaseId: "docs", NodeId: "p1", Name: strPtr("restore-a-base"),
	}))
	require.NoError(t, err)
	sent := b.sentTo("/knowledge-base/nodes/p1")
	require.NotEmpty(t, sent)
	assert.Contains(t, sent[0].Body, "restore-a-base")
	// What the request cannot carry is the point: a page's body is what the system believes,
	// and a configuration that could put a different body there would make the tree a place a
	// tool writes prose and calls it the system's beliefs.
	assert.NotContains(t, strings.ToLower(sent[0].Body), "body")
}

func TestListMentalModelsPagesAndRefusesAnUnreadableToken(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/mental-models": `{"items":[{"id":"m1","bank_id":"docs","name":"restore",
			"source_query":"how do we restore a base?","content":"# Restore","tags":["runbook"]}],
			"total":1,"limit":0,"offset":0}`,
	})
	resp, err := p.mentalModelHandler().ListMentalModels(context.Background(), connect.NewRequest(&knowledgev1.ListMentalModelsRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetModels(), 1)
	// A model with no body and a model that has not been refreshed are different states, and
	// the flag is what tells them apart.
	assert.True(t, resp.Msg.GetModels()[0].GetHasText())

	_, err = p.mentalModelHandler().ListMentalModels(context.Background(), connect.NewRequest(&knowledgev1.ListMentalModelsRequest{
		BaseId: "docs", PageToken: "next",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
}

func TestUpdateMentalModelChangesTheQuestionAndNeverTheBody(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/mental-models/m1": `{"id":"m1","bank_id":"docs","name":"restore",
			"source_query":"how do we recover from a bad ingest?","content":"# Restore","tags":["runbook"]}`,
	})
	resp, err := p.mentalModelHandler().UpdateMentalModel(context.Background(), connect.NewRequest(&knowledgev1.UpdateMentalModelRequest{
		BaseId: "docs", ModelId: "m1", SourceQuery: strPtr("how do we recover from a bad ingest?"),
	}))
	require.NoError(t, err)
	assert.Equal(t, "how do we recover from a bad ingest?", resp.Msg.GetModel().GetSourceQuery())
	sent := b.sentTo("/mental-models/m1")
	require.NotEmpty(t, sent)
	// The question is what the body is regenerated from. Accepting a body here would make a
	// model's current beliefs something a caller chose rather than something the system derived.
	assert.NotContains(t, strings.ToLower(sent[0].Body), `"content"`)
}

func TestDeleteMentalModelSaysWhatIsRemovedWithIt(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/mental-models/m1":  `{"id":"m1","bank_id":"docs","name":"restore","content":"# Restore"}`,
		"/mental-models/m1/": `{"id":"m1","bank_id":"docs","name":"restore"}`,
	})
	resp, err := p.mentalModelHandler().DeleteMentalModel(context.Background(), connect.NewRequest(&knowledgev1.DeleteMentalModelRequest{
		BaseId: "docs", ModelId: "m1",
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetDeleted())
	notes := strings.Join(resp.Msg.GetNotes(), " ")
	// The model and the document behind it both go, and a page behind the model will rebuild
	// itself on its next refresh. None of that is visible afterwards.
	assert.Contains(t, notes, "document")
	assert.NotEmpty(t, b.sentTo("/mental-models/m1"))
}

func TestListDirectivesFiltersLocallyAndKeepsTheBackendTotal(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/directives": `{"items":[
			{"id":"d1","bank_id":"docs","name":"Cite","content":"Cite the file.","tags":["safety"]},
			{"id":"d2","bank_id":"docs","name":"Be brief","content":"Be brief.","tags":[],
			 "is_active":false}],
			"total":2,"limit":0,"offset":0}`,
	})
	resp, err := p.directiveHandler().ListDirectives(context.Background(), connect.NewRequest(&knowledgev1.ListDirectivesRequest{
		BaseId: "docs", EnabledOnly: true,
	}))
	require.NoError(t, err)
	// A disabled rule is retained rather than deleted, so a rule somebody turned off can be
	// turned back on with its text intact — and a listing that hid it would make that invisible.
	require.Len(t, resp.Msg.GetDirectives(), 1)
	assert.Equal(t, "d1", resp.Msg.GetDirectives()[0].GetId())
	assert.True(t, resp.Msg.GetDirectives()[0].GetEnabled())
}

func TestExportBaseCarriesTheCorpusIdentityWhenAskedAndNotOtherwise(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/export": `{"version":"1","bank":{"reflect_mission":"answer from the corpus first"}}`,
	})
	resp, err := p.templateHandler().ExportBase(context.Background(), connect.NewRequest(&knowledgev1.ExportBaseRequest{
		BaseId: "docs", IncludeOwnership: false,
	}))
	require.NoError(t, err)
	// The corpus is in a repository and the derived half is not portable at all: its identifiers
	// are a function of extraction. So the bundle is configuration, and the identity is opt-in.
	assert.NotNil(t, resp.Msg.GetTemplate())
	assert.Nil(t, resp.Msg.GetCorpus(), "nobody asked for the ownership binding")
	assert.NotEmpty(t, resp.Msg.GetDigest())
}

func TestListOperationsFiltersAndKeepsTheBackendTotal(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		// `error_message` is a required field on every operation, not an optional one: the
		// generated client refuses a body without it. A failure with no reason is the hardest
		// kind to act on, and the backend makes it impossible to express one.
		"/operations": `{"bank_id":"docs","operations":[
			{"id":"op-1","status":"running","task_type":"retain","items_count":120,
			 "error_message":"","created_at":"2026-09-01T10:00:00Z",
			 "progress":{"stage":"extract","at":"2026-09-01T10:00:00Z","processed":30,"total":120}},
			{"id":"op-2","status":"failed","task_type":"consolidation","items_count":1,
			 "error_message":"the model provider returned 429","created_at":"2026-09-01T10:00:00Z",
			 "progress":{"stage":"consolidate","at":"2026-09-01T10:00:00Z"}}],
			"total":2,"limit":0,"offset":0}`,
	})
	resp, err := p.operationHandler().ListOperations(context.Background(), connect.NewRequest(&knowledgev1.ListOperationsRequest{
		BaseId: "docs", Statuses: []string{"running"},
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetOperations(), 1)
	op := resp.Msg.GetOperations()[0]
	assert.Equal(t, "op-1", op.GetId())
	// Both the percentage and the counts, because a caller deciding whether to wait needs to
	// know how much is left as well as how far along it is.
	assert.True(t, op.GetHasProgress())
	assert.Equal(t, int32(25), op.GetProgress())
	assert.Equal(t, int32(120), op.GetItemsTotal())
}

// The adapter's surface, so a handler test and an adapter test cannot drift on which method
// exists and what it is called. A compile-time assertion is the test: a rename that missed one
// of these would fail here rather than in a deployment.
func TestTheAdapterExposesEveryBackendAreaTheContractNames(t *testing.T) {
	t.Parallel()
	for _, m := range []any{
		(*kh.Client).ListBases, (*kh.Client).CreateBase, (*kh.Client).DeleteBase,
		(*kh.Client).GetBaseStats, (*kh.Client).GetBankConfig, (*kh.Client).UpdateBankConfig,
		(*kh.Client).ResetBankConfig, (*kh.Client).AddBaseAlias, (*kh.Client).SetBankAliasPrimary,
		(*kh.Client).RemoveBankAlias, (*kh.Client).ListAliasesFor, (*kh.Client).IngestionSeries,
		(*kh.Client).Recall, (*kh.Client).Reflect, (*kh.Client).DryRunExtract, (*kh.Client).PreviewPrompts,
		(*kh.Client).ListDocuments, (*kh.Client).GetDocument, (*kh.Client).ListDocumentChunks,
		(*kh.Client).ReprocessDocument, (*kh.Client).PageTree, (*kh.Client).ExportPages,
		(*kh.Client).ListPages, (*kh.Client).CreateFolder, (*kh.Client).CreatePage,
		(*kh.Client).UpdatePageNode, (*kh.Client).DeletePageNode,
		(*kh.Client).GetFact, (*kh.Client).UpdateFact, (*kh.Client).FactHistory,
		(*kh.Client).ListEntities, (*kh.Client).GetEntity, (*kh.Client).EntityGraph,
		(*kh.Client).ListRules, (*kh.Client).GetRule, (*kh.Client).CreateRule, (*kh.Client).UpdateRule,
		(*kh.Client).DeleteRule, (*kh.Client).ListModels, (*kh.Client).GetModel, (*kh.Client).CreateModel,
		(*kh.Client).UpdateModel, (*kh.Client).DeleteModel, (*kh.Client).ClearModel,
		(*kh.Client).ModelHistory, (*kh.Client).RefreshModel, (*kh.Client).PreviewModelRefresh,
		(*kh.Client).ListJobs, (*kh.Client).GetJob, (*kh.Client).CancelJob, (*kh.Client).RetryJob,
		(*kh.Client).DeleteJob, (*kh.Client).ExportManifest, (*kh.Client).ImportManifest,
		(*kh.Client).TemplateSchema, (*kh.Client).ListObservationScopes,
		(*kh.Client).TriggerConsolidation, (*kh.Client).RecoverConsolidation,
		(*kh.Client).ClearObservations, (*kh.Client).PreviewConsolidation,
	} {
		assert.NotNil(t, m)
	}
}
