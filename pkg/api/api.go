// Package api defines the standard, provider-neutral description of an API.
//
// It is the shared vocabulary of the framework: an API parsed from an OpenAPI
// document, from a gRPC contract, or from a description language a user adds
// later is represented by the same Go types, so an adapter never has to
// understand a provider-specific schema. The description describes an API; it
// does not authorize access to it.
//
// A description is organized in four levels:
//
//	API       one addressable API contract
//	Service   a group of operations
//	Operation one callable operation, and the unit of exposure
//	Schema    the request and response value shapes
//
// Every level carries a stable identifier so an operation can be referenced
// from a policy, an exposure decision, or a tool name without depending on the
// description language it came from.
//
// # Extensibility
//
// The framework does not enumerate the API formats or transports it supports.
// A format and a transport are open identifiers, described by FormatDescriptor
// and TransportDescriptor, and a catalog indexes the descriptors it knows so a
// consumer can discover which formats this deployment can parse and which
// transports it can reach — including formats contributed by a user that the
// framework itself has never heard of. OpenAPI and gRPC are two such formats,
// not two privileged cases.
//
// The only closed vocabularies in this package are the JSON value types, which
// JSON itself defines, and a registered server's lifecycle status, which the
// framework's registry defines.
package api

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Format is the open identifier of a description language, such as a
// user-defined identifier. The framework never switches on it: only a parser
// subsystem claims to handle a format, and a catalog indexes the format
// descriptors it has been given.
type Format = string

// Transport is the open identifier of a transport used to reach a server.
type Transport = string

// ParameterIn is the open identifier of where a parameter value is carried.
// The documented locations are path, query, header, cookie, and body; a
// transport contributed by a user may define its own.
type ParameterIn = string

// Parameter locations the framework documents. They are conventions, not a
// closed set.
const (
	ParameterInPath   ParameterIn = "path"
	ParameterInQuery  ParameterIn = "query"
	ParameterInHeader ParameterIn = "header"
	ParameterInCookie ParameterIn = "cookie"
	ParameterInBody   ParameterIn = "body"
)

// SecurityType is the open identifier of a credential scheme type, such as
// apiKey, http, oauth2, openIdConnect, or mutualTLS.
type SecurityType = string

// SourceKind is the open identifier of where a description came from, such as
// document, file, url, descriptor_set, or reflection.
type SourceKind = string

// SideEffect is the open identifier of a declared consequence of invoking an
// operation. Side effects are declarations, not observations: a description
// that omits them means unknown, not safe, and a consumer that does not
// recognize a declared effect must treat it as consequential.
type SideEffect = string

// Side effects the framework documents. They are conventions, not a closed set.
const (
	SideEffectReadOnly     SideEffect = "read_only"
	SideEffectCreate       SideEffect = "create"
	SideEffectUpdate       SideEffect = "update"
	SideEffectDelete       SideEffect = "delete"
	SideEffectExternal     SideEffect = "external"
	SideEffectIrreversible SideEffect = "irreversible"
	SideEffectFinancial    SideEffect = "financial"
)

// SchemaType states the JSON type of a schema. It is defined by JSON itself and
// is the one value vocabulary this package closes.
type SchemaType string

const (
	// TypeUnspecified is an unknown schema type.
	TypeUnspecified SchemaType = ""
	// TypeObject is a JSON object.
	TypeObject SchemaType = "object"
	// TypeArray is a JSON array.
	TypeArray SchemaType = "array"
	// TypeString is a JSON string.
	TypeString SchemaType = "string"
	// TypeInteger is a JSON number without a fractional part.
	TypeInteger SchemaType = "integer"
	// TypeNumber is a JSON number.
	TypeNumber SchemaType = "number"
	// TypeBoolean is a JSON boolean.
	TypeBoolean SchemaType = "boolean"
	// TypeNull is JSON null.
	TypeNull SchemaType = "null"
)

// String returns the schema type identifier.
func (t SchemaType) String() string { return string(t) }

// ServerStatus is the operational state of a registered server. It is owned by
// the framework's registry vocabulary and is not extensible.
type ServerStatus string

const (
	// ServerStatusUnspecified is an unknown server state.
	ServerStatusUnspecified ServerStatus = ""
	// ServerStatusServing states that the server accepts requests.
	ServerStatusServing ServerStatus = "serving"
	// ServerStatusNotServing states that the server is unavailable.
	ServerStatusNotServing ServerStatus = "not_serving"
	// ServerStatusUnknown states that reachability has not been verified.
	ServerStatusUnknown ServerStatus = "unknown"
)

// FormatDescriptor describes one description format that a deployment can
// parse. A parser subsystem contributes it; a catalog indexes it so discovery
// can answer "which formats can this deployment parse?".
type FormatDescriptor struct {
	// ID is the format identifier APIs and servers refer to, unique within a
	// catalog.
	ID string
	// Name is the human-readable format name.
	Name string
	// Version is the version of this descriptor.
	Version string
	// Description explains what the format is and when to use it.
	Description string
	// MediaTypes are the media types a document of this format is served as.
	MediaTypes []string
	// FileExtensions are the extensions commonly used for this format.
	FileExtensions []string
	// SpecificationVersion is the version of the format specification.
	SpecificationVersion string
	// Provider is the subsystem that contributed the descriptor.
	Provider string
	// Metadata holds additional descriptor attributes.
	Metadata map[string]string
}

// Clone returns a deep copy of the descriptor.
func (d FormatDescriptor) Clone() FormatDescriptor {
	clone := d
	clone.MediaTypes = append([]string(nil), d.MediaTypes...)
	clone.FileExtensions = append([]string(nil), d.FileExtensions...)
	clone.Metadata = copyMetadata(d.Metadata)
	return clone
}

// TransportDescriptor describes one transport that a deployment can use to
// reach a server. An adapter subsystem contributes it; a catalog indexes it so
// discovery can answer "which transports can this deployment reach servers
// over?".
type TransportDescriptor struct {
	// ID is the transport identifier servers refer to, unique within a catalog.
	ID string
	// Name is the human-readable transport name.
	Name string
	// Version is the version of this descriptor.
	Version string
	// Description explains what the transport is and when to use it.
	Description string
	// Schemes are the URL schemes the transport uses.
	Schemes []string
	// SupportsStreaming states whether the transport streams responses.
	SupportsStreaming bool
	// Provider is the subsystem that contributed the descriptor.
	Provider string
	// Metadata holds additional descriptor attributes.
	Metadata map[string]string
}

// Clone returns a deep copy of the descriptor.
func (d TransportDescriptor) Clone() TransportDescriptor {
	clone := d
	clone.Schemes = append([]string(nil), d.Schemes...)
	clone.Metadata = copyMetadata(d.Metadata)
	return clone
}

// Source records where a description came from and whether it has changed.
type Source struct {
	// Kind states how the description was obtained.
	Kind SourceKind
	// Location is a file path, URL, or endpoint the description came from.
	Location string
	// Digest is a content digest of the description document, so a consumer
	// can detect that a registered API changed without re-reading it.
	Digest string
}

// Server is a network location that hosts one or more registered APIs.
type Server struct {
	// ID is the stable server identifier, unique within a catalog.
	ID string
	// Name is a human-readable server name.
	Name string
	// BaseURL is the root URL used to invoke the APIs hosted here.
	BaseURL string
	// Format is the identifier of the description format this server serves. It
	// may be empty for a server that hosts more than one format.
	Format Format
	// Transport is the identifier of the transport used to reach this server.
	Transport Transport
	// Description is a human-readable server description.
	Description string
	// Source records where the contract this server serves was read from, when it
	// was read: a document a caller supplied, or an endpoint that described
	// itself. It is absent for a server someone registered by hand.
	Source Source
	// Status is the operational state of the server.
	Status ServerStatus
	// RegisteredAt is when the server entered the catalog.
	RegisteredAt time.Time
	// Metadata holds additional server attributes.
	Metadata map[string]string
}

// API is the standard description of one API.
type API struct {
	// ID is the stable API identifier, unique within a catalog.
	ID string
	// Name is a short API name.
	Name string
	// Version is the API's own version.
	Version string
	// Title is the human-readable API title.
	Title string
	// Description is the human-readable API description.
	Description string
	// Format is the identifier of the description format this API was parsed
	// from. It refers to a FormatDescriptor the catalog indexes.
	Format Format
	// Source records where the description came from.
	Source Source
	// ServerIDs are the registered servers that host this API.
	ServerIDs []string
	// DeclaredServers are the locations the description document itself
	// declares, before any server is registered against it.
	DeclaredServers []DeclaredServer
	// Security are the API-wide security requirements.
	Security []SecurityRequirement
	// SecuritySchemes describes how credentials are supplied.
	SecuritySchemes []SecurityScheme
	// Tags group operations in the description language's own terms.
	Tags []string
	// Services group the API's operations.
	Services []Service
	// Metadata holds additional API attributes.
	Metadata map[string]string
	// Transport is the transport this API's operations are reached over, such as
	// connectrpc, http, or mcp.
	//
	// It belongs to the description because the format knows it: a protocol that
	// names its own transport is stating a fact about how its operations are
	// called, not expressing a preference. A catalog registering the API uses it to
	// choose an invoker, and a description that names none defers the choice to its
	// caller.
	Transport Transport
}

// DeclaredServer is a location declared by a description document.
type DeclaredServer struct {
	// URL is the declared base URL.
	URL string
	// Description explains when the location applies.
	Description string
}

// Service groups the operations of one API. In a gRPC contract a service is a
// protobuf service; in an OpenAPI document it is a tag group; in a
// user-defined format it is whatever that format calls a group.
type Service struct {
	// ID is the stable service identifier, unique within the API.
	ID string
	// Name is the service name, unique within the API.
	Name string
	// Title is the human-readable service name.
	Title string
	// Description is the human-readable service description.
	Description string
	// SideEffects are the effects every operation of this service declares,
	// unless the operation declares its own. A protobuf contract uses this to
	// classify a uniformly read-only service once instead of on every method.
	SideEffects []SideEffect
	// Operations are the callable operations of the service, in description
	// order.
	Operations []Operation
}

// Operation is one callable operation of an API. It is the unit of exposure:
// a tool, a permission, and an approval decision all name an operation.
type Operation struct {
	// ID is the stable operation identifier, unique across the catalog.
	ID string
	// APIID is the identifier of the API that owns the operation.
	APIID string
	// Service is the name of the owning service.
	Service string
	// Name is the operation name, unique within its service.
	Name string
	// Method is the transport method: an HTTP verb, or the method name of a
	// user-defined format.
	Method string
	// Path is the request path template, empty when the transport has no paths.
	Path string
	// Summary is a one-line description of the operation.
	Summary string
	// Description is the full operation description.
	Description string
	// Tags are the description language's tags for the operation.
	Tags []string
	// Parameters are the operation's declared parameters.
	Parameters []Parameter
	// Request is the request value shape, or nil when the operation takes no
	// request value.
	Request *Schema
	// Response is the success response value shape.
	Response *Schema
	// Responses are the documented responses, including error responses.
	Responses []Response
	// Security are the security requirements for this operation.
	Security []SecurityRequirement
	// SideEffects are the declared consequences of invoking the operation. An
	// empty list means unknown, not safe.
	SideEffects []SideEffect
	// Deprecated states that the operation should not be used for new work.
	Deprecated bool
	// Streaming states the streaming behavior of the operation.
	Streaming Streaming
}

// Parameter is one declared input of an operation.
type Parameter struct {
	// Name is the parameter name as the caller supplies it.
	Name string
	// In states where the value is carried, as an open identifier.
	In ParameterIn
	// Description explains the parameter.
	Description string
	// Required states that the operation cannot run without it.
	Required bool
	// Deprecated states that the parameter should not be used for new work.
	Deprecated bool
	// Schema is the parameter value shape.
	Schema *Schema
}

// Response is one documented response of an operation.
type Response struct {
	// Status is the status code, or "default" for a fallback response. It is
	// empty when the transport has no status codes.
	Status string
	// Description explains the response.
	Description string
	// Schema is the response value shape.
	Schema *Schema
}

// Streaming states the streaming behavior of an operation.
type Streaming struct {
	// Client states that the client streams requests.
	Client bool
	// Server states that the server streams responses.
	Server bool
}

// Streaming returns true when either direction streams.
func (s Streaming) Streaming() bool { return s.Client || s.Server }

// SecurityScheme describes how credentials are supplied to an API.
type SecurityScheme struct {
	// Name is the scheme name referenced by security requirements.
	Name string
	// Type is the scheme type, as an open identifier.
	Type SecurityType
	// In states where an API key is carried.
	In ParameterIn
	// Scheme is the authorization scheme, such as bearer.
	Scheme string
	// ParameterName is the name an API-key scheme is carried under: the header,
	// query parameter, or cookie name. It is empty for schemes that do not
	// carry a named parameter.
	ParameterName string
	// Description explains the scheme.
	Description string
	// Scopes are the scopes the scheme defines.
	Scopes []string
}

// SecurityRequirement is one credential requirement of an operation or API.
type SecurityRequirement struct {
	// Scheme is the name of a declared security scheme.
	Scheme string
	// Scopes are the scopes required from that scheme.
	Scopes []string
}

// Property is one named member of an object schema.
type Property struct {
	// Name is the property name.
	Name string
	// Schema is the property value shape.
	Schema *Schema
	// Required states that the property must be present.
	Required bool
}

// Clone returns a deep copy of the property.
func (p Property) Clone() Property {
	clone := p
	clone.Schema = p.Schema.Clone()
	return clone
}

// Clone returns a deep copy of the parameter.
func (p Parameter) Clone() Parameter {
	clone := p
	clone.Schema = p.Schema.Clone()
	return clone
}

// Clone returns a deep copy of the operation.
func (o Operation) Clone() Operation {
	clone := o
	clone.Tags = append([]string(nil), o.Tags...)
	clone.Security = append([]SecurityRequirement(nil), o.Security...)
	clone.SideEffects = append([]SideEffect(nil), o.SideEffects...)
	clone.Streaming = o.Streaming
	clone.Parameters = make([]Parameter, 0, len(o.Parameters))
	for i := range o.Parameters {
		clone.Parameters = append(clone.Parameters, o.Parameters[i].Clone())
	}
	clone.Request = o.Request.Clone()
	clone.Response = o.Response.Clone()
	clone.Responses = make([]Response, 0, len(o.Responses))
	for _, response := range o.Responses {
		clone.Responses = append(clone.Responses, Response{
			Status:      response.Status,
			Description: response.Description,
			Schema:      response.Schema.Clone(),
		})
	}
	return clone
}

// Clone returns a deep copy of the service.
func (s Service) Clone() Service {
	clone := s
	clone.SideEffects = append([]SideEffect(nil), s.SideEffects...)
	clone.Operations = make([]Operation, 0, len(s.Operations))
	for i := range s.Operations {
		clone.Operations = append(clone.Operations, s.Operations[i].Clone())
	}
	return clone
}

// Clone returns a deep copy of the API.
func (a API) Clone() API {
	clone := a
	clone.ServerIDs = append([]string(nil), a.ServerIDs...)
	clone.DeclaredServers = append([]DeclaredServer(nil), a.DeclaredServers...)
	clone.Tags = append([]string(nil), a.Tags...)
	clone.Security = append([]SecurityRequirement(nil), a.Security...)
	clone.SecuritySchemes = make([]SecurityScheme, 0, len(a.SecuritySchemes))
	for _, scheme := range a.SecuritySchemes {
		copied := scheme
		copied.Scopes = append([]string(nil), scheme.Scopes...)
		clone.SecuritySchemes = append(clone.SecuritySchemes, copied)
	}
	clone.Services = make([]Service, 0, len(a.Services))
	for i := range a.Services {
		clone.Services = append(clone.Services, a.Services[i].Clone())
	}
	clone.Metadata = copyMetadata(a.Metadata)
	return clone
}

// Clone returns a deep copy of the server.
func (s Server) Clone() Server {
	clone := s
	clone.Metadata = copyMetadata(s.Metadata)
	return clone
}

func copyMetadata(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

// Operations returns every operation in the API, grouped by service in
// description order. The result is deterministic.
func (a API) Operations() []Operation {
	result := make([]Operation, 0, len(a.Services))
	for _, service := range a.Services {
		result = append(result, service.Operations...)
	}
	return result
}

// Service returns one service by name.
func (a API) Service(name string) (Service, bool) {
	name = strings.TrimSpace(name)
	for _, service := range a.Services {
		if service.Name == name || service.ID == name {
			return service, true
		}
	}
	return Service{}, false
}

// Operation returns one operation by identifier.
func (a API) Operation(id string) (Operation, bool) {
	id = strings.TrimSpace(id)
	for _, service := range a.Services {
		for _, operation := range service.Operations {
			if operation.ID == id {
				return operation, true
			}
		}
	}
	return Operation{}, false
}

// OperationIDs returns every operation identifier in deterministic order.
func (a API) OperationIDs() []string {
	operations := a.Operations()
	ids := make([]string, 0, len(operations))
	for _, operation := range operations {
		ids = append(ids, operation.ID)
	}
	return ids
}

// MetadataKeys returns the metadata keys in sorted order.
func (a API) MetadataKeys() []string { return sortedKeys(a.Metadata) }

// MetadataKeys returns the metadata keys in sorted order.
func (s Server) MetadataKeys() []string { return sortedKeys(s.Metadata) }

// MetadataKeys returns the descriptor metadata keys in sorted order.
func (d FormatDescriptor) MetadataKeys() []string { return sortedKeys(d.Metadata) }

// MetadataKeys returns the descriptor metadata keys in sorted order.
func (d TransportDescriptor) MetadataKeys() []string { return sortedKeys(d.Metadata) }

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ServiceID returns the identifier of a service within an API.
func ServiceID(apiID, serviceName string) string {
	apiID = strings.TrimSpace(apiID)
	serviceName = strings.TrimSpace(serviceName)
	switch {
	case apiID == "":
		return serviceName
	case serviceName == "":
		return apiID
	default:
		return apiID + "/" + serviceName
	}
}

// OperationID returns the identifier of an operation. The identifier is stable
// across description languages: it is built from the API, service, and
// operation names only.
func OperationID(apiID, serviceName, operationName string) string {
	prefix := ServiceID(apiID, serviceName)
	operationName = strings.TrimSpace(operationName)
	if prefix == "" {
		return operationName
	}
	return prefix + "/" + operationName
}

// AnnotateOperationIDs fills in the API, service, and operation identifiers of
// every operation in the API. It reports an error when the API contains
// duplicate service or operation names, because an ambiguous identifier would
// make an exposure decision ambiguous too.
func (a *API) AnnotateOperationIDs() error {
	if a == nil {
		return fmt.Errorf("api is nil")
	}
	apiID := strings.TrimSpace(a.ID)
	if apiID == "" {
		return fmt.Errorf("api id is required")
	}
	a.ID = apiID
	seenServices := make(map[string]bool, len(a.Services))
	for i := range a.Services {
		service := &a.Services[i]
		service.Name = strings.TrimSpace(service.Name)
		if service.Name == "" {
			return fmt.Errorf("api %q has an unnamed service at index %d", apiID, i)
		}
		if seenServices[service.Name] {
			return fmt.Errorf("api %q declares service %q more than once", apiID, service.Name)
		}
		seenServices[service.Name] = true
		service.ID = ServiceID(apiID, service.Name)
		seenOperations := make(map[string]bool, len(service.Operations))
		for j := range service.Operations {
			operation := &service.Operations[j]
			operation.Name = strings.TrimSpace(operation.Name)
			if operation.Name == "" {
				return fmt.Errorf("service %q has an unnamed operation at index %d", service.ID, j)
			}
			if seenOperations[operation.Name] {
				return fmt.Errorf("service %q declares operation %q more than once", service.ID, operation.Name)
			}
			seenOperations[operation.Name] = true
			operation.APIID = apiID
			operation.Service = service.Name
			operation.ID = OperationID(apiID, service.Name, operation.Name)
		}
	}
	return nil
}

// Summary is a compact description of one API for catalog listings.
type Summary struct {
	// ID is the API identifier.
	ID string
	// Name is the short API name.
	Name string
	// Version is the API version.
	Version string
	// Title is the human-readable title.
	Title string
	// Format is the description format identifier.
	Format Format
	// ServerIDs are the servers hosting the API.
	ServerIDs []string
	// Services is the number of services in the API.
	Services int
	// Operations is the number of operations in the API.
	Operations int
}

// Summarize returns a compact listing view of the API.
func (a API) Summarize() Summary {
	operations := 0
	for _, service := range a.Services {
		operations += len(service.Operations)
	}
	return Summary{
		ID:         a.ID,
		Name:       a.Name,
		Version:    a.Version,
		Title:      a.Title,
		Format:     a.Format,
		ServerIDs:  append([]string(nil), a.ServerIDs...),
		Services:   len(a.Services),
		Operations: operations,
	}
}

// NormalizeIdentifier trims and lowercases an identifier, so a comparison of
// open identifiers does not depend on how a caller happened to type one. Format,
// transport, and capability identifiers are chosen by whoever contributes them, so
// they arrive with whatever casing and padding that contributor used.
func NormalizeIdentifier(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
