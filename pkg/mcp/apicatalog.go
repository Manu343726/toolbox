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
	// APISelector optionally narrows the catalog to the APIs it accepts.
	APISelector func(api.API) bool
	// IgnoreExposure builds the surface from the catalog's descriptions alone,
	// ignoring the exposure decisions it recorded. It is false by default: when a
	// catalog says an operation is hidden or denied, this server does not offer it,
	// because a second opinion about a decision the catalog already made would make
	// the catalog's answers undependable.
	IgnoreExposure bool
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
	servers, err := catalog.Servers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list catalog servers: %w", err)
	}
	byID := make(map[string]api.Server, len(servers))
	for _, candidate := range servers {
		byID[candidate.ID] = candidate
	}
	selected := make([]api.API, 0, len(apis))
	for _, described := range apis {
		if catalogOptions.APISelector != nil && !catalogOptions.APISelector(described) {
			continue
		}
		selected = append(selected, described)
	}
	// The catalog's exposure decisions are read once, for every operation, so the
	// gateway and the catalog cannot disagree about a single one.
	exposures := readExposures(ctx, catalog, selected, catalogOptions.IgnoreExposure)
	// Names are assigned across the whole surface before any tool is named, so an
	// operation that shares a name with another is qualified rather than colliding
	// with it — which is what several providers serving one contract produces.
	candidates := make([]toolNameOwner, 0, 32)
	for _, described := range selected {
		for _, service := range described.Services {
			for _, operation := range service.Operations {
				candidates = append(candidates, toolNameOwner{
					qualified: apiFeatureName(described, operation.Service),
					owner:     described.ID,
					method:    operation.Method,
				})
			}
		}
	}
	sortOwners(candidates)
	namer := newToolNamer(candidates)

	server := newServer(options)
	toolNames := make(map[string]string)
	for _, described := range selected {
		for _, entry := range apiFeatures(described, byID, invoker, options.Policy, exposures, options.InitialExposure) {
			if err := server.addEntry(entry, toolNames, namer); err != nil {
				return nil, err
			}
		}
	}
	server.finish()
	return server, nil
}

// readExposures asks the catalog what it has exposed, when it can answer. A catalog
// that does not track exposure yields nothing, and the gateway falls back to its
// own policy — which is what keeps a read-only catalog usable.
func readExposures(
	ctx context.Context,
	catalog api.Catalog,
	apis []api.API,
	ignored bool,
) map[string]api.Exposure {
	if ignored {
		return nil
	}
	source, ok := catalog.(api.ExposureSource)
	if !ok {
		return nil
	}
	identifiers := make([]string, 0, 32)
	for _, described := range apis {
		for _, operation := range described.Operations() {
			identifiers = append(identifiers, operation.ID)
		}
	}
	if len(identifiers) == 0 {
		return nil
	}
	exposures, err := source.Exposures(ctx, identifiers)
	if err != nil {
		// A catalog that cannot report its decisions has not made any this gateway
		// must honour, so the gateway answers for itself.
		return nil
	}
	return exposures
}

// apiFeatures turns one registered API into candidate features, honouring what
// the catalog decided about each of them.
func apiFeatures(
	described api.API,
	servers map[string]api.Server,
	invoker api.Invoker,
	policy api.Policy,
	exposures map[string]api.Exposure,
	initialExposure InitialExposure,
) []*featureEntry {
	entries := make([]*featureEntry, 0, len(described.Operations()))
	for _, service := range described.Services {
		for _, operation := range service.Operations {
			entries = append(entries, apiFeatureEntry(described, service, operation, servers, invoker, policy, exposures, initialExposure))
		}
	}
	return entries
}

func apiFeatureEntry(
	described api.API,
	service api.Service,
	operation api.Operation,
	servers map[string]api.Server,
	invoker api.Invoker,
	policy api.Policy,
	exposures map[string]api.Exposure,
	initialExposure InitialExposure,
) *featureEntry {
	// The tool definition is the shared translation, so a tool served here and a
	// tool published from the same description are the same tool — same name, same
	// prose, same schemas, same capabilities.
	tool := feature{
		described: described,
		service:   service,
		operation: operation,
		qualified: apiFeatureName(described, operation.Service),
		streaming: operation.Streaming,
	}.definition()
	qualified := tool.Qualified
	// A description may declare no request or response shape at all, so both are
	// read defensively rather than assumed.
	inputRef, outputRef := "", ""
	if operation.Request != nil {
		inputRef = operation.Request.Ref
	}
	if operation.Response != nil {
		outputRef = operation.Response.Ref
	}
	// The catalog's decision is authoritative for the operations it knows about: a
	// denied operation is not a tool, and a hidden one is not exposed even when
	// this server's own policy would allow it. An operation the catalog has never
	// seen falls back to the policy, which is what a catalog that predates the
	// decision cannot object to.
	exposure, decided := exposures[operation.ID]
	// The catalog already applied the deployment's policy when it decided exposure,
	// so the gateway's own policy is only asked about an operation the catalog has
	// never seen.
	allowed := policy.Allows(api.OperationFacts{
		ID:          operation.ID,
		SideEffects: operation.SideEffects,
	})
	if decided && exposure.Allowed {
		allowed = true
	}
	callable := invoker != nil && !operation.Streaming.Streaming() && len(described.ServerIDs) > 0
	exposed := allowed && callable && initialExposure == ExposeAllowedFeatures
	switch {
	case !decided:
	case !exposure.Allowed:
		allowed = false
		exposed = false
	default:
		exposed = exposure.Exposed && callable
	}
	entry := &featureEntry{
		feature: Feature{
			ID:              featureID(qualified, operation.Name),
			Service:         qualified,
			Method:          operation.Name,
			ToolName:        tool.Name,
			Description:     tool.Description,
			InputType:       inputRef,
			OutputType:      outputRef,
			ClientStreaming: operation.Streaming.Client,
			ServerStreaming: operation.Streaming.Server,
			Allowed:         allowed,
			Exposed:         exposed,
			Callable:        callable,
			SideEffects:     operation.SideEffects,
		},
		// The API the operation belongs to is what tells it apart from an
		// operation in another API that reduces to the same tool name — which
		// is what several providers serving one contract produces.
		owner:              described.ID,
		inputSchema:        tool.InputSchema,
		outputSchema:       tool.OutputSchema,
		serviceDescription: firstNonEmpty(service.Description, described.Description),
	}
	if invoker != nil {
		entry.invoke = apiInvoker(invoker, described, operation, servers)
	}
	return entry
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

// normalizeArguments makes an empty or absent argument document explicit, so a
// provider always receives a JSON object.
func normalizeArguments(arguments json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(arguments))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`)
	}
	return arguments
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
