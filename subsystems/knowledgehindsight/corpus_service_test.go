package knowledgehindsight

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// The apply resolves the commit the same way the plan does.
//
// This is the whole reconcile, and it was broken for any caller who relied on the default: the
// plan resolved the commit, the apply passed the request's (empty) `commit` straight through to
// the recomputation, and the two digests could therefore never match. Every apply was refused with
// "the corpus has changed since the plan was computed", which reads as the reconciler being
// unreliable rather than as the two halves disagreeing about which commit they are talking about.
//
// It survived because every test here passed a `commit` explicitly. The end-to-end harness
// (`scripts/mcp_knowledge_reconcile.py`) plans, reads the plan and confirms the digest it was
// given — which is what a caller does — and that is what found it.
func TestTheApplyAgreesWithThePlanAboutWhichCommitItIsReconciling(t *testing.T) {
	t.Parallel()
	// A git checkout, because the commit is what the reconcile records and what makes "the index
	// matches the merge" a statement that can be checked. Without one the field is legitimately
	// empty and both halves would agree about nothing.
	root := t.TempDir()
	corpusFixtureAt(t, root)
	gitInit(t, root)

	p, _ := newAdminProviderAt(t, nil, root)
	ctx := context.Background()

	planned, err := p.corpusHandler().PlanReconcile(ctx, connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox",
	}))
	require.NoError(t, err)
	digest := planned.Msg.GetPlan().GetPlanDigest()
	require.NotEmpty(t, digest)

	// The request names no commit, which is the case that was broken.
	applied, err := p.corpusHandler().ApplyReconcile(ctx, connect.NewRequest(&knowledgev1.ApplyReconcileRequest{
		Plan: planned.Msg.GetPlan(), Confirm: digest, Owner: "toolbox",
	}))
	require.NoError(t, err, "a plan confirmed with no commit named must apply, not be refused as stale")
	assert.Equal(t, int32(3), applied.Msg.GetIngested(), "and it ingests the corpus")

	// And the commit it recorded is the one the plan resolved, so "reconciled to <commit>" is a
	// claim about the same commit the confirmation was computed over.
	head := gitHead(t, root)
	status, err := p.corpusHandler().GetCorpusStatus(ctx, connect.NewRequest(&knowledgev1.GetCorpusStatusRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	assert.Equal(t, head, status.Msg.GetStatus().GetReconciledCommit())
}

// A corpus that is not a checkout is a supported state, and both halves must agree about the
// commit being unknown — otherwise the same mismatch returns by a different route.
func TestACorpusThatIsNotACheckoutReconcilesToAnUnknownCommitAndAgreesWithItself(t *testing.T) {
	t.Parallel()
	p, _ := newAdminProvider(t, nil)
	ctx := context.Background()

	planned, err := p.corpusHandler().PlanReconcile(ctx, connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox",
	}))
	require.NoError(t, err)
	applied, err := p.corpusHandler().ApplyReconcile(ctx, connect.NewRequest(&knowledgev1.ApplyReconcileRequest{
		Plan: planned.Msg.GetPlan(), Confirm: planned.Msg.GetPlan().GetPlanDigest(), Owner: "toolbox",
	}))
	require.NoError(t, err, "an unknown commit is not a reason to refuse")
	assert.Equal(t, int32(3), applied.Msg.GetIngested())

	status, err := p.corpusHandler().GetCorpusStatus(ctx, connect.NewRequest(&knowledgev1.GetCorpusStatusRequest{
		BaseId: "docs",
	}))
	require.NoError(t, err)
	// Empty, and said to be allowed to be empty by the contract — a corpus with no history has
	// nothing to be reconciled *from*.
	assert.Empty(t, status.Msg.GetStatus().GetReconciledCommit())
	assert.NotEmpty(t, status.Msg.GetStatus().GetReconciledAt(), "but it says when it reconciled")
}

// A commit that actually moved between the plan and the apply is refused — that is what the
// confirmation is for, and the fix above must not have weakened it into "always applies".
func TestACommitThatMovedBetweenThePlanAndTheApplyIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	corpusFixtureAt(t, root)
	gitInit(t, root)
	head := gitHead(t, root)

	p, _ := newAdminProviderAt(t, nil, root)
	ctx := context.Background()
	planned, err := p.corpusHandler().PlanReconcile(ctx, connect.NewRequest(&knowledgev1.PlanReconcileRequest{
		BaseId: "docs", Owner: "toolbox",
	}))
	require.NoError(t, err)

	// A new commit with no content change: the digests of the files are identical, so the only
	// thing that moved is the commit — and it is the commit that has to be checked.
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, root, "more")
	require.NotEqual(t, head, gitHead(t, root))

	_, err = p.corpusHandler().ApplyReconcile(ctx, connect.NewRequest(&knowledgev1.ApplyReconcileRequest{
		// Naming the *old* commit explicitly, which is what a confirmation computed over it
		// means. The apply resolves the request's commit, so this is the case where the two
		// halves could still disagree — and the refusal is the correct answer.
		Plan: planned.Msg.GetPlan(), Confirm: planned.Msg.GetPlan().GetPlanDigest(),
		Owner: "toolbox", Commit: head,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the corpus has changed since the plan was computed")
}
