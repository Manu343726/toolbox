package mcp

import (
	"sort"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/discovery"
)

// This file holds the naming and schema decisions the two MCP paths share, so a
// tool is named the same way whether it is served from a catalog or published as a
// document.

// apiFeatureName is the qualified name one API's operations are exposed under. The
// API and service are joined so two APIs that both have a "list" cannot collide, and
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

// operationArgumentsSchema builds a tool's input schema: the operation's declared
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
	// How a request value reaches a tool depends on what the operation declares. An
	// operation with declared parameters and a body keeps them apart, because that
	// is the shape the description had. An operation with no declared parameters
	// has nothing to be apart from: its request value is the whole input, and
	// wrapping it in one opaque "body" argument would hand a model a tool it can
	// only fill in blind. Those arguments are promoted to the top level instead.
	switch {
	case len(operation.Parameters) == 0 && requestHasProperties(operation.Request):
		for _, property := range operation.Request.Properties {
			entry := property.Schema.JSONSchema()
			properties[property.Name] = entry
			if property.Required {
				required = append(required, property.Name)
			}
		}
	case requestHasProperties(operation.Request):
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

// requestHasProperties reports whether a request is an object with named members,
// which is what makes its shape worth surfacing.
func requestHasProperties(request *api.Schema) bool {
	return request != nil && request.Type == api.TypeObject && len(request.Properties) > 0
}

// absentResponseSchema reports a missing result shape as a permissive object,
// because a tool with no declared result must still return something valid.
func absentResponseSchema(schema *api.Schema) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	return schema.JSONSchema()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// isPlumbing reports whether a service belongs to the platform rather than to a
// capability a user adopted, and so is not offered as a tool.
func isPlumbing(name string, includeInfrastructure bool) bool {
	return !includeInfrastructure && discovery.IsInfrastructureService(name)
}
