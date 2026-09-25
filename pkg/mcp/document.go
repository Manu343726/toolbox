package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
)

// This file reads a published tool manifest back into the standard description.
//
// A manifest is the target's own document, so the reader takes it the same way the
// OpenAPI reader takes a document. That is what lets a deployment register an API
// that was published earlier, or carried in a configuration file, with no server
// running at all.

// DocumentSource names where a manifest came from, for the description's record.
const DocumentSource = "manifest"

// DescribeDocument reads a tool manifest document into a standard description.
func DescribeDocument(document []byte, options DescribeOptions) (api.API, []string, error) {
	if len(document) == 0 {
		return api.API{}, nil, api.Errorf(api.KindInvalid, "a tool manifest is required")
	}
	var manifest struct {
		ProtocolVersion string `json:"protocolVersion"`
		Server          struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Version     string `json:"version"`
		} `json:"server"`
		Tools []struct {
			Name         string         `json:"name"`
			Description  string         `json:"description"`
			InputSchema  map[string]any `json:"inputSchema"`
			OutputSchema map[string]any `json:"outputSchema"`
			Annotations  *struct {
				ReadOnlyHint    bool  `json:"readOnlyHint"`
				DestructiveHint *bool `json:"destructiveHint"`
				IdempotentHint  bool  `json:"idempotentHint"`
			} `json:"annotations"`
			Operation string `json:"x-toolbox-operation"`
		} `json:"tools"`
		Warnings []string `json:"x-toolbox-warnings"`
	}
	// either a list or a single string and a typed field would refuse the second.
	raw := map[string]any{}
	if err := json.Unmarshal(document, &raw); err != nil {
		return api.API{}, nil, api.WrapError(api.KindInvalid, err, "read the tool manifest")
	}
	if err := json.Unmarshal(document, &manifest); err != nil {
		return api.API{}, nil, api.WrapError(api.KindInvalid, err, "read the tool manifest")
	}
	if len(manifest.Tools) == 0 {
		return api.API{}, nil, api.Errorf(api.KindInvalid, "the tool manifest declares no tool")
	}
	keep := toolFilter(RenderOptions{Only: options.Tools})
	identifier := strings.TrimSpace(options.APIID)
	if identifier == "" {
		identifier = strings.TrimSpace(manifest.Server.Name)
	}
	if identifier == "" {
		identifier = "mcp-tools"
	}
	serviceName := strings.TrimSpace(options.ServerName)
	if serviceName == "" {
		serviceName = ServerName
	}
	described := api.API{
		ID:          identifier,
		Name:        identifier,
		Version:     strings.TrimSpace(manifest.Server.Version),
		Title:       strings.TrimSpace(manifest.Server.Name),
		Description: strings.TrimSpace(manifest.Server.Description),
		Format:      Format,
		Transport:   Transport,
		Source:      api.Source{Kind: DocumentSource},
		Services:    []api.Service{{Name: serviceName}},
	}
	// The manifest's declared tool name is a client-facing name, and the operation
	// keeps the name its own description used, so an operation read from a manifest
	// and an operation read from a live server are the same operation.
	names := make([]string, 0, len(manifest.Tools))
	for index, tool := range manifest.Tools {
		names = append(names, tool.Name)
		_ = index
	}
	sort.Strings(names)
	warnings := append([]string(nil), manifest.Warnings...)
	for _, name := range names {
		tool := findManifestTool(manifest.Tools, name)
		if !keep(tool.Name) {
			continue
		}
		operation, findings := manifestOperation(tool, raw, serviceName)
		described.Services[0].Operations = append(described.Services[0].Operations, operation)
		warnings = append(warnings, findings...)
	}
	if len(described.Services[0].Operations) == 0 {
		return api.API{}, warnings, api.Errorf(
			api.KindInvalid, "no tool of the manifest matched the request",
		)
	}
	normalized, err := described.Normalize()
	if err != nil {
		return api.API{}, warnings, api.WrapError(api.KindInvalid, err, "normalize the described API")
	}
	return normalized, warnings, nil
}

// manifestEntry is one tool as it appears in a manifest, read without a typed
// shape so an extension's value can be either form.
type manifestEntry struct {
	Name         string
	Description  string
	InputSchema  map[string]any
	OutputSchema map[string]any
	Operation    string
	ReadOnly     bool
	Destructive  bool
	Idempotent   bool
}

func findManifestTool(tools []struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Annotations  *struct {
		ReadOnlyHint    bool  `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
		IdempotentHint  bool  `json:"idempotentHint"`
	} `json:"annotations"`
	Operation string `json:"x-toolbox-operation"`
}, name string) manifestEntry {
	raw := map[string]any{}
	_ = json.Unmarshal([]byte("{}"), &raw)
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		entry := manifestEntry{
			Name:         tool.Name,
			Description:  tool.Description,
			InputSchema:  tool.InputSchema,
			OutputSchema: tool.OutputSchema,
			Operation:    tool.Operation,
		}
		if tool.Annotations != nil {
			entry.ReadOnly = tool.Annotations.ReadOnlyHint
			entry.Idempotent = tool.Annotations.IdempotentHint
			entry.Destructive = tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint
		}
		return entry
	}
	return manifestEntry{}
}

// manifestOperation turns one manifest entry into one operation.
func manifestOperation(tool manifestEntry, raw map[string]any, serviceName string) (api.Operation, []string) {
	name := operationNameFor(tool, serviceName)
	operation := api.Operation{
		Name:        name,
		Method:      ToolCallMethod,
		Summary:     firstLine(tool.Description),
		Description: strings.TrimSpace(tool.Description),
		Request:     api.SchemaFromJSONSchema(tool.InputSchema),
	}
	effects := make([]api.SideEffect, 0, 3)
	if tool.ReadOnly {
		effects = append(effects, api.SideEffectReadOnly)
	}
	if tool.Destructive {
		effects = append(effects, api.SideEffectIrreversible)
	}
	if tool.Idempotent {
		effects = append(effects, sideEffectRepeatable)
	}
	if len(effects) > 0 {
		sort.Slice(effects, func(i, j int) bool { return effects[i] < effects[j] })
		operation.SideEffects = effects
	}
	if len(tool.OutputSchema) > 0 {
		operation.Response = api.SchemaFromJSONSchema(tool.OutputSchema)
	} else {
		operation.Response = callResultSchema()
	}
	warnings := make([]string, 0)
	if strings.TrimSpace(tool.Description) == "" {
		warnings = append(warnings, fmt.Sprintf("tool %q declares no description", tool.Name))
	}
	return operation, warnings
}

// operationNameFor recovers the operation name a manifest's tool name was derived
// from, so a manifest round trip does not rename operations. When the manifest
// recorded the operation's own identifier, that is authoritative.
func operationNameFor(tool manifestEntry, serviceName string) string {
	if tool.Operation != "" {
		if index := strings.LastIndex(tool.Operation, "/"); index >= 0 {
			return tool.Operation[index+1:]
		}
		return tool.Operation
	}
	name := tool.Name
	prefix := toolSegmentFor(serviceName) + "__"
	name = strings.TrimPrefix(name, prefix)
	if name == "" {
		return tool.Name
	}
	return name
}

// toolSegmentFor reduces a service name to the segment a tool name starts with.
func toolSegmentFor(name string) string {
	return strings.TrimSuffix(generatedToolName("", name), "__")
}

// callResultSchema describes the protocol's own result envelope, for a tool that
// declares no result shape of its own. Describing the envelope is more useful than
// describing nothing.
func callResultSchema() *api.Schema {
	return &api.Schema{
		Type:        api.TypeObject,
		Title:       "Tool call result",
		Description: "The Model Context Protocol's result for a tool call.",
		Properties: []api.Property{{
			Name: "content",
			Schema: &api.Schema{
				Type:        api.TypeArray,
				Description: "The result content: text, an image, or an embedded resource.",
				Items:       &api.Schema{Type: api.TypeObject, AdditionalPropertiesAllowed: true},
			},
		}, {
			Name: "structuredContent",
			Schema: &api.Schema{
				Type:        api.TypeObject,
				Description: "The structured result, when the tool declares one.",
			},
		}, {
			Name:     "isError",
			Required: true,
			Schema:   &api.Schema{Type: api.TypeBoolean, Description: "True when the call ended in an error."},
		}},
		Required: []string{"content", "isError"},
	}
}
