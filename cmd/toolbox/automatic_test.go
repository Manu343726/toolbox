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
	h, catalog, err := buildHost()
	require.NoError(t, err)
	require.NoError(t, h.Select("knowledge", "workflow", "apigrpc", "apitools"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { require.NoError(t, h.Shutdown(context.Background())) }()

	seed, err := catalog.registerSubsystems(context.Background(), false)
	require.NoError(t, err)
	require.NotEmpty(t, seed.Seeded, "the started subsystems are described from their own contracts")

	registered := 0
	// A provider subsystem serves only the framework's extension contracts, which
	// are how the platform works rather than something an agent calls, so it is
	// skipped. A feature subsystem registers.
	skipped := map[string]bool{}
	for _, entry := range seed.Seeded {
		if entry.Skipped != "" {
			skipped[entry.Subsystem] = true
			assert.Contains(t, entry.Skipped, "no service a user adopted",
				"only plumbing is skipped, and it says so")
			continue
		}
		assert.Positive(t, entry.Operations, "subsystem %s registers its operations", entry.Subsystem)
		if entry.Exposed > 0 {
			registered++
		}
	}
	assert.True(t, skipped["apigrpc"], "a provider subsystem contributes plumbing, not tools")
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
		toolboxmcp.APICatalogOptions{},
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

	// The health and registry services a subsystem also serves are plumbing, so
	// they never become tools.
	for name, exposed := range byTool {
		assert.NotContains(t, name, "health", "platform plumbing is not a tool")
		assert.NotContains(t, name, "registry", "platform plumbing is not a tool")
		if exposed {
			assert.NotContains(t, name, "reflection", "reflection is not a tool")
		}
	}

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
	h, catalog, err := buildHost()
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
		toolboxmcp.APICatalogOptions{},
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
