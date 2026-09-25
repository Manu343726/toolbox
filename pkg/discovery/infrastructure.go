package discovery

import "strings"

// InfrastructureServiceSuffixes are the service names that belong to the platform
// rather than to a capability a user adopted: health, registry, documentation,
// and the framework's own API extension contracts.
//
// They are defined once, here, because two components need the same answer: the
// MCP gateway, which must not offer a user tool for a subsystem's own plumbing,
// and the catalog seeder, which must not register that plumbing as an API an agent
// could call.
var InfrastructureServiceSuffixes = []string{
	".HealthService",
	".DocumentationService",
	".RegistryService",
}

// ExtensionContractServices are the framework's API extension contracts. Many
// provider subsystems serve them on purpose, because serving one is how a format
// or a transport is contributed, so they are plumbing by definition: a catalog
// reaches a specific provider by identifier rather than by service name.
var ExtensionContractServices = []string{
	"toolbox.api.v1.ApiParserService",
	"toolbox.api.v1.ApiAdapterService",
	"toolbox.api.v1.ApiInvokerService",
}

// IsInfrastructureService reports whether a service is platform plumbing: a health
// check, the registry, the documentation service, the reflection services, or one
// of the framework's extension contracts.
func IsInfrastructureService(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if IsReflectionService(name) {
		return true
	}
	for _, suffix := range InfrastructureServiceSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	for _, contract := range ExtensionContractServices {
		if name == contract {
			return true
		}
	}
	return false
}
