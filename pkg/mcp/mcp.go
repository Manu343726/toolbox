// Package mcp generates a Model Context Protocol server from reflected
// ConnectRPC services. It creates feature tools on the fly, keeps a small
// always-available introspection surface, and lets a client reduce its tool
// footprint by exposing or hiding individual RPC methods at runtime.
//
// Reflection supplies schemas and invocation mechanics. A policy is the separate
// authorization boundary that decides which operations may be exposed or called,
// and it is supplied by the deployment rather than derived from what reflection
// found.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/discovery"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

const (
	// ToolListServices lists the services represented by this MCP server.
	ToolListServices = "list_services"
	// ToolListFeatures lists reflected RPC methods and their exposure state.
	ToolListFeatures = "list_features"
	// ToolDescribeFeature returns one feature's schema and documentation.
	ToolDescribeFeature = "describe_feature"
	// ToolReadFeatureDocumentation returns documentation without exposing the
	// feature as a generated tool.
	ToolReadFeatureDocumentation = "read_feature_documentation"
	// ToolExposeFeature adds one allowed feature to tools/list.
	ToolExposeFeature = "expose_feature"
	// ToolHideFeature removes one feature from tools/list.
	ToolHideFeature = "hide_feature"
	// ToolFeatureExposure reports the current footprint.
	ToolFeatureExposure = "feature_exposure"
	// ToolCallRPC invokes an allowed, exposed unary RPC by service and method.
	ToolCallRPC = "call_rpc"

	defaultVersion    = "0.1.0"
	defaultServerName = "toolbox"
	// Keep generated names below the 128-character MCP limit even when a
	// third-party service uses a very long protobuf name.
	maxToolNameLength = 100
)

// ProtocolRevision is the MCP protocol revision this gateway serves.
//
// It is stated here rather than read from the SDK, which keeps its own constant
// unexported, because two things depend on it and both should depend on one
// declaration: an extension that specifies a minimum revision — the Skills extension
// requires 2026-07-28 or later — and the transport configuration that makes this
// revision reachable at all, since the SDK's streamable HTTP transport serves it only
// when stateless.
//
// If the SDK's newest revision moves, this moves with it, and an extension that pinned
// a minimum is checked against this rather than against a literal of its own.
const ProtocolRevision = "2026-07-28"

// InitialExposure selects the initial generated-tool footprint.
type InitialExposure uint8

const (
	// ExposeAllowedFeatures exposes every policy-allowed unary feature. It is
	// the zero value so a normal standalone MCP is immediately usable.
	ExposeAllowedFeatures InitialExposure = iota
	// ExposeNoFeatures starts with only the management/introspection tools.
	ExposeNoFeatures
)

// Options configures an MCP server generated from a Source.
type Options struct {
	// Name is the MCP server implementation name. Empty uses "toolbox".
	Name string
	// Version is the MCP server implementation version.
	Version string
	// Description is returned to clients as server instructions.
	Description string
	// Policy decides which operations may be exposed. The zero value permits
	// nothing: a gateway with no policy exposes no tools, because "no policy" and
	// "every policy" must not be the same value. A deployment that has decided its
	// whole surface is available says so with APIPolicy.
	Policy api.Policy
	// InitialExposure controls which allowed unary features are present in the
	// initial tools/list surface. The zero value exposes them all; use
	// ExposeNoFeatures to start with only the management/introspection tools
	// and let a client grow its footprint deliberately.
	InitialExposure InitialExposure
	// IncludeReflection includes the protocol's reflection services, which
	// describe a contract rather than provide a capability. It is false by default,
	// because a client that already knows the contract has no use for it.
	IncludeReflection bool
	// Skills serves the MCP Skills extension from this gateway. Nil, the default,
	// declares no extension: a server that declared one it did not implement would be
	// making a claim it cannot keep, and a client reading declarations the
	// specification-correct way would look for methods that answer "not found".
	Skills SkillsSource
}

// Feature is one reflected RPC method that can be exposed as an MCP tool.
type Feature struct {
	ID              string `json:"id"`
	Service         string `json:"service"`
	Method          string `json:"method"`
	ToolName        string `json:"tool_name"`
	Description     string `json:"description,omitempty"`
	InputType       string `json:"input_type,omitempty"`
	OutputType      string `json:"output_type,omitempty"`
	ClientStreaming bool   `json:"client_streaming,omitempty"`
	ServerStreaming bool   `json:"server_streaming,omitempty"`
	Allowed         bool   `json:"allowed"`
	Exposed         bool   `json:"exposed"`
	Callable        bool   `json:"callable"`
	// SideEffects are what the contract declared invoking this operation does. An
	// empty list means the contract said nothing, which is not the same as a read.
	SideEffects []api.SideEffect `json:"side_effects,omitempty"`
}

// ServiceSummary is a compact description of one reflected service.
type ServiceSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Features    int    `json:"features"`
	Exposed     int    `json:"exposed"`
}

// featureEntry is one exposable tool plus everything needed to call it. The
// entry is deliberately provider-neutral: a reflected protobuf method and a
// registered API operation produce the same shape, which is what lets one MCP
// server serve both.
type featureEntry struct {
	feature Feature
	// owner is whatever tells this operation apart from one that would otherwise
	// reduce to the same tool name: an API identifier, or the endpoint that serves
	// the service. It is empty when nothing shares the name.
	owner string
	// inputSchema and outputSchema are JSON Schemas, produced either from a
	// reflected protobuf message or from a standard API description.
	inputSchema  map[string]any
	outputSchema map[string]any
	// invoke performs the call and returns the response as JSON.
	invoke func(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error)
	// documentation is the extracted prose for this method, when the source
	// carried any.
	documentation *shareddocs.Method
	// serviceDescription is the service's documentation, when the source carried
	// any.
	serviceDescription string
}

// Server is a generated MCP server over one Source. Its exposure state is
// scoped to this server instance. A stdio deployment has one instance per
// process; an aggregated deployment has one instance for all endpoints.
type Server struct {
	source  Source
	options Options
	sdk     *sdkmcp.Server
	// skills is the source the Skills extension is served from, or nil when the
	// deployment serves none. It is written once during construction and read only by
	// the handlers the SDK dispatches, so it needs no lock of its own.
	skills SkillsSource

	mu      sync.RWMutex
	entries map[string]*featureEntry
	order   []string
}

// New reflects the source and builds an MCP server without starting a
// transport. Call Run, ServeStdio, or HTTPHandler to expose it.
func New(ctx context.Context, source Source, options Options) (*Server, error) {
	if source == nil {
		return nil, fmt.Errorf("MCP source is required")
	}
	if options.Name == "" {
		options.Name = defaultServerName
	}
	if options.Version == "" {
		options.Version = defaultVersion
	}
	if options.InitialExposure != ExposeAllowedFeatures && options.InitialExposure != ExposeNoFeatures {
		return nil, fmt.Errorf("unsupported initial MCP exposure mode %d", options.InitialExposure)
	}
	serviceNames, err := source.ListServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list MCP source services: %w", err)
	}
	sort.Strings(serviceNames)
	// Every advertised service is described, and every method it declares is a
	// candidate. A service a deployment does not want is denied or hidden, not
	// invisible: what the manifests declare is what the surface offers, and the
	// policy decides what an agent gets.
	entries := make([]*featureEntry, 0, 32)
	candidates := make([]toolNameOwner, 0, 32)
	for _, serviceName := range serviceNames {
		if !options.IncludeReflection && isInfrastructure(serviceName) {
			continue
		}
		schema, describeErr := source.DescribeService(ctx, serviceName)
		if describeErr != nil {
			return nil, fmt.Errorf("describe MCP service %q: %w", serviceName, describeErr)
		}
		if schema == nil {
			continue
		}
		for i := range schema.Methods {
			method := schema.Methods[i]
			if method.Name == "" || method.Input == nil || method.Output == nil {
				return nil, fmt.Errorf("service %q contains an invalid reflected method at index %d", serviceName, i)
			}
			entry := reflectedEntry(source, serviceName, method, schema, options)
			entries = append(entries, entry)
			candidates = append(candidates, toolNameOwner{
				qualified: serviceName,
				owner:     entry.owner,
				method:    method.Name,
			})
		}
	}
	// Names are assigned across the whole surface, so an operation that shares a
	// name with another is qualified rather than colliding with it. That is what
	// several subsystems serving one contract produces, and it is by design.
	sortOwners(candidates)
	namer := newToolNamer(candidates)

	server, err := newServer(options)
	if err != nil {
		return nil, err
	}
	toolNames := make(map[string]string)
	for _, entry := range entries {
		if err := server.addEntry(entry, toolNames, namer); err != nil {
			return nil, err
		}
	}
	server.finish()
	return server, nil
}

// reflectedEntry builds one candidate from a reflected method.
func reflectedEntry(
	source Source,
	serviceName string,
	method discovery.MethodSchema,
	schema *discovery.ServiceSchema,
	options Options,
) *featureEntry {
	qualified := serviceName
	// The policy answers once per method, over the operation's identifier and
	// what its contract says invoking it does.
	allowed := options.Policy.Allows(api.OperationFacts{
		ID:          featureID(serviceName, method.Name),
		SideEffects: contractSideEffects(schema.Documentation, method.Name),
	})
	entry := &featureEntry{
		feature: Feature{
			ID:              featureID(qualified, method.Name),
			Service:         qualified,
			Method:          method.Name,
			ToolName:        generatedToolName(qualified, method.Name),
			InputType:       string(method.Input.FullName()),
			OutputType:      string(method.Output.FullName()),
			ClientStreaming: method.ClientStreaming,
			ServerStreaming: method.ServerStreaming,
			Allowed:         allowed,
			// A streaming method has no unary invocation, so it is listed and
			// describable but never offered as a tool.
			Callable:    !method.ClientStreaming && !method.ServerStreaming,
			SideEffects: contractSideEffects(schema.Documentation, method.Name),
			// The entry decides its own initial exposure, because what a
			// source can say about it differs: a reflected method knows only
			// the policy, while a catalog operation also knows what the
			// catalog decided. An operation that cannot be called is never
			// exposed, whatever decided that.
			Exposed: allowed && !method.ClientStreaming && !method.ServerStreaming &&
				options.InitialExposure == ExposeAllowedFeatures,
		},
		// The endpoint that serves a service is what tells two operations with
		// the same short name apart, so it is carried with the entry.
		owner:        serviceOwner(source, serviceName),
		inputSchema:  jsonSchemaForMessage(method.Input, documentationParameters(documentationMethod(schema.Documentation, method.Name))),
		outputSchema: jsonSchemaForMessage(method.Output, nil),
		invoke: func(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
			return invokeProto(ctx, source, serviceName, method.Name, method.Input, arguments)
		},
		serviceDescription: serviceDescription(schema.Documentation),
	}
	if methodDoc := documentationMethod(schema.Documentation, method.Name); methodDoc != nil {
		entry.feature.Description = methodDoc.Description
		entry.documentation = methodDoc
	}
	if entry.feature.Description == "" {
		entry.feature.Description = fmt.Sprintf("Call %s.%s through the Toolbox RPC gateway.", serviceName, method.Name)
	}
	return entry
}

// serviceOwner returns the endpoint that serves a service, when the source can say.
// It is what disambiguates two operations that reduce to the same tool name.
func serviceOwner(source Source, serviceName string) string {
	provider, ok := source.(serviceOwnerProvider)
	if !ok {
		return ""
	}
	return provider.ServiceOwner(serviceName)
}

// serviceOwnerProvider is a source that can report which endpoint serves a service.
type serviceOwnerProvider interface {
	// ServiceOwner returns the endpoint name serving the service, or empty.
	ServiceOwner(string) string
}

// newServer creates a server with the always-on management surface. The
// provider-neutral entries are added afterwards, so every MCP this package
// generates behaves identically whichever description it came from.
func newServer(options Options) (*Server, error) {
	// Capabilities are fixed when the server is created, so the extension is declared here
	// rather than afterwards. The specification says extensions are declared in the
	// `extensions` field of the `server/discover` response at revision 2026-07-28, and that
	// field is built from the server's capabilities — so a declaration made later would not
	// appear where a client looks for it.
	capabilities := &sdkmcp.ServerCapabilities{}
	if options.Skills != nil {
		capabilities.AddExtension(SkillsExtension, map[string]any{
			// The extension's own setting, and the gate on its third method.
			"directoryRead": DirectoryRead,
		})
		// A skill's files are read with the standard resources/read, which the extension
		// requires. It is declared explicitly because a deployment that has integrated
		// catalogs but whose project has not used a skill yet would otherwise infer no
		// resources capability and serve the extension with a method that cannot work.
		capabilities.Resources = &sdkmcp.ResourceCapabilities{}
	}
	server := &Server{
		options: options,
		entries: make(map[string]*featureEntry),
		sdk: sdkmcp.NewServer(
			&sdkmcp.Implementation{Name: options.Name, Version: options.Version},
			&sdkmcp.ServerOptions{
				Instructions: options.Description,
				// A non-nil value is required to declare anything at all: nil means the
				// SDK's historical default of the logging capability alone, which is not
				// what a gateway serves. Fields it does not set are still inferred.
				Capabilities: capabilities,
			},
		),
	}
	if err := server.installSkills(options.Skills); err != nil {
		return nil, err
	}
	return server, nil
}

// addEntry registers one feature, refusing a duplicate identifier or tool name
// because a collision would make one of the two unreachable.
//
// A namer qualifies a name that more than one operation claims, which is what
// several providers serving one contract produces. Without one, the short name is
// used and a collision is an error, because a name that reaches the wrong operation
// is worse than a refused server.
func (s *Server) addEntry(entry *featureEntry, toolNames map[string]string, namer *toolNamer) error {
	if _, exists := s.entries[entry.feature.ID]; exists {
		return fmt.Errorf("duplicate feature %q", entry.feature.ID)
	}
	if namer != nil {
		entry.feature.ToolName = namer.name(toolNameOwner{
			qualified: entry.feature.Service,
			owner:     entry.owner,
			method:    entry.feature.Method,
		})
	}
	if previous, exists := toolNames[entry.feature.ToolName]; exists {
		return fmt.Errorf("generated MCP tool name %q collides for %q and %q", entry.feature.ToolName, previous, entry.feature.ID)
	}
	toolNames[entry.feature.ToolName] = entry.feature.ID
	s.entries[entry.feature.ID] = entry
	s.order = append(s.order, entry.feature.ID)
	return nil
}

// finish sorts the feature order, installs the management tools, and installs the
// tools the entries decided to expose.
//
// An entry decides its own initial exposure, because what it knows differs: a
// reflected method knows only the policy, while a catalog operation knows what the
// catalog decided about it. This method installs what they decided and nothing
// more.
func (s *Server) finish() {
	sort.Strings(s.order)
	s.addManagementTools()
	for _, id := range s.order {
		entry := s.entries[id]
		if entry.feature.Allowed && entry.feature.Callable && entry.feature.Exposed {
			s.addFeatureTool(entry)
		}
	}
}

// invokeProto performs one dynamic unary call and returns the response as JSON.
func invokeProto(
	ctx context.Context,
	source Source,
	serviceName, methodName string,
	input protoreflect.MessageDescriptor,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	request := dynamicpb.NewMessage(input)
	if len(bytes.TrimSpace(arguments)) > 0 && string(bytes.TrimSpace(arguments)) != "null" {
		if err := (protojson.UnmarshalOptions{}).Unmarshal(arguments, request); err != nil {
			return nil, fmt.Errorf("decode request for %s/%s: %w", serviceName, methodName, err)
		}
	}
	response, err := source.Invoke(ctx, serviceName, methodName, request)
	if err != nil {
		return nil, fmt.Errorf("call %s/%s: %w", serviceName, methodName, err)
	}
	encoded, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode response for %s/%s: %w", serviceName, methodName, err)
	}
	return encoded, nil
}

// contractSideEffects returns what a contract says invoking a method does.
//
// The documentation a reflection read resolved is where that lives: the descriptor
// set a contract's build embedded, because a generated Go descriptor has no source
// locations. A method whose contract said nothing has no side effects here, which
// is the unclassified case a policy has to be told about rather than infer.
func contractSideEffects(documentation *shareddocs.Service, methodName string) []api.SideEffect {
	method := documentationMethod(documentation, methodName)
	if method == nil {
		return nil
	}
	effects, _ := api.SideEffects(method.Annotations)
	return effects
}

func documentationMethod(service *shareddocs.Service, methodName string) *shareddocs.Method {
	if service == nil {
		return nil
	}
	for i := range service.Methods {
		if service.Methods[i].Name == methodName {
			return &service.Methods[i]
		}
	}
	return nil
}

// Features returns a deterministic snapshot of all reflected features.
func (s *Server) Features() []Feature {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Feature, 0, len(s.order))
	for _, id := range s.order {
		entry := s.entries[id]
		feature := entry.feature
		feature.SideEffects = append([]api.SideEffect(nil), feature.SideEffects...)
		result = append(result, feature)
	}
	return result
}

// Services returns deterministic per-service summaries.
func (s *Server) Services() []ServiceSummary {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	byName := make(map[string]*ServiceSummary)
	order := make([]string, 0)
	for _, id := range s.order {
		entry := s.entries[id]
		summary, ok := byName[entry.feature.Service]
		if !ok {
			summary = &ServiceSummary{
				Name:        entry.feature.Service,
				Description: entry.serviceDescription,
			}
			byName[entry.feature.Service] = summary
			order = append(order, entry.feature.Service)
		}
		summary.Features++
		if entry.feature.Exposed {
			summary.Exposed++
		}
	}
	sort.Strings(order)
	result := make([]ServiceSummary, 0, len(order))
	for _, name := range order {
		summary := byName[name]

		result = append(result, *summary)
	}
	return result
}

// IsExposed reports whether a feature currently contributes a generated tool.
func (s *Server) IsExposed(id string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[normalizeFeatureID(id)]
	return ok && entry.feature.Exposed
}

// Expose adds an allowed unary feature to tools/list. It is idempotent.
func (s *Server) Expose(id string) error {
	return s.setExposure(id, true)
}

// Hide removes a feature from tools/list and rejects calls to it. It is
// idempotent.
func (s *Server) Hide(id string) error {
	return s.setExposure(id, false)
}

func (s *Server) setExposure(id string, exposed bool) error {
	if s == nil {
		return fmt.Errorf("MCP server is nil")
	}
	id = normalizeFeatureID(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("feature %q was not found", id)
	}
	if exposed {
		if !entry.feature.Allowed {
			return fmt.Errorf("feature %q is not authorized by the MCP feature policy", id)
		}
		if !entry.feature.Callable {
			return fmt.Errorf("feature %q is streaming; the unary MCP generator cannot expose it", id)
		}
	}
	if entry.feature.Exposed == exposed {
		return nil
	}
	entry.feature.Exposed = exposed
	if exposed {
		s.addFeatureTool(entry)
	} else {
		s.sdk.RemoveTools(entry.feature.ToolName)
	}
	return nil
}

func (s *Server) addFeatureTool(entry *featureEntry) {
	tool := &sdkmcp.Tool{
		Name:         entry.feature.ToolName,
		Description:  entry.feature.Description,
		InputSchema:  entry.inputSchema,
		OutputSchema: entry.outputSchema,
	}
	s.sdk.AddTool(tool, func(ctx context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return s.callFeature(ctx, entry.feature.ID, requestArguments(request))
	})
}

func documentationParameters(method *shareddocs.Method) []shareddocs.Parameter {
	if method == nil {
		return nil
	}
	return method.Parameters
}

func requestArguments(request *sdkmcp.CallToolRequest) json.RawMessage {
	if request == nil || request.Params == nil {
		return nil
	}
	return request.Params.Arguments
}

func (s *Server) callFeature(ctx context.Context, id string, arguments json.RawMessage) (*sdkmcp.CallToolResult, error) {
	s.mu.RLock()
	entry, ok := s.entries[normalizeFeatureID(id)]
	if !ok {
		s.mu.RUnlock()
		return toolError(fmt.Errorf("feature %q was not found", id)), nil
	}
	if !entry.feature.Allowed {
		s.mu.RUnlock()
		return toolError(fmt.Errorf("feature %q is not authorized by the MCP feature policy", id)), nil
	}
	if !entry.feature.Exposed {
		s.mu.RUnlock()
		return toolError(fmt.Errorf("feature %q is hidden; call %s first", id, ToolExposeFeature)), nil
	}
	if !entry.feature.Callable {
		s.mu.RUnlock()
		return toolError(fmt.Errorf("feature %q is streaming; streaming MCP invocation is not implemented", id)), nil
	}
	invoke := entry.invoke
	s.mu.RUnlock()
	if invoke == nil {
		return toolError(fmt.Errorf("feature %q has no invocation path in this deployment", id)), nil
	}
	encoded, err := invoke(ctx, arguments)
	if err != nil {
		return toolError(err), nil
	}
	return jsonToolResult(encoded), nil
}

func jsonToolResult(encoded []byte) *sdkmcp.CallToolResult {
	result := &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(encoded)}},
	}
	var structured any
	if json.Unmarshal(encoded, &structured) == nil {
		result.StructuredContent = structured
	}
	return result
}

func toolError(err error) *sdkmcp.CallToolResult {
	result := &sdkmcp.CallToolResult{}
	result.SetError(err)
	return result
}

// Run serves the generated MCP server over the supplied SDK transport.
func (s *Server) Run(ctx context.Context, transport sdkmcp.Transport) error {
	if s == nil || s.sdk == nil {
		return fmt.Errorf("MCP server is not initialized")
	}
	return s.sdk.Run(ctx, transport)
}

// ServeStdio serves the generated MCP server over newline-delimited JSON on
// stdin/stdout. Logging is kept on stderr by the SDK, so stdout remains a
// valid MCP transport.
func (s *Server) ServeStdio(ctx context.Context) error {
	err := s.Run(ctx, &sdkmcp.StdioTransport{})
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, sdkmcp.ErrConnectionClosed) || strings.Contains(err.Error(), "server is closing")) {
		return nil
	}
	return err
}

// HTTPHandler returns a Streamable HTTP handler for the generated server. The
// current exposure state is process-wide; independent deployments should use
// independent Server instances when session-isolated footprints are required.
//
// The handler is stateless, and that is what lets it serve protocol revision
// 2026-07-28: the SDK's streamable HTTP transport supports that revision only when
// Stateless is set, because the revision is sessionless by design — a stateless
// server issues no Mcp-Session-Id, reads none, and answers each request from a
// temporary session.
//
// Nothing is lost by it here. This server holds no per-session state: the exposure
// footprint is process-wide, which is the framework's design — exposure belongs to a
// deployment, not to a connection. It also makes no server-to-client request, so the
// one thing a stateless transport cannot do has nothing to refuse.
//
// What a stateless HTTP endpoint cannot do is elicit, and that is a division of
// labour rather than a shortfall. A deployment that needs the server to ask its user
// something runs the same gateway over stdio, which supports the same revision with
// sessions. AGENTS.md records which transport a capability needs.
func (s *Server) HTTPHandler() http.Handler {
	if s == nil || s.sdk == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "MCP server is not initialized", http.StatusServiceUnavailable)
		})
	}
	return sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return s.sdk
	}, &sdkmcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
}

func featureID(serviceName, methodName string) string {
	return strings.TrimSpace(serviceName) + "/" + strings.TrimSpace(methodName)
}

func normalizeFeatureID(id string) string {
	return strings.TrimSpace(id)
}

// matchesService reports whether a stored service name is the one a caller named.
//
// A feature's service carries the API it was registered under, because several providers
// serve the same contract on purpose and a bare contract name would not tell two of them
// apart. That prefix is the framework's disambiguator, not the caller's: a caller holds
// contract names from list_services and has no reason to know which API registered one. So
// the bare fully-qualified name is accepted, and the qualified form is still accepted.
//
// A suffix match is only ever a match on a whole segment, so "Service" cannot match
// "KnowledgeService" and a caller who names a service by the wrong half of a word is told
// so rather than served somebody else's operations.
func matchesService(stored, wanted string) bool {
	stored = strings.TrimSpace(stored)
	wanted = strings.TrimSpace(wanted)
	if stored == "" || wanted == "" {
		return false
	}
	if stored == wanted {
		return true
	}
	return strings.HasSuffix(stored, "."+wanted)
}

// splitFeatureReference separates a service from a method in a "service/method" id.
func splitFeatureReference(reference string) (service, method string) {
	index := strings.LastIndex(reference, "/")
	if index < 0 {
		return "", strings.TrimSpace(reference)
	}
	return strings.TrimSpace(reference[:index]), strings.TrimSpace(reference[index+1:])
}

// generatedToolName reduces a service and a method to a tool name. A name claimed
// by more than one operation is qualified with its owner; see toolNamer.
func generatedToolName(serviceName, methodName string) string {
	return shortToolName(serviceName, methodName)
}

func snakeCase(value string) string {
	var builder strings.Builder
	for i, r := range value {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				builder.WriteByte('_')
			}
			builder.WriteRune(r + ('a' - 'A'))
		} else if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	return strings.Trim(builder.String(), "_")
}

func capToolName(name string) string {
	if len(name) <= maxToolNameLength {
		return name
	}
	sum := sha1.Sum([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:8]
	return name[:maxToolNameLength-len(suffix)-1] + "_" + suffix
}

// isInfrastructure reports whether a service belongs to the protocol or the
// framework rather than to a subsystem: the reflection services, which exist so a
// client can discover a contract and are not a feature to call.
//
// Nothing else is exempt. A subsystem's health check, its registry, and the
// framework's own extension contracts are services like any other, and whether an
// agent gets them is decided by what they declare and by exposure — not by a list
// of names.
func isInfrastructure(name string) bool {
	return discovery.IsReflectionService(name)
}

func serviceDescription(service *shareddocs.Service) string {
	if service == nil {
		return ""
	}
	return service.Description
}

// CallToolForTest invokes a generated tool by feature identifier and returns its
// JSON content, so a composition can prove the whole call path without speaking
// the transport. It is the same path the stdio and Streamable HTTP handlers take.
func (s *Server) CallToolForTest(ctx context.Context, reference string, arguments json.RawMessage) ([]byte, error) {
	result, err := s.callFeature(ctx, reference, arguments)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("feature %q produced no result", reference)
	}
	if result.IsError {
		return nil, fmt.Errorf("%s", textOfResult(result))
	}
	return jsonOfResult(result), nil
}

func textOfResult(result *sdkmcp.CallToolResult) string {
	for _, content := range result.Content {
		if text, ok := content.(*sdkmcp.TextContent); ok {
			return text.Text
		}
	}
	return "the tool reported an error without a message"
}

func jsonOfResult(result *sdkmcp.CallToolResult) []byte {
	if result.StructuredContent != nil {
		if encoded, err := json.Marshal(result.StructuredContent); err == nil {
			return encoded
		}
	}
	return []byte(textOfResult(result))
}
