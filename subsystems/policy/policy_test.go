package policy_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/subsystems/policy"
	policyv1 "github.com/Manu343726/toolbox/subsystems/policy/policyv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This subsystem decides what an agent may call, so its tests are about the decisions and about
// which of them are safe by default. Two tests existed and both checked one happy path.
//
// The rules under test are not obvious from the code and that is why they are written down here:
//
//   - An empty allow list allows everything. It is a policy that has not been written yet, and
//     the alternative — allowing nothing — would make an unwritten policy indistinguishable
//     from a restrictive one.
//   - Approval is the opposite. An empty approval list requires approval of anything mutating,
//     because a policy that has not enumerated what needs a human has not decided, and the
//     safe answer to "has this been decided?" is no.
//   - A capability on the approval list requires approval whether or not it mutates. A read that
//     someone has decided needs a human is still a read that needs a human.

func handler() *policy.Handler {
	return policy.NewHandler(policy.Options{Policies: map[string]policy.Rule{
		"open": {},
		"reads-only": {
			AllowedCapabilities: []string{"knowledge.Search", "tool.List"},
		},
		"writes-with-approval": {
			AllowedCapabilities:  []string{"knowledge.Put", "knowledge.Search"},
			ApprovalCapabilities: []string{"knowledge.Put"},
		},
		"nothing": {
			AllowedCapabilities:  []string{"one.thing"},
			ApprovalCapabilities: []string{"one.thing", "two.thing"},
		},
	}})
}

func evaluate(t *testing.T, policyID, capability string, mutating bool) *policyv1.PolicyDecision {
	t.Helper()
	response, err := handler().Evaluate(context.Background(), connect.NewRequest(&policyv1.EvaluateRequest{
		PolicyId: policyID,
		Action: &policyv1.PolicyAction{
			Capability: capability,
			Mutating:   mutating,
		},
	}))
	require.NoError(t, err)
	require.NotNil(t, response.Msg.GetDecision())
	return response.Msg.GetDecision()
}

func TestAnEmptyAllowListAllowsEverything(t *testing.T) {
	decision := evaluate(t, "open", "anything.at.all", false)

	// A policy that has not been written yet allows everything, which is the reading that makes
	// an unwritten policy different from a restrictive one rather than identical to it.
	assert.True(t, decision.GetAllowed())
	assert.False(t, decision.GetApprovalRequired())
	assert.NotEmpty(t, decision.GetReason(), "a decision with no reason is not auditable")
}

func TestACapabilityOutsideTheAllowListIsRefused(t *testing.T) {
	decision := evaluate(t, "reads-only", "knowledge.Put", true)

	// A capability a policy did not list is a capability the policy did not decide about, and
	// this is the decision that keeps an agent inside what it was given.
	assert.False(t, decision.GetAllowed())
	assert.Contains(t, decision.GetReason(), "not allowed")
}

func TestACapabilityOnTheAllowListIsAllowed(t *testing.T) {
	decision := evaluate(t, "reads-only", "knowledge.Search", false)

	assert.True(t, decision.GetAllowed())
}

func TestAnEmptyApprovalListRequiresApprovalOfAnythingMutating(t *testing.T) {
	// The opposite default to the allow list, and deliberately so: a policy that has not
	// enumerated what needs a human has not decided, and the safe answer to "has this been
	// decided?" is no.
	read := evaluate(t, "reads-only", "knowledge.Search", false)
	assert.False(t, read.GetApprovalRequired(), "a read does not mutate, so it needs no approval")

	write := evaluate(t, "reads-only", "knowledge.Search", true)
	assert.True(t, write.GetApprovalRequired(),
		"a mutating action with no approval list must still need a human")
}

func TestACapabilityOnTheApprovalListNeedsApprovalEvenIfItIsARead(t *testing.T) {
	decision := evaluate(t, "writes-with-approval", "knowledge.Put", false)

	// A read someone has decided needs a human is still a read that needs a human, and the
	// mutating flag is not the only thing that can make an action worth watching.
	assert.True(t, decision.GetAllowed())
	assert.True(t, decision.GetApprovalRequired())
}

func TestAnAllowedCapabilityOutsideTheApprovalListNeedsNoApproval(t *testing.T) {
	decision := evaluate(t, "writes-with-approval", "knowledge.Search", false)

	assert.True(t, decision.GetAllowed())
	assert.False(t, decision.GetApprovalRequired())
}

func TestACapabilityAllowedButNeedingApprovalIsReportedAsBoth(t *testing.T) {
	// The two answers are independent and a caller needs both: "allowed" says the policy
	// permits the action at all, and "approval required" says a human must first. Collapsing
	// them loses the difference between a denial and a hold.
	decision := evaluate(t, "writes-with-approval", "knowledge.Put", true)

	assert.True(t, decision.GetAllowed())
	assert.True(t, decision.GetApprovalRequired())
	assert.Contains(t, decision.GetReason(), "approval")
}

func TestARefusalIsNotAlsoReportedAsNeedingApproval(t *testing.T) {
	decision := evaluate(t, "nothing", "three.thing", true)

	// A capability the policy does not allow is not waiting for a human — it is refused. The
	// alternative reads to a caller as "ask someone", which is an answer to a different question.
	assert.False(t, decision.GetAllowed())
	assert.False(t, decision.GetApprovalRequired())
}

func TestAnUnknownPolicyIsNotFound(t *testing.T) {
	// A policy that does not exist is a caller error about which snapshot to use, not a
	// decision. Answering it with "not allowed" would be indistinguishable from a real denial
	// and would make a configuration mistake look like a security decision.
	_, err := handler().Evaluate(context.Background(), connect.NewRequest(&policyv1.EvaluateRequest{
		PolicyId: "no-such-policy",
		Action:   &policyv1.PolicyAction{Capability: "x"},
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "no-such-policy")
}

func TestAnEvaluationWithNoActionIsRefused(t *testing.T) {
	_, err := handler().Evaluate(context.Background(), connect.NewRequest(&policyv1.EvaluateRequest{
		PolicyId: "open",
	}))

	// A request with no action is not a decision about anything, and defaulting it to a
	// capability would invent one.
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAnEvaluationWithNoRequestAtAllIsRefused(t *testing.T) {
	_, err := handler().Evaluate(context.Background(), nil)

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestListingPoliciesIsSortedAndComplete(t *testing.T) {
	response, err := handler().ListPolicies(context.Background(), connect.NewRequest(&policyv1.ListPoliciesRequest{}))
	require.NoError(t, err)

	// Sorted, because two processes that loaded the same policies must list them the same way
	// for a diff of two listings to mean anything.
	assert.Equal(t, []string{"nothing", "open", "reads-only", "writes-with-approval"},
		response.Msg.GetPolicyIds())
}

func TestListingCanBeNarrowedByPrefix(t *testing.T) {
	response, err := handler().ListPolicies(context.Background(), connect.NewRequest(&policyv1.ListPoliciesRequest{
		IdPrefix: "read",
	}))
	require.NoError(t, err)

	assert.Equal(t, []string{"reads-only"}, response.Msg.GetPolicyIds())
}

func TestAPrefixThatMatchesNothingListsNothingRatherThanFailing(t *testing.T) {
	response, err := handler().ListPolicies(context.Background(), connect.NewRequest(&policyv1.ListPoliciesRequest{
		IdPrefix: "zzz",
	}))

	// An empty answer is the answer. Refusing would make "no policy starts with zzz" look like
	// a broken service.
	require.NoError(t, err)
	assert.Empty(t, response.Msg.GetPolicyIds())
}

func TestAHandlerWithNoPoliciesListsNothingAndDecidesNothing(t *testing.T) {
	empty := policy.NewHandler(policy.Options{})

	list, err := empty.ListPolicies(context.Background(), connect.NewRequest(&policyv1.ListPoliciesRequest{}))
	require.NoError(t, err)
	assert.Empty(t, list.Msg.GetPolicyIds())

	_, err = empty.Evaluate(context.Background(), connect.NewRequest(&policyv1.EvaluateRequest{
		PolicyId: "anything",
		Action:   &policyv1.PolicyAction{Capability: "x"},
	}))
	// A deployment that configured no policies must not answer "allowed" for a policy that is
	// not there, or every unconfigured deployment would be wide open.
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestTheHandlerCopiesItsPolicies(t *testing.T) {
	// A caller that edits the slices it passed in must not change what the handler decides, or
	// one subsystem's configuration edit would silently rewrite another's authorization.
	rule := policy.Rule{AllowedCapabilities: []string{"one"}}
	built := policy.NewHandler(policy.Options{Policies: map[string]policy.Rule{"p": rule}})
	rule.AllowedCapabilities[0] = "two"

	response, err := built.Evaluate(context.Background(), connect.NewRequest(&policyv1.EvaluateRequest{
		PolicyId: "p",
		Action:   &policyv1.PolicyAction{Capability: "one"},
	}))
	require.NoError(t, err)
	assert.True(t, response.Msg.GetDecision().GetAllowed(), "the handler saw the caller's edit")
}

func TestConcurrentEvaluationsAreSafe(t *testing.T) {
	// The policy map is shared and the gateway evaluates on every exposed call, so a read
	// racing another read is the common case and a read racing a write the interesting one.
	built := handler()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_, _ = built.Evaluate(context.Background(), connect.NewRequest(&policyv1.EvaluateRequest{
				PolicyId: "reads-only",
				Action:   &policyv1.PolicyAction{Capability: "knowledge.Search"},
			}))
		}
	}()
	for i := 0; i < 50; i++ {
		_, err := built.Evaluate(context.Background(), connect.NewRequest(&policyv1.EvaluateRequest{
			PolicyId: "reads-only",
			Action:   &policyv1.PolicyAction{Capability: "knowledge.Search"},
		}))
		require.NoError(t, err)
	}
	<-done
}
