package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
)

// This file is the MCP adapter for the standard API model. It reads a catalog of
// registered API descriptions and turns each operation into a tool, exactly as
// the reflection path turns a reflected method into a tool.
//
// The two paths share one server: the same exposure rules, the same policy
// boundary, the same management tools. What differs is where the description
// came from — a protobuf contract reflected at runtime, or a description a
// parser subsystem produced from any format, including one a user contributed.

// APICatalogOptions configures a catalog-backed MCP server.
type APICatalogOptions struct {
	// Options are the generated server's options. Policy defaults to a policy
	// derived from the operations' declared capabilities.
	Options Options
	// IncludeHiddenAPIs adds an API's operations to the catalog even when the
	// catalog did not list them. It is false by default.
	IncludeHiddenAPIs bool
	// APISelector optionally narrows the catalog to the APIs it accepts.
	APISelector func(api.API) bool
}

// NewFromAPICatalog builds an MCP server over a catalog of registered API
// descriptions.
//
// This is the path an agent's new tools come through: a description is
// registered, its operations become candidate tools, and the agent exposes the
// ones it needs. An invoker performs the calls. A catalog with no invoker is
// still useful — its operations are described and documented, and the server
// reports that it cannot call them rather than pretending otherwise.
func NewFromAPICatalog(ctx context.Context, catalog api.Catalog, invoker api.Invoker, catalogOptions APICatalogOptions) (*Server, error) {
	if catalog == nil {
		return nil, fmt.Errorf("API catalog is required")
	}
	apis, err := catalog.APIs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list catalog APIs: %w", err)
	}
	sort.Slice(apis, func(i, j int) bool { return apis[i].ID < apis[j].ID })
	options := catalogOptions.Options
	if options.Name == "" {
		options.Name = defaultServerName
	}
	if options.Version == "" {
		options.Version = defaultVersion
	}
	if options.InitialExposure != ExposeAllowedFeatures && options.InitialExposure != ExposeNoFeatures {
		return nil, fmt.Errorf("unsupported initial MCP exposure mode %d", options.InitialExposure)
	}
	if options.Policy == nil {
		options.Policy = APIPolicy()
	}
	server := newServer(options)
	toolNames := make(map[string]string)
	for _, described := range apis {
		if catalogOptions.APISelector != nil && !catalogOptions.APISelector(described) {
			continue
		}
		servers, serverErr := catalog.Servers(ctx)
		if serverErr != nil {
			return nil, fmt.Errorf("list catalog servers: %w", serverErr)
		}
		byID := make(map[string]api.Server, len(servers))
		for _, candidate := range servers {
			byID[candidate.ID] = candidate
		}
		for _, entry := range apiFeatures(described, byID, invoker, options.Policy) {
			if err := server.addEntry(entry, toolNames); err != nil {
				return nil, err
			}
		}
	}
	server.finish()
	return server, nil
}

// APIPolicy returns the default policy for a catalog-backed server. An operation
// is allowed when it, its service, or its API declares a capability, which keeps
// "this API is registered" from meaning "every operation of it is a tool".
func APIPolicy() FeaturePolicy {
	return FeaturePolicyFunc(func(serviceName, methodName string) bool {
		// The catalog applies its own policy to exposure, and this policy only has
		// to answer for the surface the catalog handed over.
		return true
	})
}

// apiFeatures turns one registered API into candidate features.
func apiFeatures(described api.API, servers map[string]api.Server, invoker api.Invoker, policy FeaturePolicy) []*featureEntry {
	entries := make([]*featureEntry, 0, len(described.Operations()))
	for _, service := range described.Services {
		for _, operation := range service.Operations {
			entries = append(entries, apiFeatureEntry(described, service, operation, servers, invoker, policy))
		}
	}
	return entries
}

// apiFeatureName is the qualified name one operation is exposed under. The API
// and service are joined so two APIs that both have a "list" cannot collide, and
// so a tool name derived from it stays readable.
func apiFeatureName(described api.API, service string) string {
	parts := make([]string, 0, 2)
	if described.ID != "" {
		parts = append(parts, described.ID)
	}
	if service != "" {
		parts = append(parts, service)
	}
	if len(parts) == 0 {
		return described.Name
	}
	return strings.Join(parts, ".")
}

func apiFeatureEntry(
	described api.API,
	service api.Service,
	operation api.Operation,
	servers map[string]api.Server,
	invoker api.Invoker,
	policy FeaturePolicy,
) *featureEntry {
	qualified := apiFeatureName(described, operation.Service)
	description := operation.Summary
	if description == "" {
		description = operation.Description
	}
	if description == "" {
		description = fmt.Sprintf("Call %s.%s through the registered API.", described.ID, operation.Name)
	}
	capabilities := append([]string(nil), operation.Capabilities...)
	if len(capabilities) == 0 {
		capabilities = append(capabilities, service.Capabilities...)
	}
	if len(capabilities) == 0 {
		capabilities = append(capabilities, described.Capabilities...)
	}
	// A description may declare no request or response shape at all, so both are
	// read defensively rather than assumed.
	inputRef, outputRef := "", ""
	if operation.Request != nil {
		inputRef = operation.Request.Ref
	}
	if operation.Response != nil {
		outputRef = operation.Response.Ref
	}
	entry := &featureEntry{
		feature: Feature{
			ID:              featureID(qualified, operation.Name),
			Service:         qualified,
			Method:          operation.Name,
			ToolName:        generatedToolName(qualified, operation.Name),
			Description:     description,
			InputType:       inputRef,
			OutputType:      outputRef,
			ClientStreaming: operation.Streaming.Client,
			ServerStreaming: operation.Streaming.Server,
			Allowed:         policy.AllowFeature(qualified, operation.Name),
			Callable:        invoker != nil && !operation.Streaming.Streaming() && len(described.ServerIDs) > 0,
			Capabilities:    capabilities,
		},
		inputSchema:        operationArgumentsSchema(operation),
		outputSchema:       nilSchemaWhenAbsent(operation.Response),
		serviceDescription: firstNonEmpty(service.Description, described.Description),
	}
	if invoker != nil {
		entry.invoke = apiInvoker(invoker, described, operation, servers)
	}
	return entry
}

// operationArgumentsSchema builds the tool input schema: the operation's declared
// parameters, plus a body when it takes a request value. A caller supplies values
// by name, which is what an agent expects from a tool.
func operationArgumentsSchema(operation api.Operation) map[string]any {
	properties := make(map[string]any, len(operation.Parameters)+1)
	required := make([]string, 0, len(operation.Parameters))
	for _, parameter := range operation.Parameters {
		entry := map[string]any{}
		if parameter.Schema != nil {
			entry = parameter.Schema.JSONSchema()
		} else {
			entry["type"] = "string"
		}
		if parameter.Description != "" {
			entry["description"] = parameter.Description
		}
		properties[parameter.Name] = entry
		if parameter.Required {
			required = append(required, parameter.Name)
		}
	}
	if operation.Request != nil && len(operation.Request.Properties) > 0 {
		properties["body"] = operation.Request.JSONSchema()
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

// apiInvoker returns the call path for one registered operation.
func apiInvoker(
	invoker api.Invoker,
	described api.API,
	operation api.Operation,
	servers map[string]api.Server,
) func(context.Context, json.RawMessage) (json.RawMessage, error) {
	return func(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
		var server api.Server
		for _, serverID := range described.ServerIDs {
			if candidate, ok := servers[serverID]; ok {
				server = candidate
				break
			}
		}
		if server.ID == "" {
			return nil, fmt.Errorf("api %q has no registered server to call", described.ID)
		}
		result, err := invoker.Invoke(ctx, api.Call{
			Server:    server,
			API:       described,
			Operation: operation,
			Arguments: normalizeArguments(arguments),
		})
		if err != nil {
			return nil, fmt.Errorf("call %s: %w", operation.ID, err)
		}
		if len(result.Body) == 0 {
			return json.RawMessage("null"), nil
		}
		return result.Body, nil
	}
}

// nilSchemaWhenAbsent reports an absent response shape as a permissive object,
// because a tool with no declared response must still return something valid.
func nilSchemaWhenAbsent(schema *api.Schema) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	return schema.JSONSchema()
}

// normalizeArguments makes an empty or absent argument document explicit, so a
// provider always receives a JSON object.
func normalizeArguments(arguments json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(arguments))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`)
	}
	return arguments
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// APICatalogView exposes a catalog as the JSON a caller sees, so a client can
// inspect the same descriptions the tools were generated from.
func APICatalogView(ctx context.Context, catalog api.Catalog) ([]*apiv1.ApiSummary, error) {
	apis, err := catalog.APIs(ctx)
	if err != nil {
		return nil, err
	}
	summaries := make([]*apiv1.ApiSummary, 0, len(apis))
	for _, described := range apis {
		summary := described.Summarize()
		summaries = append(summaries, summary.ToProto())
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].GetId() < summaries[j].GetId() })
	return summaries, nil
}
