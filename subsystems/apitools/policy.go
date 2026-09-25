package apitools

import "github.com/Manu343726/toolbox/pkg/api"

// OperationPolicy decides whether a registered operation may be exposed as an
// agent tool. Parsing an API is not authorization: an operation becomes a tool
// only because an explicit policy allows it.
type OperationPolicy interface {
	// AllowOperation reports whether the operation may be exposed.
	AllowOperation(target api.API, operation api.Operation) bool
}

// OperationPolicyFunc adapts a function to OperationPolicy.
type OperationPolicyFunc func(api.API, api.Operation) bool

// AllowOperation implements OperationPolicy.
func (f OperationPolicyFunc) AllowOperation(target api.API, operation api.Operation) bool {
	return f(target, operation)
}

// CapabilityPolicy is the default policy. It authorizes an operation when the
// operation itself, its service, or its API declares at least one capability.
// An operation nobody declared a capability for is denied, so registering an
// API never silently exposes it — and because the rule is capability-based, an
// operation whose side effects nobody recognised is not exempt from it.
type CapabilityPolicy struct{}

// AllowOperation implements OperationPolicy.
func (CapabilityPolicy) AllowOperation(target api.API, operation api.Operation) bool {
	if len(operation.Capabilities) > 0 || len(target.Capabilities) > 0 {
		return true
	}
	service, ok := target.Service(operation.Service)
	return ok && len(service.Capabilities) > 0
}

// AllowAllOperations authorizes every operation. It is for trusted, explicitly
// composed deployments and tests; it is not the default.
type AllowAllOperations struct{}

// AllowOperation implements OperationPolicy.
func (AllowAllOperations) AllowOperation(api.API, api.Operation) bool { return true }

// DenyAllOperations authorizes no operation. The catalog stays introspectable
// so a client can see what a registered API offers, but nothing may be exposed
// until the deployment installs a policy that allows it.
type DenyAllOperations struct{}

// AllowOperation implements OperationPolicy.
func (DenyAllOperations) AllowOperation(api.API, api.Operation) bool { return false }
