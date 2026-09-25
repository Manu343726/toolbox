// Package policy implements an independent policy evaluation service. The
// reference policy model is intentionally small; richer rule languages can be
// added as separate providers without changing workflow or agent contracts.
package policy

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	policyv1 "github.com/Manu343726/toolbox/subsystems/policy/policyv1"
	"github.com/Manu343726/toolbox/subsystems/policy/policyv1/policyv1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "policy"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Rule is the reference in-memory policy definition.
type Rule struct {
	// AllowedCapabilities lists capabilities allowed by the snapshot.
	AllowedCapabilities []string
	// ApprovalCapabilities requires approval before invocation.
	ApprovalCapabilities []string
}

// Options configures the policy subsystem.
type Options struct {
	// Policies maps policy IDs to rules.
	Policies map[string]Rule
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Handler implements PolicyService.
type Handler struct {
	mu       sync.RWMutex
	policies map[string]Rule
}

// NewHandler creates a policy handler.
func NewHandler(options Options) *Handler {
	policies := make(map[string]Rule, len(options.Policies))
	for id, rule := range options.Policies {
		policies[id] = Rule{
			AllowedCapabilities:  append([]string(nil), rule.AllowedCapabilities...),
			ApprovalCapabilities: append([]string(nil), rule.ApprovalCapabilities...),
		}
	}
	return &Handler{policies: policies}
}

// New is the programmatic in-process entrypoint for the policy subsystem.
func New(options Options) (*subsystem.Server, error) {
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := policyv1connect.NewPolicyServiceHandler(NewHandler(options))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Evaluates versioned domain policies and approval requirements.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:    policyv1connect.PolicyServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
}

// Evaluate checks an action against a policy snapshot.
func (h *Handler) Evaluate(_ context.Context, req *connect.Request[policyv1.EvaluateRequest]) (*connect.Response[policyv1.EvaluateResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetAction() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("action is required"))
	}
	policyID := req.Msg.GetPolicyId()
	h.mu.RLock()
	rule, exists := h.policies[policyID]
	h.mu.RUnlock()
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("policy %q not found", policyID))
	}
	capability := req.Msg.GetAction().GetCapability()
	allowed := len(rule.AllowedCapabilities) == 0 || contains(rule.AllowedCapabilities, capability)
	approval := contains(rule.ApprovalCapabilities, capability) || (req.Msg.GetAction().GetMutating() && len(rule.ApprovalCapabilities) == 0)
	reason := "capability is allowed"
	if !allowed {
		reason = "capability is not allowed by the policy"
	} else if approval {
		reason = "capability requires human approval"
	}
	return connect.NewResponse(&policyv1.EvaluateResponse{Decision: &policyv1.PolicyDecision{
		Allowed: allowed, ApprovalRequired: approval, Reason: reason,
	}}), nil
}

// ListPolicies returns policy identifiers.
func (h *Handler) ListPolicies(_ context.Context, req *connect.Request[policyv1.ListPoliciesRequest]) (*connect.Response[policyv1.ListPoliciesResponse], error) {
	prefix := ""
	if req != nil && req.Msg != nil {
		prefix = req.Msg.GetIdPrefix()
	}
	h.mu.RLock()
	ids := make([]string, 0, len(h.policies))
	for id := range h.policies {
		if prefix == "" || strings.HasPrefix(id, prefix) {
			ids = append(ids, id)
		}
	}
	h.mu.RUnlock()
	sort.Strings(ids)
	return connect.NewResponse(&policyv1.ListPoliciesResponse{PolicyIds: ids}), nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
