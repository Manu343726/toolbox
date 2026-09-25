package mcp

import (
	"fmt"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/discovery"
)

// This file is the translation from the standard model to an MCP tool.
//
// It exists as one function because there are two consumers with different jobs: a
// gateway serves the tools it describes and forwards each call, while an adapter
// publishes the tools as a document. If the two built their tool definitions
// separately they would drift, and a client that learned a tool name from one path
// would find a different tool on the other.

// ToolDefinition is one tool as an MCP client sees it: a name, a description, and
// JSON Schemas for what it takes and what it returns.
type ToolDefinition struct {
	// Name is the tool's name, which is also its identifier to a client.
	Name string
	// Description is the prose a model reads to decide whether to call it.
	Description string
	// InputSchema is the tool's arguments as a JSON Schema object.
	InputSchema map[string]any
	// OutputSchema is the tool's result as a JSON Schema object, when the
	// description declares a result shape.
	OutputSchema map[string]any
	// Capabilities are the declared capabilities the tool's operation is covered
	// by. They are empty when nothing declared them, which is what keeps a
	// described operation unexposable until a deployment says what it authorizes.
	Capabilities []string
	// ReadOnly states that the tool does not modify its environment, as its
	// description declares.
	ReadOnly bool
	// Destructive states that the tool may make changes that cannot be undone.
	Destructive bool
	// Idempotent states that calling the tool again has no further effect.
	Idempotent bool
	// OperationID is the standard model's identifier for the operation this tool
	// came from, so a tool and its description can be matched in both directions.
	OperationID string
	// Method is the operation's method name, which a client never sees but a
	// consumer of the document needs in order to find the operation again.
	Method string
	// Qualified is the API-and-service name the operation is exposed under.
	Qualified string
}

// Rendered is a target representation: the tools an MCP client would be offered,
// and what translating the description cost.
type Rendered struct {
	// Tools are the tool definitions, in a deterministic order.
	Tools []ToolDefinition
	// Instructions are the server-level prose a client shows the model, taken from
	// the description when it has any.
	Instructions string
	// Warnings are the non-fatal findings of the translation. An operation this
	// package could not represent is always reported here, so a tool that is absent
	// is never silently absent.
	Warnings []string
}

// RenderOptions configures a translation.
type RenderOptions struct {
	// IncludeInfrastructure keeps a subsystem's own services — health, the
	// registry, the documentation service, and the framework's extension
	// contracts. It is false by default, because an agent does not need a
	// platform's plumbing offered to it.
	IncludeInfrastructure bool
	// ExcludeTools drops the named tools.
	ExcludeTools []string
	// Only keeps the named tools.
	Only []string
	// Instructions overrides the server-level prose.
	Instructions string
}

// Render translates a standard description into MCP tool definitions.
//
// It reads only the standard description. Which format that description was parsed
// from is not this function's concern, and that is the point: a protobuf contract
// read into the standard model and published as MCP tools is the same operation as
// publishing an OpenAPI document's.
func Render(described api.API, options RenderOptions) (Rendered, error) {
	keep := toolFilter(options)
	tools := make([]ToolDefinition, 0, len(described.Operations()))
	warnings := make([]string, 0)
	for _, feature := range describeFeatures(described, options.IncludeInfrastructure) {
		if !keep(feature.operation.Name) {
			continue
		}
		if feature.streaming.Streaming() {
			// A streaming operation cannot be a tool: an MCP tool call is one
			// request and one response, and pretending otherwise would produce a
			// tool that hangs.
			warnings = append(warnings, fmt.Sprintf(
				"operation %s is streaming and is not offered as a tool", feature.operation.ID,
			))
			continue
		}
		tools = append(tools, feature.definition())
	}
	if len(tools) == 0 && len(warnings) == 0 {
		return Rendered{}, api.Errorf(
			api.KindInvalid,
			"api %q has no operation that MCP can represent as a tool",
			described.ID,
		)
	}
	instructions := options.Instructions
	if instructions == "" {
		instructions = firstNonEmpty(described.Description, described.Title, described.Name)
	}
	return Rendered{Tools: tools, Instructions: instructions, Warnings: warnings}, nil
}

func toolFilter(options RenderOptions) func(string) bool {
	excluded := make(map[string]bool, len(options.ExcludeTools))
	for _, name := range options.ExcludeTools {
		if name = strings.TrimSpace(name); name != "" {
			excluded[name] = true
		}
	}
	only := make(map[string]bool, len(options.Only))
	for _, name := range options.Only {
		if name = strings.TrimSpace(name); name != "" {
			only[name] = true
		}
	}
	return func(name string) bool {
		if excluded[name] {
			return false
		}
		return len(only) == 0 || only[name]
	}
}

// feature is one operation as a tool, before any decision about who calls it.
type feature struct {
	described api.API
	service   api.Service
	operation api.Operation
	qualified string
	streaming api.Streaming
}

func (f feature) definition() ToolDefinition {
	description := f.operation.Summary
	if description == "" {
		description = f.operation.Description
	}
	if description == "" {
		description = fmt.Sprintf("Call %s.%s.", f.qualified, f.operation.Name)
	}
	return ToolDefinition{
		Name:         generatedToolName(f.qualified, f.operation.Name),
		Description:  description,
		InputSchema:  operationArgumentsSchema(f.operation),
		OutputSchema: absentResponseSchema(f.operation.Response),
		Capabilities: f.capabilities(),
		ReadOnly:     declaresEffect(f.operation, api.SideEffectReadOnly),
		Destructive:  declaresEffect(f.operation, api.SideEffectIrreversible),
		Idempotent:   declaresEffect(f.operation, sideEffectRepeatable),
		OperationID:  f.operation.ID,
		Method:       f.operation.Name,
		Qualified:    f.qualified,
	}
}

// capabilities resolves the capabilities that cover an operation: its own, else its
// service's, else its API's. The first one that declares something wins, because a
// narrower declaration is the more specific claim.
func (f feature) capabilities() []string {
	if len(f.operation.Capabilities) > 0 {
		return append([]string(nil), f.operation.Capabilities...)
	}
	if len(f.service.Capabilities) > 0 {
		return append([]string(nil), f.service.Capabilities...)
	}
	return append([]string(nil), f.described.Capabilities...)
}

// declaresEffect reports whether an operation declared one consequence. The set is
// open, so an effect this package does not recognise is carried through untouched
// rather than dropped.
func declaresEffect(operation api.Operation, effect api.SideEffect) bool {
	for _, declared := range operation.SideEffects {
		if declared == effect {
			return true
		}
	}
	return false
}

// describeFeatures turns a description's operations into features, in the order a
// client sees them.
func describeFeatures(described api.API, includeInfrastructure bool) []feature {
	features := make([]feature, 0, len(described.Operations()))
	for _, service := range described.Services {
		if !includeInfrastructure && discovery.IsInfrastructureService(service.Name) {
			continue
		}
		for _, operation := range service.Operations {
			features = append(features, feature{
				described: described,
				service:   service,
				operation: operation,
				qualified: apiFeatureName(described, operation.Service),
				streaming: operation.Streaming,
			})
		}
	}
	return features
}
