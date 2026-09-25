package policy

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	policyv1 "github.com/Manu343726/toolsbox/subsystems/policy/policyv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyEvaluate(t *testing.T) {
	handler := NewHandler(Options{Policies: map[string]Rule{
		"default": {AllowedCapabilities: []string{"read", "write"}, ApprovalCapabilities: []string{"write"}},
	}})
	ctx := context.Background()
	allowed, err := handler.Evaluate(ctx, connect.NewRequest(&policyv1.EvaluateRequest{
		PolicyId: "default", Action: &policyv1.PolicyAction{Capability: "read"},
	}))
	require.NoError(t, err)
	assert.True(t, allowed.Msg.GetDecision().GetAllowed())
	assert.False(t, allowed.Msg.GetDecision().GetApprovalRequired())

	approval, err := handler.Evaluate(ctx, connect.NewRequest(&policyv1.EvaluateRequest{
		PolicyId: "default", Action: &policyv1.PolicyAction{Capability: "write", Mutating: true},
	}))
	require.NoError(t, err)
	assert.True(t, approval.Msg.GetDecision().GetApprovalRequired())
	_, err = handler.Evaluate(ctx, connect.NewRequest(&policyv1.EvaluateRequest{PolicyId: "missing", Action: &policyv1.PolicyAction{}}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestPolicyList(t *testing.T) {
	handler := NewHandler(Options{Policies: map[string]Rule{"z": {}, "a": {}}})
	response, err := handler.ListPolicies(context.Background(), connect.NewRequest(&policyv1.ListPoliciesRequest{}))
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "z"}, response.Msg.GetPolicyIds())
}
