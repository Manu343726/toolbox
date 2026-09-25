package mcp

// FeaturePolicy decides whether a reflected RPC method may become an MCP
// tool. Reflection describes a method; it does not authorize that method for
// agent use. Callers should provide an explicit policy for externally
// discovered services.
//
// A policy is deliberately service-and-method scoped. A service may expose a
// broad policy for read-only operations and deny mutating operations without
// changing its protobuf contract.
type FeaturePolicy interface {
	AllowFeature(serviceName, methodName string) bool
}

// FeaturePolicyFunc adapts a function to FeaturePolicy.
type FeaturePolicyFunc func(serviceName, methodName string) bool

// AllowFeature implements FeaturePolicy.
func (f FeaturePolicyFunc) AllowFeature(serviceName, methodName string) bool {
	return f(serviceName, methodName)
}

// AllowAllFeatures returns an explicit policy that authorizes every unary
// method. It is useful for trusted, explicitly composed endpoints and for
// tests. It should not be the default for an unknown remote service.
func AllowAllFeatures() FeaturePolicy {
	return FeaturePolicyFunc(func(string, string) bool { return true })
}

// DenyAllFeatures returns a policy that authorizes no reflected method. The
// MCP management tools remain available so a client can inspect the catalog,
// but it must explicitly change the policy outside this server to authorize
// calls.
func DenyAllFeatures() FeaturePolicy {
	return FeaturePolicyFunc(func(string, string) bool { return false })
}

// PolicyFromServices builds a policy from explicit service capabilities. A
// service with at least one declared capability may expose its unary methods;
// a service without declared capabilities is denied. When AllowedMethods is
// non-empty, it narrows the service to that explicit method allow-list. This
// is the default policy for subsystems created through the SDK.
func PolicyFromServices(services []ServiceMetadata) FeaturePolicy {
	type rule struct {
		all       bool
		permitted map[string]bool
	}
	rules := make(map[string]rule, len(services))
	for _, service := range services {
		if service.Name == "" || len(service.Capabilities) == 0 {
			continue
		}
		current := rules[service.Name]
		if len(service.AllowedMethods) == 0 {
			current.all = true
		} else {
			if current.permitted == nil {
				current.permitted = make(map[string]bool, len(service.AllowedMethods))
			}
			for _, method := range service.AllowedMethods {
				current.permitted[method] = true
			}
		}
		rules[service.Name] = current
	}
	return FeaturePolicyFunc(func(serviceName, methodName string) bool {
		current, ok := rules[serviceName]
		if !ok {
			return false
		}
		return current.all || current.permitted[methodName]
	})
}
