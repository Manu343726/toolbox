package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the translation a client sees: the names, the schemas, and the
// findings. A tool's name is a promise to whatever already learned it, so the
// naming tests here are the reason they exist.

func describedShop(t *testing.T) api.API {
	t.Helper()
	described := api.API{
		ID:          "shop",
		Name:        "shop",
		Version:     "1.0.0",
		Title:       "Shop",
		Description: "The shop API",
		Format:      "openapi",
		Transport:   "http",
		Services: []api.Service{{
			Name:        "shop.pets",
			Description: "Pet operations",
			Operations: []api.Operation{{
				Name:        "getPet",
				Method:      "get",
				Path:        "/pets/{petId}",
				Summary:     "Fetch one pet",
				SideEffects: []api.SideEffect{api.SideEffectReadOnly},
				Parameters: []api.Parameter{{
					Name:        "petId",
					In:          api.ParameterInPath,
					Description: "Pet identifier",
					Required:    true,
					Schema:      api.StringSchema(),
				}},
				Response: api.ObjectSchema(api.Property{Name: "name", Schema: api.StringSchema()}),
			}, {
				Name:        "deletePet",
				Method:      "delete",
				Path:        "/pets/{petId}",
				SideEffects: []api.SideEffect{api.SideEffectIrreversible},
			}},
		}, {
			// A second service, so the tests can see that one is neither
			// privileged nor dropped: a health check a subsystem declared a
			// capability for is published like any other operation.
			Name: "toolbox.shop.v1.HealthService",
			Operations: []api.Operation{{
				Name: "Check",
			}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)
	return normalized
}

func TestRenderTurnsADescriptionIntoTools(t *testing.T) {
	rendered, err := Render(describedShop(t), RenderOptions{})
	require.NoError(t, err)
	require.Len(t, rendered.Tools, 3, "the description declares three operations and all three are tools")

	byName := map[string]ToolDefinition{}
	for _, tool := range rendered.Tools {
		byName[tool.Name] = tool
	}
	fetch := byName["pets__get_pet"]
	require.Contains(t, byName, "pets__get_pet", "the tool name comes from the service and method")
	assert.Equal(t, "Fetch one pet", fetch.Description)
	assert.True(t, fetch.ReadOnly, "a read-only side effect is carried through to the tool")
	assert.Equal(t, "shop/shop.pets/getPet", fetch.OperationID)
	assert.Equal(t, "getPet", fetch.Method)
	assert.Equal(t, "shop.shop.pets", fetch.Qualified)

	// The arguments are a JSON Schema object, because that is what an MCP client
	// already knows how to read.
	properties, ok := fetch.InputSchema["properties"].(map[string]any)
	require.True(t, ok)
	petID, ok := properties["petId"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", petID["type"])
	assert.Equal(t, "Pet identifier", petID["description"])
	assert.Equal(t, []string{"petId"}, fetch.InputSchema["required"])

	output, ok := fetch.OutputSchema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, output, "name")

	remove := byName["pets__delete_pet"]
	assert.False(t, remove.ReadOnly, "a destructive operation is not read-only")
}

func TestRenderNamesToolsTheSameWayTheGatewayDoes(t *testing.T) {
	// The gateway and the publisher must agree, or a client that learned a name
	// from one surface would find a different tool on the other.
	rendered, err := Render(describedShop(t), RenderOptions{})
	require.NoError(t, err)
	for _, tool := range rendered.Tools {
		assert.Equal(t, tool.Name, generatedToolName(tool.Qualified, tool.Method),
			"tool %q is named by the shared rule", tool.Name)
	}
}

func TestRenderOffersEveryDeclaredOperation(t *testing.T) {
	// A subsystem's health service is a service it declared a capability for, so
	// it is a tool like any other. What a deployment exposes is its decision, made
	// through exposure — not something a list of names in this package decides.
	rendered, err := Render(describedShop(t), RenderOptions{})
	require.NoError(t, err)
	assert.Len(t, rendered.Tools, 3, "every operation the description declares is published")
}

func TestRenderSelectsTools(t *testing.T) {
	only, err := Render(describedShop(t), RenderOptions{Only: []string{"getPet"}})
	require.NoError(t, err)
	require.Len(t, only.Tools, 1)
	assert.Equal(t, "pets__get_pet", only.Tools[0].Name)

	without, err := Render(describedShop(t), RenderOptions{ExcludeTools: []string{"getPet"}})
	require.NoError(t, err)
	names := make([]string, 0, len(without.Tools))
	for _, tool := range without.Tools {
		names = append(names, tool.Name)
	}
	assert.Equal(t, []string{"pets__delete_pet", "health__check"}, names,
		"excluding one operation leaves the rest, in the order the description declares them")
}

func TestRenderReportsAStreamingOperationInsteadOfOfferingIt(t *testing.T) {
	described := describedShop(t)
	described.Services[0].Operations[0].Streaming = api.Streaming{Server: true}
	rendered, err := Render(described, RenderOptions{})
	require.NoError(t, err)
	require.Len(t, rendered.Warnings, 1)
	assert.Contains(t, rendered.Warnings[0], "is streaming and is not offered as a tool")
	for _, tool := range rendered.Tools {
		assert.NotEqual(t, "pets__get_pet", tool.Name)
	}
}

func TestRenderRefusesADescriptionWithNothingToOffer(t *testing.T) {
	described := describedShop(t)
	described.Services = nil
	_, err := Render(described, RenderOptions{})
	require.Error(t, err, "an empty tool set is refused, not published as an empty server")
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "no operation that MCP can represent")
}

func TestRenderFallsBackToSomethingReadableForADescribedOperation(t *testing.T) {
	described := describedShop(t)
	described.Services[0].Operations[0].Summary = ""
	described.Services[0].Operations[0].Description = ""
	rendered, err := Render(described, RenderOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, rendered.Tools)
	assert.Equal(t, "Call shop.shop.pets.getPet.", rendered.Tools[0].Description,
		"an operation with no prose is still described, so a model is not left with a blank")
}

func TestRenderUsesTheDescriptionForInstructions(t *testing.T) {
	rendered, err := Render(describedShop(t), RenderOptions{})
	require.NoError(t, err)
	assert.Equal(t, "The shop API", rendered.Instructions)

	explicit, err := Render(describedShop(t), RenderOptions{Instructions: "Use sparingly."})
	require.NoError(t, err)
	assert.Equal(t, "Use sparingly.", explicit.Instructions)
}

func TestRenderCarriesTheDeclaredSideEffects(t *testing.T) {
	// A rendered manifest states what calling a tool does, so a deployment reading
	// one classifies the operations without having to know the original contract.
	described := describedShop(t)
	described.Services[0].Operations[0].SideEffects = nil

	rendered, err := Render(described, RenderOptions{})
	require.NoError(t, err)
	byName := map[string]ToolDefinition{}
	for _, tool := range rendered.Tools {
		byName[tool.Name] = tool
	}
	assert.Empty(t, byName["pets__get_pet"].SideEffects,
		"an operation that classified nothing renders unclassified, not read-only")
	assert.Equal(t, []api.SideEffect{api.SideEffectIrreversible},
		byName["pets__delete_pet"].SideEffects)
	assert.False(t, byName["pets__get_pet"].ReadOnly,
		"an unclassified tool is not advertised as read-only, which is the safe direction")
}

func TestRenderedToolsAreTheOnesAGatewayOffers(t *testing.T) {
	// The rendered definitions must be exactly what a live gateway offers — same
	// name, same arguments, same result — so a description can be published as a
	// working surface rather than as a document nobody can use.
	rendered, err := Render(describedShop(t), RenderOptions{})
	require.NoError(t, err)
	server, err := NewFromAPICatalog(
		context.Background(),
		catalogWith(describedShop(t)),
		api.InvokerFunc(func(context.Context, api.Call) (api.Result, error) {
			return api.Result{Status: 200, Body: json.RawMessage(`{"name":"rex"}`)}, nil
		}),
		APICatalogOptions{Options: Options{Policy: APIPolicy()}},
	)
	require.NoError(t, err)
	exposed := map[string]bool{}
	for _, feature := range server.Features() {
		exposed[feature.ToolName] = feature.Exposed
	}
	require.NotEmpty(t, exposed)
	for _, tool := range rendered.Tools {
		assert.True(t, exposed[tool.Name], "a rendered tool %q is the tool the gateway offers", tool.Name)
		entry, err := server.lookupFeature(featureID(tool.Qualified, tool.Method))
		require.NoError(t, err)
		assert.Equal(t, tool.Name, entry.feature.ToolName)
		assert.Equal(t, tool.InputSchema, entry.inputSchema, "the arguments are the same on both paths")
		assert.Equal(t, tool.OutputSchema, entry.outputSchema, "the result shape is the same on both paths")
		assert.Equal(t, tool.Description, entry.feature.Description)
	}
}

// catalogWith binds each described API to a registered server, which is what
// registration does and what makes an operation callable.
func catalogWith(described ...api.API) *api.StaticCatalog {
	apis := make([]api.API, 0, len(described))
	for _, target := range described {
		target.ServerIDs = []string{"shop-host"}
		apis = append(apis, target)
	}
	return &api.StaticCatalog{
		RegisteredServers: []api.Server{{ID: "shop-host", Name: "Shop", BaseURL: "http://shop.test", Transport: "http"}},
		RegisteredAPIs:    apis,
	}
}

func TestToolNameSegmentIsReadable(t *testing.T) {
	// A name is what a model has to type, so the reduction is part of the contract
	// and is asserted here rather than discovered later.
	assert.Equal(t, "knowledge__search", generatedToolName("toolbox.knowledge.v1.KnowledgeService", "Search"))
	assert.Equal(t, "workflow__validate_workflow", generatedToolName("workflow", "ValidateWorkflow"))
	// An unnamed operation has no name to offer. An empty name is refused by the
	// boundaries that accept a name; a separator made of nothing is not.
	assert.Empty(t, generatedToolName("", ""))
	assert.Empty(t, generatedToolName("toolbox.shop.v1.", ""))
}

func TestToolNamesSharedBySeveralOperationsAreQualified(t *testing.T) {
	// Several providers serving one contract is how a format is added, so two
	// operations can reduce to the same name. The name each is given must still
	// reach exactly one of them, and every other name must stay as it was.
	shared := []toolNameOwner{{
		qualified: "apimcp.toolbox.api.v1.ApiParserService",
		owner:     "apimcp",
		method:    "ParseApi",
	}, {
		qualified: "apigrpc.toolbox.api.v1.ApiParserService",
		owner:     "apigrpc",
		method:    "ParseApi",
	}, {
		qualified: "apimcp.toolbox.api.v1.ApiAdapterService",
		owner:     "apimcp",
		method:    "RenderApi",
	}, {
		qualified: "shop.shop.pets",
		owner:     "shop",
		method:    "getPet",
	}}
	sortOwners(shared)
	namer := newToolNamer(shared)

	names := make([]string, 0, len(shared))
	for _, candidate := range shared {
		names = append(names, namer.name(candidate))
	}
	assert.Equal(t, []string{
		"apigrpc__api_parser__parse_api",
		"api_adapter__render_api",
		"apimcp__api_parser__parse_api",
		"pets__get_pet",
	}, names, "only the operations sharing a name are qualified; the rest keep the name a client already learned")

	// The result must not depend on the order the operations were found in.
	shuffled := []toolNameOwner{shared[3], shared[1], shared[2], shared[0]}
	sortOwners(shuffled)
	renamed := newToolNamer(shuffled)
	for _, candidate := range shuffled {
		assert.Equal(t, namer.name(candidate), renamed.name(candidate))
	}
}

func TestToolNamesThatCollideEvenWhenQualifiedAreReported(t *testing.T) {
	// A name that still reaches two operations after qualifying is the one case
	// where nothing is left to say, so the surface falls back to the full service
	// name rather than letting a client reach the wrong operation.
	shared := []toolNameOwner{{
		qualified: "shop.a.v1.Quote",
		owner:     "shop",
		method:    "Get",
	}, {
		qualified: "shop.b.v1.Quote",
		owner:     "shop",
		method:    "Get",
	}}
	sortOwners(shared)
	namer := newToolNamer(shared)
	assert.Equal(t, "shop.a.v1.quote__quote__get", namer.name(shared[0]))
	assert.Equal(t, "shop.b.v1.quote__quote__get", namer.name(shared[1]))
}

func TestRenderSurfacesARequestValuesOwnArguments(t *testing.T) {
	// An operation with no declared parameters — every protobuf method, and every
	// MCP tool — has nothing to be apart from: its request value is the whole
	// input. Publishing it as one opaque "body" argument would hand a model a tool
	// it can only fill in blind, so the arguments are surfaced by name.
	described := api.API{
		ID:     "contract",
		Name:   "contract",
		Format: "grpc",
		Services: []api.Service{{
			Name: "contract.pets",
			Operations: []api.Operation{{
				Name: "getPet",
				Request: api.ObjectSchema(
					api.Property{Name: "petId", Schema: api.StringSchema(), Required: true},
					api.Property{Name: "verbose", Schema: &api.Schema{Type: api.TypeBoolean}},
				),
				Response: api.ObjectSchema(api.Property{Name: "name", Schema: api.StringSchema()}),
			}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)

	rendered, err := Render(normalized, RenderOptions{})
	require.NoError(t, err)
	require.Len(t, rendered.Tools, 1)
	properties, ok := rendered.Tools[0].InputSchema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "petId", "an argument a model has to supply is named, not nested")
	assert.Contains(t, properties, "verbose", "an optional argument is named too")
	assert.NotContains(t, properties, "body", "there is nothing for a body wrapper to separate")
	assert.Equal(t, []string{"petId"}, rendered.Tools[0].InputSchema["required"],
		"a required argument stays required, or the tool would be offered un-runnable")
}

func TestRenderKeepsABodyWhenTheOperationDeclaresParameters(t *testing.T) {
	// An operation that declares parameters and a request body keeps them apart,
	// because that is the shape its description had and flattening it would lose
	// the distinction the description made.
	described := describedShop(t)
	described.Services[0].Operations[0].Request = api.ObjectSchema(
		api.Property{Name: "reason", Schema: api.StringSchema(), Required: true},
	)
	normalized, err := described.Normalize()
	require.NoError(t, err)

	rendered, err := Render(normalized, RenderOptions{})
	require.NoError(t, err)
	var fetch ToolDefinition
	for _, tool := range rendered.Tools {
		if tool.Method == "getPet" {
			fetch = tool
		}
	}
	properties, ok := fetch.InputSchema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "petId", "a declared parameter keeps its name")
	assert.Contains(t, properties, "body", "a declared request value stays the body, as the description had it")
}
