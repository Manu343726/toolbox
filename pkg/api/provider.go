package api

// ProviderRole is the open identifier of the role a provider subsystem plays.
// The documented roles are parser and adapter; a user may add another.
type ProviderRole = string

// Provider roles the framework documents. They are conventions, not a closed
// set: a user may add another role together with its own contract.
const (
	// ProviderParser parses a description document into the standard model.
	ProviderParser ProviderRole = "parser"
	// ProviderAdapter renders a standard description into a target
	// representation, such as an OpenAPI document or an MCP tool surface. It is
	// direction-agnostic: its source is the standard description, not the format
	// the description was parsed from.
	ProviderAdapter ProviderRole = "adapter"
	// ProviderInvoker executes the operations of a registered API against a live
	// server.
	ProviderInvoker ProviderRole = "invoker"
)

// Provider describes one extension-point subsystem available in a deployment.
// A catalog lists providers to answer "which formats can this deployment parse
// and which transports can it invoke over?", and resolves a provider to call
// it.
type Provider struct {
	// ID is the stable provider identifier, unique within the deployment.
	ID string
	// Subsystem is the subsystem hosting the provider.
	Subsystem string
	// Role states which contract the provider implements, as an open identifier.
	Role ProviderRole
	// Formats are the description formats the provider can parse. It is empty
	// for roles that do not read documents.
	Formats []Format
	// Targets are the representations the provider can render a description
	// into, such as an OpenAPI document, a protobuf contract, or an MCP tool
	// surface. It is empty for roles that do not render.
	Targets []string
	// Transports are the transports the provider can invoke over. It is empty
	// for roles that do not call.
	Transports []Transport
	// Endpoint is where the provider is reachable.
	Endpoint string
	// ServiceNames are the fully-qualified services the provider serves.
	ServiceNames []string
	// Status is the operational state of the provider.
	Status ServerStatus
	// ImplementationVersion is the provider subsystem version.
	ImplementationVersion string
}

// Clone returns a deep copy of the provider.
func (p Provider) Clone() Provider {
	clone := p
	clone.Formats = append([]Format(nil), p.Formats...)
	clone.Targets = append([]string(nil), p.Targets...)
	clone.Transports = append([]Transport(nil), p.Transports...)
	clone.ServiceNames = append([]string(nil), p.ServiceNames...)
	return clone
}

// HandlesFormat reports whether the provider declares support for a format.
func (p Provider) HandlesFormat(format Format) bool {
	for _, candidate := range p.Formats {
		if candidate == format {
			return true
		}
	}
	return false
}

// HandlesTarget reports whether the provider can render into a representation.
func (p Provider) HandlesTarget(target string) bool {
	for _, candidate := range p.Targets {
		if candidate == target {
			return true
		}
	}
	return false
}

// HandlesTransport reports whether the provider declares support for a
// transport.
func (p Provider) HandlesTransport(transport Transport) bool {
	for _, candidate := range p.Transports {
		if candidate == transport {
			return true
		}
	}
	return false
}

// Serves reports whether the provider advertises a service name.
func (p Provider) Serves(serviceName string) bool {
	for _, candidate := range p.ServiceNames {
		if candidate == serviceName {
			return true
		}
	}
	return false
}

// The framework's extension contract names, in one place so components that
// reason about them — the MCP gateway, for instance — do not repeat string
// literals.
const (
	// ParserService is the contract a parser subsystem implements.
	ParserService = "toolbox.api.v1.ApiParserService"
	// AdapterService is the contract an adapter subsystem implements.
	AdapterService = "toolbox.api.v1.ApiAdapterService"
	// InvokerService is the contract an invoker subsystem implements.
	InvokerService = "toolbox.api.v1.ApiInvokerService"
)
