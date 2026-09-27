package knowledgehindsight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// backend is a stand-in for the memory backend.
//
// The subsystem's tests assert on what it *sends* as much as on what it returns, because the
// decisions worth protecting — replace rather than append, entity resolution off, a timeless
// timestamp — are all decisions about a request body.
type backend struct {
	*httptest.Server
	requests []recordedRequest
	// responses maps a path suffix to a canned body. A key may be qualified with a method and
	// a space — "DELETE /operations/op-1" — for the cases where one path has two readers that
	// reject each other's fields: the generated models disallow unknown properties, so a body
	// that satisfied a status read would fail a cancel read on the same path.
	responses map[string]string
	// absent names path suffixes the backend should report as missing, so a test can assert
	// on what this build says about a thing that is not there. Without it the stub would have
	// to answer an empty body, and an empty body is a malformed response rather than an
	// absent one — so the test would be asserting on a decoding failure.
	absent map[string]bool
}

type recordedRequest struct {
	Method string
	Path   string
	Body   string
}

func newBackend(t *testing.T) *backend {
	t.Helper()
	// The defaults are the shapes the generated client requires. It validates required
	// response fields, so a stub answering `{}` produces "no value given for required
	// property" and a test that would otherwise pass fails for a reason that has nothing
	// to do with what it is testing.
	b := &backend{absent: map[string]bool{}, responses: map[string]string{
		// The defaults are the shapes the generated client requires. It validates required
		// response fields, so a stub answering `{}` produces "no value given for required
		// property" and a test that would otherwise pass fails for a reason that has nothing
		// to do with what it is testing — which is how a fixture becomes a debugging session.
		"/banks": `{"banks":[{"bank_id":"docs","display_alias":"docs",` +
			`"disposition":{"skepticism":0,"literalism":0,"empathy":0}}],"total":1,"limit":0,"offset":0}`,
		"/aliases":                  `{"bank_id":"docs","aliases":[{"alias":"docs","primary":true}]}`,
		"/memories/dry-run-extract": `{"facts":[],"chunks":[]}`,
		"/memories/recall":          `{"results":[]}`,
		"/memories":                 `{"success":true,"bank_id":"docs","items_count":0,"async":true}`,
		"/reflect":                  `{"text":"an answer","based_on":{}}`,
		"/knowledge-base/tree":      `{"roots":[]}`,
		"/knowledge-base/export":    `{"files":[]}`,
		"/config":                   `{"bank_id":"docs","config":{},"overrides":{}}`,
		"/timeseries":               `{"bank_id":"docs","period":"day","trunc":"day","buckets":[]}`,
		"/stats": `{"bank_id":"docs","total_nodes":0,"total_links":0,"total_documents":0,` +
			`"nodes_by_fact_type":{},"links_by_link_type":{},"links_by_fact_type":{},` +
			`"links_breakdown":{},"pending_operations":0,"failed_operations":0}`,
		"/version": `{"api_version":"0.10.1","features":{"observations":true,"mcp":true,` +
			`"worker":true,"bank_config_api":true,"bank_llm_health":true,"file_upload_api":true,` +
			`"document_export_api":true,"document_import_api":true,"audit_log":true,` +
			`"llm_trace":true,"store_document_text":true}}`,
	}}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		b.requests = append(b.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Body: body})
		w.Header().Set("Content-Type", "application/json")
		// The longest matching suffix wins, so a general `/memories` cannot shadow a
		// specific `/memories/recall`. Matching in map order would make the test
		// intermittent rather than wrong, which is worse: it fails sometimes and looks
		// like a race in this package.
		best, bestBody := "", "{}"
		for key, canned := range b.responses {
			method, suffix := "", key
			if m, rest, ok := strings.Cut(key, " "); ok {
				method, suffix = m, rest
			}
			if method != "" && method != r.Method {
				continue
			}
			if strings.HasSuffix(r.URL.Path, suffix) && len(suffix) > len(best) {
				best, bestBody = suffix, canned
			}
		}
		for suffix := range b.absent {
			if strings.HasSuffix(r.URL.Path, suffix) && len(suffix) >= len(best) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"detail":"not found"}`))
				return
			}
		}
		_, _ = w.Write([]byte(bestBody))
	}))
	t.Cleanup(b.Close)
	return b
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	if r.Body == nil {
		return ""
	}
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

// writes returns the mutating requests the stub received, so a test can assert that a refusal
// wrote nothing without also asserting that it read nothing — which a refusal often has to do in
// order to name what it is refusing.
func (b *backend) writes() []recordedRequest {
	var out []recordedRequest
	for _, r := range b.requests {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

func (b *backend) sentTo(suffix string) []recordedRequest {
	var out []recordedRequest
	for _, r := range b.requests {
		if strings.HasSuffix(r.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

// corpusFixture writes a small corpus and returns its directory.
func corpusFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.md":                 "---\ntitle: Index\n---\n\n# Index\n\nStart here.\n",
		"runbooks/restore.md":      "---\nid: wiki:restore\nkind: procedure\nstatus: active\n---\n\n# Restore a base\n\nDrop it and reconcile again.\n",
		"architecture/overview.md": "---\nkind: architecture\n---\n\n# Overview\n\nOne base, two halves.\n",
	}
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

func newProvider(t *testing.T, b *backend, dir string) *Provider {
	t.Helper()
	p, err := NewService(Options{
		BackendEndpoint: b.URL,
		CorpusRoots:     []knowledge.CorpusRoot{{Path: dir, Name: "docs"}},
		StateDir:        t.TempDir(),
		Owner:           "toolbox",
		RequestTimeout:  5 * time.Second,
	})
	require.NoError(t, err)
	return p
}

// --- base resolution.

func TestProviderRefusesToStartWithoutABackendOrACorpus(t *testing.T) {
	t.Parallel()

	for name, opts := range map[string]Options{
		"no backend": {CorpusRoots: []knowledge.CorpusRoot{{Path: ".", Name: "docs"}}},
		"no corpus":  {BackendEndpoint: "http://127.0.0.1:1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewService(opts)
			require.Error(t, err)
			var apiErr *api.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, api.KindInvalid, apiErr.Kind)
		})
	}
}

func TestAnUnaddressableBaseIsNotFoundAndSaysWhatIs(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.resolveBase(context.Background(), "a name with spaces")
	require.Error(t, err)
	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindNotFound, apiErr.Kind)
	assert.Contains(t, apiErr.Message, "docs",
		"a caller who mistyped a base name should be told what the deployment does have, not handed a confident empty list")
}

// --- the corpus is never written through the API.

func TestWriteContentRefusesAuthoredAndDerivedAndSaysWhereToGoInstead(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))
	h := p.contentHandler()

	authored := &knowledgev1.Content{Id: "wiki:restore", Origin: knowledgev1.Origin_ORIGIN_AUTHORED, Body: "x"}
	_, err := h.WriteContent(context.Background(), connect.NewRequest(&knowledgev1.WriteContentRequest{
		BaseId: "knowledge", Content: authored,
	}))
	require.Error(t, err)
	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindFailedPrecondition, apiErr.Kind,
		"the request was well formed; the content just does not accept that kind of change")
	assert.Contains(t, apiErr.Message, "run ApplyReconcile",
		"a refusal that says what to do instead is a better answer than a success that is discarded")

	derived := &knowledgev1.Content{Id: "page-1", Origin: knowledgev1.Origin_ORIGIN_DERIVED, Body: "x"}
	_, err = h.WriteContent(context.Background(), connect.NewRequest(&knowledgev1.WriteContentRequest{
		BaseId: "knowledge", Content: derived,
	}))
	require.Error(t, err)
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindFailedPrecondition, apiErr.Kind)
	assert.Contains(t, apiErr.Message, "regenerated")

	_, err = h.WriteContent(context.Background(), connect.NewRequest(&knowledgev1.WriteContentRequest{
		BaseId: "knowledge", Content: &knowledgev1.Content{Id: "x"},
	}))
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Message, "an origin is required")
}

func TestDeleteContentRefusesAuthored(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.contentHandler().DeleteContent(context.Background(), connect.NewRequest(&knowledgev1.DeleteContentRequest{
		BaseId: "knowledge", ContentId: "wiki:restore", Origin: knowledgev1.Origin_ORIGIN_AUTHORED,
	}))
	require.Error(t, err)
	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindFailedPrecondition, apiErr.Kind)
	assert.Contains(t, apiErr.Message, "Delete the file in your repository",
		"removing a corpus file's content from the base removes what a person wrote from the only place the deployment can act on it")
}

// --- the plan and the confirmation.

func TestPlanReconcileReportsFilesAndWritesNothing(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	res, err := p.corpusHandler().PlanReconcile(context.Background(), connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox",
	}))
	require.NoError(t, err)
	plan := res.Msg.GetPlan()
	require.NotNil(t, plan)
	assert.Len(t, plan.GetCreated(), 3, "every file in a fresh corpus is a creation")
	assert.Empty(t, plan.GetDeleted())
	assert.False(t, plan.GetEmpty())
	assert.NotEmpty(t, plan.GetPlanDigest())
	assert.Empty(t, b.sentTo("/memories"),
		"a plan reads the corpus and the base and writes nothing: that is what makes it safe to compute over a directory somebody is editing")

	// The plan names every file with its digest, because a plan that said "changed" without
	// saying what it changed from is asking for trust rather than for consent.
	for _, e := range plan.GetCreated() {
		assert.NotEmpty(t, e.GetId())
		assert.NotEmpty(t, e.GetDigest())
		assert.NotEmpty(t, e.GetTitle())
		assert.Contains(t, e.GetTags(), "toolbox:owned-by=toolbox")
	}
}

func TestPlanReconcileRefusesAnOwnerlessRequest(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.corpusHandler().PlanReconcile(context.Background(), connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner is required",
		"a document ingested without an ownership marker can never be pruned, and that is worth catching at the plan")
}

func TestApplyReconcileRefusesAnUnconfirmedAndAMismatchedPlan(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))
	h := p.corpusHandler()

	planned, err := h.PlanReconcile(context.Background(), connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox",
	}))
	require.NoError(t, err)
	plan := planned.Msg.GetPlan()

	// No confirmation at all.
	_, err = h.ApplyReconcile(context.Background(), connect.NewRequest(&knowledgev1.ApplyReconcileRequest{
		BaseId: "docs", Plan: plan, Owner: "toolbox",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation is required")
	assert.Contains(t, err.Error(), "works on every transport",
		"a value the agent relays and the user answers is the shape that works where the transport cannot ask")

	// A confirmation that is not this plan's digest.
	_, err = h.ApplyReconcile(context.Background(), connect.NewRequest(&knowledgev1.ApplyReconcileRequest{
		BaseId: "docs", Plan: plan, Confirm: "sha256:0000000000000000000000000000000000000000000000000000000000000000", Owner: "toolbox",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the digest of the plan supplied")

	// A confirmation that is right but whose owner does not match, which is the value a caller
	// most often forgets and the one that would silently orphan every document.
	_, err = h.ApplyReconcile(context.Background(), connect.NewRequest(&knowledgev1.ApplyReconcileRequest{
		BaseId: "docs", Plan: plan, Confirm: plan.GetPlanDigest(),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner is required on ApplyReconcile as well")
}

func TestApplyReconcileRefusesAConfirmationForAChangedCorpus(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	dir := corpusFixture(t)
	p := newProvider(t, b, dir)
	h := p.corpusHandler()

	planned, err := h.PlanReconcile(context.Background(), connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox", Commit: "c1",
	}))
	require.NoError(t, err)
	plan := planned.Msg.GetPlan()

	// Somebody edits the corpus between the plan and the apply, which is the ordinary case of a
	// person working while a reconcile is computed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "architecture", "overview.md"),
		[]byte("---\nkind: architecture\n---\n\n# Overview\n\nEdited after the plan.\n"), 0o644))

	_, err = h.ApplyReconcile(context.Background(), connect.NewRequest(&knowledgev1.ApplyReconcileRequest{
		BaseId: "docs", Plan: plan, Confirm: plan.GetPlanDigest(), Owner: "toolbox", Commit: "c1",
	}))
	require.Error(t, err)
	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindFailedPrecondition, apiErr.Kind)
	assert.Contains(t, apiErr.Message, "corpus has changed since the plan was computed",
		"a confirmation applied to a different directory is a decision nobody made")
}

func TestApplyReconcileSendsTheFieldsThatDecideWhetherAnEditSupersedes(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	b.responses["/memories"] = `{"success":true,"bank_id":"docs","items_count":3,"async":true,"operation_ids":["op-1"]}`
	p := newProvider(t, b, corpusFixture(t))
	h := p.corpusHandler()

	planned, err := h.PlanReconcile(context.Background(), connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox", Commit: "c1",
	}))
	require.NoError(t, err)
	plan := planned.Msg.GetPlan()

	res, err := h.ApplyReconcile(context.Background(), connect.NewRequest(&knowledgev1.ApplyReconcileRequest{
		BaseId: "docs", Plan: plan, Confirm: plan.GetPlanDigest(), Owner: "toolbox", Commit: "c1",
	}))
	require.NoError(t, err)
	assert.Equal(t, int32(3), res.Msg.GetIngested())
	assert.Equal(t, "c1", res.Msg.GetRecordedCommit(), "a commit is recorded so the index can be checked against the merge")

	sent := b.sentTo("/memories")
	require.Len(t, sent, 1)
	for _, needle := range []string{
		`"update_mode":"replace"`,
		`"resolve_entities":false`,
		`"timestamp":"unset"`,
		`"operation_id":`,
		`"context":"toolbox-knowledge"`,
	} {
		assert.Contains(t, sent[0].Body, needle,
			"these are the fields whose defaults are the wrong side of what this subsystem wants, and each has to be sent explicitly")
	}
	assert.NotContains(t, sent[0].Body, "kind: procedure",
		"the frontmatter is metadata this package acts on; leaving it in the extracted text would teach the extractor that a person wrote it")
}

func TestTheOwnershipRecordIsWrittenOnlyAfterASuccessfulApply(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	dir := corpusFixture(t)
	p := newProvider(t, b, dir)
	h := p.corpusHandler()

	planned, err := h.PlanReconcile(context.Background(), connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox", Commit: "c1",
	}))
	require.NoError(t, err)
	plan := planned.Msg.GetPlan()

	_, err = h.ApplyReconcile(context.Background(), connect.NewRequest(&knowledgev1.ApplyReconcileRequest{
		BaseId: "docs", Plan: plan, Confirm: plan.GetPlanDigest(), Owner: "toolbox", Commit: "c1",
	}))
	require.NoError(t, err)

	// The record is the thing that authorises a later prune, so its existence is the property
	// worth asserting: a second plan over an unchanged corpus has nothing to do.
	settled, err := h.PlanReconcile(context.Background(), connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox", Commit: "c1", Prune: true,
	}))
	require.NoError(t, err)
	assert.True(t, settled.Msg.GetPlan().GetEmpty(),
		"a second plan over an unchanged corpus and the written record has nothing to do")
	assert.Len(t, settled.Msg.GetPlan().GetUnchanged(), 3)
}

func TestADegradedOwnershipRecordSwitchesPruningOffAndSaysWhy(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	dir := corpusFixture(t)
	p := newProvider(t, b, dir)

	// Corrupt the record the provider will load.
	identity := p.bases.identity(p.client, "docs", defaultNamespace)
	recordPath := p.bases.recordPath(identity)
	require.NoError(t, os.MkdirAll(filepath.Dir(recordPath), 0o755))
	require.NoError(t, os.WriteFile(recordPath, []byte("{ not json"), 0o644))

	res, err := p.corpusHandler().PlanReconcile(context.Background(), connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox", Prune: true,
	}))
	require.NoError(t, err, "a corrupt record must not stop a plan being read")
	plan := res.Msg.GetPlan()
	assert.Empty(t, plan.GetDeleted(), "a record that could not be loaded cannot authorise a deletion")
	joined := strings.Join(plan.GetWarnings(), " ")
	assert.Contains(t, joined, "orphaned documents are NOT pruned",
		"a plan that silently omitted its deletions would read as \"nothing to remove\"")
}

func TestReadCorpusFileReturnsTheBytesAndWhatWasDeclared(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	res, err := p.corpusHandler().ReadCorpusFile(context.Background(), connect.NewRequest(&knowledgev1.ReadCorpusFileRequest{
		BaseId: "docs", Path: "runbooks/restore.md",
	}))
	require.NoError(t, err)
	msg := res.Msg
	assert.Equal(t, "wiki:restore", msg.GetDeclaredId())
	assert.Equal(t, "wiki:restore", msg.GetEffectiveId())
	assert.Contains(t, string(msg.GetRaw()), "# Restore a base", "a person wants the file, not a re-rendering of it")
	assert.Equal(t, knowledgev1.DocKind_DOC_KIND_PROCEDURE, msg.GetMeta().GetKind())
	assert.Equal(t, knowledgev1.DocStatus_DOC_STATUS_ACTIVE, msg.GetMeta().GetStatus())
	assert.Contains(t, msg.GetTags(), "toolbox:owned-by=toolbox")

	_, err = p.corpusHandler().ReadCorpusFile(context.Background(), connect.NewRequest(&knowledgev1.ReadCorpusFileRequest{
		BaseId: "docs", Path: "nowhere.md",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no file at")
}

// --- searching.

func TestSearchRefusesAnEmptyQuery(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.queryHandler().Search(context.Background(), connect.NewRequest(&knowledgev1.SearchRequest{
		BaseId: "docs", Query: "   ",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query is required",
		"a search that returns the whole base is not a search and would look like one")
}

func TestSearchFindsAuthoredContentLocally(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	res, err := p.queryHandler().Search(context.Background(), connect.NewRequest(&knowledgev1.SearchRequest{
		BaseId: "docs", Query: "restore", IncludeBodies: true,
	}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetContent(), 1)
	found := res.Msg.GetContent()[0]
	assert.Equal(t, knowledgev1.Origin_ORIGIN_AUTHORED, found.GetOrigin())
	assert.Contains(t, found.GetBody(), "Restore a base")
	assert.Equal(t, "runbooks/restore.md", found.GetLocation().GetPath())
	assert.Equal(t, knowledgev1.Mutability_MUTABILITY_NONE, found.GetMutability(),
		"the rule for how this may be changed is declared, not left to be discovered by having a write overwritten")
}

func TestAnUnspecifiedOriginInAFilterIsRefused(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.queryHandler().Search(context.Background(), connect.NewRequest(&knowledgev1.SearchRequest{
		BaseId: "docs", Query: "x", Scope: &knowledgev1.SearchScope{Origins: []knowledgev1.Origin{knowledgev1.Origin_ORIGIN_UNSPECIFIED}},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches nothing",
		"an unspecified origin and an absent one look identical in a request and mean opposite things, so it is refused rather than guessed")
}

// --- the mount, without FUSE.

func TestGetMountStatusIsHonestOnABuildWithoutFUSE(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	res, err := p.mountHandler().GetMountStatus(context.Background(), connect.NewRequest(&knowledgev1.GetMountStatusRequest{}))
	require.NoError(t, err)
	if FuseBuilt() {
		assert.True(t, res.Msg.GetFuseAvailable() || res.Msg.GetFuseUnavailableReason() != "")
	} else {
		assert.False(t, res.Msg.GetFuseAvailable())
		assert.Contains(t, res.Msg.GetFuseUnavailableReason(), "fuse",
			"a caller can tell \"not supported in this build\" from \"the host said no\" without attempting it")
		assert.Contains(t, res.Msg.GetFuseUnavailableReason(), fuseBuildNote,
			"the refusal says what to do, because \"unimplemented\" on its own sends an operator looking for a bug")
	}
}

func TestEnableMountRefusesAMissingOrUnusableMountpoint(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))
	h := p.mountHandler()

	_, err := h.EnableMount(context.Background(), connect.NewRequest(&knowledgev1.EnableMountRequest{
		Spec: &knowledgev1.MountSpec{BaseId: "docs"},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mountpoint is required",
		"a command that invents a directory is a directory somebody has to find and clean up afterwards")

	_, err = h.EnableMount(context.Background(), connect.NewRequest(&knowledgev1.EnableMountRequest{
		Spec: &knowledgev1.MountSpec{BaseId: "docs", Mountpoint: "relative/path"},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute path")

	_, err = h.EnableMount(context.Background(), connect.NewRequest(&knowledgev1.EnableMountRequest{
		Spec: &knowledgev1.MountSpec{BaseId: "docs", Mountpoint: filepath.Join(t.TempDir(), "absent")},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist; create it first")

	nonEmpty := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(nonEmpty, "something"), []byte("x"), 0o644))
	_, err = h.EnableMount(context.Background(), connect.NewRequest(&knowledgev1.EnableMountRequest{
		Spec: &knowledgev1.MountSpec{BaseId: "docs", Mountpoint: nonEmpty},
	}))
	require.Error(t, err)
	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindFailedPrecondition, apiErr.Kind)
	assert.Contains(t, apiErr.Message, "not empty",
		"mounting over somebody's files would hide them, and this surface will not do that")
}

func TestAMountIsReadOnlyAndReportsTheStalenessBound(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	res, err := p.mountHandler().GetMountStatus(context.Background(), connect.NewRequest(&knowledgev1.GetMountStatusRequest{}))
	require.NoError(t, err)
	if !FuseBuilt() {
		// The capabilities are what a reader is told about the notification asymmetry, and
		// they are worth asserting on either build: a caller that cannot mount still needs
		// to know what it would have got.
		assert.NotEmpty(t, res.Msg.GetStalenessBound().AsDuration(),
			"the bound is a number rather than \"eventually\"")
		return
	}
	assert.NotEmpty(t, res.Msg.GetNotificationCapabilities())
	assert.Contains(t, strings.Join(res.Msg.GetNotificationCapabilities(), " "), "no inotify event",
		"a content change invalidates the cache but emits nothing, and a reader has to be told that rather than discover it")
}

func TestDisableMountReportsAnUnknownMount(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.mountHandler().DisableMount(context.Background(), connect.NewRequest(&knowledgev1.DisableMountRequest{
		MountId: "nope",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no mount is called")
}

// --- configuration.

func TestAnEmptyConfigurationUpdateIsRefusedRatherThanInterpreted(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.baseHandler().UpdateBaseConfig(context.Background(), connect.NewRequest(&knowledgev1.UpdateBaseConfigRequest{
		Ref: &knowledgev1.BaseRef{Name: "docs"},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous between them",
		"an absent configuration and an empty one mean different things and a caller must not have to guess which was meant")
}

func TestUpdateBaseRefusesToChangeTheCorpusRootsOverRPC(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.baseHandler().UpdateBase(context.Background(), connect.NewRequest(&knowledgev1.UpdateBaseRequest{
		Ref:         &knowledgev1.BaseRef{Name: "docs"},
		CorpusRoots: []*knowledgev1.CorpusRootSpec{{Name: "other", Path: "/elsewhere"}},
	}))
	require.Error(t, err)
	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, api.KindFailedPrecondition, apiErr.Kind)
	assert.Contains(t, apiErr.Message, "bound into the ownership record",
		"a root that changed without the record changing is a record that would authorise deleting the wrong documents")
}

func TestIngestionSeriesRefusesAnUnrecognisedGranularity(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.baseHandler().GetBaseIngestionSeries(context.Background(), connect.NewRequest(&knowledgev1.GetBaseIngestionSeriesRequest{
		Ref: &knowledgev1.BaseRef{Name: "docs"}, Granularity: "fortnight", Buckets: 5,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not one of hour, day, week or month",
		"forwarding an unrecognised value would produce a series at an unexpected resolution with nothing to say so")
}

func TestIngestionSeriesRefusesAnUnboundedRequest(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	p := newProvider(t, b, corpusFixture(t))

	_, err := p.baseHandler().GetBaseIngestionSeries(context.Background(), connect.NewRequest(&knowledgev1.GetBaseIngestionSeriesRequest{
		Ref: &knowledgev1.BaseRef{Name: "docs"}, Granularity: "day",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rather than defaulted")
}

func TestServiceNamesMatchWhatIsRegistered(t *testing.T) {
	t.Parallel()

	names := ServiceNames()
	require.Len(t, names, 5)
	seen := map[string]bool{}
	for _, n := range names {
		assert.True(t, strings.HasPrefix(n, "toolbox.knowledge.v1."), "unexpected service name %q", n)
		assert.False(t, seen[n], "a service listed twice would be registered twice")
		seen[n] = true
	}
}
