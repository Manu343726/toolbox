package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Manu343726/toolbox/pkg/api"
)

// Extensions a document may use to declare what the framework needs to know and
// cannot infer. Everything else about an operation is described by the document
// itself; these extensions are the only way it states authorization-relevant
// facts, which keeps a parsed operation honest: an operation nobody declared
// capabilities for stays unexposable.
const (
	// ExtensionCapabilities declares the capabilities an API, operation, or tag
	// grants, as a list of strings.
	ExtensionCapabilities = "x-toolbox-capabilities"
	// ExtensionSideEffects declares additional side effects, as a list of
	// strings, beyond the ones implied by the HTTP method.
	ExtensionSideEffects = "x-toolbox-side-effects"
	// ExtensionDeprecated marks an operation as deprecated beyond the document's
	// own deprecated flag.
	ExtensionDeprecated = "x-toolbox-deprecated"
)

// document is the subset of an OpenAPI 3.x document this parser reads. Unknown
// fields are ignored on purpose: a document may contain anything, and a parser
// that failed on fields it does not understand could not read a real API.
type document struct {
	OpenAPI    string                `yaml:"openapi"`
	Info       info                  `yaml:"info"`
	Servers    []serverEntry         `yaml:"servers"`
	Paths      map[string]pathItem   `yaml:"paths"`
	Components components            `yaml:"components"`
	Security   []map[string][]string `yaml:"security"`
	Tags       []tagEntry            `yaml:"tags"`
	Extensions map[string]yaml.Node  `yaml:",inline"`
	Raw        map[string]yaml.Node  `yaml:"-"`
	_          struct{}              `yaml:"-"`
}

type info struct {
	Title       string               `yaml:"title"`
	Version     string               `yaml:"version"`
	Description string               `yaml:"description"`
	Extensions  map[string]yaml.Node `yaml:",inline"`
}

type serverEntry struct {
	URL         string                    `yaml:"url"`
	Description string                    `yaml:"description"`
	Variables   map[string]serverVariable `yaml:"variables"`
	Extensions  map[string]yaml.Node      `yaml:",inline"`
}

type serverVariable struct {
	Default string `yaml:"default"`
}

type tagEntry struct {
	Name        string               `yaml:"name"`
	Description string               `yaml:"description"`
	Extensions  map[string]yaml.Node `yaml:",inline"`
}

type components struct {
	Schemas         map[string]schemaNode         `yaml:"schemas"`
	Parameters      map[string]parameterNode      `yaml:"parameters"`
	Responses       map[string]responseNode       `yaml:"responses"`
	RequestBodies   map[string]requestBodyNode    `yaml:"requestBodies"`
	SecuritySchemes map[string]securitySchemeNode `yaml:"securitySchemes"`
	Extensions      map[string]yaml.Node          `yaml:",inline"`
}

type securitySchemeNode struct {
	Type        string               `yaml:"type"`
	Description string               `yaml:"description"`
	In          string               `yaml:"in"`
	Name        string               `yaml:"name"`
	Scheme      string               `yaml:"scheme"`
	BearerFmt   string               `yaml:"bearerFormat"`
	Flows       map[string]yaml.Node `yaml:"flows"`
	Scopes      map[string]string    `yaml:"scopes"`
	OpenID      string               `yaml:"openIdConnectUrl"`
	Extensions  map[string]yaml.Node `yaml:",inline"`
}

// pathItem is one entry of the document's paths map. OpenAPI mixes the
// per-method operations and the path-level metadata in the same object, so the
// path item decodes itself and separates the two.
type pathItem struct {
	Summary     string
	Description string
	Parameters  []parameterNode
	Operations  map[string]*operationNode
	Extensions  map[string]yaml.Node
}

// UnmarshalYAML separates the HTTP methods of a path item from its metadata.
func (p *pathItem) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("path item must be a mapping")
	}
	p.Operations = make(map[string]*operationNode)
	p.Extensions = make(map[string]yaml.Node)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		value := node.Content[i+1]
		switch key {
		case "summary", "description":
			var text string
			if err := value.Decode(&text); err != nil {
				return fmt.Errorf("path item %s: %w", key, err)
			}
			if key == "summary" {
				p.Summary = text
			} else {
				p.Description = text
			}
		case "parameters":
			if err := value.Decode(&p.Parameters); err != nil {
				return fmt.Errorf("path item parameters: %w", err)
			}
		case "servers":
			// Path-level servers repeat locations the document already declares.
		default:
			if isHTTPMethod(key) {
				operation := &operationNode{}
				if err := value.Decode(operation); err != nil {
					return fmt.Errorf("path item %s: %w", key, err)
				}
				p.Operations[key] = operation
				continue
			}
			p.Extensions[key] = *value
		}
	}
	return nil
}

func isHTTPMethod(key string) bool {
	for _, method := range httpMethods {
		if method.field == key {
			return true
		}
	}
	return false
}

type operationNode struct {
	OperationID string                  `yaml:"operationId"`
	Summary     string                  `yaml:"summary"`
	Description string                  `yaml:"description"`
	Tags        []string                `yaml:"tags"`
	Parameters  []parameterNode         `yaml:"parameters"`
	RequestBody *requestBodyNode        `yaml:"requestBody"`
	Responses   map[string]responseNode `yaml:"responses"`
	Security    []map[string][]string   `yaml:"security"`
	Deprecated  bool                    `yaml:"deprecated"`
	Extensions  map[string]yaml.Node    `yaml:",inline"`
}

type parameterNode struct {
	Ref         string               `yaml:"$ref"`
	Name        string               `yaml:"name"`
	In          string               `yaml:"in"`
	Description string               `yaml:"description"`
	Required    bool                 `yaml:"required"`
	Deprecated  bool                 `yaml:"deprecated"`
	Schema      *schemaNode          `yaml:"schema"`
	Content     map[string]mediaNode `yaml:"content"`
	Extensions  map[string]yaml.Node `yaml:",inline"`
}

type requestBodyNode struct {
	Ref         string               `yaml:"$ref"`
	Description string               `yaml:"description"`
	Required    bool                 `yaml:"required"`
	Content     map[string]mediaNode `yaml:"content"`
	Extensions  map[string]yaml.Node `yaml:",inline"`
}

type responseNode struct {
	Ref         string                `yaml:"$ref"`
	Description string                `yaml:"description"`
	Content     map[string]mediaNode  `yaml:"content"`
	Headers     map[string]headerNode `yaml:"headers"`
	Extensions  map[string]yaml.Node  `yaml:",inline"`
}

type headerNode struct {
	Description string               `yaml:"description"`
	Required    bool                 `yaml:"required"`
	Schema      *schemaNode          `yaml:"schema"`
	Extensions  map[string]yaml.Node `yaml:",inline"`
}

type mediaNode struct {
	Schema     *schemaNode          `yaml:"schema"`
	Extensions map[string]yaml.Node `yaml:",inline"`
}

type schemaNode struct {
	Ref                  string                `yaml:"$ref"`
	Type                 string                `yaml:"type"`
	Format               string                `yaml:"format"`
	Title                string                `yaml:"title"`
	Description          string                `yaml:"description"`
	Deprecated           bool                  `yaml:"deprecated"`
	ReadOnly             bool                  `yaml:"readOnly"`
	Properties           map[string]schemaNode `yaml:"properties"`
	Required             []string              `yaml:"required"`
	Items                *schemaNode           `yaml:"items"`
	AdditionalProperties *schemaNode           `yaml:"additionalProperties"`
	Enum                 []yaml.Node           `yaml:"enum"`
	OneOf                []*schemaNode         `yaml:"oneOf"`
	AnyOf                []*schemaNode         `yaml:"anyOf"`
	Default              yaml.Node             `yaml:"default"`
	Nullable             bool                  `yaml:"nullable"`
	Minimum              *float64              `yaml:"minimum"`
	Maximum              *float64              `yaml:"maximum"`
	ExclusiveMinimum     *float64              `yaml:"exclusiveMinimum"`
	ExclusiveMaximum     *float64              `yaml:"exclusiveMaximum"`
	MinLength            *int64                `yaml:"minLength"`
	MaxLength            *int64                `yaml:"maxLength"`
	Pattern              string                `yaml:"pattern"`
	Extensions           map[string]yaml.Node  `yaml:",inline"`
}

// httpMethod is one HTTP method a path item may declare, with the side effect
// the method implies. The implied effect is a default the document can extend
// through ExtensionSideEffects, not an authorization decision.
type httpMethod struct {
	field      string
	sideEffect api.SideEffect
}

var httpMethods = []httpMethod{
	{field: "get", sideEffect: api.SideEffectReadOnly},
	{field: "head", sideEffect: api.SideEffectReadOnly},
	{field: "options", sideEffect: api.SideEffectReadOnly},
	{field: "trace", sideEffect: api.SideEffectReadOnly},
	{field: "post", sideEffect: api.SideEffectCreate},
	{field: "put", sideEffect: api.SideEffectUpdate},
	{field: "patch", sideEffect: api.SideEffectUpdate},
	{field: "delete", sideEffect: api.SideEffectDelete},
}

// methodSuccess returns the status code a method conventionally returns.
func methodSuccess(method httpMethod) string {
	switch method.field {
	case "post":
		return "201"
	case "delete":
		return "204"
	default:
		return "200"
	}
}

// parseDocument interprets an OpenAPI 3.x document. The document may be JSON or
// YAML, and the format identifier is the one this subsystem owns.
func parseDocument(data []byte, request parseRequest) (api.API, []string, error) {
	if len(data) == 0 {
		return api.API{}, nil, fmt.Errorf("document is empty")
	}
	digest := sha256.Sum256(data)
	parsed := document{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return api.API{}, nil, fmt.Errorf("decode OpenAPI document: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(parsed.OpenAPI), "3.") {
		return api.API{}, nil, fmt.Errorf("unsupported OpenAPI version %q; this parser handles 3.x", parsed.OpenAPI)
	}
	if strings.TrimSpace(parsed.Info.Title) == "" {
		return api.API{}, nil, fmt.Errorf("OpenAPI document has no info.title")
	}
	identifier := strings.TrimSpace(request.APID)
	if identifier == "" {
		identifier = deriveAPIID(parsed.Info.Title, parsed.Info.Version)
	}
	builder := &builder{document: parsed, warnings: make([]string, 0)}
	target := api.API{
		ID:          identifier,
		Name:        identifier,
		Version:     strings.TrimSpace(parsed.Info.Version),
		Title:       strings.TrimSpace(parsed.Info.Title),
		Description: strings.TrimSpace(parsed.Info.Description),
		Format:      Format,
		Source: api.Source{
			Kind:     request.Source.Kind,
			Location: request.Source.Location,
			Digest:   hex.EncodeToString(digest[:]),
		},
		Tags: capabilitiesFromExtensions(parsed.Extensions),
	}
	if target.Source.Kind == "" {
		target.Source.Kind = "document"
	}
	if target.Tags == nil {
		target.Tags = []string{identifier}
	}
	for _, entry := range parsed.Servers {
		target.DeclaredServers = append(target.DeclaredServers, api.DeclaredServer{
			URL:         expandServerURL(entry),
			Description: strings.TrimSpace(entry.Description),
		})
	}
	for name, node := range parsed.Components.SecuritySchemes {
		scheme := convertSecurityScheme(node)
		// The map key is the scheme name a security requirement refers to; the
		// node's own name field is the parameter an API key travels under.
		scheme.Name = strings.TrimSpace(name)
		target.SecuritySchemes = append(target.SecuritySchemes, scheme)
	}
	sort.Slice(target.SecuritySchemes, func(i, j int) bool { return target.SecuritySchemes[i].Name < target.SecuritySchemes[j].Name })
	target.Security = builder.securityRequirements(parsed.Security, "")
	tagCapabilities := make(map[string][]string, len(parsed.Tags))
	tagDescriptions := make(map[string]string, len(parsed.Tags))
	for _, entry := range parsed.Tags {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		tagCapabilities[name] = capabilitiesFromExtensions(entry.Extensions)
		tagDescriptions[name] = strings.TrimSpace(entry.Description)
	}
	for _, path := range sortedPaths(parsed.Paths) {
		services, err := builder.buildServices(target.ID, path, parsed.Paths[path], tagCapabilities, tagDescriptions)
		if err != nil {
			return api.API{}, nil, err
		}
		mergeServices(&target, services)
	}
	if len(target.Services) == 0 {
		return api.API{}, nil, fmt.Errorf("OpenAPI document declares no operations")
	}
	normalized, err := target.Normalize()
	if err != nil {
		return api.API{}, nil, err
	}
	return normalized, builder.warnings, nil
}

// parseRequest carries the catalog's provenance hints for one document.
type parseRequest struct {
	// APID overrides the identifier assigned to the parsed API.
	APID string
	// Source records where the document came from.
	Source api.Source
}

// builder converts one document, accumulating warnings for anything it could not
// resolve completely. A document that references something missing is a warning
// rather than a failure when the operation is still describable, because a
// partial description is more useful to an agent than no description; a
// document that cannot be read at all is an error.
type builder struct {
	document document
	warnings []string
}

func (b *builder) warn(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	for _, existing := range b.warnings {
		if existing == message {
			return
		}
	}
	b.warnings = append(b.warnings, message)
}

// buildServices converts one path item into the service groups its operations
// declare. A path item may contribute to several groups — a document may tag two
// operations on the same path differently — and groups that share a name are
// merged so a service is one entry, not one per path.
func (b *builder) buildServices(
	apiID, path string,
	item pathItem,
	tagCapabilities map[string][]string,
	tagDescriptions map[string]string,
) ([]api.Service, error) {
	shared, err := b.convertParameters(item.Parameters)
	if err != nil {
		return nil, err
	}
	grouped := make(map[string][]api.Operation)
	order := make([]string, 0)
	for _, method := range httpMethods {
		node, ok := item.Operations[method.field]
		if !ok {
			continue
		}
		operation, err := b.buildOperation(apiID, path, method, node, shared)
		if err != nil {
			return nil, err
		}
		if _, seen := grouped[operation.Service]; !seen {
			order = append(order, operation.Service)
		}
		grouped[operation.Service] = append(grouped[operation.Service], operation)
	}
	if len(order) == 0 {
		return nil, nil
	}
	// A document may declare a tag order; a group whose tag is declared follows
	// it, and the rest keep the order their operations appeared in, so listings
	// stay stable between runs of the same document.
	rank := make(map[string]int, len(b.document.Tags))
	for i, entry := range b.document.Tags {
		rank[strings.TrimSpace(entry.Name)] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		left, leftRanked := rank[order[i]]
		right, rightRanked := rank[order[j]]
		if leftRanked && rightRanked {
			return left < right
		}
		return leftRanked && !rightRanked
	})
	services := make([]api.Service, 0, len(order))
	for _, name := range order {
		operations := grouped[name]
		for i := range operations {
			merged := make([]string, 0, len(tagCapabilities[name])+len(operations[i].Capabilities))
			merged = append(merged, tagCapabilities[name]...)
			merged = append(merged, operations[i].Capabilities...)
			operations[i].Capabilities = deduplicate(merged)
		}
		service := api.Service{
			Name:        name,
			Description: strings.TrimSpace(tagDescriptions[name]),
		}
		if service.Description == "" {
			service.Description = strings.TrimSpace(item.Description)
		}
		service.Operations = operations
		services = append(services, service)
	}
	return services, nil
}

// mergeServices appends a path item's groups to the API, merging with a group of
// the same name that an earlier path already created.
func mergeServices(target *api.API, incoming []api.Service) {
	for _, service := range incoming {
		merged := false
		for i := range target.Services {
			if target.Services[i].Name != service.Name {
				continue
			}
			target.Services[i].Operations = append(target.Services[i].Operations, service.Operations...)
			if target.Services[i].Description == "" {
				target.Services[i].Description = service.Description
			}
			merged = true
			break
		}
		if !merged {
			target.Services = append(target.Services, service)
		}
	}
}

func (b *builder) buildOperation(
	apiID, path string,
	method httpMethod,
	node *operationNode,
	shared []api.Parameter,
) (api.Operation, error) {
	name := strings.TrimSpace(node.OperationID)
	if name == "" {
		// An operation without an identifier still needs a stable name; the
		// method and path are the only thing the document gives us.
		name = sanitizeIdentifier(strings.ToLower(method.field) + path)
		b.warn("operation %s %s has no operationId; using the derived name %q", strings.ToUpper(method.field), path, name)
	}
	parameters := make([]api.Parameter, 0, len(shared)+len(node.Parameters))
	seen := make(map[string]bool, len(shared)+len(node.Parameters))
	for _, parameter := range append(append([]api.Parameter(nil), shared...), mustParameters(b, node.Parameters)...) {
		if seen[parameter.Name] {
			continue
		}
		seen[parameter.Name] = true
		parameters = append(parameters, parameter)
	}
	operation := api.Operation{
		Name:         name,
		Method:       strings.ToUpper(method.field),
		Path:         path,
		Summary:      strings.TrimSpace(node.Summary),
		Description:  strings.TrimSpace(node.Description),
		Tags:         deduplicate(node.Tags),
		Parameters:   parameters,
		Security:     b.securityRequirements(node.Security, ""),
		Capabilities: capabilitiesFromExtensions(node.Extensions),
		Deprecated:   node.Deprecated || boolFromExtensions(node.Extensions, ExtensionDeprecated),
	}
	if service := firstTag(node.Tags); service != "" {
		operation.Service = service
	} else {
		operation.Service = "default"
	}
	operation.SideEffects = sideEffectsFor(method, node.Extensions)
	if body, ok := b.requestSchema(node.RequestBody); ok {
		operation.Request = body
	}
	if response, ok := b.responseSchema(node.Responses, method); ok {
		operation.Response = response
	}
	for _, status := range sortedStatuses(node.Responses) {
		declared := node.Responses[status]
		schema, _ := b.convertSchema(declared.Content)
		operation.Responses = append(operation.Responses, api.Response{
			Status:      status,
			Description: strings.TrimSpace(declared.Description),
			Schema:      schema,
		})
	}
	return operation, nil
}

func mustParameters(b *builder, nodes []parameterNode) []api.Parameter {
	parameters, err := b.convertParameters(nodes)
	if err != nil {
		for _, node := range nodes {
			b.warn("parameter %q in %q could not be resolved", node.Name, node.In)
		}
		return nil
	}
	return parameters
}

func (b *builder) convertParameters(nodes []parameterNode) ([]api.Parameter, error) {
	result := make([]api.Parameter, 0, len(nodes))
	for _, node := range nodes {
		resolved, err := b.resolveParameter(node)
		if err != nil {
			return nil, err
		}
		if resolved == nil {
			continue
		}
		var schema *api.Schema
		if resolved.Schema != nil {
			converted, err := b.convertComponentSchema(resolved.Schema, make(map[string]bool))
			if err != nil {
				return nil, fmt.Errorf("parameter %q: %w", resolved.Name, err)
			}
			schema = converted
		}
		if schema == nil {
			schema = api.StringSchema()
		}
		location := strings.ToLower(strings.TrimSpace(resolved.In))
		// OpenAPI path parameters are always required; the document's own flag
		// is often omitted even though the path cannot be built without it.
		required := resolved.Required || location == api.ParameterInPath
		result = append(result, api.Parameter{
			Name:        strings.TrimSpace(resolved.Name),
			In:          location,
			Description: strings.TrimSpace(resolved.Description),
			Required:    required,
			Deprecated:  resolved.Deprecated,
			Schema:      schema,
		})
	}
	return result, nil
}

func (b *builder) resolveParameter(node parameterNode) (*parameterNode, error) {
	if node.Ref == "" {
		copied := node
		return &copied, nil
	}
	name, ok := componentName(node.Ref, "parameters")
	if !ok {
		b.warn("parameter reference %q is not a local component reference; the parameter is skipped", node.Ref)
		return nil, nil
	}
	declared, found := b.document.Components.Parameters[name]
	if !found {
		return nil, fmt.Errorf("parameter reference %q is not declared in components", node.Ref)
	}
	return b.resolveParameter(declared)
}

func (b *builder) requestSchema(body *requestBodyNode) (*api.Schema, bool) {
	if body == nil {
		return nil, false
	}
	resolved := *body
	if resolved.Ref != "" {
		name, ok := componentName(resolved.Ref, "requestBodies")
		if !ok {
			b.warn("request body reference %q is not a local component reference; the body is treated as free-form", resolved.Ref)
			return api.ObjectSchema(), true
		}
		declared, found := b.document.Components.RequestBodies[name]
		if !found {
			b.warn("request body reference %q is not declared in components; the body is treated as free-form", resolved.Ref)
			return api.ObjectSchema(), true
		}
		resolved = declared
	}
	schema, ok := b.convertSchema(resolved.Content)
	if !ok {
		return api.ObjectSchema(), true
	}
	return schema, true
}

func (b *builder) responseSchema(responses map[string]responseNode, method httpMethod) (*api.Schema, bool) {
	if len(responses) == 0 {
		return nil, false
	}
	for _, status := range []string{"200", "201", "202", "204", methodSuccess(method)} {
		declared, found := responses[status]
		if !found {
			continue
		}
		schema, ok := b.convertSchema(declared.Content)
		if !ok {
			return nil, false
		}
		return schema, true
	}
	return nil, false
}

func (b *builder) convertSchema(content map[string]mediaNode) (*api.Schema, bool) {
	if len(content) == 0 {
		return nil, false
	}
	media, ok := b.pickMedia(content)
	if !ok {
		return nil, false
	}
	if media.Schema == nil {
		return api.ObjectSchema(), true
	}
	schema, err := b.convertComponentSchema(media.Schema, make(map[string]bool))
	if err != nil {
		b.warn("schema could not be converted: %v", err)
		return api.ObjectSchema(), true
	}
	return schema, true
}

// pickMedia chooses the media type to describe. JSON is preferred because it is
// what agent tooling can call with directly, and the choice is reported so a
// document that only describes, say, XML does not silently become a JSON tool.
func (b *builder) pickMedia(content map[string]mediaNode) (mediaNode, bool) {
	if media, ok := content["application/json"]; ok {
		return media, true
	}
	types := make([]string, 0, len(content))
	for mediaType := range content {
		types = append(types, mediaType)
	}
	sort.Strings(types)
	for _, mediaType := range types {
		if strings.Contains(mediaType, "json") {
			return content[mediaType], true
		}
	}
	if len(types) > 0 {
		b.warn("no JSON media type is declared; describing %q instead", types[0])
		return content[types[0]], true
	}
	return mediaNode{}, false
}

func (b *builder) convertComponentSchema(node *schemaNode, visiting map[string]bool) (*api.Schema, error) {
	if node == nil {
		return nil, nil
	}
	if node.Ref != "" {
		name, ok := componentName(node.Ref, "schemas")
		if !ok {
			b.warn("schema reference %q is not a local component reference; treating it as free-form", node.Ref)
			return api.ObjectSchema(), nil
		}
		if visiting[name] {
			return &api.Schema{
				Ref:         node.Ref,
				Type:        api.TypeObject,
				Description: fmt.Sprintf("recursive reference to %s", name),
			}, nil
		}
		declared, found := b.document.Components.Schemas[name]
		if !found {
			return nil, fmt.Errorf("schema reference %q is not declared in components", node.Ref)
		}
		visiting[name] = true
		defer delete(visiting, name)
		converted, err := b.convertComponentSchema(&declared, visiting)
		if err != nil {
			return nil, err
		}
		// The reference is preserved so a consumer can see the document's own
		// naming, and the resolved shape is inlined for one that cannot follow it.
		converted.Ref = node.Ref
		return converted, nil
	}
	schema := &api.Schema{
		Format:      strings.TrimSpace(node.Format),
		Title:       strings.TrimSpace(node.Title),
		Description: strings.TrimSpace(node.Description),
		Deprecated:  node.Deprecated,
		ReadOnly:    node.ReadOnly,
		Pattern:     strings.TrimSpace(node.Pattern),
		Minimum:     node.Minimum,
		Maximum:     node.Maximum,
		MinLength:   node.MinLength,
		MaxLength:   node.MaxLength,
	}
	schema.Type = schemaTypeFor(node, schema)
	if len(node.Properties) > 0 {
		names := make([]string, 0, len(node.Properties))
		for name := range node.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		required := make(map[string]bool, len(node.Required))
		for _, name := range node.Required {
			required[name] = true
		}
		requiredList := make([]string, 0, len(node.Required))
		for _, name := range node.Required {
			requiredList = append(requiredList, name)
		}
		sort.Strings(requiredList)
		for _, name := range names {
			property := node.Properties[name]
			converted, err := b.convertComponentSchema(&property, visiting)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", name, err)
			}
			schema.Properties = append(schema.Properties, api.Property{
				Name:     name,
				Schema:   converted,
				Required: required[name],
			})
		}
		schema.Required = requiredList
	}
	if node.Items != nil {
		items, err := b.convertComponentSchema(node.Items, visiting)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		schema.Items = items
	}
	if node.AdditionalProperties != nil {
		additional, err := b.convertComponentSchema(node.AdditionalProperties, visiting)
		if err != nil {
			return nil, fmt.Errorf("additionalProperties: %w", err)
		}
		schema.AdditionalProperties = additional
	}
	if node.AdditionalPropertiesAllowed() {
		schema.AdditionalPropertiesAllowed = true
	}
	for _, value := range node.Enum {
		schema.Enum = append(schema.Enum, scalarString(value))
	}
	for _, alternative := range node.OneOf {
		converted, err := b.convertComponentSchema(alternative, visiting)
		if err != nil {
			return nil, fmt.Errorf("oneOf: %w", err)
		}
		schema.OneOf = append(schema.OneOf, converted)
	}
	for _, alternative := range node.AnyOf {
		converted, err := b.convertComponentSchema(alternative, visiting)
		if err != nil {
			return nil, fmt.Errorf("anyOf: %w", err)
		}
		schema.AnyOf = append(schema.AnyOf, converted)
	}
	if !node.Default.IsZero() {
		schema.Default = scalarValue(node.Default)
	}
	return schema, nil
}

func (n schemaNode) AdditionalPropertiesAllowed() bool {
	value, ok := n.Extensions["additionalProperties"]
	if !ok {
		return false
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(value.Value))
	return err == nil && enabled
}

func (b *builder) securityRequirements(entries []map[string][]string, _ string) []api.SecurityRequirement {
	result := make([]api.SecurityRequirement, 0, len(entries))
	for _, entry := range entries {
		schemes := make([]string, 0, len(entry))
		for scheme := range entry {
			schemes = append(schemes, scheme)
		}
		sort.Strings(schemes)
		for _, scheme := range schemes {
			result = append(result, api.SecurityRequirement{
				Scheme: scheme,
				Scopes: append([]string(nil), entry[scheme]...),
			})
		}
	}
	return result
}

func convertSecurityScheme(node securitySchemeNode) api.SecurityScheme {
	scheme := api.SecurityScheme{
		Type:          strings.TrimSpace(node.Type),
		In:            strings.ToLower(strings.TrimSpace(node.In)),
		Scheme:        strings.TrimSpace(node.Scheme),
		ParameterName: strings.TrimSpace(node.Name),
		Description:   strings.TrimSpace(node.Description),
	}
	for scope := range node.Scopes {
		scheme.Scopes = append(scheme.Scopes, scope)
	}
	sort.Strings(scheme.Scopes)
	return scheme
}

func expandServerURL(entry serverEntry) string {
	url := strings.TrimSpace(entry.URL)
	if len(entry.Variables) == 0 {
		return url
	}
	names := make([]string, 0, len(entry.Variables))
	for name := range entry.Variables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		variable := entry.Variables[name]
		url = strings.ReplaceAll(url, "{"+name+"}", variable.Default)
	}
	return url
}

func deriveAPIID(title, version string) string {
	slug := sanitizeIdentifier(strings.ToLower(strings.TrimSpace(title)))
	if slug == "" {
		slug = "api"
	}
	if version = strings.TrimSpace(version); version != "" {
		slug += "-" + sanitizeIdentifier(version)
	}
	return slug
}

func sanitizeIdentifier(value string) string {
	var builder strings.Builder
	previousDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			previousDash = false
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r + ('a' - 'A'))
			previousDash = false
		default:
			if !previousDash && builder.Len() > 0 {
				builder.WriteRune('-')
				previousDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func firstTag(tags []string) string {
	for _, tag := range tags {
		if trimmed := strings.TrimSpace(tag); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func capabilitiesFromExtensions(extensions map[string]yaml.Node) []string {
	if extensions == nil {
		return nil
	}
	return stringsFromNode(extensions[ExtensionCapabilities])
}

func sideEffectsFor(method httpMethod, extensions map[string]yaml.Node) []api.SideEffect {
	effects := []api.SideEffect{method.sideEffect}
	if extensions != nil {
		effects = append(effects, stringsFromNode(extensions[ExtensionSideEffects])...)
	}
	return deduplicate(effects)
}

func boolFromExtensions(extensions map[string]yaml.Node, name string) bool {
	value, ok := extensions[name]
	if !ok {
		return false
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value.Value))
	return err == nil && parsed
}

func stringsFromNode(node yaml.Node) []string {
	if node.IsZero() {
		return nil
	}
	values := make([]string, 0)
	if err := node.Decode(&values); err == nil {
		return deduplicate(values)
	}
	var single string
	if err := node.Decode(&single); err == nil && strings.TrimSpace(single) != "" {
		return []string{strings.TrimSpace(single)}
	}
	return nil
}

func scalarString(node yaml.Node) string {
	var value any
	if err := node.Decode(&value); err != nil {
		return node.Value
	}
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}

func scalarValue(node yaml.Node) any {
	var value any
	if err := node.Decode(&value); err != nil {
		return nil
	}
	return value
}

func schemaTypeFor(node *schemaNode, schema *api.Schema) api.SchemaType {
	if node.Nullable {
		schema.Description = strings.TrimSpace(node.Description)
	}
	switch strings.ToLower(strings.TrimSpace(node.Type)) {
	case "object":
		return api.TypeObject
	case "array":
		return api.TypeArray
	case "string":
		return api.TypeString
	case "integer":
		return api.TypeInteger
	case "number":
		return api.TypeNumber
	case "boolean":
		return api.TypeBoolean
	case "null":
		return api.TypeNull
	}
	// A schema without a type is described by its shape, which is what a
	// consumer needs in order to build a valid request.
	switch {
	case len(node.Properties) > 0:
		return api.TypeObject
	case node.Items != nil:
		return api.TypeArray
	case len(node.Enum) > 0:
		return api.TypeString
	case node.Format != "":
		return api.TypeString
	default:
		return api.TypeUnspecified
	}
}

func componentName(ref, kind string) (string, bool) {
	prefix := "#/components/" + kind + "/"
	if !strings.HasPrefix(ref, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(ref, prefix)
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

func sortedPaths(paths map[string]pathItem) []string {
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func sortedStatuses(responses map[string]responseNode) []string {
	result := make([]string, 0, len(responses))
	for status := range responses {
		result = append(result, status)
	}
	sort.Strings(result)
	return result
}

func deduplicate(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
