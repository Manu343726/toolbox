// Package mcp generates a Model Context Protocol server from reflected
// ConnectRPC services. It creates feature tools on the fly, keeps a small
// always-available introspection surface, and lets a client reduce its tool
// footprint by exposing or hiding individual RPC methods at runtime.
//
// Reflection supplies schemas and invocation mechanics. A FeaturePolicy is the
// separate authorization boundary that decides which reflected methods may be
// exposed or called.
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

	"github.com/Manu343726/toolbox/pkg/discovery"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
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
	// Policy authorizes reflected methods. When nil, metadata exposed by the
	// source is used; services without explicit capabilities are denied.
	Policy FeaturePolicy
	// InitialExposure controls which allowed unary features are present in the
	// initial tools/list surface. The zero value exposes them all; use
	// ExposeNoFeatures to start with only the management/introspection tools
	// and let a client grow its footprint deliberately.
	InitialExposure InitialExposure
	// IncludeInfrastructure includes reflection, health, registry, and
	// documentation services in the generated catalog. It is false by
	// default, matching the generated CLI's user-facing surface.
	IncludeInfrastructure bool
}

// Feature is one reflected RPC method that can be exposed as an MCP tool.
type Feature struct {
	ID              string   `json:"id"`
	Service         string   `json:"service"`
	Method          string   `json:"method"`
	ToolName        string   `json:"tool_name"`
	Description     string   `json:"description,omitempty"`
	InputType       string   `json:"input_type,omitempty"`
	OutputType      string   `json:"output_type,omitempty"`
	ClientStreaming bool     `json:"client_streaming,omitempty"`
	ServerStreaming bool     `json:"server_streaming,omitempty"`
	Allowed         bool     `json:"allowed"`
	Exposed         bool     `json:"exposed"`
	Callable        bool     `json:"callable"`
	Capabilities    []string `json:"capabilities,omitempty"`
}

// ServiceSummary is a compact description of one reflected service.
type ServiceSummary struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Features     int      `json:"features"`
	Exposed      int      `json:"exposed"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type featureEntry struct {
	feature       Feature
	schema        *discovery.ServiceSchema
	method        discovery.MethodSchema
	documentation *shareddocs.Method
	serviceDoc    *shareddocs.Service
}

// Server is a generated MCP server over one Source. Its exposure state is
// scoped to this server instance. A stdio deployment has one instance per
// process; an aggregated deployment has one instance for all endpoints.
type Server struct {
	source  Source
	options Options
	sdk     *sdkmcp.Server

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
	if options.Policy == nil {
		options.Policy = policyForSource(source, serviceNames)
	}
	server := &Server{
		source:  source,
		options: options,
		entries: make(map[string]*featureEntry),
		sdk: sdkmcp.NewServer(
			&sdkmcp.Implementation{Name: options.Name, Version: options.Version},
			&sdkmcp.ServerOptions{Instructions: options.Description},
		),
	}
	toolNames := make(map[string]string)
	for _, serviceName := range serviceNames {
		if !options.IncludeInfrastructure && isInfrastructure(serviceName) {
			continue
		}
		schema, describeErr := source.DescribeService(ctx, serviceName)
		if describeErr != nil {
			return nil, fmt.Errorf("describe MCP service %q: %w", serviceName, describeErr)
		}
		if schema == nil {
			continue
		}
		metadata := serviceMetadata(source, serviceName)
		for i := range schema.Methods {
			method := schema.Methods[i]
			if method.Name == "" || method.Input == nil || method.Output == nil {
				return nil, fmt.Errorf("service %q contains an invalid reflected method at index %d", serviceName, i)
			}
			entry := &featureEntry{
				feature: Feature{
					ID:              featureID(serviceName, method.Name),
					Service:         serviceName,
					Method:          method.Name,
					ToolName:        generatedToolName(serviceName, method.Name),
					InputType:       string(method.Input.FullName()),
					OutputType:      string(method.Output.FullName()),
					ClientStreaming: method.ClientStreaming,
					ServerStreaming: method.ServerStreaming,
					Allowed:         options.Policy.AllowFeature(serviceName, method.Name),
					Callable:        !method.ClientStreaming && !method.ServerStreaming,
					Capabilities:    append([]string(nil), metadata.Capabilities...),
				},
				schema:     schema,
				method:     method,
				serviceDoc: schema.Documentation,
			}
			if methodDoc := documentationMethod(schema.Documentation, method.Name); methodDoc != nil {
				entry.feature.Description = methodDoc.Description
				entry.documentation = methodDoc
			}
			if entry.feature.Description == "" {
				entry.feature.Description = fmt.Sprintf("Call %s.%s through the Toolbox RPC gateway.", serviceName, method.Name)
			}
			if _, exists := server.entries[entry.feature.ID]; exists {
				return nil, fmt.Errorf("duplicate reflected feature %q", entry.feature.ID)
			}
			if previous, exists := toolNames[entry.feature.ToolName]; exists {
				return nil, fmt.Errorf("generated MCP tool name %q collides for %q and %q", entry.feature.ToolName, previous, entry.feature.ID)
			}
			toolNames[entry.feature.ToolName] = entry.feature.ID
			server.entries[entry.feature.ID] = entry
			server.order = append(server.order, entry.feature.ID)
		}
	}
	sort.Strings(server.order)
	server.addManagementTools()
	if options.InitialExposure == ExposeAllowedFeatures {
		for _, id := range server.order {
			entry := server.entries[id]
			if entry.feature.Allowed && entry.feature.Callable {
				entry.feature.Exposed = true
				server.addFeatureTool(entry)
			}
		}
	}
	return server, nil
}

func policyForSource(source Source, serviceNames []string) FeaturePolicy {
	metadataSource, ok := source.(interface {
		ServiceMetadata(string) (ServiceMetadata, bool)
	})
	if !ok {
		return DenyAllFeatures()
	}
	services := make([]ServiceMetadata, 0, len(serviceNames))
	for _, name := range serviceNames {
		if metadata, found := metadataSource.ServiceMetadata(name); found {
			services = append(services, metadata)
		}
	}
	return PolicyFromServices(services)
}

func serviceMetadata(source Source, serviceName string) ServiceMetadata {
	if metadataSource, ok := source.(interface {
		ServiceMetadata(string) (ServiceMetadata, bool)
	}); ok {
		if metadata, found := metadataSource.ServiceMetadata(serviceName); found {
			return metadata
		}
	}
	return ServiceMetadata{}
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
		feature.Capabilities = append([]string(nil), feature.Capabilities...)
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
				Name:         entry.feature.Service,
				Description:  serviceDescription(entry.serviceDoc),
				Capabilities: append([]string(nil), entry.feature.Capabilities...),
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
		summary.Capabilities = append([]string(nil), summary.Capabilities...)
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
	schema := jsonSchemaForMessage(entry.method.Input, documentationParameters(entry.documentation))
	tool := &sdkmcp.Tool{
		Name:         entry.feature.ToolName,
		Description:  entry.feature.Description,
		InputSchema:  schema,
		OutputSchema: jsonSchemaForMessage(entry.method.Output, nil),
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
	method := entry.method
	serviceName := entry.feature.Service
	methodName := entry.feature.Method
	s.mu.RUnlock()

	request := dynamicpb.NewMessage(method.Input)
	if len(bytes.TrimSpace(arguments)) > 0 && string(bytes.TrimSpace(arguments)) != "null" {
		if err := (protojson.UnmarshalOptions{}).Unmarshal(arguments, request); err != nil {
			return toolError(fmt.Errorf("decode request for %s: %w", id, err)), nil
		}
	}
	response, err := s.source.Invoke(ctx, serviceName, methodName, request)
	if err != nil {
		return toolError(fmt.Errorf("call %s: %w", id, err)), nil
	}
	encoded, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(response)
	if err != nil {
		return toolError(fmt.Errorf("encode response for %s: %w", id, err)), nil
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
func (s *Server) HTTPHandler() http.Handler {
	if s == nil || s.sdk == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "MCP server is not initialized", http.StatusServiceUnavailable)
		})
	}
	return sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return s.sdk
	}, &sdkmcp.StreamableHTTPOptions{JSONResponse: true})
}

func featureID(serviceName, methodName string) string {
	return strings.TrimSpace(serviceName) + "/" + strings.TrimSpace(methodName)
}

func normalizeFeatureID(id string) string {
	return strings.TrimSpace(id)
}

func generatedToolName(serviceName, methodName string) string {
	parts := strings.Split(serviceName, ".")
	short := parts[len(parts)-1]
	short = strings.TrimSuffix(short, "Service")
	name := snakeCase(short) + "__" + snakeCase(methodName)
	return capToolName(name)
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

func isInfrastructure(name string) bool {
	return discovery.IsReflectionService(name) ||
		strings.HasSuffix(name, ".HealthService") ||
		strings.HasSuffix(name, ".DocumentationService") ||
		strings.HasSuffix(name, ".RegistryService")
}

func serviceDescription(service *shareddocs.Service) string {
	if service == nil {
		return ""
	}
	return service.Description
}
