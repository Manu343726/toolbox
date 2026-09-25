package openapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Manu343726/toolbox/pkg/api"
)

// Renderer implements the framework's adapter contract: it renders a standard
// API description into an OpenAPI 3.1 document.
//
// It reads only the standard description. Which format that description was
// parsed from is not this renderer's concern, and that is the point: a protobuf
// contract parsed into the standard model and published as an OpenAPI document
// goes through exactly this code path, with no branch on the origin format.
type Renderer struct {
	targets map[string]bool
}

// NewRenderer creates the OpenAPI renderer.
func NewRenderer() *Renderer {
	return &Renderer{targets: map[string]bool{Target: true}}
}

// Targets returns the representations this renderer produces.
func (r *Renderer) Targets() []string {
	result := make([]string, 0, len(r.targets))
	for target := range r.targets {
		result = append(result, target)
	}
	sort.Strings(result)
	return result
}

// RenderApi implements the framework's ApiAdapterService.

type renderOptions struct {
	mediaType string
	options   map[string]string
}

func (o renderOptions) boolean(name string, fallback bool) bool {
	value, ok := o.options[name]
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return fallback
	}
}

// TargetDescriptor describes the representation this renderer produces, so a
// catalog can index what the deployment can publish.
func TargetDescriptor() api.FormatDescriptor {
	return api.FormatDescriptor{
		ID:                   Target,
		Name:                 "OpenAPI document",
		Version:              Version,
		SpecificationVersion: "3.1.0",
		Description:          "An OpenAPI 3.1 document rendered from a standard API description, whatever format that description was parsed from.",
		MediaTypes:           []string{"application/json", "application/yaml"},
		FileExtensions:       []string{"json", "yaml", "yml"},
	}
}

// renderOpenAPI writes an OpenAPI 3.1 document for a standard description.
func renderOpenAPI(described api.API, options renderOptions) ([]byte, []string, error) {
	warnings := make([]string, 0)
	document := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   firstNonEmpty(described.Title, described.Name, described.ID),
			"version": firstNonEmpty(described.Version, "0.0.0"),
		},
		"paths":      make(map[string]any),
		"components": map[string]any{"schemas": make(map[string]any)},
	}
	if described.Description != "" {
		document["info"].(map[string]any)["description"] = described.Description
	}
	if len(described.DeclaredServers) > 0 {
		servers := make([]any, 0, len(described.DeclaredServers))
		for _, server := range described.DeclaredServers {
			entry := map[string]any{"url": server.URL}
			if server.Description != "" {
				entry["description"] = server.Description
			}
			servers = append(servers, entry)
		}
		document["servers"] = servers
	}
	if options.boolean("x-toolbox-include-capabilities", true) {
		// Capabilities and side effects are Toolbox declarations, not OpenAPI
		// ones, so they travel as extensions. Dropping them would mean a rendered
		// document could no longer be parsed back into something the catalog is
		// willing to expose.
		document[ExtensionCapabilities] = described.Capabilities
	}
	if len(described.SecuritySchemes) > 0 {
		schemes := make(map[string]any, len(described.SecuritySchemes))
		for _, scheme := range described.SecuritySchemes {
			entry := map[string]any{"type": scheme.Type}
			for key, value := range map[string]string{
				"description": scheme.Description,
				"in":          scheme.In,
				"name":        scheme.ParameterName,
				"scheme":      scheme.Scheme,
			} {
				if value != "" {
					entry[key] = value
				}
			}
			if len(scheme.Scopes) > 0 {
				scopes := make(map[string]any, len(scheme.Scopes))
				for _, scope := range scheme.Scopes {
					scopes[scope] = ""
				}
				entry["scopes"] = scopes
			}
			schemes[scheme.Name] = entry
		}
		document["components"].(map[string]any)["securitySchemes"] = schemes
	}
	names := make(map[string]string)
	components := document["components"].(map[string]any)
	paths := document["paths"].(map[string]any)
	for _, service := range described.Services {
		for _, operation := range service.Operations {
			method, path, derived, err := operationMapping(operation, options)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("operation %s is omitted: %v", operation.ID, err))
				continue
			}
			warnings = append(warnings, derived...)
			item, ok := paths[path].(map[string]any)
			if !ok {
				item = make(map[string]any)
				paths[path] = item
			}
			item[method] = renderOperation(operation, names, components, &warnings)
		}
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("api %q has no operation that OpenAPI can represent", described.ID)
	}
	return marshalDocument(document, options)
}

// operationMapping maps a standard operation onto an HTTP method and path.
//
// A description parsed from an HTTP document already carries both. A description
// parsed from something else — a protobuf contract, say — carries a method name
// and no path at all, and refusing to render it would make the standard model
// useless for exactly the conversion it exists to enable. So the conventional
// mapping is applied instead, and every derived value is reported, because a
// rendered document must never pretend a derived path was declared.
func operationMapping(operation api.Operation, options renderOptions) (string, string, []string, error) {
	warnings := make([]string, 0)
	method := strings.ToLower(strings.TrimSpace(operation.Method))
	switch method {
	case "get", "put", "post", "delete", "patch", "head", "options", "trace":
	case "":
		if !options.boolean("derive-http-mapping", true) {
			return "", "", nil, fmt.Errorf("operation has no method")
		}
		method = "post"
		warnings = append(warnings, fmt.Sprintf("operation %s has no method; rendered as POST", operation.ID))
	default:
		// A unary method name such as a protobuf method is not an HTTP verb. The
		// conventional mapping of a remote procedure to HTTP is POST.
		method = "post"
		warnings = append(warnings, fmt.Sprintf(
			"operation %s declares method %q, which is not an HTTP method; rendered as POST",
			operation.ID, operation.Method,
		))
	}
	path := strings.TrimSpace(operation.Path)
	if path == "" {
		if !options.boolean("derive-http-mapping", true) {
			return "", "", nil, fmt.Errorf("operation has no request path")
		}
		path = derivePath(operation, options)
		warnings = append(warnings, fmt.Sprintf(
			"operation %s declares no request path; rendered as %s", operation.ID, path,
		))
	}
	if !strings.HasPrefix(path, "/") {
		return "", "", nil, fmt.Errorf("path %q must start with /", path)
	}
	return method, path, warnings, nil
}

// derivePath builds the conventional path for an operation that has none, from
// its service and method names.
func derivePath(operation api.Operation, options renderOptions) string {
	prefix := strings.TrimSpace(options.options["path-prefix"])
	if prefix == "" {
		prefix = "/"
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	service := shortServiceName(operation.Service)
	method := operation.Name
	if method == "" {
		method = "call"
	}
	return strings.TrimRight(prefix, "/") + "/" + service + "/" + method
}

// shortServiceName reduces a fully-qualified service name to its last segment,
// so a protobuf package prefix does not become path noise.
func shortServiceName(name string) string {
	parts := strings.Split(name, "/")
	service := parts[len(parts)-1]
	if index := strings.LastIndex(service, "."); index >= 0 {
		service = service[index+1:]
	}
	service = strings.TrimSuffix(service, "Service")
	// The segment is lowercased because a derived path is a URL, and a URL segment
	// that differs only in case collides with itself on some servers.
	return sanitizePathSegment(strings.ToLower(service))
}

func sanitizePathSegment(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	segment := strings.Trim(builder.String(), "-")
	if segment == "" {
		return "operation"
	}
	return segment
}

func renderOperation(operation api.Operation, names map[string]string, target map[string]any, warnings *[]string) map[string]any {
	entry := map[string]any{
		"operationId": operation.Name,
		"summary":     operation.Summary,
		"responses": map[string]any{
			"200": map[string]any{"description": firstNonEmpty(operation.Summary, "Success")},
		},
	}
	if operation.Description != "" {
		entry["description"] = operation.Description
	}
	if operation.Deprecated {
		entry["deprecated"] = true
	}
	// A standard description groups operations by service, and OpenAPI has no
	// such concept: it groups by tag. The service name is therefore written as a
	// tag when the operation declares none, so the grouping survives a round trip
	// and a rendered document parsed back yields the same operations.
	tags := operation.Tags
	if len(tags) == 0 && operation.Service != "" {
		tags = []string{operation.Service}
	}
	if len(tags) > 0 {
		entry["tags"] = tags
	}
	parameters := make([]any, 0, len(operation.Parameters))
	for _, parameter := range operation.Parameters {
		converted := map[string]any{
			"name":     parameter.Name,
			"in":       parameter.In,
			"required": parameter.Required,
		}
		if parameter.Description != "" {
			converted["description"] = parameter.Description
		}
		if parameter.Schema != nil {
			converted["schema"] = renderSchema(parameter.Schema, names, target, "parameter", parameter.Name, warnings)
		}
		parameters = append(parameters, converted)
	}
	if len(parameters) > 0 {
		entry["parameters"] = parameters
	}
	if operation.Request != nil {
		entry["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": renderSchema(operation.Request, names, target, "body", operation.Name, warnings),
				},
			},
		}
	}
	if operation.Response != nil {
		entry["responses"].(map[string]any)["200"] = map[string]any{
			"description": firstNonEmpty(operation.Summary, "Success"),
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": renderSchema(operation.Response, names, target, "response", operation.Name, warnings),
				},
			},
		}
	}
	entry[ExtensionCapabilities] = operation.Capabilities
	if len(operation.SideEffects) > 0 {
		entry[ExtensionSideEffects] = operation.SideEffects
	}
	return entry
}

// renderSchema writes a standard schema, extracting a reusable component when
// the same named schema appears more than once, so a rendered document keeps its
// structure instead of repeating it inline.
func renderSchema(schema *api.Schema, names map[string]string, target map[string]any, role, owner string, warnings *[]string) map[string]any {
	if schema == nil {
		return map[string]any{}
	}
	converted := map[string]any{}
	if schema.Type != api.TypeUnspecified {
		converted["type"] = schema.Type.String()
	}
	if schema.Format != "" {
		converted["format"] = schema.Format
	}
	if schema.Title != "" {
		converted["title"] = schema.Title
	}
	if schema.Description != "" {
		converted["description"] = schema.Description
	}
	if schema.Deprecated {
		converted["deprecated"] = true
	}
	if schema.ReadOnly {
		converted["readOnly"] = true
	}
	if schema.Pattern != "" {
		converted["pattern"] = schema.Pattern
	}
	if len(schema.Enum) > 0 {
		converted["enum"] = schema.Enum
	}
	if schema.Items != nil {
		converted["items"] = renderSchema(schema.Items, names, target, role, owner, warnings)
	}
	if schema.AdditionalProperties != nil {
		converted["additionalProperties"] = renderSchema(schema.AdditionalProperties, names, target, role, owner, warnings)
	}
	if len(schema.Properties) > 0 {
		properties := make(map[string]any, len(schema.Properties))
		for _, property := range schema.Properties {
			properties[property.Name] = renderSchema(property.Schema, names, target, role, owner, warnings)
		}
		converted["properties"] = properties
	}
	if len(schema.Required) > 0 {
		converted["required"] = schema.Required
	}
	if len(schema.OneOf) > 0 {
		alternatives := make([]any, 0, len(schema.OneOf))
		for _, alternative := range schema.OneOf {
			alternatives = append(alternatives, renderSchema(alternative, names, target, role, owner, warnings))
		}
		converted["oneOf"] = alternatives
	}
	if len(schema.AnyOf) > 0 {
		alternatives := make([]any, 0, len(schema.AnyOf))
		for _, alternative := range schema.AnyOf {
			alternatives = append(alternatives, renderSchema(alternative, names, target, role, owner, warnings))
		}
		converted["anyOf"] = alternatives
	}
	// A schema that carries a reference and is used more than once becomes a
	// component the document points at, which is what a reader of the rendered
	// document expects to see.
	if schema.Ref != "" {
		name := componentNameFor(schema.Ref, role, owner)
		if existing, seen := names[schema.Ref]; seen {
			if role == "body" || role == "response" {
				converted["$ref"] = "#/components/schemas/" + existing
				delete(converted, "properties")
				delete(converted, "required")
			}
			return converted
		}
		names[schema.Ref] = name
		if role == "body" || role == "response" {
			target["schemas"].(map[string]any)[name] = converted
			return map[string]any{"$ref": "#/components/schemas/" + name}
		}
	}
	return converted
}

func componentNameFor(ref, role, owner string) string {
	parts := strings.Split(strings.TrimPrefix(ref, "/"), "/")
	base := parts[len(parts)-1]
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, base)
	switch role {
	case "body":
		return base + "Request"
	case "response":
		return base + "Response"
	default:
		return base + capitalize(owner)
	}
}

func capitalize(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// marshalDocument encodes a rendered document. JSON is the default because it is
// what agent tooling reads without a parser; YAML is available for a document
// meant to be read by a person or committed to a repository.
func marshalDocument(document map[string]any, options renderOptions) ([]byte, []string, error) {
	asYAML := strings.Contains(strings.ToLower(options.mediaType), "yaml")
	if requested := strings.ToLower(strings.TrimSpace(options.options["format"])); requested == "yaml" || requested == "yml" {
		asYAML = true
	}
	if asYAML {
		encoded, err := yaml.Marshal(document)
		if err != nil {
			return nil, nil, fmt.Errorf("encode YAML document: %w", err)
		}
		return encoded, nil, nil
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode JSON document: %w", err)
	}
	return encoded, nil, nil
}

// Adapter implements the framework's adapter contract by combining the two
// faces of adaptation: it returns the target's schema files, and it can serve the
// target's surface while tunneling to the original server.
//
// Keeping both in one service is deliberate. A schema that a client reads and a
