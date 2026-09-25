package host

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/mcp"
	"github.com/Manu343726/toolbox/pkg/protocontract"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This test is the whole automatic path, in one process: a host starts a subsystem,
// reads that subsystem's contract from the subsystem itself, stores it in a
// catalog, and generates tools from the stored description.
//
// No reflection call from the gateway, no hand-written description, and no
// provider subsystem: the composition reads what the subsystem already serves.

// fixtureService is a service a fixture subsystem declares. It is a name no
// subsystem in this repository uses, so a test that describes it is unmistakably
// describing a fixture rather than something real.
const fixtureService = "toolbox.fixture.v1.CatalogService"

// startFixtureSubsystem runs a subsystem that declares a capability for one user
// service. The contract is described by a stub in most tests, because what those
// tests exercise is the seeder's own contract; one test reads a real contract
// instead.
func startFixtureSubsystem(t *testing.T) *Host {
	t.Helper()
	return startSubsystem(t, "fixture", []subsystem.Service{{
		Name:         fixtureService,
		Path:         "/" + fixtureService + "/",
		Handler:      http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		Capabilities: []string{api.ParseCapability("openapi"), api.InvokeCapability("http")},
	}})
}

func startSubsystem(t *testing.T, name string, services []subsystem.Service) *Host {
	t.Helper()
	h := New()
	server, err := subsystem.NewServer(subsystem.Config{Name: name, Services: services})
	require.NoError(t, err)
	require.NoError(t, h.Register(name, func() (*subsystem.Server, error) { return server, nil }))
	require.NoError(t, h.Select(name))
	require.NoError(t, h.Start(context.Background()))
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	return h
}

// memoryCatalog is a minimal api.Registrar and api.ExposureSource, so this test
// exercises the seeder against the framework's interfaces rather than a
// subsystem's store.
type memoryCatalog struct {
	servers    map[string]api.Server
	apis       map[string]api.API
	exposure   map[string]api.Exposure
	serverSeq  int
	exposeFail bool
}

func newMemoryCatalog() *memoryCatalog {
	return &memoryCatalog{
		servers:  map[string]api.Server{},
		apis:     map[string]api.API{},
		exposure: map[string]api.Exposure{},
	}
}

func (c *memoryCatalog) RegisterServer(_ context.Context, server api.Server) (api.Server, bool, error) {
	if server.Status == "" {
		server.Status = api.ServerStatusServing
	}
	_, replaced := c.servers[server.ID]
	c.servers[server.ID] = server
	return server, replaced, nil
}

func (c *memoryCatalog) DescribeAPI(context.Context, api.DescribeRequest) (api.DescribeResult, error) {
	return api.DescribeResult{}, api.Errorf(api.KindUnsupported, "this test catalog does not describe")
}

func (c *memoryCatalog) RegisterAPI(_ context.Context, described api.API, serverID string) (api.API, bool, error) {
	if serverID != "" {
		described.ServerIDs = []string{serverID}
	}
	normalized, err := described.Normalize()
	if err != nil {
		return api.API{}, false, err
	}
	_, replaced := c.apis[normalized.ID]
	c.apis[normalized.ID] = normalized
	return normalized, replaced, nil
}

func (c *memoryCatalog) SetExposed(_ context.Context, apiID, operationID string, exposed bool) (bool, error) {
	described, ok := c.apis[apiID]
	if !ok {
		return false, api.Errorf(api.KindNotFound, "no api %q", apiID)
	}
	if _, found := described.Operation(operationID); !found {
		return false, api.Errorf(api.KindNotFound, "no operation %q", operationID)
	}
	if exposed && c.exposeFail {
		return false, api.Errorf(api.KindDenied, "the policy refuses this operation")
	}
	if !exposed {
		delete(c.exposure, operationID)
		return true, nil
	}
	previous, existed := c.exposure[operationID]
	c.exposure[operationID] = api.Exposure{OperationID: operationID, Allowed: true, Exposed: true, Invokable: true}
	return !existed || !previous.Exposed, nil
}

func (c *memoryCatalog) Exposures(_ context.Context, identifiers []string) (map[string]api.Exposure, error) {
	result := make(map[string]api.Exposure, len(identifiers))
	for _, id := range identifiers {
		if exposure, ok := c.exposure[id]; ok {
			result[id] = exposure
		}
	}
	return result, nil
}

// stubDescriber answers with a description, standing in for reading a live
// contract. The seeder's own contract is what is under test here.
type stubDescriber struct {
	described api.API
	err       error
	calls     int
}

func (d *stubDescriber) Describe(context.Context, api.DescribeRequest) (api.DescribeResult, error) {
	d.calls++
	if d.err != nil {
		return api.DescribeResult{}, d.err
	}
	return api.DescribeResult{API: d.described, ProviderID: "stub"}, nil
}

func describedFixture(t *testing.T) api.API {
	t.Helper()
	described := api.API{
		ID:     "fixture",
		Name:   "fixture",
		Format: "grpc",
		Source: api.Source{Kind: "reflection", Location: "http://fixture.test"},
		Services: []api.Service{{
			Name: fixtureService,
			Operations: []api.Operation{
				{Name: "ListThings", Method: "ListThings"},
				{Name: "DeleteThing", Method: "DeleteThing"},
			},
		}, {
			// A second service with no capability behind it: it is registered
			// because the subsystem serves it, and none of its operations is
			// exposed because nothing declared one.
			Name:       "toolbox.fixture.v1.HealthService",
			Operations: []api.Operation{{Name: "Check", Method: "Check"}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)
	return normalized
}

func TestRegisterIntoDescribesAndExposesAHostSubsystem(t *testing.T) {
	h := startFixtureSubsystem(t)
	catalog := newMemoryCatalog()
	describer := &stubDescriber{described: describedFixture(t)}

	result, err := h.RegisterInto(context.Background(), catalog, describer, SeedOptions{})
	require.NoError(t, err)
	require.Len(t, result.Seeded, 1)
	entry := result.Seeded[0]
	assert.Empty(t, entry.Skipped)
	assert.Equal(t, "fixture", entry.Subsystem)
	assert.Equal(t, "fixture", entry.ServerID)
	assert.Equal(t, "fixture", entry.APIID)
	assert.Equal(t, 2, entry.Services, "every service the subsystem serves is registered")
	assert.Equal(t, 3, entry.Operations)
	assert.Equal(t, 2, entry.Exposed, "the capabilities the manifest declared cover both operations")
	assert.Equal(t, 1, result.Exposed())

	// The server record is the subsystem's own endpoint.
	server, ok := catalog.servers["fixture"]
	require.True(t, ok)
	assert.Equal(t, h.Servers()["fixture"].Endpoint(), server.BaseURL)
	assert.Equal(t, DefaultSubsystemTransport, server.Transport)
	assert.Equal(t, "grpc", string(server.Format))
	assert.Equal(t, "reflection", server.Source.Kind)

	// The capabilities the subsystem's manifest declared reached the description,
	// joined to the service they belong to.
	stored, ok := catalog.apis["fixture"]
	require.True(t, ok)
	require.Len(t, stored.Services, 2)
	assert.Equal(t, []string{api.InvokeCapability("http"), api.ParseCapability("openapi")}, stored.Services[0].Capabilities,
		"the capabilities the manifest declared, joined to the service they belong to")

	// Both operations are exposed, and the seeder asked for exactly those.
	assert.Contains(t, catalog.exposure, "fixture/"+fixtureService+"/ListThings")
	assert.Contains(t, catalog.exposure, "fixture/"+fixtureService+"/DeleteThing")
}

func TestRegisterIntoExposesNothingWithoutACapability(t *testing.T) {
	// A subsystem whose service declares no capability: its contract is described
	// and stored, and none of its operations is a tool.
	described := describedFixture(t)
	described.Services[0].Capabilities = nil
	described.Services[0].Name = "toolbox.bare.v1.BareService"
	described.ID = "bare"
	stripped := startSubsystem(t, "bare", []subsystem.Service{{
		Name:    "toolbox.bare.v1.BareService",
		Path:    "/toolbox.bare.v1.BareService/",
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}})

	catalog := newMemoryCatalog()
	describer := &stubDescriber{described: described}
	result, err := stripped.RegisterInto(context.Background(), catalog, describer, SeedOptions{})
	require.NoError(t, err)
	require.Len(t, result.Seeded, 1)
	assert.Equal(t, 3, result.Seeded[0].Operations)
	assert.Zero(t, result.Seeded[0].Exposed, "an operation no capability covers is not a tool")
	assert.Zero(t, result.Exposed())
	assert.Empty(t, catalog.exposure)
}

func TestRegisterIntoReportsWhatPolicyRefuses(t *testing.T) {
	h := startFixtureSubsystem(t)
	catalog := newMemoryCatalog()
	catalog.exposeFail = true
	describer := &stubDescriber{described: describedFixture(t)}

	result, err := h.RegisterInto(context.Background(), catalog, describer, SeedOptions{})
	require.NoError(t, err)
	// Seeding never forces an operation past a policy: it reports and moves on.
	assert.Zero(t, result.Seeded[0].Exposed)
	require.Len(t, result.Warnings, 2, "every operation that asked to be exposed and was refused is reported")
	assert.Contains(t, result.Warnings[0], "stays hidden")
	assert.Empty(t, catalog.exposure)
}

func TestRegisterIntoSkipsWhatItCannotDescribe(t *testing.T) {
	h := startFixtureSubsystem(t)
	catalog := newMemoryCatalog()
	describer := &stubDescriber{err: api.Errorf(api.KindUnavailable, "the endpoint went away")}

	result, err := h.RegisterInto(context.Background(), catalog, describer, SeedOptions{})
	require.NoError(t, err, "one undescribable subsystem is not a failed startup")
	require.Len(t, result.Seeded, 1)
	assert.Contains(t, result.Seeded[0].Skipped, "went away")
	assert.Empty(t, catalog.apis)
}

func TestRegisterIntoSelectsSubsystems(t *testing.T) {
	h := startFixtureSubsystem(t)
	catalog := newMemoryCatalog()
	describer := &stubDescriber{described: describedFixture(t)}

	result, err := h.RegisterInto(context.Background(), catalog, describer, SeedOptions{Only: []string{"absent"}})
	require.NoError(t, err)
	assert.Empty(t, result.Seeded)
	assert.Zero(t, describer.calls, "a subsystem the options did not select is not even described")
}

func TestRegisterIntoRegistersEveryServiceUnlessExcluded(t *testing.T) {
	h := startFixtureSubsystem(t)
	catalog := newMemoryCatalog()
	describer := &stubDescriber{described: describedFixture(t)}

	// Nothing is dropped by name: a health service is registered because the
	// subsystem declared it, and left out only when a deployment says so.
	result, err := h.RegisterInto(context.Background(), catalog, describer, SeedOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Seeded[0].Services)
	assert.Equal(t, 3, result.Seeded[0].Operations)

	excluded := newMemoryCatalog()
	filtered, err := h.RegisterInto(context.Background(), excluded, describer, SeedOptions{
		ExcludeServices: []string{"toolbox.fixture.v1.HealthService"},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, filtered.Seeded[0].Services, "an excluded service is left out on request")
	assert.Equal(t, 2, filtered.Seeded[0].Operations)
}

func TestRegisterIntoRequiresBothCollaborators(t *testing.T) {
	h := startFixtureSubsystem(t)
	_, err := h.RegisterInto(context.Background(), nil, &stubDescriber{}, SeedOptions{})
	require.Error(t, err)
	_, err = h.RegisterInto(context.Background(), newMemoryCatalog(), nil, SeedOptions{})
	require.Error(t, err)
}

// TestGatewayExposesWhatTheCatalogExposed proves the last link: the gateway reads
// the catalog's own decisions, so a registration that the catalog hides stays out
// of the tool surface.
func TestGatewayExposesWhatTheCatalogExposed(t *testing.T) {
	h := startFixtureSubsystem(t)
	catalog := newMemoryCatalog()
	describer := &stubDescriber{described: describedFixture(t)}
	_, err := h.RegisterInto(context.Background(), catalog, describer, SeedOptions{})
	require.NoError(t, err)

	// The catalog is read through the framework's own interfaces, which is what a
	// composition hands the gateway.
	readable := &api.StaticCatalog{}
	readable.RegisteredServers = []api.Server{catalog.servers["fixture"]}
	readable.RegisteredAPIs = []api.API{catalog.apis["fixture"]}
	// A catalog that tracks exposure reports every operation it knows, exposed or
	// not. Silence means "I do not track this", which is a different answer from
	// "I decided to keep this hidden".
	exposures := api.ExposureMap{}
	for _, described := range readable.RegisteredAPIs {
		for _, operation := range described.Operations() {
			exposures[operation.ID] = api.Exposure{
				OperationID: operation.ID,
				Allowed:     true,
				Exposed:     catalog.exposure[operation.ID].Exposed,
				Invokable:   true,
			}
		}
	}
	readable.ExposedOperations = exposures

	gateway, err := mcp.NewFromAPICatalog(context.Background(), readable, api.InvokerFunc(
		func(context.Context, api.Call) (api.Result, error) {
			return api.Result{Status: 200, Body: json.RawMessage(`{"things":[]}`)}, nil
		},
	), mcp.APICatalogOptions{})
	require.NoError(t, err)

	features := gateway.Features()
	require.Len(t, features, 3, "every operation the description declares is part of the surface")
	byID := map[string]mcp.Feature{}
	for _, feature := range features {
		byID[feature.ID] = feature
	}
	// A catalog operation is named by its API and service, joined with a dot, so two
	// APIs that both have a "list" cannot collide.
	listed := byID["fixture."+fixtureService+"/ListThings"]
	assert.True(t, listed.Allowed)
	assert.True(t, listed.Exposed, "the catalog exposed it, so the gateway offers it")
	assert.True(t, listed.Callable)

	// Hiding an operation in the catalog removes it from the gateway's surface,
	// even though the gateway's own policy would allow it.
	// The exposure is keyed by the operation's own identifier, which is the
	// description's identifier rather than the gateway's tool name.
	exposures["fixture/"+fixtureService+"/DeleteThing"] = api.Exposure{
		OperationID: "fixture/" + fixtureService + "/DeleteThing",
		Allowed:     true,
		Exposed:     false,
		Invokable:   true,
	}
	hidden, err := mcp.NewFromAPICatalog(context.Background(), readable, api.InvokerFunc(
		func(context.Context, api.Call) (api.Result, error) { return api.Result{}, nil },
	), mcp.APICatalogOptions{})
	require.NoError(t, err)
	features = hidden.Features()
	require.Len(t, features, 3)
	for _, feature := range features {
		if feature.Method == "DeleteThing" {
			assert.False(t, feature.Exposed, "a hidden operation is not offered")
		}
	}
}

// TestRealContractIsReadFromARealEndpoint closes the loop with no stubs: the
// framework's own contract, served by a real subsystem, read from that subsystem's
// reflection.
func TestRealContractIsReadFromARealEndpoint(t *testing.T) {
	// The framework's own contract is the one contract whose descriptor is linked
	// into this test binary, so it is what a real reflection read can be pointed
	// at here.
	path, handler := apiv1connect.NewApiParserServiceHandler(&apiv1connect.UnimplementedApiParserServiceHandler{})
	h := startSubsystem(t, "framework", []subsystem.Service{{
		Name:    apiv1connect.ApiParserServiceName,
		Path:    path,
		Handler: handler,
	}})
	catalog := newMemoryCatalog()

	result, err := h.RegisterInto(
		context.Background(),
		catalog,
		protocontract.NewDescriptor(protocontract.Descriptor{}),
		SeedOptions{},
	)
	require.NoError(t, err)
	require.Len(t, result.Seeded, 1)
	// The parser contract is registered like any other contract. Several providers
	// serve it on purpose, and a provider subsystem exists to serve it, so hiding
	// it from a catalog would hide the mechanism the catalog itself is made of.
	// Whether an agent gets it is decided by exposure.
	assert.Equal(t, 1, result.Seeded[0].Services)
	assert.Equal(t, 1, result.Seeded[0].Operations)
	stored, ok := catalog.apis["framework"]
	require.True(t, ok)
	require.Len(t, stored.Services, 1)
	assert.Equal(t, apiv1connect.ApiParserServiceName, stored.Services[0].Name)
	operation, ok := stored.Operation("framework/" + apiv1connect.ApiParserServiceName + "/ParseApi")
	require.True(t, ok, "the operation was read from real reflection, by its contract name")
	assert.Equal(t, "ParseApi", operation.Method)
	require.NotNil(t, operation.Request)
	assert.Equal(t, "toolbox.api.v1.ParseApiRequest", operation.Request.Ref,
		"the request is named by its protobuf message, read from real reflection")
}
