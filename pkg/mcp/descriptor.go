package mcp

import (
	"encoding/json"

	"github.com/Manu343726/toolbox/pkg/api"
)

// The descriptors let a catalog index what this implementation reads, writes, and
// reaches, so "can this deployment read an MCP server?" is answerable for an
// identifier the framework itself never enumerates.

// Version is the reference implementation version.
const Version = "0.1.0"

// ToolManifestFile is the file name a rendered tool manifest is published under.
const ToolManifestFile = "mcp-tools.json"

// Formats returns the format identifiers this implementation claims.
func Formats() []api.Format { return []api.Format{Format} }

// Targets returns the representations this implementation renders into.
func Targets() []string { return []string{string(Format)} }

// HandlesFormat reports whether this implementation reads a format.
func HandlesFormat(format api.Format) bool {
	return api.NormalizeIdentifier(string(format)) == string(Format)
}

// HandlesTarget reports whether this implementation renders into a target.
func HandlesTarget(target string) bool {
	return api.NormalizeIdentifier(target) == string(Format)
}

// FormatDescriptor describes the format for a catalog's index.
func FormatDescriptor() api.FormatDescriptor {
	return api.FormatDescriptor{
		ID:          string(Format),
		Name:        "Model Context Protocol",
		Version:     Version,
		Description: "A Model Context Protocol server's tool list, read from a live endpoint's own tools/list response.",
		MediaTypes:  []string{"application/json"},
	}
}

// TargetDescriptor describes the representation this implementation renders.
func TargetDescriptor() api.FormatDescriptor {
	return api.FormatDescriptor{
		ID:                   string(Format),
		Name:                 "Model Context Protocol tool manifest",
		Version:              Version,
		SpecificationVersion: "2025-06-18",
		Description:          "A Model Context Protocol tool manifest rendered from a standard API description, whatever format that description was parsed from.",
		MediaTypes:           []string{"application/json"},
		FileExtensions:       []string{"json"},
	}
}

// TransportDescriptors describes the transports this implementation reaches.
func TransportDescriptors() []api.TransportDescriptor {
	return []api.TransportDescriptor{{
		ID:          string(Transport),
		Name:        "Model Context Protocol",
		Version:     Version,
		Description: "Tool calls to a Model Context Protocol server over Streamable HTTP.",
		Schemes:     []string{"http", "https"},
	}}
}

// Manifest renders a description as the tool manifest document a client is
// offered, which is the file an adapter publishes.
//
// It is the same translation ServeApi would use, written out: the manifest is what
// a reader inspects, and a document that a server can also serve must come from
// the one translation.
func Manifest(described api.API, options RenderOptions) ([]byte, error) {
	rendered, err := Render(described, options)
	if err != nil {
		return nil, err
	}
	tools := make([]map[string]any, 0, len(rendered.Tools))
	for _, tool := range rendered.Tools {
		entry := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		}
		if tool.OutputSchema != nil {
			entry["outputSchema"] = tool.OutputSchema
		}
		entry["x-toolbox-operation"] = tool.OperationID
		if len(tool.Capabilities) > 0 {
			entry[CapabilitiesExtension] = tool.Capabilities
		}
		// A description's declared consequences are written as the protocol's own
		// annotations, so a manifest read back says the same thing the description
		// did. Dropping them here would lose exactly what a policy needs.
		if annotations := toolAnnotations(tool); len(annotations) > 0 {
			entry["annotations"] = annotations
		}
		tools = append(tools, entry)
	}
	document := map[string]any{
		"protocolVersion": "2025-06-18",
		"server": map[string]any{
			"name":        firstNonEmpty(described.Name, described.ID),
			"description": rendered.Instructions,
		},
		"tools": tools,
	}
	if len(rendered.Warnings) > 0 {
		// Findings travel with the document, so a reader sees what was left out
		// instead of inferring it from what is missing.
		document["x-toolbox-warnings"] = rendered.Warnings
	}
	return indentJSON(document)
}

// toolAnnotations renders a tool's declared consequences as the protocol's
// annotations.
func toolAnnotations(tool ToolDefinition) map[string]any {
	annotations := map[string]any{}
	if tool.ReadOnly {
		annotations["readOnlyHint"] = true
	}
	if tool.Destructive {
		annotations["destructiveHint"] = true
	}
	if tool.Idempotent {
		annotations["idempotentHint"] = true
	}
	if len(annotations) == 0 {
		return nil
	}
	return annotations
}

func indentJSON(document map[string]any) ([]byte, error) {
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, api.WrapError(api.KindInternal, err, "encode the tool manifest")
	}
	return encoded, nil
}
