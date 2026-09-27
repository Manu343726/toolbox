package knowledgehindsight

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Manu343726/toolbox/pkg/api"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// These tests cover the eight services added after the content, query, corpus, mount and base
// surfaces. What they protect is the same thing the earlier tests protect: what the subsystem
// *decides*, in a system where most of the interesting decisions are refusals. A service that
// forwards a request faithfully is easy; a service that has to decline something the backend offers
// is where the behaviour is, and a decline that stops naming what it declined is a decline nobody
// can act on.

func newAdminProvider(t *testing.T, responses map[string]string) (*Provider, *backend) {
	t.Helper()
	b := newBackend(t)
	for suffix, body := range responses {
		b.responses[suffix] = body
	}
	return newProvider(t, b, corpusFixture(t)), b
}

// --- MemoryService.

func TestGetMemoryReportsAFactAndWhatItCameFrom(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/memories/fact-1": `{"id":"fact-1","text":"one base, two halves","fact_type":"world",` +
			`"document_id":"doc-7","entities":"base, corpus","tags":["architecture"],` +
			`"updated_at":"2026-09-01T10:00:00Z","edited_at":"2026-09-02T10:00:00Z"}`,
	})
	resp, err := p.memoryHandler().GetMemory(context.Background(), connect.NewRequest(&knowledgev1.GetMemoryRequest{
		BaseId: "docs", MemoryId: "fact-1",
	}))
	require.NoError(t, err)
	fact := resp.Msg.GetMemory()
	// The document is what a reader needs in order to check the claim, so it travels with the
	// claim rather than being a second call.
	assert.Equal(t, "doc-7", fact.GetDocumentId())
	assert.Equal(t, "world", fact.GetFactType())
	// The backend packs entities into one comma-separated string. The contract says
	// `repeated`, and a list is the only shape a caller can filter — so the split happens here
	// and a test pins it, because a contract that says a list and delivers a string is a
	// contract nobody can rely on.
	assert.Equal(t, []string{"base", "corpus"}, fact.GetEntities())
	assert.True(t, fact.GetCurated(), "a fact with an edit time was corrected by a person")
}

func TestCurateMemorySendsTheReasonAndSaysTheCascadeIsUnmeasured(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/memories/fact-1": `{"id":"fact-1","text":"one base, two halves","fact_type":"world"}`,
	})
	resp, err := p.memoryHandler().CurateMemory(context.Background(), connect.NewRequest(&knowledgev1.CurateMemoryRequest{
		BaseId: "docs", MemoryId: "fact-1", Text: "one base, two halves, and a corpus", Reason: "the corpus half is load-bearing",
	}))
	require.NoError(t, err)
	patched := b.sentTo("/memories/fact-1")
	require.NotEmpty(t, patched)
	// The reason is not decoration. A correction with no record of why is
	// indistinguishable from one somebody changed by accident, and the backend has a place to
	// keep it — so a test asserts it is sent rather than assuming the field is honoured.
	assert.Equal(t, "PATCH", patched[0].Method)
	assert.Contains(t, patched[0].Body, "the corpus half is load-bearing")
	// The cascade is the expensive half of a correction, and the backend re-derives without
	// reporting. Reporting zeros as measurements would tell a caller nothing moved, which is
	// the one answer that would be wrong.
	assert.Zero(t, resp.Msg.GetCascade().GetFactsReplaced())
	assert.NotEmpty(t, resp.Msg.GetCascade().GetNotes())
	assert.Contains(t, strings.Join(resp.Msg.GetCascade().GetNotes(), " "), "unmeasured")
}

func TestCurateMemoryRefusesAnEmptyCorrection(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.memoryHandler().CurateMemory(context.Background(), connect.NewRequest(&knowledgev1.CurateMemoryRequest{
		BaseId: "docs", MemoryId: "fact-1", Text: "",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// A refusal that still writes is not a refusal.
	assert.Empty(t, b.sentTo("/memories/fact-1"))
}

func TestGetMemoryHistoryNamesWhichOfTheTwoProducedEachVersion(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/memories/fact-1/history": `{"items":[
			{"id":"fact-1","text":"what the extractor said","updated_at":"2026-09-01T10:00:00Z"},
			{"id":"fact-1","text":"what a person said","updated_at":"2026-09-02T10:00:00Z","edited_at":"2026-09-02T10:00:00Z"}]}`,
	})
	resp, err := p.memoryHandler().GetMemoryHistory(context.Background(), connect.NewRequest(&knowledgev1.GetMemoryHistoryRequest{
		BaseId: "docs", MemoryId: "fact-1",
	}))
	require.NoError(t, err)
	revisions := resp.Msg.GetRevisions()
	require.Len(t, revisions, 2)
	// "Why does the base believe this" is usually answered by which of two produced the current
	// version, so the history says which rather than leaving a reader to infer it from a
	// timestamp.
	assert.Equal(t, "extraction", revisions[0].GetSource())
	assert.Equal(t, "curation", revisions[1].GetSource())
}

func TestGetMemoryGraphSaysWhatItWalkedRatherThanPromisingANeighbourhood(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/graph": `{"nodes":[{"data":{"id":"e1","label":"base","mentionCount":3}}],"total_entities":1,"total_edges":1,"limit":50,` +
			`"edges":[{"data":{"id":"l1","source":"e1","target":"e2","linkType":"mentions","weight":80}}]}`,
	})
	resp, err := p.memoryHandler().GetMemoryGraph(context.Background(), connect.NewRequest(&knowledgev1.GetMemoryGraphRequest{
		BaseId: "docs", MemoryId: "fact-1",
	}))
	require.NoError(t, err)
	// The backend's graph has no root, depth or limit, so a method that promised a
	// neighbourhood and returned the whole graph would be answering a different question
	// quietly. The response says what it walked instead.
	assert.NotEmpty(t, resp.Msg.GetWalked())
	require.Len(t, resp.Msg.GetNodes(), 1)
	assert.Equal(t, "base", resp.Msg.GetNodes()[0].GetLabel())
	require.Len(t, resp.Msg.GetLinks(), 1)
	// The backend expresses a weight as a whole percentage and the contract as a float. A
	// reader comparing 80 to a recall score of 0.8 would conclude the graph was certain about
	// something it was only fairly sure about.
	assert.InDelta(t, 0.8, resp.Msg.GetLinks()[0].GetWeight(), 0.001)
}

// --- EntityService.

func TestListEntitiesReportsTheBackendTotalAndFiltersHere(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/entities": `{"items":[
			{"id":"e1","canonical_name":"base","mention_count":3},
			{"id":"e2","canonical_name":"corpus","mention_count":1}],
			"total":2,"limit":50,"offset":0}`,
	})
	resp, err := p.entityHandler().ListEntities(context.Background(), connect.NewRequest(&knowledgev1.ListEntitiesRequest{
		BaseId: "docs", Query: "bas",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetEntities(), 1)
	// The filtering is local, so the total is the backend's — the unfiltered one. A token is
	// therefore still offered even though this page came back short: the entity after the
	// one that matched is on the next page, and reporting "that was the end" would hide it.
	assert.Equal(t, "1", resp.Msg.GetNextPageToken())
}

func TestGetEntityCountsTheObservationsItAppearsInAndNotTheirText(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/entities/e1": `{"id":"e1","canonical_name":"base","mention_count":3,"observations":[
			{"text":"one base has two halves","mentioned_at":"2026-09-01T10:00:00Z"},
			{"text":"a base is disposable","mentioned_at":"2026-09-02T10:00:00Z"}]}`,
	})
	resp, err := p.entityHandler().GetEntity(context.Background(), connect.NewRequest(&knowledgev1.GetEntityRequest{
		BaseId: "docs", EntityId: "e1",
	}))
	require.NoError(t, err)
	// The backend's references carry the claim's text, so there is no identifier to hand back
	// and an always-empty identifier list would read as "in no observation" rather than as
	// "this build cannot say which". The count is the part that is traversable.
	assert.Equal(t, int32(2), resp.Msg.GetEntity().GetObservationCount())
	assert.Equal(t, int32(3), resp.Msg.GetEntity().GetMentionCount())
}

func TestGetEntityRefusesAnEntityTheBackendDidNotReturn(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	b.absent["/entities/e-nope"] = true
	_, err := p.entityHandler().GetEntity(context.Background(), connect.NewRequest(&knowledgev1.GetEntityRequest{
		BaseId: "docs", EntityId: "e-nope",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	assert.Contains(t, err.Error(), "e-nope")
}

func TestGetEntityGraphRootedLocallyAndSaysTheBackendHasNoRoot(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/graph": `{"nodes":[{"data":{"id":"e1","label":"base"}},{"data":{"id":"e2","label":"corpus"}},
			{"data":{"id":"e9","label":"unrelated"}}],
			"edges":[{"data":{"id":"l1","source":"e1","target":"e2","linkType":"mentions"}}],
			"total_entities":3,"total_edges":1,"limit":50}`,
	})
	resp, err := p.entityHandler().GetEntityGraph(context.Background(), connect.NewRequest(&knowledgev1.GetEntityGraphRequest{
		BaseId: "docs", RootEntityId: "e1",
	}))
	require.NoError(t, err)
	// The walk is filtered here because the endpoint has no root parameter. A graph that
	// ignored the root and returned everything would be answering a question nobody asked.
	assert.Len(t, resp.Msg.GetNodes(), 2)
	assert.Len(t, resp.Msg.GetLinks(), 1)
}

// --- PageService.

func TestCreatePageRefusesAPageWithNoQuestion(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.pageHandler().CreatePage(context.Background(), connect.NewRequest(&knowledgev1.CreatePageRequest{
		BaseId: "docs", Name: "restore",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The refusal says what a page without a question is, because "it looks wrong" is not
	// actionable and a caller retrying the same call is how an empty page gets created.
	assert.Contains(t, err.Error(), "synthesized")
	assert.Empty(t, b.sentTo("/knowledge-base"))
}

func TestCreatePageSendsTheTriggerInFullAndNamesWhatItIsMissing(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/knowledge-base/pages": `{"page_id":"page-1","mental_model_id":"model-1"}`,
		"/knowledge-base/tree":  `{"roots":[{"id":"page-1","kind":"page","name":"restore","parent_id":"","mental_model_id":"model-1"}]}`,
	})
	_, err := p.pageHandler().CreatePage(context.Background(), connect.NewRequest(&knowledgev1.CreatePageRequest{
		BaseId: "docs", Name: "restore", SourceQuery: "how do we restore a base?",
		Trigger: &knowledgev1.PageTrigger{Mode: "teleport"},
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The accepted values are named, because "invalid mode" leaves the caller to guess the
	// vocabulary and a contract with an unstated enum is a contract nobody validates against.
	assert.Contains(t, err.Error(), "delta")
	assert.Empty(t, b.sentTo("/knowledge-base/pages"))
}

func TestCreatePageSendsTheResolvedTriggerSoTheInputsAreExplicit(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/knowledge-base/pages": `{"page_id":"page-1","mental_model_id":"model-1"}`,
		"/knowledge-base/tree":  `{"roots":[{"id":"page-1","kind":"page","name":"restore","parent_id":"","mental_model_id":"model-1"}]}`,
	})
	_, err := p.pageHandler().CreatePage(context.Background(), connect.NewRequest(&knowledgev1.CreatePageRequest{
		BaseId: "docs", Name: "restore", SourceQuery: "how do we restore a base?",
		Tags: []string{"runbook"},
		Trigger: &knowledgev1.PageTrigger{
			Mode: "all", Tags: []string{"runbook"}, TagsMatch: "all_strict",
		},
	}))
	require.NoError(t, err)
	sent := b.sentTo("/knowledge-base/pages")
	require.NotEmpty(t, sent)
	// A trigger that matches nothing produces an empty document with no visible error, so the
	// inputs are made explicit at the moment they are cheapest to be wrong about.
	assert.Contains(t, sent[0].Body, `"all_strict"`)
	assert.Contains(t, sent[0].Body, `"tag_groups"`)
}

func TestATriggerCannotBothFollowConsolidationAndRunOnASchedule(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, nil)
	_, err := p.pageHandler().CreatePage(context.Background(), connect.NewRequest(&knowledgev1.CreatePageRequest{
		BaseId: "docs", Name: "restore", SourceQuery: "how do we restore a base?",
		Trigger: &knowledgev1.PageTrigger{RefreshAfterConsolidation: true, Cron: "0 * * * *"},
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The two are not two settings for one thing. Naming both means the page refreshes twice,
	// for reasons nothing in the resulting document would show.
	assert.Contains(t, err.Error(), "twice")
}

func TestCreatePageFolderRefusesAPathThatIsNotThereAndDoesNotMakeOne(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/knowledge-base/tree": `{"roots":[]}`,
	})
	_, err := p.pageHandler().CreatePageFolder(context.Background(), connect.NewRequest(&knowledgev1.CreatePageFolderRequest{
		BaseId: "docs", TreePath: "runbooks", Name: "restore",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	// A typo in segment three would otherwise make two folders and put the result somewhere
	// nobody is looking — a tree that exists, containing something, which is the hardest kind
	// of mistake to notice later.
	assert.Empty(t, b.sentTo("/knowledge-base/folders"))
}

func TestCreatePageFolderForwardsTheBackendHavingNoTagsOrDescription(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, nil)
	_, err := p.pageHandler().CreatePageFolder(context.Background(), connect.NewRequest(&knowledgev1.CreatePageFolderRequest{
		BaseId: "docs", Name: "runbooks", Tags: []string{"ops"}, Description: "things to do when things break",
	}))
	require.Error(t, err)
	// The backend's folder create takes a name and a parent and nothing else. Accepting the
	// tags and dropping them would report a success for a call that applied half of what was
	// asked, and the folder would be undescribable and unfindable by the thing that described
	// it.
	assert.Equal(t, api.KindUnsupported, api.KindOf(err))
	assert.Contains(t, err.Error(), "tags")
	assert.Contains(t, err.Error(), "description")
}

func TestDeletePageSaysWhatWentBecauseTheBackendCannot(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/knowledge-base/tree": `{"roots":[{"id":"f1","kind":"folder","name":"runbooks","children":[
			{"id":"p1","kind":"page","name":"restore","parent_id":"f1"},
			{"id":"p2","kind":"page","name":"rollback","parent_id":"f1"}]}]}`,
		"/knowledge-base/nodes": `{}`,
	})
	resp, err := p.pageHandler().DeletePage(context.Background(), connect.NewRequest(&knowledgev1.DeletePageRequest{
		BaseId: "docs", NodeId: "f1",
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetDeleted())
	// A folder takes its whole subtree and the backend does not enumerate what went, so the
	// response is the only record of it. A caller that wanted to keep a page has to know it
	// was there.
	notes := strings.Join(resp.Msg.GetNotes(), " ")
	assert.Contains(t, notes, "f1")
	assert.Contains(t, notes, "p1")
	assert.Contains(t, notes, "p2")
	assert.NotEmpty(t, b.sentTo("/knowledge-base/nodes/f1"))
}

func TestDeletePageRefusesANodeThatIsNotThere(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{"/knowledge-base/tree": `{"roots":[]}`})
	_, err := p.pageHandler().DeletePage(context.Background(), connect.NewRequest(&knowledgev1.DeletePageRequest{
		BaseId: "docs", NodeId: "f1",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	assert.Empty(t, b.sentTo("/knowledge-base/nodes"))
}

func TestRefreshPageRefusesAFlagThatDisagreesWithTheTrigger(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/knowledge-base/tree": `{"roots":[{"id":"page-1","kind":"page","name":"restore","parent_id":"","mental_model_id":"model-1"}]}`,
		"/mental-models/model-1": `{"id":"model-1","bank_id":"docs","name":"restore","source_query":"how do we restore a base?",
			"trigger":{"mode":"all"}}`,
	})
	_, err := p.pageHandler().RefreshPage(context.Background(), connect.NewRequest(&knowledgev1.RefreshPageRequest{
		BaseId: "docs", PageId: "page-1", Full: false,
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The flag would be a second place to set what the trigger already says, and a caller
	// honouring one while the backend used the other would get a refresh nobody asked for.
	assert.Contains(t, err.Error(), "all")
	assert.Contains(t, err.Error(), "incremental")
}

func TestPreviewPageRefreshSaysAFolderRefreshesNothing(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/knowledge-base/tree": `{"roots":[{"id":"f1","kind":"folder","name":"runbooks","parent_id":""}]}`,
	})
	resp, err := p.pageHandler().PreviewPageRefresh(context.Background(), connect.NewRequest(&knowledgev1.PreviewPageRefreshRequest{
		BaseId: "docs", PageId: "f1",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetWouldRefresh())
	// A folder has no model behind it, and reporting "not stale" would let a caller conclude
	// the folder is current.
	assert.Contains(t, strings.Join(resp.Msg.GetNotes(), " "), "folder")
}

func TestExportPageBundleFiltersToASubtreeAndNamesTheOrigin(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/knowledge-base/export": `{"files":[
			{"path":"runbooks/restore.md","content":"# Restore"},
			{"path":"architecture/overview.md","content":"# Overview"}]}`,
	})
	resp, err := p.pageHandler().ExportPageBundle(context.Background(), connect.NewRequest(&knowledgev1.ExportPageBundleRequest{
		BaseId: "docs", Subtree: "runbooks",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetFiles(), 1)
	assert.Equal(t, "runbooks/restore.md", resp.Msg.GetFiles()[0].GetPath())
	// The bundle is reused rather than re-rendered, so every file in it is derived and saying
	// so is what keeps a caller from treating it as authored.
	assert.Equal(t, "generated", resp.Msg.GetFiles()[0].GetOrigin())
}

// --- MentalModelService.

func TestCreateMentalModelRefusesAModelWithNoQuestion(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.mentalModelHandler().CreateMentalModel(context.Background(), connect.NewRequest(&knowledgev1.CreateMentalModelRequest{
		BaseId: "docs", Name: "restore",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Empty(t, b.sentTo("/mental-models"))
}

func TestClearMentalModelSaysWhatIsStillThere(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/mental-models/model-1":       `{"id":"model-1","bank_id":"docs","name":"restore","content":"# Restore\n\nDrop it."}`,
		"/mental-models/model-1/clear": `{"id":"model-1","bank_id":"docs","name":"restore","content":""}`,
	})
	resp, err := p.mentalModelHandler().ClearMentalModel(context.Background(), connect.NewRequest(&knowledgev1.ClearMentalModelRequest{
		BaseId: "docs", ModelId: "model-1",
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetCleared())
	// An empty model and a model that does not exist are different states with different next
	// steps, so the response says which one this is.
	notes := strings.Join(resp.Msg.GetNotes(), " ")
	assert.Contains(t, notes, "still there")
	assert.NotEmpty(t, b.sentTo("/mental-models/model-1/clear"))
}

func TestGetMentalModelHistoryReportsThatTheWholeHistoryCameBack(t *testing.T) {
	t.Parallel()
	items := make([]string, 0, 7)
	for _, text := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		items = append(items, `{"content":"`+text+`","created_at":"2026-09-01T10:00:00Z","trigger":"consolidation"}`)
	}
	p, _ := newAdminProvider(t, map[string]string{
		"/mental-models/model-1/history": `{"items":[` + strings.Join(items, ",") + `]}`,
	})
	resp, err := p.mentalModelHandler().GetMentalModelHistory(context.Background(), connect.NewRequest(&knowledgev1.GetMentalModelHistoryRequest{
		BaseId: "docs", ModelId: "model-1", PageSize: 3,
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetRevisions(), 3)
	// The backend's history takes no paging parameters, so the whole history arrives and the
	// page size applies here. A caller asking for three of seven and being told it has three
	// would never learn there were four more.
	assert.NotEmpty(t, resp.Msg.GetNextPageToken())
}

func TestAModelHistoryThatReportsFailuresIsNotAHistoryOfACorrectModel(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/mental-models/model-1/history": `{"items":[
			{"content":"# Restore","created_at":"2026-09-01T10:00:00Z","trigger":"consolidation"},
			{"content":"","created_at":"2026-09-02T10:00:00Z","trigger":"cron",
			 "error_message":"the model provider returned 429"}]}`,
	})
	resp, err := p.mentalModelHandler().GetMentalModelHistory(context.Background(), connect.NewRequest(&knowledgev1.GetMentalModelHistoryRequest{
		BaseId: "docs", ModelId: "model-1",
	}))
	require.NoError(t, err)
	revisions := resp.Msg.GetRevisions()
	require.Len(t, revisions, 2)
	// A history that reported only successes would read as a model that has never been wrong,
	// and a reader deciding whether to trust the current body is exactly who reads this.
	assert.True(t, revisions[1].GetFailed())
	assert.Contains(t, revisions[1].GetFailure(), "429")
}

// --- DirectiveService.

func TestCreateDirectiveDerivesANameSoAReaderDoesNotHaveToInventOne(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/directives": `{"id":"d1","bank_id":"docs","name":"Never delete a base",
			"content":"Never delete a base without an exported bundle.","tags":["safety"]}`,
	})
	_, err := p.directiveHandler().CreateDirective(context.Background(), connect.NewRequest(&knowledgev1.CreateDirectiveRequest{
		BaseId: "docs", Text: "Never delete a base without an exported bundle.", Tags: []string{"safety"},
	}))
	require.NoError(t, err)
	sent := b.sentTo("/directives")
	require.NotEmpty(t, sent)
	// The backend requires a name and the contract does not ask for one, because a name is not
	// something a reader has to supply to write a rule. A create and a template import derive
	// it the same way, so the same text produces the same record either way.
	assert.Contains(t, sent[0].Body, `"name":"Never delete a base without an exported bundle."`)
}

func TestCreateDirectiveRefusesAnEmptyRuleAndSaysWhy(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.directiveHandler().CreateDirective(context.Background(), connect.NewRequest(&knowledgev1.CreateDirectiveRequest{
		BaseId: "docs",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// A rule with no words in it governs nothing while reading as though it governs
	// everything, which is the worst of the two possible readings.
	assert.Contains(t, err.Error(), "governs nothing")
	assert.Empty(t, b.sentTo("/directives"))
}

func TestADirectiveWithNoExplicitFlagIsActive(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/directives/d1": `{"id":"d1","bank_id":"docs","name":"Never delete a base",
			"content":"Never delete a base.","tags":[]}`,
	})
	resp, err := p.directiveHandler().GetDirective(context.Background(), connect.NewRequest(&knowledgev1.GetDirectiveRequest{
		BaseId: "docs", DirectiveId: "d1",
	}))
	require.NoError(t, err)
	// The backend reports the flag as nullable. Reading an absent one as false would silently
	// turn off every rule a base ever created, and a base with no governing rules looks exactly
	// like a base that answers from its documents alone.
	assert.True(t, resp.Msg.GetDirective().GetEnabled())
}

func TestDeleteDirectiveRefusesWithoutAConfirmationAndPointsAtDisabling(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/directives/d1": `{"id":"d1","bank_id":"docs","name":"Never delete a base",
			"content":"Never delete a base."}`,
	})
	_, err := p.directiveHandler().DeleteDirective(context.Background(), connect.NewRequest(&knowledgev1.DeleteDirectiveRequest{
		BaseId: "docs", DirectiveId: "d1",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// A rule quietly retained after somebody believes they removed it keeps governing every
	// answer, and nothing in the base would say so. The refusal names the alternative that is
	// a visible state rather than an absence.
	assert.Contains(t, err.Error(), "enabled = false")
	// The only call is the read the refusal needed; nothing was written.
	assert.Empty(t, b.writes())
}

func TestDeleteDirectiveRemovesItOnceConfirmed(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/directives/d1": `{"id":"d1","bank_id":"docs","name":"Never delete a base",
			"content":"Never delete a base."}`,
		"/directives": `{}`,
	})
	resp, err := p.directiveHandler().DeleteDirective(context.Background(), connect.NewRequest(&knowledgev1.DeleteDirectiveRequest{
		BaseId: "docs", DirectiveId: "d1", Confirm: true,
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetDeleted())
	assert.NotEmpty(t, b.sentTo("/directives/d1"))
}

func TestUpdateDirectiveRefusesToEmptyATextAndSaysWhatToDoInstead(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/directives/d1": `{"id":"d1","bank_id":"docs","name":"Never delete a base",
			"content":"Never delete a base."}`,
	})
	emptied := ""
	_, err := p.directiveHandler().UpdateDirective(context.Background(), connect.NewRequest(&knowledgev1.UpdateDirectiveRequest{
		BaseId: "docs", DirectiveId: "d1", Text: &emptied,
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// A caller that emptied the text almost certainly meant to stop the rule governing
	// answers, and that has a way of being said.
	assert.Contains(t, err.Error(), "enabled = false")
	assert.Empty(t, b.sentTo("/directives/d1"))
}

// --- ObservationService.

func TestListObservationScopesNamesTheGlobalOneRatherThanReportingAnEmptyScope(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/observations/scopes": `{"scopes":[
			{"tags":[],"count":4},{"tags":["architecture"],"count":2},{"tags":["a","b"],"count":1}],
			"total":3,"limit":0,"offset":0}`,
	})
	resp, err := p.observationHandler().ListObservationScopes(context.Background(), connect.NewRequest(&knowledgev1.ListObservationScopesRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	scopes := resp.Msg.GetScopes()
	require.Len(t, scopes, 3)
	// A scope has tags and a count and no name of its own, and an empty name reads as a scope
	// nothing consolidated into — which is the wrong conclusion about the base's global one.
	assert.Equal(t, "(global)", scopes[0].GetId())
	assert.Equal(t, "shared", scopes[0].GetMode())
	assert.Equal(t, "architecture", scopes[1].GetId())
	assert.Equal(t, "per_tag", scopes[1].GetMode())
	assert.Equal(t, "a+b", scopes[2].GetId())
	assert.Equal(t, "combined", scopes[2].GetMode())
}

func TestPreviewConsolidationRefusesAPreviewOfNoProposal(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.observationHandler().PreviewConsolidation(context.Background(), connect.NewRequest(&knowledgev1.PreviewConsolidationRequest{
		BaseId: "docs",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The claims are a function of the strategies, so a preview of none would be asking the
	// backend to supply the answer being checked.
	assert.Contains(t, err.Error(), "nobody made")
	assert.Empty(t, b.sentTo("/consolidation-strategies/preview"))
}

func TestPreviewConsolidationReportsWhatEachStrategyWouldClaim(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/consolidation-strategies/preview": `{"strategies":[
			{"active":true,"claimed_count":10,"rules":[
				{"match_count":12,"taken_count":10,"observation_count":2,"samples":[
					{"tags":["architecture"],"count":12,"handled_by":0}]}]},
			{"active":true,"claimed_count":0,"rules":[
				{"match_count":5,"taken_count":0,"observation_count":0,"samples":[
					{"tags":["runbook"],"count":5,"handled_by":0}]}]}],
			"default":{"match_count":17,"observation_count":0,"samples":[]},
			"scopes_scanned":17,"complete":true}`,
	})
	resp, err := p.observationHandler().PreviewConsolidation(context.Background(), connect.NewRequest(&knowledgev1.PreviewConsolidationRequest{
		BaseId: "docs",
		Strategies: []*knowledgev1.ConsolidationStrategy{
			{Mission: "consolidate architecture", Scopes: []*knowledgev1.TagPattern{{Tags: []string{"architecture"}, TagsMatch: strPtr("all_strict")}}, MaxObservationsPerScope: 2},
			{Mission: "consolidate runbooks", Scopes: []*knowledgev1.TagPattern{{Tags: []string{"runbook"}}}},
		},
	}))
	require.NoError(t, err)
	sent := b.sentTo("/consolidation-strategies/preview")
	require.NotEmpty(t, sent)
	// The proposal is sent as it was given, because the claims are a function of it and a
	// preview that quietly resolved a default would be reporting on a different plan.
	assert.Contains(t, sent[0].Body, "consolidate architecture")
	assert.Contains(t, sent[0].Body, "all_strict")
	claims := resp.Msg.GetClaims()
	require.Len(t, claims, 2)
	assert.True(t, claims[0].GetActive())
	assert.Equal(t, int32(10), claims[0].GetFactsClaimed())
	require.Len(t, claims[0].GetRules(), 1)
	assert.Equal(t, int32(12), claims[0].GetRules()[0].GetMatched())
	assert.Equal(t, int32(2), claims[0].GetRules()[0].GetObservations())
	// The second strategy matched five facts and produced nothing from them. That is reported
	// with its counts rather than omitted, because a scope selected and dropped is where a
	// proposal is quietly doing nothing.
	require.Len(t, claims[1].GetRules(), 1)
	assert.Equal(t, int32(5), claims[1].GetRules()[0].GetMatched())
	assert.Zero(t, claims[1].GetRules()[0].GetObservations())
	assert.Equal(t, int32(17), resp.Msg.GetScopesScanned())
	assert.True(t, resp.Msg.GetComplete())
}

func TestPreviewConsolidationSaysWhenTheBackendStoppedEarly(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/consolidation-strategies/preview": `{"strategies":[],"default":{"match_count":4,"observation_count":0,"samples":[]},
			"scopes_scanned":4,"complete":false}`,
	})
	resp, err := p.observationHandler().PreviewConsolidation(context.Background(), connect.NewRequest(&knowledgev1.PreviewConsolidationRequest{
		BaseId:     "docs",
		Strategies: []*knowledgev1.ConsolidationStrategy{{Mission: "m"}},
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetComplete())
	// A claim computed from half a base is a number about half a base, and the response says so
	// rather than letting the caller read it as the whole.
	assert.Contains(t, strings.Join(resp.Msg.GetNotes(), " "), "part of the base")
}

func TestPreviewConsolidationRefusesAMatchModeThatDoesNotExist(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.observationHandler().PreviewConsolidation(context.Background(), connect.NewRequest(&knowledgev1.PreviewConsolidationRequest{
		BaseId: "docs",
		Strategies: []*knowledgev1.ConsolidationStrategy{{
			Mission: "m", Scopes: []*knowledgev1.TagPattern{{Tags: []string{"a"}, TagsMatch: strPtr("sometimes")}},
		}},
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The accepted values are named, and the offending strategy and scope are identified —
	// a list of three strategies where the second is wrong is otherwise a guessing exercise.
	assert.Contains(t, err.Error(), "strategy 0, scope 0")
	assert.Contains(t, err.Error(), "any_strict")
	assert.Empty(t, b.sentTo("/consolidation-strategies/preview"))
}

func TestRecoverConsolidationRefusesAScopeTheBackendCannotBeGiven(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.observationHandler().RecoverConsolidation(context.Background(), connect.NewRequest(&knowledgev1.RecoverConsolidationRequest{
		BaseId: "docs", ObservationScope: "architecture",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindUnsupported, api.KindOf(err))
	// Recovery resumes whatever is interrupted. Honouring a scope by consolidating something
	// else would be a different action than the one that was asked for, and the response would
	// say the scope had been recovered.
	assert.Contains(t, err.Error(), "no scope parameter")
	assert.Empty(t, b.sentTo("/consolidation/recover"))
}

func TestRecoverConsolidationReportsTheCountAndSaysThereIsNoOperation(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/consolidation/recover": `{"retried_count":2}`,
	})
	resp, err := p.observationHandler().RecoverConsolidation(context.Background(), connect.NewRequest(&knowledgev1.RecoverConsolidationRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	// The backend reports how many scopes it retried and not an operation, so a caller that
	// polls for a job would poll for one that will never appear.
	notes := strings.Join(resp.Msg.GetNotes(), " ")
	assert.Contains(t, notes, "2 scope")
	assert.Contains(t, notes, "no operation")
}

func TestClearBaseObservationsCountsWhatIsAboutToGoAndSaysTheFactsStay(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/observations/scopes": `{"scopes":[{"tags":[],"count":7}],"total":1,"limit":0,"offset":0}`,
		"/observations":        `{"success":true,"deleted_count":7}`,
	})
	resp, err := p.observationHandler().ClearBaseObservations(context.Background(), connect.NewRequest(&knowledgev1.ClearBaseObservationsRequest{BaseId: "docs"}))
	require.NoError(t, err)
	assert.Equal(t, int32(7), resp.Msg.GetCleared())
	// The facts are expensive and cheap to keep, observations the other way round — so what is
	// being destroyed is reported, and what survives is named.
	notes := strings.Join(resp.Msg.GetNotes(), " ")
	assert.Contains(t, notes, "facts are still there")
	assert.Contains(t, notes, "7 observation")
	assert.NotEmpty(t, b.sentTo("/observations"))
}

func TestClearBaseObservationsRefusesNamedScopesRatherThanClearingAllOfThem(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.observationHandler().ClearBaseObservations(context.Background(), connect.NewRequest(&knowledgev1.ClearBaseObservationsRequest{
		BaseId: "docs", ObservationScopes: []string{"architecture"},
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindUnsupported, api.KindOf(err))
	// A caller that named one scope and got all of them has lost more than it asked to, and
	// the only warning would be the observations that are no longer there.
	assert.Empty(t, b.sentTo("/observations"))
}

func TestTriggerConsolidationRefusesAScopeThatIsNotThere(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/observations/scopes": `{"scopes":[{"tags":["architecture"],"count":2}],"total":1,"limit":0,"offset":0}`,
	})
	_, err := p.observationHandler().TriggerConsolidation(context.Background(), connect.NewRequest(&knowledgev1.TriggerConsolidationRequest{
		BaseId: "docs", ObservationScope: "operations",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	// Consolidating a scope that is not there would do nothing while reporting an operation,
	// and a caller watching that operation would be watching nothing happen.
	assert.Empty(t, b.sentTo("/consolidate"))
}

func TestTriggerConsolidationSendsTheScopeAsATagGroup(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/observations/scopes": `{"scopes":[{"tags":["architecture"],"count":2}],"total":1,"limit":0,"offset":0}`,
		"/consolidate":         `{"operation_id":"op-1"}`,
	})
	resp, err := p.observationHandler().TriggerConsolidation(context.Background(), connect.NewRequest(&knowledgev1.TriggerConsolidationRequest{
		BaseId: "docs", ObservationScope: "architecture",
	}))
	require.NoError(t, err)
	assert.Equal(t, "op-1", resp.Msg.GetOperationId())
	sent := b.sentTo("/consolidate")
	require.NotEmpty(t, sent)
	// The backend takes a list of tag groups rather than a scope name, so a caller naming one
	// scope is translated into one group.
	assert.Contains(t, sent[0].Body, `[["architecture"]]`)
}

// --- TemplateService.

func TestGetTemplateSchemaNamesTheVersionItAnswersFor(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/bank-template-schema": `{"version":"1","fields":[
			{"name":"retain_mission","type":"string","required":true,"description":"what to extract"}]}`,
	})
	resp, err := p.templateHandler().GetTemplateSchema(context.Background(), connect.NewRequest(&knowledgev1.GetTemplateSchemaRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "1", resp.Msg.GetVersion())
	require.Len(t, resp.Msg.GetFields(), 1)
	assert.True(t, resp.Msg.GetFields()[0].GetRequired())
}

func TestImportTemplateRefusesWithoutTheDigestAndNamesIt(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.templateHandler().ImportTemplate(context.Background(), connect.NewRequest(&knowledgev1.ImportTemplateRequest{
		BaseId:   "docs",
		Template: &knowledgev1.Template{Name: "house-style", Version: "1"},
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The digest is named in the refusal, so the caller does not have to make a second call to
	// find out what to confirm. A confirmation that named only the base would approve
	// whatever happened to arrive later.
	assert.Contains(t, err.Error(), "digest")
	assert.Empty(t, b.sentTo("/import"))
}

func TestImportTemplateRefusesADigestForADifferentTemplate(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/bank-template-schema": `{"version":"1","fields":[]}`,
	})
	_, err := p.templateHandler().ImportTemplate(context.Background(), connect.NewRequest(&knowledgev1.ImportTemplateRequest{
		BaseId:   "docs",
		Template: &knowledgev1.Template{Name: "house-style", Version: "1", Directives: []string{"Be plain."}},
		Confirm:  "not-the-digest",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// This is the failure a person would otherwise blame on the template author: the proposal
	// and the approval were reordered, or the approval was for a different one.
	assert.Contains(t, err.Error(), "not-the-digest")
	assert.Empty(t, b.sentTo("/import"))
}

func TestImportTemplateAppliesAndReadsBackRatherThanAssuming(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/docs/export": `{"version":"1","bank":{"reflect_mission":"answer from the corpus first"},
			"directives":[{"name":"Be plain.","content":"Be plain."}]}`,
		"/docs/import": `{"bank_id":"docs","config_applied":true,"mental_models_created":[],
			"mental_models_updated":[],"directives_created":["Be plain."],"directives_updated":[]}`,
		"/banks": `{"banks":[{"bank_id":"docs","display_alias":"docs",
			"disposition":{"skepticism":0,"literalism":0,"empathy":0}}],"total":1,"limit":0,"offset":0}`,
	})
	// The template is built from the export and confirmed with its digest, which is what a
	// caller does: read, build, digest, apply. A hard-coded digest would stop testing the
	// confirmation the first time the manifest's shape changed.
	manifest := khManifest(t, p)
	exported, err := p.templateHandler().ExportTemplate(context.Background(), connect.NewRequest(&knowledgev1.ExportTemplateRequest{
		BaseId: "docs", Name: "house-style", Version: "1",
	}))
	require.NoError(t, err)
	resp, err := p.templateHandler().ImportTemplate(context.Background(), connect.NewRequest(&knowledgev1.ImportTemplateRequest{
		BaseId:   "docs",
		Template: exported.Msg.GetTemplate(),
		Confirm:  exported.Msg.GetDigest(),
	}))
	_ = manifest
	require.NoError(t, err)
	assert.Equal(t, "docs", resp.Msg.GetBase().GetId())
	// What the import did is what the backend reported. A read-back of the base cannot tell a
	// created directive from an updated one, and a caller told "created" about an update would
	// conclude a rule they had written had been replaced.
	assert.Equal(t, []string{"Be plain."}, resp.Msg.GetRulesCreated())
	assert.Empty(t, resp.Msg.GetRulesUpdated())
	assert.True(t, resp.Msg.GetConfigApplied())
	assert.Empty(t, resp.Msg.GetWarnings())
}

func TestImportBaseAppliesAndReportsTheReconcileThatShouldFollow(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/docs/export": `{"version":"1","bank":{"reflect_mission":"answer from the corpus first"}}`,
		"/docs/import": `{"bank_id":"docs","config_applied":true,"mental_models_created":[],
			"mental_models_updated":[],"directives_created":[],"directives_updated":[]}`,
		"/banks": `{"banks":[{"bank_id":"docs","display_alias":"docs",
			"disposition":{"skepticism":0,"literalism":0,"empathy":0}}],"total":1,"limit":0,"offset":0}`,
	})
	exported, err := p.templateHandler().ExportTemplate(context.Background(), connect.NewRequest(&knowledgev1.ExportTemplateRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	resp, err := p.templateHandler().ImportBase(context.Background(), connect.NewRequest(&knowledgev1.ImportBaseRequest{
		BaseId:   "docs",
		Template: exported.Msg.GetTemplate(),
		Confirm:  exported.Msg.GetDigest(),
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, b.sentTo("/import"), "the manifest was applied")
	// A bundle is configuration and identity, not content. Importing it does not populate the
	// base, and the reconcile that would is the useful thing to hand back: a caller told
	// "imported" and nothing else has to discover an empty base.
	require.NotNil(t, resp.Msg.GetPlan())
	assert.Equal(t, "docs", resp.Msg.GetPlan().GetBaseId())
	assert.NotEmpty(t, resp.Msg.GetPlan().GetCreated(), "the corpus has files and the base has none of them")
}

func TestCloneBaseRefusesToCloneIntoItself(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/docs/export": `{"version":"1","bank":{}}`,
	})
	_, err := p.templateHandler().CloneBase(context.Background(), connect.NewRequest(&knowledgev1.CloneBaseRequest{
		SourceBaseId: "docs", TargetBaseId: "docs",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
}

func TestAManifestDigestIsStableAndOrderIndependent(t *testing.T) {
	t.Parallel()
	first := manifestDigest("house-style", "1", kh.Manifest{
		Version:    "1",
		Config:     map[string]any{"b": "2", "a": "1"},
		Directives: []kh.ManifestDirective{{Name: "one", Content: "One."}, {Name: "two", Content: "Two."}},
		Models:     []kh.ManifestModel{{Name: "m1", SourceQuery: "Q1"}, {Name: "m2", SourceQuery: "Q2"}},
	})
	// Map iteration order in Go is deliberately randomised, so two reads of the same template
	// agreeing is a property of the digest and not an accident. A digest that varied would
	// make every confirmation fail intermittently.
	second := manifestDigest("house-style", "1", kh.Manifest{
		Version:    "1",
		Config:     map[string]any{"a": "1", "b": "2"},
		Directives: []kh.ManifestDirective{{Name: "two", Content: "Two."}, {Name: "one", Content: "One."}},
		Models:     []kh.ManifestModel{{Name: "m2", SourceQuery: "Q2"}, {Name: "m1", SourceQuery: "Q1"}},
	})
	assert.Equal(t, first, second)
	// And it changes when the content does, which is the only reason it can confirm anything.
	third := manifestDigest("house-style", "1", kh.Manifest{Version: "1", Config: map[string]any{"a": "1"}})
	assert.NotEqual(t, first, third)
}

func TestAnExportSaysWhichConfigurationFieldsItDidNotCarry(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/docs/export": `{"version":"1","bank":{"reflect_mission":"be plain","retain_chunk_size":512}}`,
	})
	resp, err := p.templateHandler().ExportTemplate(context.Background(), connect.NewRequest(&knowledgev1.ExportTemplateRequest{
		BaseId: "docs", Name: "house-style", Version: "1",
	}))
	require.NoError(t, err)
	assert.Equal(t, "be plain", resp.Msg.GetTemplate().GetConfig().GetMission())
	// The backend's configuration has dozens of fields and the message models a handful. A
	// caller that believed it had everything would drop settings on a round trip, so the
	// omissions are named in a stable order.
	notes := strings.Join(resp.Msg.GetTemplate().GetRequiredFields(), " ")
	assert.Contains(t, notes, "carries only")
}

// --- OperationService.

func TestAnOperationWithNoProgressReportsNoIdeaRatherThanZero(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/operations/op-1": `{"operation_id":"op-1","status":"running",
			"progress":{"stage":"extract","at":"2026-09-01T10:00:00Z"}}`,
	})
	resp, err := p.operationHandler().GetOperation(context.Background(), connect.NewRequest(&knowledgev1.GetOperationRequest{
		BaseId: "docs", OperationId: "op-1",
	}))
	require.NoError(t, err)
	op := resp.Msg.GetOperation()
	// A job that has just started and a job the backend has no idea about are different
	// states, and a confident 0% for the second would be a fact nobody can act on.
	assert.False(t, op.GetHasProgress())
	assert.Zero(t, op.GetProgress())
	assert.Equal(t, "running", op.GetStatus())
}

func TestAnOperationWithProgressReportsBothThePercentageAndTheCounts(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/operations/op-1": `{"operation_id":"op-1","status":"running",
			"progress":{"stage":"extract","at":"2026-09-01T10:00:00Z","processed":30,"total":120}}`,
	})
	resp, err := p.operationHandler().GetOperation(context.Background(), connect.NewRequest(&knowledgev1.GetOperationRequest{
		BaseId: "docs", OperationId: "op-1",
	}))
	require.NoError(t, err)
	op := resp.Msg.GetOperation()
	assert.True(t, op.GetHasProgress())
	assert.Equal(t, int32(25), op.GetProgress())
	assert.Equal(t, int32(30), op.GetItemsDone())
	assert.Equal(t, int32(120), op.GetItemsTotal())
}

func TestAFailedOperationIsNotRetryableOnceTheBackendHasRetriedIt(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/operations/op-1": `{"operation_id":"op-1","status":"failed","retry_count":2,
			"error_message":"the model provider returned 429","next_retry_at":"2026-09-01T11:00:00Z"}`,
	})
	resp, err := p.operationHandler().GetOperation(context.Background(), connect.NewRequest(&knowledgev1.GetOperationRequest{
		BaseId: "docs", OperationId: "op-1",
	}))
	require.NoError(t, err)
	// The backend schedules its own retries. Offering one here would start a second copy of
	// work that is already going to happen, and the two would race.
	assert.False(t, resp.Msg.GetOperation().GetRetryable())
	assert.Equal(t, "the model provider returned 429", resp.Msg.GetOperation().GetError())
}

func TestRetryOperationRefusesOneTheBackendIsAlreadyRetrying(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/operations/op-1": `{"operation_id":"op-1","status":"failed","retry_count":2}`,
	})
	_, err := p.operationHandler().RetryOperation(context.Background(), connect.NewRequest(&knowledgev1.RetryOperationRequest{
		BaseId: "docs", OperationId: "op-1",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "already been retried")
	assert.Empty(t, b.sentTo("/operations/op-1/retry"))
}

func TestDeleteOperationRefusesARunningOneAndSaysWhy(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, map[string]string{
		"/operations/op-1": `{"operation_id":"op-1","status":"running"}`,
	})
	_, err := p.operationHandler().DeleteOperation(context.Background(), connect.NewRequest(&knowledgev1.DeleteOperationRequest{
		BaseId: "docs", OperationId: "op-1",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// The work is in the backend. Removing the record would not stop it, and a caller left
	// with no way to find out what happened to it is worse off than one holding a stale
	// record.
	assert.Contains(t, err.Error(), "Cancel it first")
	// The read is there; the DELETE is not.
	assert.Empty(t, b.writes()) // the read is there; no DELETE is
}

func TestCancelOperationSeparatesTheRequestFromTheState(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"GET /operations/op-1":    `{"operation_id":"op-1","status":"succeeded","completed_at":"2026-09-01T10:05:00Z"}`,
		"DELETE /operations/op-1": `{"operation_id":"op-1","success":false,"message":"the operation had already finished"}`,
	})
	resp, err := p.operationHandler().CancelOperation(context.Background(), connect.NewRequest(&knowledgev1.CancelOperationRequest{
		BaseId: "docs", OperationId: "op-1",
	}))
	require.NoError(t, err)
	// "I asked and it was too late" is a different answer from "it was cancelled", and
	// conflating them leaves a caller believing a job is stopped when it is not — or that a
	// finished job is still running.
	assert.False(t, resp.Msg.GetAccepted())
	assert.Equal(t, "the operation had already finished", resp.Msg.GetNote())
	require.NotNil(t, resp.Msg.GetOperation())
	assert.Equal(t, "succeeded", resp.Msg.GetOperation().GetStatus())
}

// --- shared paging.

func TestAPageTokenThatIsNotAnOffsetIsRefusedRatherThanReadAsTheFirstPage(t *testing.T) {
	t.Parallel()
	p, b := newAdminProvider(t, nil)
	_, err := p.entityHandler().ListEntities(context.Background(), connect.NewRequest(&knowledgev1.ListEntitiesRequest{
		BaseId: "docs", PageToken: "next",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	// Silently treating an unreadable token as the first page would make a caller believe it
	// had read the whole list.
	assert.Contains(t, err.Error(), "decimal offsets")
	assert.Empty(t, b.sentTo("/entities"))
}

func TestANextPageTokenIsOnlyOfferedWhenThereIsMore(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, map[string]string{
		"/entities": `{"items":[{"id":"e1","canonical_name":"base","mention_count":3}],
			"total":10,"limit":1,"offset":0}`,
	})
	resp, err := p.entityHandler().ListEntities(context.Background(), connect.NewRequest(&knowledgev1.ListEntitiesRequest{
		BaseId: "docs", PageSize: 1,
	}))
	require.NoError(t, err)
	assert.Equal(t, "1", resp.Msg.GetNextPageToken())
}

// --- helpers used by the tests above.

// khManifest reads the manifest a stub backend reports, so a test can digest exactly what a
// caller would have been shown rather than a digest copied out of a failing run.
func khManifest(t *testing.T, p *Provider) kh.Manifest {
	t.Helper()
	manifest, err := p.client.ExportManifest(context.Background(), "docs")
	require.NoError(t, err)
	return manifest
}

func TestTimestampOrNilLeavesAnAbsentTimeAbsent(t *testing.T) {
	t.Parallel()
	// Go's zero time is "the backend did not say", and only that is. The epoch is an instant
	// the backend can genuinely report, and collapsing it to absent would make a fact observed
	// at 1970 look unobserved — which is a different claim, and a wrong one.
	assert.Nil(t, timestampOrNil(time.Time{}))
	assert.NotNil(t, timestampOrNil(time.Unix(0, 0).UTC()))
}

func TestSubtreePathsMatchOnABoundaryNotAPrefix(t *testing.T) {
	t.Parallel()
	// A prefix match would put "runbooks-archive/restore.md" inside "runbooks", and an
	// export filtered to a subtree would then contain a file the caller did not ask for.
	assert.True(t, withinSubtree("runbooks", "runbooks"))
	assert.True(t, withinSubtree("runbooks/restore.md", "runbooks"))
	assert.False(t, withinSubtree("runbooks-archive/restore.md", "runbooks"))
	assert.False(t, withinSubtree("architecture/overview.md", "runbooks"))
}
