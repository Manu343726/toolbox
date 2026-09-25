package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This file reads a Model Context Protocol server into the standard description.
//
// It is the parser half of the MCP format: an MCP server describes itself through
// its tool list, exactly as a gRPC server describes itself through reflection. A
// caller hands it an endpoint and gets back a description it can register, expose,
// and serve like any other.
//
// What a tool manifest states and what it does not is the important part. A tool
// states its name, its prose, its argument schema, and optional annotations about
// whether it modifies the environment. It states nothing about what a deployment
// authorizes, so capabilities stay empty and a policy refuses the operation until
// someone declares otherwise. Annotations are what the server *does* say about
// consequences, so they are carried as side effects.

// Format is the description format an MCP server is read as. It is an identifier a
// catalog indexes; nothing in the framework branches on it.
const Format api.Format = "mcp"

// Transport is the transport an MCP server is reached over.
const Transport api.Transport = "mcp"

// ToolCallMethod is the protocol method a tool's operation is called by. An
// operation names the method it is invoked with, and for this format the answer is
// always the protocol's own.
const ToolCallMethod = "tools/call"

// SourceKind records that a description came from a live MCP server rather than
// from a document.
const SourceKind = "mcp"

// ServerName is the service name a described MCP server's tools are grouped under
// when the caller does not supply one.
const ServerName = "mcp"

// sideEffectRepeatable is this implementation's word for what the protocol calls an
// idempotent tool: calling it again has no additional effect.
const sideEffectRepeatable api.SideEffect = "repeatable"

// DescribeOptions configures reading an MCP server.
type DescribeOptions struct {
	// HTTPClient performs the protocol calls. The zero value uses a bounded client.
	HTTPClient *http.Client
	// RequestTimeout bounds one protocol call. It defaults to 30 seconds.
	RequestTimeout time.Duration
	// Tools restricts the description to the named tools. Empty describes all of
	// them.
	Tools []string
	// APIID overrides the identifier given to the described API. Empty derives one
	// from the endpoint.
	APIID string
	// ServerName overrides the service name the tools are grouped under.
	ServerName string
}

// Describe reads a live MCP server's tool list into a standard description.
func Describe(ctx context.Context, endpoint string, options DescribeOptions) (api.API, []string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return api.API{}, nil, api.Errorf(api.KindInvalid, "an endpoint is required")
	}
	tools, server, err := listTools(ctx, endpoint, options)
	if err != nil {
		return api.API{}, nil, err
	}
	if len(tools) == 0 {
		return api.API{}, nil, api.Errorf(api.KindInvalid, "the MCP server at %q offers no tool", endpoint)
	}
	keep := toolFilter(RenderOptions{Only: options.Tools})
	identifier := strings.TrimSpace(options.APIID)
	if identifier == "" {
		identifier = apiSlugFromEndpoint(endpoint)
	}
	serviceName := strings.TrimSpace(options.ServerName)
	if serviceName == "" {
		serviceName = ServerName
	}
	service := api.Service{Name: serviceName, Title: firstNonEmpty(server.Title, server.Name)}
	warnings := make([]string, 0)
	described := api.API{
		ID:        identifier,
		Name:      identifier,
		Format:    Format,
		Transport: Transport,
		Title:     firstNonEmpty(server.Title, server.Name, identifier),
		Source:    api.Source{Kind: SourceKind, Location: endpoint},
		DeclaredServers: []api.DeclaredServer{{
			URL:         endpoint,
			Description: firstNonEmpty(server.Title, "The Model Context Protocol server."),
		}},
	}
	for _, tool := range tools {
		if !keep(tool.Name) {
			continue
		}
		operation, warning := describeTool(tool)
		service.Operations = append(service.Operations, operation)
		warnings = append(warnings, warning...)
	}
	if len(service.Operations) == 0 {
		return api.API{}, warnings, api.Errorf(
			api.KindInvalid, "no tool of the MCP server at %q matched the request", endpoint,
		)
	}
	sort.Slice(service.Operations, func(i, j int) bool {
		return service.Operations[i].Name < service.Operations[j].Name
	})
	described.Services = []api.Service{service}
	normalized, err := described.Normalize()
	if err != nil {
		return api.API{}, warnings, api.WrapError(api.KindInvalid, err, "normalize the described API")
	}
	return normalized, warnings, nil
}

// Describe implements the shape a composition reads a live endpoint through, so a
// host can describe an MCP server the same way it describes any other.
func (o DescribeOptions) Describe(ctx context.Context, request api.DescribeRequest) (api.DescribeResult, error) {
	described, warnings, err := Describe(ctx, request.BaseURL, o)
	if err != nil {
		return api.DescribeResult{API: described, Warnings: warnings}, err
	}
	if identifier := strings.TrimSpace(request.APIID); identifier != "" && identifier != described.ID {
		described.ID = identifier
		described.Name = identifier
		described, err = described.Normalize()
		if err != nil {
			return api.DescribeResult{Warnings: warnings}, api.WrapError(api.KindInvalid, err, "normalize the described API")
		}
	}
	if kind := strings.TrimSpace(request.Source.Kind); kind != "" {
		described.Source.Kind = kind
	}
	return api.DescribeResult{API: described, Warnings: warnings, Formats: []api.FormatDescriptor{FormatDescriptor()}}, nil
}

// describeTool turns one tool into one operation.
func describeTool(tool *sdkmcp.Tool) (api.Operation, []string) {
	operation := api.Operation{
		Name:        tool.Name,
		Method:      ToolCallMethod,
		Summary:     firstLine(tool.Description),
		Description: strings.TrimSpace(tool.Description),
		// The request is the tool's argument schema, read into the model so the
		// description travels as a description rather than as a document.
		Request: api.SchemaFromJSONSchema(schemaDocument(tool.InputSchema)),
		// A tool manifest states what its tools do, in the vocabulary MCP already
		// defines for exactly that, and nothing about what a deployment authorizes.
		// The two are different questions, and only one of them is the server's.
		SideEffects: sideEffectsOf(tool),
	}
	warnings := make([]string, 0)
	if output := schemaDocument(tool.OutputSchema); len(output) > 0 {
		operation.Response = api.SchemaFromJSONSchema(output)
	} else {
		// A tool that declares no result shape still returns the protocol's result
		// envelope, and describing that is more useful than describing nothing.
		operation.Response = callResultSchema()
	}
	if strings.TrimSpace(tool.Description) == "" {
		warnings = append(warnings, fmt.Sprintf("tool %q declares no description", tool.Name))
	}
	if operation.Request == nil {
		warnings = append(warnings, fmt.Sprintf(
			"tool %q declares no input schema; its arguments are unconstrained", tool.Name,
		))
	}
	return operation, warnings
}

// sideEffectsOf reads what a tool says about its consequences.
//
// Annotations are a statement about the tool's behaviour, not about authorization,
// so they become side effects and never capabilities. A tool that says nothing has
// declared nothing, which is why the list is empty rather than assumed safe.
func sideEffectsOf(tool *sdkmcp.Tool) []api.SideEffect {
	annotations := tool.Annotations
	if annotations == nil {
		return nil
	}
	effects := make([]api.SideEffect, 0, 3)
	if annotations.ReadOnlyHint {
		effects = append(effects, api.SideEffectReadOnly)
	}
	if annotations.DestructiveHint != nil && *annotations.DestructiveHint {
		effects = append(effects, api.SideEffectIrreversible)
	}
	if annotations.IdempotentHint {
		// Repeating the call has no further effect. The framework documents no word
		// for it, and side effects are an open vocabulary: an identifier this
		// implementation contributes travels like any other unrecognized effect,
		// preserved rather than dropped.
		effects = append(effects, sideEffectRepeatable)
	}
	if len(effects) == 0 {
		return nil
	}
	sort.Slice(effects, func(i, j int) bool { return effects[i] < effects[j] })
	return effects
}

// schemaDocument reads a tool's schema, which the protocol leaves as "any value
// that marshals to valid JSON Schema".
func schemaDocument(value any) map[string]any {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]any:
		return typed
	case json.RawMessage:
		document := map[string]any{}
		if err := json.Unmarshal(typed, &document); err != nil {
			return nil
		}
		return document
	case []byte:
		document := map[string]any{}
		if err := json.Unmarshal(typed, &document); err != nil {
			return nil
		}
		return document
	default:
		// A schema the SDK has already decoded arrives as a struct, so it is
		// re-encoded rather than reflected over.
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		document := map[string]any{}
		if err := json.Unmarshal(encoded, &document); err != nil {
			return nil
		}
		return document
	}
}

// firstLine takes the first line of a tool's prose, which is what a model reads
// when it decides whether to look further.
func firstLine(text string) string {
	trimmed := strings.TrimSpace(text)
	if index := strings.IndexAny(trimmed, "\r\n"); index >= 0 {
		return strings.TrimSpace(trimmed[:index])
	}
	return trimmed
}

// apiSlugFromEndpoint derives an identifier from a server's endpoint.
//
// The host is used, without its port: a port is an accident of one run, and an
// identifier that changed every time the process restarted would replace a
// registration rather than refresh it. A caller that wants a particular name passes
// one; this is only the default.
func apiSlugFromEndpoint(endpoint string) string {
	host := strings.TrimSpace(endpoint)
	for _, scheme := range []string{"https://", "http://"} {
		host = strings.TrimPrefix(host, scheme)
	}
	host = strings.TrimRight(host, "/")
	if index := strings.IndexAny(host, "/?#"); index >= 0 {
		host = host[:index]
	}
	// A bracketed IPv6 authority keeps its brackets, which are part of the address.
	if index := strings.LastIndex(host, ":"); index >= 0 && !strings.HasSuffix(host, "]") {
		host = host[:index]
	}
	host = strings.Trim(host, "[]")
	var builder strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			builder.WriteRune(r)
		default:
			// A host may carry characters an identifier should not, such as an
			// underscore in an internal name; they are dropped rather than
			// replaced, so the identifier stays readable.
		}
	}
	slug := strings.Trim(builder.String(), ".-")
	if slug == "" {
		return "mcp-server"
	}
	return slug
}

// listTools opens a session with a server and reads its tool list.
func listTools(ctx context.Context, endpoint string, options DescribeOptions) ([]*sdkmcp.Tool, sdkmcp.Implementation, error) {
	invoker := NewInvoker(InvokerOptions{
		HTTPClient:     options.HTTPClient,
		RequestTimeout: options.RequestTimeout,
	})
	defer func() { _ = invoker.Close() }()
	current, err := invoker.sessionFor(ctx, endpoint)
	if err != nil {
		return nil, sdkmcp.Implementation{}, err
	}
	listed, err := current.ListTools(ctx, nil)
	if err != nil {
		return nil, sdkmcp.Implementation{}, api.WrapError(
			api.KindUnavailable, err, "list the tools of the MCP server at %s", endpoint,
		)
	}
	server := sdkmcp.Implementation{}
	if initialization := current.InitializeResult(); initialization != nil && initialization.ServerInfo != nil {
		server = *initialization.ServerInfo
	}
	tools := make([]*sdkmcp.Tool, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		if tool != nil {
			tools = append(tools, tool)
		}
	}
	// A stable order makes two reads of one server equal, so a re-registration
	// that changed nothing changes nothing in the catalog either.
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, server, nil
}
