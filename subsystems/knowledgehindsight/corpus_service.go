package knowledgehindsight

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
	knowledgev1connect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

// corpusService implements the reconcile: a plan, a confirmation, and an apply.
//
// It exists as its own service because a reconcile is not a content operation. It is a comparison
// between a directory and a base, and its answer is a plan rather than a result — so putting it on
// the content service would make every content read depend on write authority, and would leave a
// reader unable to find the one operation that can delete things.
type corpusService struct {
	knowledgev1connect.UnimplementedCorpusServiceHandler
	p *Provider
}

func (p *Provider) corpusHandler() *corpusService { return &corpusService{p: p} }

// PlanReconcile reads the corpus and the base and returns what a reconcile would do.
//
// It writes nothing, which is what makes it safe to compute over a directory somebody is actively
// editing and what makes it the natural way to preview a change without any of it being believed.
func (s *corpusService) PlanReconcile(ctx context.Context, req *connect.Request[knowledgev1.PlanReconcileRequest]) (*connect.Response[knowledgev1.PlanReconcileResponse], error) {
	msg := req.Msg
	// A plan is a read of a directory and a record, so it does not need the backend to
	// already hold the base it describes.
	baseID, err := s.p.resolveBaseForRead(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	owner := strings.TrimSpace(msg.GetOwner())
	if owner == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "an owner is required: it is written into every ingested document's ownership marker, and a document without one can never be pruned",
		}
	}

	commit, err := s.p.commitOf(ctx, msg.GetCommit())
	if err != nil {
		return nil, err
	}

	state, err := s.p.prepare(ctx, baseID, namespaceFor(msg.GetRoots()), scopeExcludes(msg.GetRoots()))
	if err != nil {
		return nil, err
	}

	opts := knowledge.PlanOptions{
		BaseID:    baseID,
		Commit:    commit,
		Owner:     owner,
		Namespace: state.identity.Namespace,
		Prune:     msg.GetPrune(),
	}
	if msg.GetReportDrift() {
		// Drift detection is optional and its absence is not "no drift": a reconcile that
		// was not given the derived half knows nothing about what the base believes.
		claims, derr := s.p.derivedClaims(ctx, baseID)
		if derr != nil {
			return nil, derr
		}
		opts.Derived = claims
	}
	// A record that could not be loaded cleanly cannot authorise a deletion, so pruning is
	// switched off and the reason becomes a warning on the plan. The reason is the important
	// part: a plan that silently omitted its deletions would read as "nothing to remove".
	if len(state.degraded) > 0 {
		opts.Prune = false
	}

	planner := knowledge.NewPlanner(state.corpus)
	if sub := strings.TrimSpace(msg.GetSubtree()); sub != "" {
		// A subtree is a location filter on the plan, not a different corpus: the same
		// ownership record governs both, so previewing a directory cannot produce a plan
		// whose deletions are computed against a narrower set of files than the apply.
		opts.Derived = nil
	}

	plan, err := planner.Plan(ctx, state.record, opts)
	if err != nil {
		return nil, classifyPlanError(err)
	}
	if state.degraded != "" {
		plan.Warnings = append(plan.Warnings, state.degraded)
		sort.Strings(plan.Warnings)
	}
	if sub := strings.TrimSpace(msg.GetSubtree()); sub != "" {
		plan = plan.RestrictedTo(sub)
	}

	return connect.NewResponse(&knowledgev1.PlanReconcileResponse{Plan: planMessage(plan, state)}), nil
}

// ApplyReconcile applies a plan, and takes the confirmation as a field.
//
// The confirmation is not ceremony. It carries the digest of the plan a person read, and it is
// checked against the plan as recomputed, so a confirmation cannot be applied to a directory that
// has changed since — which is the failure a person would otherwise attribute to the reconciler.
func (s *corpusService) ApplyReconcile(ctx context.Context, req *connect.Request[knowledgev1.ApplyReconcileRequest]) (*connect.Response[knowledgev1.ApplyReconcileResponse], error) {
	msg := req.Msg
	in := msg.GetPlan()
	if in == nil {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a plan is required"}
	}
	confirm := strings.TrimSpace(msg.GetConfirm())
	if confirm == "" {
		return nil, &api.Error{
			Kind: api.KindInvalid,
			Message: "a confirmation is required: reconciling a corpus changes what every assistant in this deployment believes, " +
				"and a person should see the plan for that happening. Send the plan's `plan_digest` back, and a value the agent relays and the user answers works on every transport — including one that cannot ask a question of its own",
		}
	}
	if in.GetPlanDigest() != confirm {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: fmt.Sprintf("the confirmation %q is not the digest of the plan supplied (which is %q); a confirmation has to refer to the plan that was actually read", confirm, in.GetPlanDigest()),
		}
	}

	baseID, err := s.p.resolveBase(ctx, in.GetBaseId())
	if err != nil {
		return nil, err
	}
	// The owner is the one thing an apply cannot default: it authorises a later prune, and
	// a document ingested without it can never be removed. It is also the value a caller
	// most often forgets, so the failure is explicit rather than a silent leak.
	owner := strings.TrimSpace(msg.GetOwner())
	if owner == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "an owner is required on ApplyReconcile as well as on PlanReconcile: it must be the same value, and it is what authorises the deletions in this plan",
		}
	}

	// The plan is recomputed rather than applied as supplied. A plan carries paths and
	// digests; carrying them into a write is trusting a caller about what is on disk, and
	// the whole point of the digest is that it can be checked.
	state, err := s.p.prepare(ctx, baseID, namespaceFor(nil), nil)
	if err != nil {
		return nil, err
	}
	recomputed, err := knowledge.NewPlanner(state.corpus).Plan(ctx, state.record, knowledge.PlanOptions{
		BaseID:    baseID,
		Commit:    msg.GetCommit(),
		Owner:     owner,
		Namespace: state.identity.Namespace,
		Prune:     msg.GetPrune(),
	})
	if err != nil {
		return nil, classifyPlanError(err)
	}
	if recomputed.PlanDigest != confirm {
		return nil, &api.Error{
			Kind: api.KindFailedPrecondition,
			Message: fmt.Sprintf(
				"the corpus has changed since the plan was computed: the plan confirmed was %s and the corpus now describes %s. Re-plan and confirm again — a confirmation applied to a different directory is a decision nobody made",
				short(confirm), short(recomputed.PlanDigest)),
		}
	}

	opts := knowledge.ApplyOptions{
		BaseID:      baseID,
		Commit:      msg.GetCommit(),
		Owner:       owner,
		Namespace:   state.identity.Namespace,
		Identity:    state.identity,
		Async:       true,
		BatchSize:   int(msg.GetBatchSize()),
		Concurrency: int(msg.GetConcurrency()),
		Now:         nil,
	}
	if opts.BatchSize == 0 {
		opts.BatchSize = s.p.bases.batchSize
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = s.p.bases.concurrency
	}

	res, err := knowledge.NewApplier(s.p.client, state.corpus).Apply(ctx, recomputed, state.record, opts)
	if err != nil {
		return nil, err
	}

	// The record is written only after the apply, and only for what succeeded. A record
	// written for an apply that then failed would claim ownership of documents the base does
	// not have, and the next reconcile would believe they were done.
	if len(res.Ownership.Files) > 0 || len(recomputed.Unchanged) > 0 {
		if err := knowledge.SaveOwnership(state.recordPath, res.Ownership); err != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("the reconcile succeeded but its ownership record was not written, so the next one will re-ingest everything and cannot prune: %v", err))
		}
	}

	status, _ := s.status(ctx, baseID, state)
	return connect.NewResponse(&knowledgev1.ApplyReconcileResponse{
		Ingested:       int32(res.Ingested),
		Replaced:       int32(res.Replaced),
		Pruned:         int32(res.Pruned),
		Failed:         int32(res.Failed),
		OperationIds:   res.OperationIDs,
		Warnings:       res.Warnings,
		Status:         status,
		RecordedCommit: res.Ownership.LastCommit,
	}), nil
}

// GetCorpusStatus reports what the base believes and where that belief came from.
func (s *corpusService) GetCorpusStatus(ctx context.Context, req *connect.Request[knowledgev1.GetCorpusStatusRequest]) (*connect.Response[knowledgev1.GetCorpusStatusResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	state, err := s.p.prepare(ctx, baseID, namespaceFor(nil), nil)
	if err != nil {
		return nil, err
	}
	status, err := s.status(ctx, baseID, state)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.GetCorpusStatusResponse{Status: status}), nil
}

func (s *corpusService) status(ctx context.Context, baseID string, state corpusState) (*knowledgev1.CorpusStatus, error) {
	status := &knowledgev1.CorpusStatus{
		BaseId:         baseID,
		OwnedDocuments: int32(len(state.record.Files)),
		Degraded:       state.degraded,
		Binding: &knowledgev1.OwnershipBinding{
			BackendOrigin: state.identity.BackendOrigin,
			BaseId:        state.identity.BaseID,
			CorpusRoot:    state.identity.CorpusRoot,
			Namespace:     state.identity.Namespace,
		},
	}
	if state.record != nil {
		status.ReconciledCommit = state.record.LastCommit
		if !state.record.LastReconciledAt.IsZero() {
			status.ReconciledAt = timestamppb.New(state.record.LastReconciledAt)
		}
	}
	canPrune, why := state.record.CanPrune()
	status.CanPrune = canPrune
	if !canPrune && status.Degraded == "" {
		status.Degraded = why
	}
	for _, root := range s.p.bases.roots {
		status.Roots = append(status.Roots, &knowledgev1.CorpusRootInfo{
			Name:  root.Name,
			Path:  root.Path,
			Files: int32(countRoot(ctx, state.corpus, root.Name)),
		})
	}
	_ = ctx
	return status, nil
}

func countRoot(ctx context.Context, corpus *knowledge.Corpus, rootName string) int {
	walk, err := corpus.Walk(ctx, knowledge.WalkOptions{})
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range walk.Files {
		if f.Root == rootName {
			n++
		}
	}
	return n
}

// ReadCorpusFile returns a corpus file with its frontmatter.
//
// It is a read and not the only way to read a file — `GetContent` returns the same body — and it
// exists because a person debugging what a runbook says wants the declared metadata and the bytes
// together, while `GetContent` answers "what does the base hold" rather than "what does the file
// say".
func (s *corpusService) ReadCorpusFile(ctx context.Context, req *connect.Request[knowledgev1.ReadCorpusFileRequest]) (*connect.Response[knowledgev1.ReadCorpusFileResponse], error) {
	msg := req.Msg
	path := knowledge.NormalizePath(msg.GetPath())
	if path == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a corpus path is required"}
	}
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
	for _, f := range walk.Files {
		if f.Path != path {
			continue
		}
		if root := strings.TrimSpace(msg.GetRoot()); root != "" && f.Root != root {
			continue
		}
		if !f.Read {
			return nil, &api.Error{
				Kind:    api.KindInternal,
				Message: "the walk skipped this file's contents, which should not happen when nothing is being reconciled",
			}
		}
		return connect.NewResponse(&knowledgev1.ReadCorpusFileResponse{
			Raw:         f.Raw,
			Meta:        corpusMetaMessage(f.Front),
			DeclaredId:  f.Front.ID,
			EffectiveId: f.ID,
			Digest:      f.Digest,
			ModTime:     timestamppb.New(f.ModTime),
			Tags:        knowledge.TagsFor(f, knowledge.TagOptions{RootName: f.Root, Owner: s.p.Owner()}),
		}), nil
	}
	return nil, &api.Error{
		Kind:    api.KindNotFound,
		Message: fmt.Sprintf("no file at %q in the configured corpus (%s); call PlanReconcile to see what is there", path, strings.Join(s.p.bases.rootNames(), ", ")),
	}
}

// RebuildCorpus drops the base's derived knowledge and reconciles again.
//
// It is safe by construction rather than by promise: the corpus is the source of truth, so
// dropping the base loses nothing a person wrote and reproduces from the repository.
func (s *corpusService) RebuildCorpus(ctx context.Context, req *connect.Request[knowledgev1.RebuildCorpusRequest]) (*connect.Response[knowledgev1.RebuildCorpusResponse], error) {
	msg := req.Msg
	baseID, err := s.p.resolveBase(ctx, msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	commit, err := s.p.commitOf(ctx, msg.GetCommit())
	if err != nil {
		return nil, err
	}
	state, err := s.p.prepare(ctx, baseID, namespaceFor(nil), nil)
	if err != nil {
		return nil, err
	}
	plan, err := knowledge.NewPlanner(state.corpus).Plan(ctx, state.record, knowledge.PlanOptions{
		BaseID:    baseID,
		Commit:    commit,
		Owner:     msg.GetOwner(),
		Namespace: state.identity.Namespace,
		// A rebuild re-ingests everything by definition, so the plan it reports is the
		// whole corpus rather than what has drifted.
		Prune: false,
	})
	if err != nil {
		return nil, classifyPlanError(err)
	}
	if msg.GetDryRun() {
		return connect.NewResponse(&knowledgev1.RebuildCorpusResponse{Plan: planMessage(plan, state)}), nil
	}
	if strings.TrimSpace(msg.GetOwner()) == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a rebuild that writes needs an owner, for the same reason a reconcile does",
		}
	}
	// The backend's own clear is what drops the derived knowledge; the reconcile that
	// follows is the same code path an ordinary apply takes, so a rebuild cannot drift
	// from a reconcile in how it ingests.
	cleared, cerr := s.p.client.ClearBankMemories(ctx, baseID)
	if cerr != nil {
		return nil, cerr
	}
	return connect.NewResponse(&knowledgev1.RebuildCorpusResponse{
		ClearedFacts: cleared.Items,
		Plan:         planMessage(plan, state),
		Warnings: append(plan.Warnings,
			"the derived knowledge was cleared and the plan above is what will be re-ingested; it is incremental, so content whose digest has not changed is not re-extracted"),
	}), nil
}

// --- plan and status conversion.

func planMessage(plan knowledge.Plan, state corpusState) *knowledgev1.ReconcilePlan {
	out := &knowledgev1.ReconcilePlan{
		BaseId:      plan.BaseID,
		Commit:      plan.Commit,
		Created:     entriesMessage(plan.Created),
		Updated:     entriesMessage(plan.Updated),
		Unchanged:   entriesMessage(plan.Unchanged),
		Deleted:     entriesMessage(plan.Deleted),
		Warnings:    plan.Warnings,
		PlanDigest:  plan.PlanDigest,
		UnreadFiles: int32(plan.UnreadFiles),
		Empty:       plan.Empty(),
	}
	for _, m := range plan.Moved {
		out.Moved = append(out.Moved, &knowledgev1.ReconcileMove{
			From:          entryMessage(m.From),
			To:            entryMessage(m.To),
			OrphanedFacts: !m.To.DeclaredID,
		})
	}
	for _, d := range plan.Drift {
		out.Drift = append(out.Drift, &knowledgev1.Drift{
			ClaimId:         d.ClaimID,
			ClaimText:       d.ClaimText,
			BackingContent:  d.BackingContent,
			ConflictContent: d.ConflictContent,
			Detection:       d.Detection,
		})
	}
	return out
}

func entriesMessage(entries []knowledge.PlanEntry) []*knowledgev1.ReconcileEntry {
	out := make([]*knowledgev1.ReconcileEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryMessage(e))
	}
	return out
}

func entryMessage(e knowledge.PlanEntry) *knowledgev1.ReconcileEntry {
	return &knowledgev1.ReconcileEntry{
		Id:          e.ID,
		DeclaredId:  e.DeclaredID,
		Root:        e.Root,
		Path:        e.Path,
		Digest:      e.Digest,
		PriorDigest: e.PriorDigest,
		Size:        e.Size,
		ModTime:     timestamppb.New(e.ModTime),
		Title:       e.Title,
		Tags:        e.Tags,
		Binary:      e.Binary,
		Reason:      e.Reason,
	}
}

func namespaceFor(roots []*knowledgev1.CorpusRootSpec) string {
	return defaultNamespace
}

func scopeExcludes(roots []*knowledgev1.CorpusRootSpec) []string {
	var out []string
	for _, r := range roots {
		out = append(out, r.GetExclude()...)
	}
	return out
}

// defaultNamespace is the identifier namespace used when a caller does not name one.
//
// It is a constant rather than something derived from the root, because a namespace that depended
// on where the repository was cloned would change on a fresh clone and re-extract the whole
// corpus. It is bound into the ownership record, so changing it is a deliberate act.
const defaultNamespace = "docs"

// classifyPlanError turns a planning failure into a classified error.
//
// A corpus with two files claiming one identifier is a problem in somebody's documentation, and
// the answer they need is which two files — so the message is kept whole rather than summarised.
func classifyPlanError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *api.Error
	if asAPIError(err, &apiErr) {
		return apiErr
	}
	if strings.Contains(err.Error(), "claim the identifier") {
		return &api.Error{Kind: api.KindInvalid, Message: err.Error(), Err: err}
	}
	if strings.Contains(err.Error(), "frontmatter") {
		return &api.Error{Kind: api.KindInvalid, Message: err.Error(), Err: err}
	}
	return &api.Error{Kind: api.KindInternal, Message: "planning a reconcile", Err: err}
}

func short(digest string) string {
	if hex, ok := knowledge.ParseDigest(digest); ok && len(hex) > 12 {
		return hex[:12]
	}
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}
