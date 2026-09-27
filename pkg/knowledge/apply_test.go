package knowledge_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// recordingEngine captures what an apply sent, so that the properties that matter
// can be asserted without a backend.
type recordingEngine struct {
	mu sync.Mutex

	retains  []knowledge.RetainRequest
	binaries []knowledge.BinaryRetainRequest
	deleted  []string

	// seenKeys records every idempotency key, so a test can assert that re-applying
	// a plan re-sends the same key.
	seenKeys map[string]int

	// duplicateKeys makes the engine behave as a backend would for a key it has
	// already accepted: no new work, and a Duplicate result.
	duplicateKeys bool

	retainErr error
	deleteErr error
}

func newEngine() *recordingEngine {
	return &recordingEngine{seenKeys: map[string]int{}}
}

func (e *recordingEngine) Retain(_ context.Context, req knowledge.RetainRequest) (knowledge.RetainResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.retainErr != nil {
		return knowledge.RetainResult{}, e.retainErr
	}
	e.retains = append(e.retains, req)
	e.seenKeys[req.IdempotencyKey]++
	if e.duplicateKeys && e.seenKeys[req.IdempotencyKey] > 1 {
		return knowledge.RetainResult{Accepted: len(req.Items), Duplicate: true}, nil
	}
	return knowledge.RetainResult{
		Accepted:     len(req.Items),
		OperationIDs: []string{"op-" + req.IdempotencyKey[:8]},
	}, nil
}

func (e *recordingEngine) RetainBinary(_ context.Context, req knowledge.BinaryRetainRequest) (knowledge.RetainResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.retainErr != nil {
		return knowledge.RetainResult{}, e.retainErr
	}
	e.binaries = append(e.binaries, req)
	return knowledge.RetainResult{Accepted: len(req.Items)}, nil
}

func (e *recordingEngine) DeleteContent(_ context.Context, _, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.deleteErr != nil {
		return e.deleteErr
	}
	e.deleted = append(e.deleted, id)
	return nil
}

func (e *recordingEngine) keys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.retains)+len(e.binaries))
	for _, r := range e.retains {
		out = append(out, r.IdempotencyKey)
	}
	// A batch carrying an attachment goes out through `RetainBinary`, so reading only the
	// text requests would drop half of what was sent — and a test asserting on a batch's
	// identity would then assert on a batch that never existed.
	for _, r := range e.binaries {
		out = append(out, r.IdempotencyKey)
	}
	return out
}

func fixedClock() func() time.Time {
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

func applyOptions(t *testing.T) knowledge.ApplyOptions {
	t.Helper()
	return knowledge.ApplyOptions{
		BaseID:   "test",
		Commit:   "c1",
		Owner:    "toolbox",
		Identity: identity(),
		Async:    true,
		Now:      fixedClock(),
	}
}

func TestApplySendsAuthoredContentWithTheTwoFieldsTheShippedClientLeavesAlone(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{
		"runbook.md": "---\nid: wiki:runbook\ntitle: Runbook\nkind: procedure\nstatus: active\n---\n\n# Runbook\n\nStep one.\nStep two.\n",
	})
	corpus := mustCorpus(t, dir)
	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), emptyRecord(t),
		knowledge.PlanOptions{BaseID: "test", Commit: "c1", Owner: "toolbox"})
	require.NoError(t, err)
	require.Len(t, plan.Created, 1)

	engine := newEngine()
	res, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, emptyRecord(t), applyOptions(t))
	require.NoError(t, err)
	require.Len(t, engine.retains, 1)

	req := engine.retains[0]
	assert.Equal(t, "test", req.BaseID)
	assert.True(t, req.Async, "a corpus is thousands of extractions and cannot be a synchronous call")
	assert.NotEmpty(t, req.IdempotencyKey, "a retry after a lost acknowledgement must not enqueue a duplicate")
	require.Len(t, req.Items, 1)

	item := req.Items[0]
	assert.Equal(t, "wiki:runbook", item.ID)
	assert.Equal(t, knowledge.UpdateModeReplace, item.UpdateMode,
		"an edited runbook must stop being retrievable by the sentence it used to contain, and the schema carries no default so it must be sent")
	assert.False(t, item.ResolveEntities,
		"the backend's default is on, and on resolves a name close to one already in the base to that one — so an authored statement gets attributed to something the author never mentioned")
	assert.Equal(t, knowledge.TimelessTimestamp(), item.EventTime,
		"asserting something happened when a person last pressed save is a claim about the file, not about the event")
	assert.Equal(t, "# Runbook\n\nStep one.\nStep two.\n", item.Content,
		"the body is what a person wrote; frontmatter is metadata, and leaving it in would teach the extractor that somebody wrote \"kind: procedure\"")
	assert.Equal(t, "runbook.md", item.Metadata["path"],
		"a citation needs a field to point at and a filter does not")
	assert.Equal(t, "c1", item.Metadata["commit"], "a commit is recorded so the index can be checked against the merge")
	assert.NotEmpty(t, item.Context)

	assert.Contains(t, item.Tags, "toolbox:origin=authored")
	assert.Contains(t, item.Tags, "vault:docs")
	assert.Contains(t, item.Tags, "toolbox:kind=procedure")
	assert.Contains(t, item.Tags, "toolbox:status=active")
	assert.Contains(t, item.Tags, "toolbox:owned-by=toolbox")

	assert.Equal(t, 1, res.Ingested)
	assert.True(t, res.Committed)
}

func TestApplyUsesADeclaredEventTimeWhenTheAuthorGaveOne(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{
		"adr.md": "---\nid: wiki:adr\ndate: 2026-01-15\n---\n\n# ADR\n\nWe chose X.\n",
	})
	corpus := mustCorpus(t, dir)
	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), emptyRecord(t), knowledge.PlanOptions{BaseID: "test", Owner: "toolbox"})
	require.NoError(t, err)

	engine := newEngine()
	_, err = knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, emptyRecord(t), applyOptions(t))
	require.NoError(t, err)
	require.Len(t, engine.retains[0].Items, 1)

	item := engine.retains[0].Items[0]
	assert.Equal(t, "2026-01-15T00:00:00Z", item.EventTime)
	assert.Contains(t, item.Tags, "created:2026")
	assert.Contains(t, item.Tags, "created:2026-01",
		"the backend has no date-range filter, so a filterable date has to be a tag")
}

func TestApplyIsIdempotentForTheSamePlan(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# A\n", "b.md": "# B\n", "c.md": "# C\n"})
	corpus := mustCorpus(t, dir)
	opts := knowledge.PlanOptions{BaseID: "test", Commit: "c1", Owner: "toolbox"}
	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), emptyRecord(t), opts)
	require.NoError(t, err)

	engine := newEngine()
	applier := knowledge.NewApplier(engine, corpus)
	applyOpts := applyOptions(t)
	applyOpts.BatchSize = 2

	first, err := applier.Apply(t.Context(), plan, emptyRecord(t), applyOpts)
	require.NoError(t, err)
	assert.Equal(t, 3, first.Ingested)
	require.Len(t, engine.retains, 2, "a batch is bounded, because one enormous request is one enormous failure")

	// Batches are composed from a sorted list, so each key always covers the same
	// content; they complete concurrently, so the order they arrive in is not
	// fixed and is not part of the property.
	keys := engine.keys()
	sort.Strings(keys)
	require.Len(t, keys, 2)
	assert.NotEqual(t, keys[0], keys[1], "different batches are different operations")

	// Re-applying the same plan over the same commit sends the same keys, which
	// is what makes a retry safe rather than duplicating work.
	engine.duplicateKeys = true
	second, err := applier.Apply(t.Context(), plan, first.Ownership, applyOpts)
	require.NoError(t, err)
	retryKeys := engine.keys()[2:]
	sort.Strings(retryKeys)
	assert.Equal(t, keys, retryKeys,
		"the key is derived from what the batch contains, so a retry reproduces it")
	require.NotEmpty(t, second.Warnings)
	assert.Contains(t, second.Warnings[0], "idempotency key")
	assert.Contains(t, second.Warnings[0], "created no new work")

	// And the end-to-end property: applying what the record now describes changes
	// nothing, so a reconcile that runs twice in a row is not a reconcile that
	// keeps re-extracting the corpus.
	settled, err := knowledge.NewPlanner(corpus).Plan(t.Context(), second.Ownership, opts)
	require.NoError(t, err)
	assert.True(t, settled.Empty(),
		"a second plan over an unchanged corpus and the returned record has nothing to do")
}

func TestAFailedBatchIsNotRecordedAndDoesNotStopTheRest(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{
		"a.md": "# A\n", "b.md": "# B\n", "c.md": "# C\n", "d.md": "# D\n",
	})
	corpus := mustCorpus(t, dir)
	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), emptyRecord(t), knowledge.PlanOptions{BaseID: "test", Owner: "toolbox"})
	require.NoError(t, err)

	engine := newEngine()
	engine.retainErr = assert.AnError
	applyOpts := applyOptions(t)
	applyOpts.BatchSize = 2

	res, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, emptyRecord(t), applyOpts)
	require.NoError(t, err)
	assert.Equal(t, 4, res.Failed)
	assert.Empty(t, res.Ownership.Files,
		"recording a failed batch would claim ownership of content the base does not have, and the next reconcile would treat it as done")
	assert.Len(t, res.Warnings, 2)
}

func TestAnEntryWithNoBodyIsReadAtApplyTimeAndRefusedIfItNoLongerMatches(t *testing.T) {
	t.Parallel()

	// An entry the plan did not read — because the cheap filter skipped it, or
	// because a caller handed the applier a hand-built plan — has a digest but no
	// bytes. The apply reads them, and reading is the moment the digest is free to
	// check, so a file edited in between is refused rather than ingested stale.
	dir := writeCorpus(t, map[string]string{"a.md": "# Original\n"})
	corpus := mustCorpus(t, dir)

	plan := knowledge.Plan{
		BaseID: "test",
		Updated: []knowledge.PlanEntry{{
			ID:        "docs/a.md",
			SourceKey: "docs:a.md",
			Root:      "docs",
			Path:      "a.md",
			Digest:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Read:      false,
			EventTime: knowledge.TimelessTimestamp(),
			Tags:      []string{"toolbox:owned-by=toolbox"},
		}},
	}
	rec := emptyRecord(t)

	engine := newEngine()
	res, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, rec, applyOptions(t))
	require.NoError(t, err)
	assert.Equal(t, 1, res.Failed)
	assert.Empty(t, engine.retains, "a digest that no longer matches the file must not be ingested as if it had")
	assert.Empty(t, res.Ownership.Files, "a failed batch leaves its documents out of the record, so the next reconcile retries them")
	require.NotEmpty(t, res.Warnings)
	assert.Contains(t, res.Warnings[0], "re-run the plan")
}

func TestAnEntryWithNoBodyIsReadAtApplyTimeWhenItDoesMatch(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "---\nid: wiki:a\n---\n\n# Original\n\nBody.\n"})
	corpus := mustCorpus(t, dir)

	raw, err := os.ReadFile(filepath.Join(dir, "a.md"))
	require.NoError(t, err)

	plan := knowledge.Plan{
		BaseID: "test",
		Created: []knowledge.PlanEntry{{
			ID: "wiki:a", SourceKey: "docs:a.md", Root: "docs", Path: "a.md",
			Digest: knowledge.Digest(raw), Read: false, EventTime: knowledge.TimelessTimestamp(),
			Tags: []string{"toolbox:owned-by=toolbox"},
		}},
	}
	engine := newEngine()
	res, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, emptyRecord(t), applyOptions(t))
	require.NoError(t, err)

	assert.Equal(t, 0, res.Failed)
	require.Len(t, engine.retains, 1)
	require.Len(t, engine.retains[0].Items, 1)
	assert.Equal(t, "# Original\n\nBody.\n", engine.retains[0].Items[0].Content,
		"the frontmatter is stripped here too, not only when the plan read the file")
	assert.Contains(t, res.Ownership.Files, "docs:a.md")
}

func TestAnApplyIsRefusedWhenThePlanLacksTheOwnershipMarker(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# A\n"})
	corpus := mustCorpus(t, dir)
	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), emptyRecord(t), knowledge.PlanOptions{BaseID: "test"})
	require.NoError(t, err)
	require.Len(t, plan.Created, 1)

	// The plan was built with no owner and the apply declares one: content would
	// go in with no marker on it, and the next prune could never remove it.
	_, err = knowledge.NewApplier(newEngine(), corpus).Apply(t.Context(), plan, emptyRecord(t), applyOptions(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ownership marker")
	assert.Contains(t, err.Error(), "pass the same owner to PlanReconcile and ApplyReconcile",
		"a silent leak of exactly the kind the ownership record exists to prevent has to be a loud failure")
}

func TestADeletionIsAuthorisedByTheRecordNotByATag(t *testing.T) {
	t.Parallel()

	// A deleted entry carries whatever tags the record held, including none. The
	// record already says this reconciler ingested it, and the record is what the
	// prune walks, so demanding a tag as well would be a second authority for one
	// fact — and would refuse a legitimate prune of a file written before the
	// marker existed.
	dir := writeCorpus(t, map[string]string{"kept.md": "# Kept\n"})
	corpus := mustCorpus(t, dir)
	rec := emptyRecord(t)
	rec.Files["docs:legacy.md"] = knowledge.OwnershipEntry{ID: "docs:legacy.md", Root: "docs", Path: "legacy.md", Digest: "sha256:x"}

	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Prune: true, Owner: "toolbox"})
	require.NoError(t, err)
	require.Len(t, plan.Deleted, 1)
	assert.Empty(t, plan.Deleted[0].Tags)

	engine := newEngine()
	res, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, rec, applyOptions(t))
	require.NoError(t, err)
	assert.Equal(t, []string{"docs:legacy.md"}, engine.deleted)
	assert.Equal(t, 1, res.Pruned)
}

func TestAFileEditedBetweenPlanAndApplyIsCaughtByTheNextReconcile(t *testing.T) {
	t.Parallel()

	// A plan that read the file sends the bytes it holds without re-reading. That
	// is a deliberate trade: re-reading every file doubles the work of every
	// apply, and a change since the plan is caught immediately afterwards because
	// the record will hold a digest the file no longer has.
	dir := writeCorpus(t, map[string]string{"a.md": "# A\n"})
	corpus := mustCorpus(t, dir)
	rec := emptyRecord(t)
	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Owner: "toolbox"})
	require.NoError(t, err)
	require.Len(t, plan.Created, 1)
	require.True(t, plan.Created[0].Read)

	first, err := knowledge.NewApplier(newEngine(), corpus).Apply(t.Context(), plan, rec, applyOptions(t))
	require.NoError(t, err)

	require.NoError(t, writeFileString(filepath.Join(dir, "a.md"), "# A, edited while you were planning\n"))

	second, err := knowledge.NewPlanner(corpus).Plan(t.Context(), first.Ownership, knowledge.PlanOptions{BaseID: "test", Owner: "toolbox"})
	require.NoError(t, err)
	assert.Len(t, second.Updated, 1, "the edit is not lost; the next plan reports it")
	assert.Equal(t, knowledge.Digest([]byte("# A, edited while you were planning\n")), second.Updated[0].Digest)
}

func TestPruneOnlyRemovesWhatTheRecordOwns(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"kept.md": "# Kept\n"})
	corpus := mustCorpus(t, dir)

	rec := emptyRecord(t)
	rec.Files["docs:gone.md"] = knowledge.OwnershipEntry{ID: "docs:gone.md", Root: "docs", Path: "gone.md", Digest: "sha256:x"}

	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Prune: true, Owner: "toolbox"})
	require.NoError(t, err)

	engine := newEngine()
	res, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, rec, applyOptions(t))
	require.NoError(t, err)

	assert.Equal(t, []string{"docs:gone.md"}, engine.deleted,
		"every identifier pruned came from the record; a document this reconciler did not create cannot appear in it")
	assert.Equal(t, 1, res.Pruned)
	assert.Contains(t, res.Ownership.Files, "docs:kept.md")
	assert.NotContains(t, res.Ownership.Files, "docs:gone.md")
}

func TestPruneIsRefusedWhenTheRecordIsDegraded(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"kept.md": "# Kept\n"})
	corpus := mustCorpus(t, dir)

	rec := emptyRecord(t)
	rec.Degraded = "the ownership record could not be parsed, so orphans are not pruned"
	rec.Files["docs:gone.md"] = knowledge.OwnershipEntry{ID: "docs:gone.md", Root: "docs", Path: "gone.md"}

	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Prune: true, Owner: "toolbox"})
	require.NoError(t, err)

	engine := newEngine()
	res, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, rec, applyOptions(t))
	require.NoError(t, err)
	assert.Empty(t, engine.deleted, "claiming a working record would let a prune delete content it has no record of having created")
	require.NotEmpty(t, res.Warnings)
	assert.Contains(t, res.Warnings[0], "nothing was pruned")
}

func TestAFailedDeleteKeepsTheRecordEntrySoTheNextRunRetries(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"kept.md": "# Kept\n"})
	corpus := mustCorpus(t, dir)
	rec := emptyRecord(t)
	rec.Files["docs:gone.md"] = knowledge.OwnershipEntry{ID: "docs:gone.md", Root: "docs", Path: "gone.md", Digest: "sha256:x"}

	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Prune: true, Owner: "toolbox"})
	require.NoError(t, err)

	engine := newEngine()
	engine.deleteErr = assert.AnError
	res, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, rec, applyOptions(t))
	require.NoError(t, err)
	assert.Equal(t, 1, res.Failed)
	assert.Contains(t, res.Ownership.Files, "docs:gone.md",
		"forgetting a document this reconciler still owns is how content is orphaned for good")
}

func TestADeclaredIdMoveIngestsOnceAndPrunesNothing(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"old/place.md": "---\nid: wiki:runbook\n---\n\n# Runbook\n\nSteps.\n"})
	corpus := mustCorpus(t, dir)
	planner := knowledge.NewPlanner(corpus)

	first, err := planner.Plan(t.Context(), emptyRecord(t), knowledge.PlanOptions{BaseID: "test", Commit: "c1", Owner: "toolbox"})
	require.NoError(t, err)
	engine := newEngine()
	applier := knowledge.NewApplier(engine, corpus)
	prior, err := applier.Apply(t.Context(), first, emptyRecord(t), applyOptions(t))
	require.NoError(t, err)

	require.NoError(t, moveFile(filepath.Join(dir, "old/place.md"), filepath.Join(dir, "new/place.md")))

	second, err := planner.Plan(t.Context(), prior.Ownership, knowledge.PlanOptions{BaseID: "test", Commit: "c2", Prune: true, Owner: "toolbox"})
	require.NoError(t, err)
	require.Len(t, second.Moved, 1)

	engine2 := newEngine()
	res, err := knowledge.NewApplier(engine2, corpus).Apply(t.Context(), second, prior.Ownership, applyOptions(t))
	require.NoError(t, err)

	require.Len(t, engine2.retains, 1)
	require.Len(t, engine2.retains[0].Items, 1)
	assert.Equal(t, "wiki:runbook", engine2.retains[0].Items[0].ID,
		"re-ingesting under the same identifier is what preserves the facts already extracted from the file")
	assert.Empty(t, engine2.deleted, "the identifier did not change, so there is nothing to remove")
	assert.Equal(t, 1, res.Ingested+res.Replaced)
}

func TestAnIdentityRenamePrunesTheOldIdentifier(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"doc.md": "---\nid: wiki:before\n---\n\n# Doc\n\nBody.\n"})
	corpus := mustCorpus(t, dir)
	planner := knowledge.NewPlanner(corpus)

	first, err := planner.Plan(t.Context(), emptyRecord(t), knowledge.PlanOptions{BaseID: "test", Commit: "c1", Owner: "toolbox"})
	require.NoError(t, err)
	prior, err := knowledge.NewApplier(newEngine(), corpus).Apply(t.Context(), first, emptyRecord(t), applyOptions(t))
	require.NoError(t, err)

	require.NoError(t, writeFileString(filepath.Join(dir, "doc.md"), "---\nid: wiki:after\n---\n\n# Doc\n\nBody.\n"))

	second, err := planner.Plan(t.Context(), prior.Ownership, knowledge.PlanOptions{BaseID: "test", Commit: "c2", Prune: true, Owner: "toolbox"})
	require.NoError(t, err)
	require.Len(t, second.Moved, 1)

	engine := newEngine()
	_, err = knowledge.NewApplier(engine, corpus).Apply(t.Context(), second, prior.Ownership, applyOptions(t))
	require.NoError(t, err)

	require.Len(t, engine.retains[0].Items, 1)
	assert.Equal(t, "wiki:after", engine.retains[0].Items[0].ID)
	assert.Equal(t, []string{"wiki:before"}, engine.deleted,
		"the old identifier is ours — the record holds it — so it is removed, and removed because the record says so rather than because anything listed the base")
}

func TestDryRunSendsNothing(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# A\n"})
	corpus := mustCorpus(t, dir)
	rec := emptyRecord(t)
	rec.Files["docs:gone.md"] = knowledge.OwnershipEntry{ID: "docs:gone.md", Root: "docs", Path: "gone.md"}

	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Prune: true, Owner: "toolbox"})
	require.NoError(t, err)
	require.Len(t, plan.Created, 1)

	engine := newEngine()
	opts := applyOptions(t)
	opts.DryRun = true
	res, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, rec, opts)
	require.NoError(t, err)

	assert.Empty(t, engine.retains)
	assert.Empty(t, engine.deleted)
	assert.False(t, res.Committed)
	assert.Equal(t, 1, res.Ingested, "a dry run reports what it would do")
}

func TestApplyRecordsTheCommitAndTheReconciledTime(t *testing.T) {
	t.Parallel()

	dir := writeCorpus(t, map[string]string{"a.md": "# A\n"})
	corpus := mustCorpus(t, dir)
	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), emptyRecord(t), knowledge.PlanOptions{BaseID: "test", Owner: "toolbox"})
	require.NoError(t, err)

	opts := applyOptions(t)
	opts.Commit = "deadbeef"
	res, err := knowledge.NewApplier(newEngine(), corpus).Apply(t.Context(), plan, emptyRecord(t), opts)
	require.NoError(t, err)

	assert.Equal(t, "deadbeef", res.Ownership.LastCommit)
	assert.Equal(t, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), res.Ownership.LastReconciledAt,
		"the clock is injected so that a reconcile's output is reproducible and a test does not assert on the machine it ran on")
	entry := res.Ownership.Files["docs:a.md"]
	assert.Equal(t, "deadbeef", entry.SourceCommit)
	assert.Equal(t, "docs:a.md", entry.SourceKeyOrPath())
}

func TestApplyCarriesForwardFilesThisPlanDidNotCover(t *testing.T) {
	t.Parallel()

	// Two roots reconciled into one base, reconciled separately. The second
	// reconcile's plan covers only its own root, and must not disown the first's
	// documents.
	dirA := writeCorpus(t, map[string]string{"a.md": "# A\n"})
	dirB := writeCorpus(t, map[string]string{"b.md": "# B\n"})

	rec := emptyRecord(t)
	rec.Files["alpha:a.md"] = knowledge.OwnershipEntry{ID: "alpha/a.md", Root: "alpha", Path: "a.md", Digest: "sha256:a"}

	corpusB, err := knowledge.NewCorpus([]knowledge.CorpusRoot{{Path: dirB, Name: "beta"}}, knowledge.CorpusOptions{})
	require.NoError(t, err)
	plan, err := knowledge.NewPlanner(corpusB).Plan(t.Context(), rec, knowledge.PlanOptions{BaseID: "test", Namespace: "beta", Owner: "toolbox"})
	require.NoError(t, err)
	require.Len(t, plan.Created, 1)
	assert.Empty(t, plan.Deleted, "a plan over one root must not delete another root's documentation")

	res, err := knowledge.NewApplier(newEngine(), corpusB).Apply(t.Context(), plan, rec, applyOptions(t))
	require.NoError(t, err)
	assert.Contains(t, res.Ownership.Files, "alpha:a.md",
		"reconciling a second directory must not delete the first one's documentation")
	assert.Contains(t, res.Ownership.Files, "beta:b.md")
	_ = dirA
}

// The key covers the batch that was *sent*, not the chunk the batch started as.
//
// An entry dropped for being unreadable is not in the batch, and a key that still covered it
// named work the backend never received. The failure is quiet in both directions: a later run that
// read the file successfully looks like new work under a key the backend has already used, and two
// runs whose batches differed only in an entry that failed to read claim the same key for different
// payloads — so the backend skips the difference and the corpus and the base disagree with nothing
// reporting it.
func TestTheBatchKeyCoversWhatWasSentAndNotWhatWasAttempted(t *testing.T) {
	t.Parallel()

	// A `.md` file whose contents are binary is the shape the binary path exists for: the walk
	// takes markdown by extension and then classifies the content, so a file named as prose that
	// is not prose would either be mangled by the extractor or rejected by it. It is sent as
	// bytes beside its chunk instead.
	dir := writeCorpus(t, map[string]string{"a.md": "# A\n", "export.md": "PNG\x00\r\n\x1a\n"})
	corpus := mustCorpus(t, dir)

	// Following binaries is off by default, because an attachment is ingested beside its chunk
	// rather than extracted from and a corpus of a thousand documents may hold a few thousand
	// images. This test needs one, so it asks — which is the only way to reach the binary path
	// at all, and that alone is worth a test existing for.
	plan, err := knowledge.NewPlanner(corpus).Plan(t.Context(), emptyRecord(t), knowledge.PlanOptions{
		BaseID: "test", Commit: "c1", Owner: "toolbox", FollowBinary: true,
	})
	require.NoError(t, err)
	applyOpts := applyOptions(t)
	applyOpts.BatchSize = 10

	// A batch carrying an attachment goes out whole through the binary path, so both files
	// share one key and losing one of them is visible in it.
	engine := newEngine()
	first, err := knowledge.NewApplier(engine, corpus).Apply(t.Context(), plan, emptyRecord(t), applyOpts)
	require.NoError(t, err)
	require.Equal(t, 2, first.Ingested)
	require.NotEmpty(t, engine.binaries, "the batch went out as bytes, not as prose")
	firstKey := onlyKey(t, engine)
	require.Len(t, plan.ChangedFiles(), 2)

	// The file is gone between the plan and the apply. It is dropped, the batch goes out with
	// what is left of it, and the key covers what is left.
	require.NoError(t, os.Remove(filepath.Join(dir, "export.md")))
	engine2 := newEngine()
	second, err := knowledge.NewApplier(engine2, corpus).Apply(t.Context(), plan, emptyRecord(t), applyOpts)
	require.NoError(t, err)
	assert.Equal(t, 1, second.Ingested, "the readable file is still ingested")
	assert.Equal(t, 1, second.Failed)
	secondKey := onlyKey(t, engine2)

	assert.NotEqual(t, firstKey, secondKey,
		"a batch that lost an entry is a different batch; a key that did not change would have the backend skip a payload it has never seen")

	// The dropped entry is gone from the record, so the next reconcile plans it again rather
	// than believing a corpus it never read.
	_, stillThere := second.Ownership.Files[knowledge.SourceKey("docs", "export.md")]
	assert.False(t, stillThere, "a file this apply could not read is not recorded, so the next plan retries it")

	// A file whose content changed after the plan was computed is refused rather than sent,
	// and that is stronger than a new key would be. The plan is what a person confirmed; a
	// digest that no longer matches the file means the confirmation was for different content,
	// so the answer is to re-plan rather than to ingest something nobody saw.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export.md"), []byte("PNG-changed\x00\r\n\x1a\n"), 0o644))
	engine3 := newEngine()
	third, err := knowledge.NewApplier(engine3, corpus).Apply(t.Context(), plan, emptyRecord(t), applyOpts)
	require.NoError(t, err)
	assert.Equal(t, 1, third.Ingested, "the unchanged file is still ingested")
	assert.Equal(t, 1, third.Failed)
	require.NotEmpty(t, third.Warnings)
	assert.Contains(t, strings.Join(third.Warnings, " "), "changed since the plan was computed")
	assert.Equal(t, secondKey, onlyKey(t, engine3),
		"the batch that did not change keeps its key, so the backend recognises work it already did")

	// A plan recomputed over the changed file produces a different key for the same set,
	// because the digest moved. This is the half the refusal depends on: if the key ignored
	// digests, a re-plan would claim work already done and the change would never land.
	replanned, err := knowledge.NewPlanner(corpus).Plan(t.Context(), second.Ownership, knowledge.PlanOptions{
		BaseID: "test", Commit: "c1", Owner: "toolbox", FollowBinary: true,
	})
	require.NoError(t, err)
	require.Len(t, replanned.ChangedFiles(), 1, "only the changed file is still outstanding")
	engine4 := newEngine()
	_, err = knowledge.NewApplier(engine4, corpus).Apply(t.Context(), replanned, second.Ownership, applyOpts)
	require.NoError(t, err)
	assert.NotEqual(t, secondKey, onlyKey(t, engine4),
		"a re-plan over changed content is new work and needs a new key, or the backend would skip the change")
}

// onlyKey is the one key a run sent, and it fails rather than guessing when there is not exactly
// one. A helper that returned the first of several would let a test assert on a batch that was not
// the one under discussion.
func onlyKey(t *testing.T, e *recordingEngine) string {
	t.Helper()
	keys := e.keys()
	require.Len(t, keys, 1, "this test is about one batch")
	return keys[0]
}
