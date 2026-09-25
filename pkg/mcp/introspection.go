package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addManagementTools() {
	s.addRawTool(ToolListServices, "List the ConnectRPC services represented by this MCP server, including feature counts and exposure totals.", objectSchema(nil, nil), s.handleListServices)
	s.addRawTool(ToolListFeatures, "List reflected RPC features. Hidden features remain discoverable so a client can expose only the operations it needs and keep its prompt footprint small.", objectSchema(map[string]any{
		"service":            map[string]any{"type": "string", "description": "Optional fully-qualified service name filter."},
		"include_disallowed": map[string]any{"type": "boolean", "description": "Include methods denied by the feature policy. Default false."},
	}, nil), s.handleListFeatures)
	s.addRawTool(ToolDescribeFeature, "Describe one reflected RPC feature, including its generated tool name, input/output JSON Schema, capabilities, exposure state, and documentation.", featureReferenceSchema(true), s.handleDescribeFeature)
	s.addRawTool(ToolReadFeatureDocumentation, "Read documentation for one reflected RPC feature without exposing its generated tool.", featureReferenceSchema(true), s.handleReadFeatureDocumentation)
	s.addRawTool(ToolExposeFeature, "Expose one allowed unary RPC feature in tools/list. Use list_features first to discover its fully-qualified id. This reduces the default tool footprint to the requested features.", featureReferenceSchema(true), s.handleExposeFeature)
	s.addRawTool(ToolHideFeature, "Hide one generated RPC feature from tools/list and reject direct calls until it is exposed again. Hidden features remain discoverable through list_features and describe_feature.", featureReferenceSchema(true), s.handleHideFeature)
	s.addRawTool(ToolFeatureExposure, "Report the current feature footprint: allowed, exposed, hidden, and denied methods, optionally filtered by service.", objectSchema(map[string]any{
		"service": map[string]any{"type": "string", "description": "Optional fully-qualified service name filter."},
	}, nil), s.handleFeatureExposure)
	s.addRawTool(ToolCallRPC, "Call one allowed unary RPC method by fully-qualified service and method name. The method must be explicitly exposed first; this is a generic escape hatch, not a way around the feature policy.", objectSchema(map[string]any{
		"service": map[string]any{"type": "string", "description": "Fully-qualified protobuf service name."},
		"method":  map[string]any{"type": "string", "description": "RPC method name."},
		"request": map[string]any{"type": "object", "description": "Protobuf JSON request object.", "additionalProperties": true},
	}, []string{"service", "method"}), s.handleCallRPC)
}

func (s *Server) addRawTool(name, description string, schema map[string]any, handler func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error)) {
	s.sdk.AddTool(&sdkmcp.Tool{
		Name:        name,
		Description: description,
		InputSchema: schema,
	}, handler)
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func featureReferenceSchema(required bool) map[string]any {
	schema := objectSchema(map[string]any{
		"feature": map[string]any{"type": "string", "description": "Feature id in service/method form, or its generated MCP tool name."},
		"service": map[string]any{"type": "string", "description": "Fully-qualified service name when feature is omitted."},
		"method":  map[string]any{"type": "string", "description": "RPC method name when feature is omitted."},
	}, nil)
	if required {
		// The reference can be supplied either as feature or as the pair
		// service/method, so no single property is marked required. The
		// handler validates the alternative explicitly and returns an
		// actionable error.
		schema["minProperties"] = 1
	}
	return schema
}

func (s *Server) handleListServices(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	return jsonValueResult(map[string]any{"services": s.Services()}), nil
}

func (s *Server) handleListFeatures(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	args, err := decodeArguments(request)
	if err != nil {
		return toolError(err), nil
	}
	serviceName, err := optionalString(args, "service")
	if err != nil {
		return toolError(err), nil
	}
	includeDisallowed, err := optionalBool(args, "include_disallowed")
	if err != nil {
		return toolError(err), nil
	}
	features := make([]Feature, 0)
	for _, feature := range s.Features() {
		if serviceName != "" && feature.Service != serviceName {
			continue
		}
		if !feature.Allowed && !includeDisallowed {
			continue
		}
		features = append(features, feature)
	}
	return jsonValueResult(map[string]any{
		"features": features,
		"total":    len(features),
	}), nil
}

func (s *Server) handleDescribeFeature(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	entry, err := s.lookupRequestFeature(request)
	if err != nil {
		return toolError(err), nil
	}
	if !entry.feature.Allowed {
		return toolError(fmt.Errorf("feature %q is not authorized by the MCP feature policy", entry.feature.ID)), nil
	}
	return jsonValueResult(map[string]any{
		"feature":       entry.feature,
		"documentation": featureDocumentationView(entry),
		"input_schema":  jsonSchemaForMessage(entry.method.Input, documentationParameters(entry.documentation)),
		"output_schema": jsonSchemaForMessage(entry.method.Output, nil),
	}), nil
}

func (s *Server) handleReadFeatureDocumentation(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	entry, err := s.lookupRequestFeature(request)
	if err != nil {
		return toolError(err), nil
	}
	if !entry.feature.Allowed {
		return toolError(fmt.Errorf("feature %q is not authorized by the MCP feature policy", entry.feature.ID)), nil
	}
	return jsonValueResult(featureDocumentationView(entry)), nil
}

func (s *Server) handleExposeFeature(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	entry, err := s.lookupRequestFeature(request)
	if err != nil {
		return toolError(err), nil
	}
	if err := s.Expose(entry.feature.ID); err != nil {
		return toolError(err), nil
	}
	return jsonValueResult(map[string]any{
		"feature": entry.feature.ID,
		"exposed": true,
		"tool":    entry.feature.ToolName,
	}), nil
}

func (s *Server) handleHideFeature(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	entry, err := s.lookupRequestFeature(request)
	if err != nil {
		return toolError(err), nil
	}
	if err := s.Hide(entry.feature.ID); err != nil {
		return toolError(err), nil
	}
	return jsonValueResult(map[string]any{
		"feature": entry.feature.ID,
		"exposed": false,
		"tool":    entry.feature.ToolName,
	}), nil
}

func (s *Server) handleFeatureExposure(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	args, err := decodeArguments(request)
	if err != nil {
		return toolError(err), nil
	}
	serviceName, err := optionalString(args, "service")
	if err != nil {
		return toolError(err), nil
	}
	features := make([]Feature, 0)
	allowed, exposed, hidden, denied := 0, 0, 0, 0
	for _, feature := range s.Features() {
		if serviceName != "" && feature.Service != serviceName {
			continue
		}
		features = append(features, feature)
		if !feature.Allowed {
			denied++
			continue
		}
		allowed++
		if feature.Exposed {
			exposed++
		} else {
			hidden++
		}
	}
	return jsonValueResult(map[string]any{
		"service":  serviceName,
		"allowed":  allowed,
		"exposed":  exposed,
		"hidden":   hidden,
		"denied":   denied,
		"features": features,
	}), nil
}

func (s *Server) handleCallRPC(ctx context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	args, err := decodeArguments(request)
	if err != nil {
		return toolError(err), nil
	}
	serviceName, err := requiredString(args, "service")
	if err != nil {
		return toolError(err), nil
	}
	methodName, err := requiredString(args, "method")
	if err != nil {
		return toolError(err), nil
	}
	payload := json.RawMessage(`{}`)
	if raw, ok := args["request"]; ok && len(raw) > 0 && string(raw) != "null" {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return toolError(fmt.Errorf("request must be a JSON object: %w", err)), nil
		}
		payload = raw
	}
	return s.callFeature(ctx, featureID(serviceName, methodName), payload)
}

func (s *Server) lookupRequestFeature(request *sdkmcp.CallToolRequest) (*featureEntry, error) {
	args, err := decodeArguments(request)
	if err != nil {
		return nil, err
	}
	featureRef, err := optionalString(args, "feature")
	if err != nil {
		return nil, err
	}
	serviceName, err := optionalString(args, "service")
	if err != nil {
		return nil, err
	}
	methodName, err := optionalString(args, "method")
	if err != nil {
		return nil, err
	}
	if featureRef == "" {
		if serviceName == "" || methodName == "" {
			return nil, fmt.Errorf("provide feature or both service and method")
		}
		featureRef = featureID(serviceName, methodName)
	}
	return s.lookupFeature(featureRef)
}

func (s *Server) lookupFeature(reference string) (*featureEntry, error) {
	reference = normalizeFeatureID(reference)
	if reference == "" {
		return nil, fmt.Errorf("feature reference is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if entry, ok := s.entries[reference]; ok {
		return cloneFeatureEntry(entry), nil
	}
	for _, id := range s.order {
		entry := s.entries[id]
		if entry.feature.ToolName == reference {
			return cloneFeatureEntry(entry), nil
		}
	}
	return nil, fmt.Errorf("feature %q was not found", reference)
}

func cloneFeatureEntry(entry *featureEntry) *featureEntry {
	if entry == nil {
		return nil
	}
	clone := *entry
	clone.feature.Capabilities = append([]string(nil), entry.feature.Capabilities...)
	return &clone
}

func decodeArguments(request *sdkmcp.CallToolRequest) (map[string]json.RawMessage, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
		return nil, fmt.Errorf("decode tool arguments: %w", err)
	}
	if args == nil {
		return map[string]json.RawMessage{}, nil
	}
	return args, nil
}

func optionalString(args map[string]json.RawMessage, key string) (string, error) {
	raw, ok := args[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string: %w", key, err)
	}
	return strings.TrimSpace(value), nil
}

func requiredString(args map[string]json.RawMessage, key string) (string, error) {
	value, err := optionalString(args, key)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func optionalBool(args map[string]json.RawMessage, key string) (bool, error) {
	raw, ok := args[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return value, nil
}

func jsonValueResult(value any) *sdkmcp.CallToolResult {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return toolError(fmt.Errorf("encode MCP result: %w", err))
	}
	return jsonToolResult(encoded)
}

type documentationView struct {
	ID              string                   `json:"id"`
	Service         string                   `json:"service"`
	Method          string                   `json:"method"`
	Description     string                   `json:"description,omitempty"`
	InputType       string                   `json:"input_type,omitempty"`
	OutputType      string                   `json:"output_type,omitempty"`
	ClientStreaming bool                     `json:"client_streaming,omitempty"`
	ServerStreaming bool                     `json:"server_streaming,omitempty"`
	Parameters      []documentationParameter `json:"parameters,omitempty"`
}

type documentationParameter struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description,omitempty"`
	TypeName    string                   `json:"type_name,omitempty"`
	Repeated    bool                     `json:"repeated,omitempty"`
	Required    bool                     `json:"required,omitempty"`
	Fields      []documentationParameter `json:"fields,omitempty"`
}

func featureDocumentationView(entry *featureEntry) documentationView {
	view := documentationView{
		ID:              entry.feature.ID,
		Service:         entry.feature.Service,
		Method:          entry.feature.Method,
		Description:     entry.feature.Description,
		InputType:       entry.feature.InputType,
		OutputType:      entry.feature.OutputType,
		ClientStreaming: entry.feature.ClientStreaming,
		ServerStreaming: entry.feature.ServerStreaming,
	}
	if entry.documentation != nil {
		view.Description = entry.documentation.Description
		view.Parameters = convertParameters(entry.documentation.Parameters)
	}
	return view
}

func convertParameters(parameters []shareddocs.Parameter) []documentationParameter {
	if parameters == nil {
		return nil
	}
	result := make([]documentationParameter, 0, len(parameters))
	for _, parameter := range parameters {
		result = append(result, documentationParameter{
			Name:        parameter.Name,
			Description: parameter.Description,
			TypeName:    parameter.TypeName,
			Repeated:    parameter.Repeated,
			Required:    parameter.Required,
			Fields:      convertParameters(parameter.Fields),
		})
	}
	return result
}
