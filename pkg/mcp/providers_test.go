package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the case several providers serving one contract produces,
// which used to be exempted by name and now has to work on the same rules as
// everything else. The contract is the framework's parser contract because that is
// the real one: three provider subsystems serve it on purpose.

const sharedContract = "toolbox.api.v1.ApiParserService"

// providerAPI is one provider's description of the contract it serves.
func providerAPI(t *testing.T, id, serverID string) api.API {
	t.Helper()
	described := api.API{
		ID:        id,
		Name:      id,
		Format:    "grpc",
		ServerIDs: []string{serverID},
		Services: []api.Service{{
			Name: sharedContract,
			Operations: []api.Operation{{
				Name:    "ParseApi",
				Method:  "ParseApi",
				Request: &api.Schema{Ref: "toolbox.api.v1.ParseApiRequest", Type: api.TypeObject},
			}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)
	return normalized
}

func TestNewFromAPICatalogOffersEveryProviderOfOneContract(t *testing.T) {
	// Three APIs, one contract, one server each. The catalog can tell them apart,
	// so an agent gets one tool per provider rather than one tool or an error.
	catalog := &api.StaticCatalog{
		RegisteredServers: []api.Server{
			{ID: "apigrpc", Name: "apigrpc", BaseURL: "http://grpc.test", Transport: "http"},
			{ID: "apimcp", Name: "apimcp", BaseURL: "http://mcp.test", Transport: "http"},
			{ID: "apiopenapi", Name: "apiopenapi", BaseURL: "http://openapi.test", Transport: "http"},
		},
		RegisteredAPIs: []api.API{
			providerAPI(t, "apigrpc", "apigrpc"),
			providerAPI(t, "apimcp", "apimcp"),
			providerAPI(t, "apiopenapi", "apiopenapi"),
		},
	}

	invoked := make([]string, 0, 3)
	server, err := NewFromAPICatalog(context.Background(), catalog, api.InvokerFunc(
		func(_ context.Context, call api.Call) (api.Result, error) {
			invoked = append(invoked, call.API.ID)
			return api.Result{Status: 200, Body: json.RawMessage(`{"apis":[]}`)}, nil
		},
	), APICatalogOptions{})
	require.NoError(t, err, "several providers serving one contract is a supported deployment")

	byTool := map[string]string{}
	for _, feature := range server.Features() {
		byTool[feature.ToolName] = feature.ID
	}
	require.Len(t, byTool, 3, "one tool per provider, each reachable under its own name")
	assert.Contains(t, byTool, "apigrpc__api_parser__parse_api")
	assert.Contains(t, byTool, "apimcp__api_parser__parse_api")
	assert.Contains(t, byTool, "apiopenapi__api_parser__parse_api")

	// A disambiguated name must still reach the provider it names, or the rename
	// bought nothing: the call's API is the one in the tool name.
	for _, featureID := range byTool {
		entry, err := server.lookupFeature(featureID)
		require.NoError(t, err)
		require.NotNil(t, entry.invoke)
		_, err = entry.invoke(context.Background(), json.RawMessage(`{}`))
		require.NoError(t, err)
	}
	assert.ElementsMatch(t, []string{"apigrpc", "apimcp", "apiopenapi"}, invoked,
		"each tool reached the API its name identifies")
}

func TestEndpointSourceAcceptsSeveralEndpointsServingOneContract(t *testing.T) {
	// The reflection path addresses a service by name, so it reaches the endpoint
	// that registered it. The other owners are kept rather than refused, so nothing
	// is lost and the gateway still starts.
	source, err := NewEndpointSource(
		ServiceEndpoint{
			Name: "apigrpc",
			URL:  "http://grpc.test",
			Services: []ServiceMetadata{{
				Name: sharedContract,
			}},
		},
		ServiceEndpoint{
			Name: "apimcp",
			URL:  "http://mcp.test",
			Services: []ServiceMetadata{{
				Name: sharedContract,
			}},
		},
	)
	require.NoError(t, err, "two endpoints serving one contract is a supported deployment")

	services, err := source.ListServices(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{sharedContract}, services, "the contract is reachable by name")
	assert.Equal(t, "apigrpc", source.ServiceOwner(sharedContract),
		"a name addresses one endpoint, so the first registration stands")
	assert.Equal(t, []string{sharedContract}, source.SharedServices(),
		"a contract two endpoints serve is reported, because a name reaches only one of them")
}

func TestSharedServicesIsEmptyWhenNothingIsShared(t *testing.T) {
	source, err := NewEndpointSource(ServiceEndpoint{
		Name: "knowledge",
		URL:  "http://knowledge.test",
		Services: []ServiceMetadata{
			{Name: "toolbox.knowledge.v1.KnowledgeService"},
			{Name: "toolbox.knowledge.v1.SourceService"},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, source.SharedServices(),
		"an endpoint serving several contracts shares nothing with another endpoint")
}

func TestGeneratedToolNamesSurviveAnAddedProvider(t *testing.T) {
	// The cost of qualifying a name must fall only on the operations that share
	// one. A provider added later must not rename a tool that was already
	// unambiguous, or every agent's saved tool name breaks.
	before := newToolNamer([]toolNameOwner{
		{qualified: "shop.shop.pets", owner: "shop", method: "getPet"},
		{qualified: "shop.toolbox.api.v1.ApiParserService", owner: "shop", method: "ParseApi"},
	})
	assert.Equal(t, "pets__get_pet", before.name(toolNameOwner{qualified: "shop.shop.pets", owner: "shop", method: "getPet"}))
	assert.Equal(t, "api_parser__parse_api", before.name(toolNameOwner{qualified: "shop.toolbox.api.v1.ApiParserService", owner: "shop", method: "ParseApi"}))

	after := newToolNamer([]toolNameOwner{
		{qualified: "shop.shop.pets", owner: "shop", method: "getPet"},
		{qualified: "shop.toolbox.api.v1.ApiParserService", owner: "shop", method: "ParseApi"},
		{qualified: "apimcp.toolbox.api.v1.ApiParserService", owner: "apimcp", method: "ParseApi"},
	})
	assert.Equal(t, "pets__get_pet", after.name(toolNameOwner{qualified: "shop.shop.pets", owner: "shop", method: "getPet"}),
		"an unrelated tool keeps its name when a provider appears")
	assert.Equal(t, "shop__api_parser__parse_api", after.name(toolNameOwner{qualified: "shop.toolbox.api.v1.ApiParserService", owner: "shop", method: "ParseApi"}),
		"the shared name is qualified on both sides, so neither reaches the other")
	assert.Equal(t, "apimcp__api_parser__parse_api", after.name(toolNameOwner{qualified: "apimcp.toolbox.api.v1.ApiParserService", owner: "apimcp", method: "ParseApi"}))
}
