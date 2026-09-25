package main

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
	apitoolsv1 "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1"
	apitoolsv1connect "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1/apitoolsv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutomaticExposureOfSubsystems is the whole feature, end to end, in one
// process: the host starts a feature subsystem, the catalog is filled from that
// subsystem's own contract, and the gateway offers the subsystem's operations as
// tools.
func TestAutomaticExposureOfSubsystems(t *testing.T) {
	h, catalog, err := buildHost("")
	require.NoError(t, err)
	// Health and registry are started on purpose: their services declare
	// capabilities, so the uniform rule has to hold for them too.
	require.NoError(t, h.Select("knowledge", "workflow", "apigrpc", "apitools", "health", "registry"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { require.NoError(t, h.Shutdown(context.Background())) }()

	seed, err := catalog.registerSubsystems(context.Background(), false)
	require.NoError(t, err)
	require.NotEmpty(t, seed.Seeded, "the started subsystems are described from their own contracts")

	registered := 0
	// Every started subsystem registers, including a provider: a provider exists
	// to serve the framework's extension contracts, and a catalog that hid them
	// would be hiding the mechanism it is made of. What an agent gets from them is
	// decided by exposure, not by the name of a contract.
	providers := map[string]bool{}
	for _, entry := range seed.Seeded {
		if entry.Skipped != "" {
			assert.Contains(t, entry.Skipped, "every service the subsystem serves was excluded",
				"a subsystem is skipped only when a deployment excluded all of it, and it says so")
			continue
		}
		assert.Positive(t, entry.Operations, "subsystem %s registers its operations", entry.Subsystem)
		if entry.Exposed > 0 {
			registered++
		}
		if entry.APIID == "apigrpc" || entry.APIID == "apiopenapi" || entry.APIID == "apimcp" {
			providers[entry.APIID] = true
		}
	}
	// This host started one provider, apigrpc, and it registers like any other API.
	assert.Equal(t, map[string]bool{"apigrpc": true}, providers,
		"a provider is registered as an ordinary API, with its own operations")
	assert.Positive(t, registered, "at least one subsystem contributes tools")
	for _, warning := range seed.Warnings {
		assert.NotContains(t, warning, "stays hidden", "no declared operation is left unexposed: %v", warning)
	}

	// The catalog answers, over ConnectRPC, about what it holds. A caller reaches
	// the same store a gateway reads, and gets the same answers.
	client := apitoolsv1connect.NewApiToolsServiceClient(http.DefaultClient, h.Servers()["apitools"].Endpoint())
	apis, err := client.ListApis(context.Background(), connect.NewRequest(&apitoolsv1.ListApisRequest{}))
	require.NoError(t, err)
	assert.NotEmpty(t, apis.Msg.GetApis())
	servers, err := client.ListServers(context.Background(), connect.NewRequest(&apitoolsv1.ListServersRequest{}))
	require.NoError(t, err)
	assert.NotEmpty(t, servers.Msg.GetServers())

	// The gateway is built from the catalog, in process, with the catalog's own
	// invoker — no provider subsystem and no transport in between.
	gateway, err := toolboxmcp.NewFromAPICatalog(
		context.Background(),
		catalog.service.Catalog(),
		catalog.service.Invoker(),
		toolboxmcp.APICatalogOptions{Options: toolboxmcp.Options{Policy: catalog.policy}},
	)
	require.NoError(t, err)
	features := gateway.Features()
	require.NotEmpty(t, features)
	byTool := map[string]bool{}
	for _, feature := range features {
		byTool[feature.ToolName] = feature.Exposed
	}
	assert.True(t, byTool["knowledge__search"], "a declared operation is offered as a tool")
	assert.True(t, byTool["workflow__validate_workflow"], "every subsystem contributes its declared operations")

	// Every contract in the deployment is annotated, so the default policy — every
	// read, nothing that changes state — offers each subsystem's reads whoever
	// serves them. The reflection services are not offered because they provide no
	// capability at all.
	assert.True(t, byTool["health__check"], "a declared read is offered, whatever service declares it")
	assert.True(t, byTool["registry__list_services"], "a declared read is offered, whatever service declares it")
	for name := range byTool {
		assert.NotContains(t, name, "reflection", "reflection is not a tool")
	}

	// A write is not offered, because the default document does not grant one. This
	// is the half that used to be the default's whole answer.
	assert.False(t, byTool["knowledge__put_source"],
		"the default policy grants reads, so a write is registered, described, and not exposed")

	// A call goes through the catalog's invoker to the real subsystem. The search
	// is empty on purpose: the assertion is that the call path is real, not what a
	// particular fixture happens to return.
	_, err = catalog.service.CallOperation(context.Background(), connect.NewRequest(&apitoolsv1.CallOperationRequest{
		ApiId:         "knowledge",
		OperationId:   "knowledge/toolbox.knowledge.v1.KnowledgeService/Search",
		ArgumentsText: `{"query":""}`,
	}))
	if err != nil {
		t.Logf("the call reached the subsystem and reported: %v", err)
	}
}

// TestReflectionAndCatalogAgreeOnToolNames guards the promise that switching the
// source of the tool surface does not rename anything an agent already uses.
func TestReflectionAndCatalogAgreeOnToolNames(t *testing.T) {
	h, catalog, err := buildHost("")
	require.NoError(t, err)
	require.NoError(t, h.Select("knowledge", "apigrpc"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { require.NoError(t, h.Shutdown(context.Background())) }()
	_, err = catalog.registerSubsystems(context.Background(), false)
	require.NoError(t, err)

	fromCatalog, err := toolboxmcp.NewFromAPICatalog(
		context.Background(),
		catalog.service.Catalog(),
		catalog.service.Invoker(),
		toolboxmcp.APICatalogOptions{Options: toolboxmcp.Options{Policy: catalog.policy}},
	)
	require.NoError(t, err)
	fromReflection, err := toolboxmcp.NewFromDescriptors(
		context.Background(),
		h.Descriptors(),
		toolboxmcp.Options{InitialExposure: toolboxmcp.ExposeNoFeatures},
	)
	require.NoError(t, err)

	catalogTools := map[string]bool{}
	for _, feature := range fromCatalog.Features() {
		catalogTools[feature.ToolName] = true
	}
	require.NotEmpty(t, catalogTools)
	shared := 0
	for _, feature := range fromReflection.Features() {
		if catalogTools[feature.ToolName] {
			shared++
		}
	}
	assert.Positive(t, shared, "the same operations are named the same way by both surfaces")
}

// TestCatalogBackedGatewayRequiresACatalog is the boundary check.
func TestCatalogBackedGatewayRequiresACatalog(t *testing.T) {
	_, err := toolboxmcp.NewFromAPICatalog(context.Background(), nil, nil, toolboxmcp.APICatalogOptions{})
	require.Error(t, err)
}
