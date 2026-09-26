package apitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/protocontract"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	apitoolsv1 "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testFormat = "openapi"

func fixedClock() func() time.Time {
	moment := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return moment }
}

// newTestStore builds a store that permits its whole surface, because most of
// these tests are about the mechanism rather than about the default. The default
// is deny-everything and is tested once, on its own.
func newTestStore(t *testing.T, options ...func(*StoreOptions)) *Memory {
	t.Helper()
	settings := StoreOptions{Now: fixedClock(), Policy: api.AllowAll()}
	for _, option := range options {
		option(&settings)
	}
	return NewMemory(settings)
}

func TestACatalogWithNoPolicyRefusesEveryOperation(t *testing.T) {
	// "No policy" and "every policy" must never be the same value. A catalog nobody
	// stated a policy for still describes and documents everything it holds; none
	// of it is callable.
	store := NewMemory(StoreOptions{Now: fixedClock()})
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	target := testAPI("shop-api")
	target.Services[0].Operations[0].SideEffects = []api.SideEffect{api.SideEffectReadOnly}
	stored, _, err := store.RegisterAPI(target, "shop", false)
	require.NoError(t, err)

	assert.Equal(t, api.DenyAll(), store.Policy())
	_, err = store.SetExposed(stored.ID, "shop-api/pets/getPetById", true)
	assert.ErrorIs(t, err, ErrNotAllowed,
		"a read is not permitted by the absence of a policy, however harmless it is")

	// Hiding is always permitted, so a deployment can record a decision either way.
	changed, err := store.SetExposed(stored.ID, "shop-api/pets/getPetById", false)
	require.NoError(t, err)
	assert.False(t, changed)
}

func testServer(id string) api.Server {
	return api.Server{
		ID:        id,
		Name:      id + " server",
		BaseURL:   "http://" + id + ".example/api",
		Format:    testFormat,
		Transport: "http",
	}
}

func testAPI(id string) api.API {
	return api.API{
		ID:      id,
		Name:    id,
		Version: "1.0.0",
		Title:   id + " API",
		Format:  testFormat,
		Source:  api.Source{Kind: "document", Location: "inline"},
		Services: []api.Service{{
			Name:        "pets",
			Description: "Pet operations",
			Operations: []api.Operation{{
				Name:     "getPetById",
				Method:   "get",
				Path:     "/pets/{petId}",
				Summary:  "Fetch one pet",
				Request:  api.ObjectSchema(api.Property{Name: "petId", Schema: api.StringSchema(), Required: true}),
				Response: api.ObjectSchema(api.Property{Name: "name", Schema: api.StringSchema()}),
				SideEffects: []api.SideEffect{
					api.SideEffectReadOnly,
				},
			}},
		}},
	}
}

func TestRegisterServerReplacesAndRejectsDuplicates(t *testing.T) {
	store := newTestStore(t)

	stored, replaced, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	assert.False(t, replaced)
	assert.Equal(t, api.ServerStatusUnknown, stored.Status)
	assert.Equal(t, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), stored.RegisteredAt)

	_, replaced, err = store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	assert.True(t, replaced, "a second registration without fail_if_exists replaces")

	_, _, err = store.RegisterServer(testServer("shop"), true)
	require.ErrorIs(t, err, ErrAlreadyExists)
}

func TestRegisterServerRejectsIncompleteRegistration(t *testing.T) {
	store := newTestStore(t)

	_, _, err := store.RegisterServer(api.Server{Name: "no id", BaseURL: "http://x"}, false)
	require.ErrorIs(t, err, ErrInvalid)

	_, _, err = store.RegisterServer(api.Server{ID: "no-url"}, false)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestRegisterAPIRejectsFormatMismatchWithServer(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)

	target := testAPI("shop-api")
	target.Format = "grpc"
	_, _, err = store.RegisterAPI(target, "shop", false)
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "grpc")
}

func TestRegisterAPIAnnotatesIdentifiers(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)

	stored, _, err := store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"shop-api/pets/getPetById"}, stored.OperationIDs())
	assert.Equal(t, "shop-api/pets", stored.Services[0].ID)
	assert.Equal(t, []string{"shop"}, stored.ServerIDs)
	assert.Equal(t, "Fetch one pet", stored.Services[0].Operations[0].Summary)
}

func TestRegisterAPIRejectsUnknownServerAndDuplicate(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterAPI(testAPI("shop-api"), "missing", false)
	require.ErrorIs(t, err, ErrNotFound)

	_, _, err = store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", true)
	require.ErrorIs(t, err, ErrAlreadyExists)
}

func TestExposureRequiresPolicyCapabilityAndServerBinding(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterAPI(testAPI("shop-api"), "", false)
	require.NoError(t, err)

	// Unbound API: the catalog refuses to expose because there is nowhere to
	// invoke the operation.
	_, err = store.SetExposed("shop-api", "shop-api/pets/getPetById", true)
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "not bound to a server")

	_, _, err = store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	stored, _, err := store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)

	changed, err := store.SetExposed(stored.ID, "shop-api/pets/getPetById", true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.True(t, store.Exposed("shop-api/pets/getPetById"))

	// Idempotent.
	changed, err = store.SetExposed(stored.ID, "shop-api/pets/getPetById", true)
	require.NoError(t, err)
	assert.False(t, changed)

	changed, err = store.SetExposed(stored.ID, "shop-api/pets/getPetById", false)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, store.Exposed("shop-api/pets/getPetById"))
}

func TestExposureIsRefinedByWhatTheOperationDoes(t *testing.T) {
	// The same class filter, at the catalog: a deployment that permits reads does
	// not thereby permit the writes sitting beside them.
	readsOnly, err := api.NewPolicy(api.Rule{
		Pattern:  "*",
		Classes:  []api.EffectClass{api.EffectClassRead},
		Decision: api.DecisionAllow,
	})
	require.NoError(t, err)
	store := newTestStore(t, func(options *StoreOptions) { options.Policy = readsOnly })
	_, _, err = store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)

	target := testAPI("shop-api")
	// A description nobody classified is not a read, so a read grant does not cover
	// it.
	target.Services[0].Operations[0].SideEffects = nil
	stored, _, err := store.RegisterAPI(target, "shop", false)
	require.NoError(t, err)

	_, err = store.SetExposed(stored.ID, "shop-api/pets/getPetById", true)
	require.ErrorIs(t, err, ErrNotAllowed)

}

func TestExposureRejectsStreamingOperation(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)

	target := testAPI("shop-api")
	target.Services[0].Operations[0].Streaming = api.Streaming{Server: true}
	stored, _, err := store.RegisterAPI(target, "shop", false)
	require.NoError(t, err)

	_, err = store.SetExposed(stored.ID, "shop-api/pets/getPetById", true)
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "streaming")

	footprint := store.Exposure(ExposureFilter{APIID: stored.ID})
	require.Len(t, footprint, 1)
	assert.False(t, footprint[0].Invokable)
}

func TestExposureCountsAndOrdering(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)

	// Two APIs so the report ordering is observable.
	_, _, err = store.RegisterAPI(testAPI("alpha"), "shop", false)
	require.NoError(t, err)
	second := testAPI("beta")
	second.Services[0].Operations = append(second.Services[0].Operations, api.Operation{
		Name:        "deletePet",
		Method:      "delete",
		Path:        "/pets/{petId}",
		SideEffects: []api.SideEffect{api.SideEffectDelete},
	})
	_, _, err = store.RegisterAPI(second, "shop", false)
	require.NoError(t, err)

	_, err = store.SetExposed("alpha", "alpha/pets/getPetById", true)
	require.NoError(t, err)

	footprint := store.Exposure(ExposureFilter{})
	require.Len(t, footprint, 3)
	assert.Equal(t, "alpha/pets/getPetById", footprint[0].Operation.ID)
	assert.Equal(t, "beta/pets/getPetById", footprint[1].Operation.ID)
	assert.Equal(t, "beta/pets/deletePet", footprint[2].Operation.ID, "declared operation order is preserved")
	assert.True(t, footprint[0].Exposed)
	assert.False(t, footprint[1].Exposed)
	assert.True(t, footprint[2].Invokable)
}

func TestReplacingAPIPrunesExposureOfRemovedOperations(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)
	_, err = store.SetExposed("shop-api", "shop-api/pets/getPetById", true)
	require.NoError(t, err)

	replacement := testAPI("shop-api")
	replacement.Services[0].Operations[0].Name = "listPets"
	_, _, err = store.RegisterAPI(replacement, "shop", false)
	require.NoError(t, err)

	assert.False(t, store.Exposed("shop-api/pets/getPetById"), "exposure of a removed operation is pruned")
}

func TestDeleteServerUnbindsOnlyWhenForced(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)

	_, _, err = store.DeleteServer("shop", false)
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "force")

	removed, unbound, err := store.DeleteServer("shop", true)
	require.NoError(t, err)
	assert.True(t, removed)
	assert.Equal(t, []string{"shop-api"}, unbound)

	stored, err := store.GetAPI("shop-api")
	require.NoError(t, err)
	assert.Empty(t, stored.ServerIDs, "the API survives; it just has no location")

	_, _, err = store.DeleteServer("shop", true)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFormatAndTransportIndexAreOpen(t *testing.T) {
	store := newTestStore(t)

	// A format the framework has never heard of indexes like any other.
	descriptor, replaced, err := store.RegisterFormat(api.FormatDescriptor{
		ID:                   "smithy",
		Name:                 "Smithy IDL",
		SpecificationVersion: "1.0",
		Provider:             "smithy-parser",
	}, false)
	require.NoError(t, err)
	assert.False(t, replaced)
	assert.Equal(t, "smithy", descriptor.ID)

	_, _, err = store.RegisterFormat(api.FormatDescriptor{ID: "smithy", Name: "Smithy"}, true)
	require.ErrorIs(t, err, ErrAlreadyExists)

	transport, _, err := store.RegisterTransport(api.TransportDescriptor{
		ID:      "carrier-pigeon",
		Name:    "Carrier pigeon",
		Schemes: []string{"pigeon"},
	}, false)
	require.NoError(t, err)
	assert.Equal(t, "carrier-pigeon", transport.ID)

	_, _, err = store.RegisterTransport(api.TransportDescriptor{Name: "no id"}, false)
	require.ErrorIs(t, err, ErrInvalid)

	formats := store.Formats()
	require.Len(t, formats, 1)
	assert.Equal(t, "smithy", formats[0].ID)
	require.Len(t, store.Transports(), 1)
}

func TestFormatUsageCountsRegisteredAPIs(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)

	usage := store.FormatUsage()
	assert.Equal(t, 1, usage[testFormat])
	assert.Equal(t, 1, store.TransportUsage()["http"])
}

func TestListAPIsFilters(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("pets-api"), "shop", false)
	require.NoError(t, err)
	billing := testAPI("billing-api")
	// One API whose operation declares a write, so the effect filter has something
	// to exclude.
	billing.Services[0].Operations[0].SideEffects = []api.SideEffect{api.SideEffectFinancial}
	_, _, err = store.RegisterAPI(billing, "", false)
	require.NoError(t, err)

	byServer := store.ListAPIs(APIFilter{ServerID: "shop"})
	require.Len(t, byServer, 1)
	assert.Equal(t, "pets-api", byServer[0].ID)

	byFormat := store.ListAPIs(APIFilter{Format: testFormat})
	assert.Len(t, byFormat, 2)

	byEffect := store.ListAPIs(APIFilter{SideEffects: []api.SideEffect{api.SideEffectReadOnly}})
	require.Len(t, byEffect, 1)
	assert.Equal(t, "pets-api", byEffect[0].ID,
		"the filter asks what an API's operations do, not what a deployment permits")

	byQuery := store.ListAPIs(APIFilter{Query: "billing"})
	require.Len(t, byQuery, 1)
	assert.Equal(t, "billing-api", byQuery[0].ID)

	all := store.ListAPIs(APIFilter{})
	require.Len(t, all, 2)
	assert.Equal(t, "billing-api", all[0].ID, "listings are ordered by identifier")
}

func TestCatalogViewExposesServersAndAPIs(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)

	var catalog api.Catalog = store.Catalog()
	servers, err := catalog.Servers(context.Background())
	require.NoError(t, err)
	require.Len(t, servers, 1)
	assert.Equal(t, "shop", servers[0].ID)

	apis, err := catalog.APIs(context.Background())
	require.NoError(t, err)
	require.Len(t, apis, 1)
	assert.Equal(t, "shop-api", apis[0].ID)
}

func TestConcurrentRegistrationAndExposure(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)

	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = store.SetExposed("shop-api", "shop-api/pets/getPetById", true)
			_ = store.Exposure(ExposureFilter{})
			_ = store.ListAPIs(APIFilter{})
			_ = store.Revision()
		}()
	}
	wait.Wait()
	assert.True(t, store.Exposed("shop-api/pets/getPetById"))
}

// fakeProvider serves the framework's parser and invocation contracts in
// process, so the catalog's provider routing is exercised over real ConnectRPC
// without a second subsystem.
type fakeProvider struct {
	parse   func(*apiv1.ParseApiRequest) (*apiv1.ParseApiResponse, error)
	invoke  func(*apiv1.InvokeApiRequest) (*apiv1.InvokeApiResponse, error)
	render  func(*apiv1.RenderApiRequest) (*apiv1.RenderApiResponse, error)
	serve   func(*apiv1.ServeApiRequest) (*apiv1.ServeApiResponse, error)
	stop    func(*apiv1.StopApiRequest) (*apiv1.StopApiResponse, error)
	formats []api.FormatDescriptor
	stopped []string
}

func (p *fakeProvider) ParseApi(ctx context.Context, req *connect.Request[apiv1.ParseApiRequest]) (*connect.Response[apiv1.ParseApiResponse], error) {
	if p.parse == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no parser configured"))
	}
	response, err := p.parse(req.Msg)
	if err != nil {
		return nil, err
	}
	// A real parser reports the format descriptors it implements, so the
	// catalog can index them without a separate registration step.
	for _, descriptor := range p.formats {
		response.Formats = append(response.Formats, descriptor.ToProto())
	}
	return connect.NewResponse(response), nil
}

// RenderApi implements the adapter contract for the test provider.
func (p *fakeProvider) RenderApi(ctx context.Context, req *connect.Request[apiv1.RenderApiRequest]) (*connect.Response[apiv1.RenderApiResponse], error) {
	if p.render == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no renderer configured"))
	}
	response, err := p.render(req.Msg)
	if err != nil {
		return nil, err
	}
	for _, descriptor := range p.formats {
		response.Targets = append(response.Targets, descriptor.ToProto())
	}
	return connect.NewResponse(response), nil
}

// ServeApi implements the adapter contract for the test provider.
func (p *fakeProvider) ServeApi(ctx context.Context, req *connect.Request[apiv1.ServeApiRequest]) (*connect.Response[apiv1.ServeApiResponse], error) {
	if p.serve == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no server configured"))
	}
	response, err := p.serve(req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

// StopApi implements the adapter contract for the test provider.
func (p *fakeProvider) StopApi(ctx context.Context, req *connect.Request[apiv1.StopApiRequest]) (*connect.Response[apiv1.StopApiResponse], error) {
	if p.stop != nil {
		response, err := p.stop(req.Msg)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(response), nil
	}
	p.stopped = append(p.stopped, req.Msg.GetInstanceId())
	return connect.NewResponse(&apiv1.StopApiResponse{Stopped: true}), nil
}

func (p *fakeProvider) InvokeApi(ctx context.Context, req *connect.Request[apiv1.InvokeApiRequest]) (*connect.Response[apiv1.InvokeApiResponse], error) {
	if p.invoke == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no invoker configured"))
	}
	response, err := p.invoke(req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func serveProvider(t *testing.T, provider *fakeProvider) string {
	t.Helper()
	mux := http.NewServeMux()
	parserPath, parserHandler := apiv1connect.NewApiParserServiceHandler(provider)
	invokerPath, invokerHandler := apiv1connect.NewApiInvokerServiceHandler(provider)
	adapterPath, adapterHandler := apiv1connect.NewApiAdapterServiceHandler(provider)
	mux.Handle(parserPath, parserHandler)
	mux.Handle(invokerPath, invokerHandler)
	mux.Handle(adapterPath, adapterHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func staticDirectory(t *testing.T, endpoint string, roles ...api.ProviderRole) *StaticDirectory {
	t.Helper()
	httpClient := http.DefaultClient
	parsers := map[string]ParserClient{}
	adapters := map[string]AdapterClient{}
	invokers := map[string]InvokerClient{}
	providers := make([]api.Provider, 0, len(roles))
	for _, role := range roles {
		provider := api.Provider{ID: "fake-" + role, Subsystem: "fake", Role: role, Endpoint: endpoint}
		switch role {
		case api.ProviderParser:
			provider.Formats = []api.Format{testFormat}
			provider.ServiceNames = []string{apiv1connect.ApiParserServiceName}
			parsers[provider.ID] = apiv1connect.NewApiParserServiceClient(httpClient, endpoint)
		case api.ProviderAdapter:
			provider.Targets = []string{"openapi"}
			provider.ServiceNames = []string{apiv1connect.ApiAdapterServiceName}
			adapters[provider.ID] = apiv1connect.NewApiAdapterServiceClient(httpClient, endpoint)
		case api.ProviderInvoker:
			provider.Transports = []api.Transport{"http"}
			provider.ServiceNames = []string{apiv1connect.ApiInvokerServiceName}
			invokers[provider.ID] = apiv1connect.NewApiInvokerServiceClient(httpClient, endpoint)
		}
		providers = append(providers, provider)
	}
	directory, err := NewStaticDirectory(providers, parsers, adapters, invokers)
	require.NoError(t, err)
	return directory
}

func TestStaticDirectoryRequiresClientForDeclaredRole(t *testing.T) {
	_, err := NewStaticDirectory([]api.Provider{{ID: "p", Role: api.ProviderParser}}, nil, nil)
	require.Error(t, err, "a declared parser must have a client")

	_, err = NewStaticDirectory([]api.Provider{{Role: api.ProviderParser}}, nil, nil)
	require.Error(t, err)
}

func TestServiceRoutesParseThroughProviderAndIndexesFormat(t *testing.T) {
	provider := &fakeProvider{
		formats: []api.FormatDescriptor{{
			ID:                   testFormat,
			Name:                 "OpenAPI",
			SpecificationVersion: "3.1.0",
			Provider:             "fake-openapi-parser",
		}},
		parse: func(*apiv1.ParseApiRequest) (*apiv1.ParseApiResponse, error) {
			target := testAPI("shop-api")
			normalized, err := target.Normalize()
			if err != nil {
				return nil, err
			}
			return &apiv1.ParseApiResponse{Api: normalized.ToProto(), Warnings: []string{"skipped HEAD /pets"}}, nil
		},
	}
	endpoint := serveProvider(t, provider)
	directory := staticDirectory(t, endpoint, api.ProviderParser)
	store := newTestStore(t)
	service := NewService(store, directory)

	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)

	response, err := service.RegisterApi(context.Background(), connect.NewRequest(&apitoolsv1.RegisterApiRequest{Format: testFormat, ServerId: "shop", Document: []byte(`{"openapi":"3.1.0"}`)}))
	require.NoError(t, err)
	assert.Equal(t, "fake-parser", response.Msg.GetParserId())
	assert.Equal(t, "shop-api", response.Msg.GetApi().GetId())
	assert.Equal(t, int32(1), response.Msg.GetSummary().GetOperations())
	assert.False(t, response.Msg.GetReplaced())

	// The parser contributed its format descriptor, so the index learns the
	// format without a separate registration step.
	indexed, ok := store.Format(testFormat)
	require.True(t, ok)
	assert.Equal(t, "3.1.0", indexed.SpecificationVersion)
	assert.Equal(t, "fake-openapi-parser", indexed.Provider)

	// And discovery reports the format as available because a parser is present.
	formats, err := service.ListApiFormats(context.Background(), connect.NewRequest(&apitoolsv1.ListApiFormatsRequest{}))
	require.NoError(t, err)
	require.Len(t, formats.Msg.GetFormats(), 1)
	assert.True(t, formats.Msg.GetFormats()[0].GetParserAvailable())
	assert.True(t, formats.Msg.GetFormats()[0].GetDescribed())
	assert.Equal(t, int32(1), formats.Msg.GetFormats()[0].GetRegisteredApis())
}

func TestServiceReportsNoProviderWhenDirectoryMissing(t *testing.T) {
	store := newTestStore(t)
	service := NewService(store, nil)

	_, err := service.RegisterApi(context.Background(), connect.NewRequest(&apitoolsv1.RegisterApiRequest{
		Format:   testFormat,
		Document: []byte(`{}`),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestServiceSelectsParserByFormatAndRejectsAmbiguity(t *testing.T) {
	provider := &fakeProvider{parse: func(*apiv1.ParseApiRequest) (*apiv1.ParseApiResponse, error) {
		target := testAPI("shop-api")
		normalized, err := target.Normalize()
		if err != nil {
			return nil, err
		}
		return &apiv1.ParseApiResponse{Api: normalized.ToProto()}, nil
	}}
	endpoint := serveProvider(t, provider)
	httpClient := http.DefaultClient
	parsers := map[string]ParserClient{
		"first":  apiv1connect.NewApiParserServiceClient(httpClient, endpoint),
		"second": apiv1connect.NewApiParserServiceClient(httpClient, endpoint),
	}
	providers := []api.Provider{
		{ID: "first", Role: api.ProviderParser, Endpoint: endpoint, Formats: []api.Format{testFormat}},
		{ID: "second", Role: api.ProviderParser, Endpoint: endpoint, Formats: []api.Format{testFormat}},
	}
	directory, err := NewStaticDirectory(providers, parsers, nil)
	require.NoError(t, err)
	service := NewService(newTestStore(t), directory)

	_, err = service.ParseApi(context.Background(), connect.NewRequest(&apitoolsv1.ParseApiRequest{Format: testFormat, Document: []byte(`{}`)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "select one with parser_id")

	response, err := service.ParseApi(context.Background(), connect.NewRequest(&apitoolsv1.ParseApiRequest{
		Format:   testFormat,
		ParserId: "second",
		Document: []byte(`{}`),
	}))
	require.NoError(t, err)
	assert.Equal(t, "second", response.Msg.GetParserId())

	_, err = service.ParseApi(context.Background(), connect.NewRequest(&apitoolsv1.ParseApiRequest{Format: "unknown", Document: []byte(`{}`)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestServiceCallOperationEnforcesExposureThenRoutesToAdapter(t *testing.T) {
	var received *apiv1.InvokeApiRequest
	provider := &fakeProvider{invoke: func(request *apiv1.InvokeApiRequest) (*apiv1.InvokeApiResponse, error) {
		received = request
		return &apiv1.InvokeApiResponse{
			Status:      200,
			ContentType: "application/json",
			BodyJson:    json.RawMessage(`{"name":"rex"}`),
		}, nil
	}}
	endpoint := serveProvider(t, provider)
	directory := staticDirectory(t, endpoint, api.ProviderInvoker)
	store := newTestStore(t)
	service := NewService(store, directory)

	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)

	// Not exposed yet: a call must be refused before any provider is contacted.
	_, err = service.CallOperation(context.Background(), connect.NewRequest(&apitoolsv1.CallOperationRequest{
		ApiId:       "shop-api",
		OperationId: "shop-api/pets/getPetById",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Nil(t, received, "no provider call is made for an unexposed operation")

	_, err = service.ExposeOperation(context.Background(), connect.NewRequest(&apitoolsv1.ExposeOperationRequest{ApiId: "shop-api", OperationId: "shop-api/pets/getPetById"}))
	require.NoError(t, err)

	response, err := service.CallOperation(context.Background(), connect.NewRequest(&apitoolsv1.CallOperationRequest{
		ApiId:         "shop-api",
		OperationId:   "shop-api/pets/getPetById",
		ArgumentsJson: json.RawMessage(`{"petId":"7"}`),
		RequestId:     "req-1",
	}))
	require.NoError(t, err)
	assert.Equal(t, int32(200), response.Msg.GetStatus())
	assert.JSONEq(t, `{"name":"rex"}`, string(response.Msg.GetBodyJson()))
	assert.Equal(t, "fake-invoker", response.Msg.GetAdapterId())

	require.NotNil(t, received)
	assert.Equal(t, "shop", received.GetServerId())
	assert.Equal(t, "shop-api", received.GetApiId())
	assert.Equal(t, "shop-api/pets/getPetById", received.GetOperationId())
	assert.Equal(t, "req-1", received.GetRequestId())
	// The request is self-describing: the adapter received the server, API and
	// operation it needs without a second lookup.
	assert.Equal(t, "http://shop.example/api", received.GetServer().GetBaseUrl())
	assert.Equal(t, "getPetById", received.GetOperation().GetName())
}

func TestServiceCallOperationRejectsNonObjectArguments(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)
	_, err = store.SetExposed("shop-api", "shop-api/pets/getPetById", true)
	require.NoError(t, err)

	service := NewService(store, nil)
	_, err = service.CallOperation(context.Background(), connect.NewRequest(&apitoolsv1.CallOperationRequest{
		ApiId:         "shop-api",
		OperationId:   "shop-api/pets/getPetById",
		ArgumentsJson: json.RawMessage(`["not","an","object"]`),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestResolverDirectoryBindsGeneratedClients(t *testing.T) {
	provider := &fakeProvider{parse: func(*apiv1.ParseApiRequest) (*apiv1.ParseApiResponse, error) {
		target := testAPI("shop-api")
		normalized, err := target.Normalize()
		if err != nil {
			return nil, err
		}
		return &apiv1.ParseApiResponse{Api: normalized.ToProto()}, nil
	}}
	endpoint := serveProvider(t, provider)

	resolver := core.NewStaticResolver(core.Endpoint{
		Name:         "fake-parser",
		URL:          endpoint,
		ServiceNames: []string{apiv1connect.ApiParserServiceName},
	})
	directory, err := NewResolverDirectory([]api.Provider{{
		ID:           "fake-parser",
		Role:         api.ProviderParser,
		Endpoint:     endpoint,
		Formats:      []api.Format{testFormat},
		ServiceNames: []string{apiv1connect.ApiParserServiceName},
	}}, resolver)
	require.NoError(t, err)

	service := NewService(newTestStore(t), directory)
	response, err := service.ParseApi(context.Background(), connect.NewRequest(&apitoolsv1.ParseApiRequest{
		Format:   testFormat,
		Document: []byte(`{}`),
	}))
	require.NoError(t, err)
	assert.Equal(t, "shop-api", response.Msg.GetApi().GetId())
}

func TestResolverDirectoryFailsWhenProviderCannotBeResolved(t *testing.T) {
	resolver := core.NewStaticResolver()
	directory, err := NewResolverDirectory([]api.Provider{{
		ID:       "ghost",
		Role:     api.ProviderParser,
		Endpoint: "http://ghost.invalid",
		Formats:  []api.Format{testFormat},
	}}, resolver)
	require.NoError(t, err)

	service := NewService(newTestStore(t), directory)
	_, err = service.ParseApi(context.Background(), connect.NewRequest(&apitoolsv1.ParseApiRequest{
		Format:   testFormat,
		Document: []byte(`{}`),
	}))
	require.Error(t, err)
	assert.True(t,
		errors.Is(err, ErrNoProvider) || strings.Contains(err.Error(), "resolve parser provider"),
		"resolution failure is reported before any call: %v", err,
	)
}

func TestServiceDescribesOperationFootprint(t *testing.T) {
	store := newTestStore(t)
	service := NewService(store, staticDirectory(t, serveProvider(t, &fakeProvider{}), api.ProviderInvoker))
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)

	response, err := service.DescribeOperation(context.Background(), connect.NewRequest(&apitoolsv1.DescribeOperationRequest{ApiId: "shop-api", OperationId: "shop-api/pets/getPetById"}))
	require.NoError(t, err)
	assert.True(t, response.Msg.GetAllowed())
	assert.False(t, response.Msg.GetExposed())
	assert.True(t, response.Msg.GetInvokable())

	exposure, err := service.OperationExposure(context.Background(), connect.NewRequest(&apitoolsv1.OperationExposureRequest{}))
	require.NoError(t, err)
	assert.Equal(t, int32(1), exposure.Msg.GetAllowed())
	assert.Equal(t, int32(0), exposure.Msg.GetExposed())
	assert.Equal(t, int32(1), exposure.Msg.GetHidden())
	assert.Equal(t, int32(0), exposure.Msg.GetDenied())
	assert.True(t, exposure.Msg.GetInvokersAvailable())
}

func TestServiceRegistersAndIndexesUnknownFormat(t *testing.T) {
	provider := &fakeProvider{parse: func(*apiv1.ParseApiRequest) (*apiv1.ParseApiResponse, error) {
		target := testAPI("smithy-api")
		target.Format = "smithy"
		normalized, err := target.Normalize()
		if err != nil {
			return nil, err
		}
		return &apiv1.ParseApiResponse{Api: normalized.ToProto()}, nil
	}}
	endpoint := serveProvider(t, provider)
	directory, err := NewStaticDirectory([]api.Provider{{
		ID:           "smithy-parser",
		Role:         api.ProviderParser,
		Endpoint:     endpoint,
		Formats:      []api.Format{"smithy"},
		ServiceNames: []string{apiv1connect.ApiParserServiceName},
	}}, map[string]ParserClient{
		"smithy-parser": apiv1connect.NewApiParserServiceClient(http.DefaultClient, endpoint),
	}, nil)
	require.NoError(t, err)
	store := newTestStore(t)
	service := NewService(store, directory)

	_, _, err = store.RegisterServer(api.Server{
		ID:        "smithy-host",
		Name:      "Smithy host",
		BaseURL:   "http://smithy.example",
		Format:    "smithy",
		Transport: "carrier-pigeon",
	}, false)
	require.NoError(t, err)

	_, err = service.RegisterApi(context.Background(), connect.NewRequest(&apitoolsv1.RegisterApiRequest{
		Format:   "smithy",
		ServerId: "smithy-host",
		Document: []byte("#@ service"),
	}))
	require.NoError(t, err)

	stored, err := store.GetAPI("smithy-api")
	require.NoError(t, err)
	assert.Equal(t, "smithy", stored.Format)
	assert.Equal(t, []string{"smithy-host"}, stored.ServerIDs)

	// A user-defined format is discoverable in the index even though the
	// framework knows nothing about it.
	formats, err := service.ListApiFormats(context.Background(), connect.NewRequest(&apitoolsv1.ListApiFormatsRequest{OnlyAvailable: true}))
	require.NoError(t, err)
	require.Len(t, formats.Msg.GetFormats(), 1)
	assert.Equal(t, "smithy", formats.Msg.GetFormats()[0].GetId())
	assert.False(t, formats.Msg.GetFormats()[0].GetDescribed())
	assert.True(t, formats.Msg.GetFormats()[0].GetParserAvailable())
}

// --- rendering and serving through a provider ---

func TestServiceRendersDescriptionThroughAdapterProvider(t *testing.T) {
	provider := &fakeProvider{
		formats: []api.FormatDescriptor{{ID: "openapi", Name: "OpenAPI"}},
		render: func(request *apiv1.RenderApiRequest) (*apiv1.RenderApiResponse, error) {
			assert.Equal(t, "openapi", request.GetTarget())
			// The adapter receives the standard description and nothing about
			// where it came from, which is what makes the conversion
			// direction-agnostic.
			assert.Equal(t, "shop-api", request.GetApi().GetId())
			return &apiv1.RenderApiResponse{
				Files: []*apiv1.ApiSchemaFile{{
					Name:      "openapi.json",
					Content:   []byte(`{"openapi":"3.1.0"}`),
					MediaType: "application/json",
					Primary:   true,
				}},
				MediaType:     "application/json",
				FileExtension: "json",
			}, nil
		},
	}
	endpoint := serveProvider(t, provider)
	directory := staticDirectory(t, endpoint, api.ProviderAdapter)
	store := newTestStore(t)
	service := NewService(store, directory)

	response, err := service.RenderApi(context.Background(), connect.NewRequest(&apitoolsv1.RenderApiRequest{
		Api:    normalizedProtoForTest(t, "shop-api"),
		Target: "openapi",
	}))
	require.NoError(t, err)
	assert.Equal(t, "fake-adapter", response.Msg.GetAdapterId())
	require.Len(t, response.Msg.GetFiles(), 1)
	assert.True(t, response.Msg.GetFiles()[0].GetPrimary())
	assert.JSONEq(t, `{"openapi":"3.1.0"}`, string(response.Msg.GetFiles()[0].GetContent()))

	// The adapter contributed the target descriptor, so the index learns what the
	// deployment can produce.
	indexed, ok := store.Format("openapi")
	require.True(t, ok)
	assert.Equal(t, "OpenAPI", indexed.Name)
}

func TestServiceRenderRejectsUnknownTarget(t *testing.T) {
	provider := &fakeProvider{}
	service := NewService(newTestStore(t), staticDirectory(t, serveProvider(t, provider), api.ProviderAdapter))

	_, err := service.RenderApi(context.Background(), connect.NewRequest(&apitoolsv1.RenderApiRequest{
		Api:    normalizedProtoForTest(t, "shop-api"),
		Target: "protoset",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestServiceServesRegisteredAPIItselfBecomesAServer(t *testing.T) {
	provider := &fakeProvider{serve: func(request *apiv1.ServeApiRequest) (*apiv1.ServeApiResponse, error) {
		// The provider is given the original server, so it can tunnel to it.
		assert.Equal(t, "shop", request.GetServer().GetId())
		assert.Equal(t, "http://shop.example/api", request.GetServer().GetBaseUrl())
		assert.Equal(t, "shop-api", request.GetApi().GetId())
		return &apiv1.ServeApiResponse{
			Target:                "openapi",
			Endpoint:              "http://127.0.0.1:9999/_toolbox/adapted",
			BasePath:              "/_toolbox/adapted",
			Paths:                 []string{"/_toolbox/adapted/pets/{petId}"},
			DocumentationEndpoint: "/_toolbox/adapted/_docs/",
			SchemaEndpoint:        "/_toolbox/adapted/_schema/openapi.json",
			InstanceId:            "shop-api-served",
		}, nil
	}}
	store := newTestStore(t)
	service := NewService(store, staticDirectory(t, serveProvider(t, provider), api.ProviderAdapter))
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)

	response, err := service.ServeApi(context.Background(), connect.NewRequest(&apitoolsv1.ServeApiRequest{
		ApiId:  "shop-api",
		Target: "openapi",
	}))
	require.NoError(t, err)
	assert.Equal(t, "fake-adapter", response.Msg.GetAdapterId())
	assert.Equal(t, "/_toolbox/adapted/_schema/openapi.json", response.Msg.GetSchemaEndpoint())

	// The served surface is a registered server, so it can be listed and bound
	// like any other.
	served, err := store.GetServer("shop-api-served")
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9999/_toolbox/adapted", served.BaseURL)
	list := service.store.ListServers(ServerFilter{})
	require.Len(t, list, 2)
}

func TestServiceServeRequiresABoundServer(t *testing.T) {
	store := newTestStore(t)
	service := NewService(store, staticDirectory(t, serveProvider(t, &fakeProvider{}), api.ProviderAdapter))
	_, _, err := store.RegisterAPI(testAPI("shop-api"), "", false)
	require.NoError(t, err)

	_, err = service.ServeApi(context.Background(), connect.NewRequest(&apitoolsv1.ServeApiRequest{
		ApiId:  "shop-api",
		Target: "openapi",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "not bound to a server")
}

func TestServiceStopsAdaptedSurface(t *testing.T) {
	provider := &fakeProvider{serve: func(*apiv1.ServeApiRequest) (*apiv1.ServeApiResponse, error) {
		return &apiv1.ServeApiResponse{
			Target:     "openapi",
			Endpoint:   "http://127.0.0.1:9999/adapted",
			InstanceId: "shop-api-served",
		}, nil
	}}
	store := newTestStore(t)
	service := NewService(store, staticDirectory(t, serveProvider(t, provider), api.ProviderAdapter))
	_, _, err := store.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)

	_, err = service.ServeApi(context.Background(), connect.NewRequest(&apitoolsv1.ServeApiRequest{
		ApiId:  "shop-api",
		Target: "openapi",
	}))
	require.NoError(t, err)

	stopped, err := service.StopApi(context.Background(), connect.NewRequest(&apitoolsv1.StopApiRequest{
		InstanceId:   "shop-api-served",
		RemoveServer: true,
	}))
	require.NoError(t, err)
	assert.True(t, stopped.Msg.GetStopped())
	assert.True(t, stopped.Msg.GetServerRemoved())
	assert.Equal(t, []string{"shop-api-served"}, provider.stopped)

	_, err = store.GetServer("shop-api-served")
	require.ErrorIs(t, err, ErrNotFound)
}

// normalizedProtoForTest returns one normalized description in contract form.
func normalizedProtoForTest(t *testing.T, id string) *apiv1.Api {
	t.Helper()
	target := testAPI(id)
	parsed, err := target.Normalize()
	require.NoError(t, err)
	return parsed.ToProto()
}

func TestServiceDescribesALiveEndpointRatherThanADocument(t *testing.T) {
	// A server that already publishes its own contract is the common case: the
	// parser that handles the format reads it from the server, and a caller never
	// has to obtain bytes the server had already published.
	provider := &fakeProvider{parse: func(request *apiv1.ParseApiRequest) (*apiv1.ParseApiResponse, error) {
		assert.Empty(t, request.GetDocument(), "a live endpoint is described without a document")
		assert.Equal(t, "https://petstore.test/mcp", request.GetBaseUrl())
		source := testAPI("petstore")
		described, err := source.Normalize()
		if err != nil {
			return nil, err
		}
		described.Source = api.Source{Kind: "mcp", Location: request.GetBaseUrl()}
		return &apiv1.ParseApiResponse{Api: described.ToProto()}, nil
	}}
	provider.formats = []api.FormatDescriptor{{ID: testFormat, Name: "Test format"}}
	service := NewService(newTestStore(t), staticDirectory(t, serveProvider(t, provider), api.ProviderParser))

	described, err := service.ParseApi(context.Background(), connect.NewRequest(&apitoolsv1.ParseApiRequest{
		Format:  string(testFormat),
		BaseUrl: "https://petstore.test/mcp",
		ApiId:   "petstore",
	}))
	require.NoError(t, err)
	assert.Equal(t, "petstore", described.Msg.GetApi().GetId())
	assert.Equal(t, "mcp", described.Msg.GetApi().GetSource().GetKind())

	// The registration path reads the same way, which is what a deployment does
	// when it points the catalog at a service it does not own.
	_, _, err = service.store.RegisterServer(testServer("petstore-host"), false)
	require.NoError(t, err)
	registered, err := service.RegisterApi(context.Background(), connect.NewRequest(&apitoolsv1.RegisterApiRequest{
		Format:    string(testFormat),
		BaseUrl:   "https://petstore.test/mcp",
		ApiId:     "petstore",
		ServerId:  "petstore-host",
		ExposeAll: true,
	}))
	require.NoError(t, err)
	assert.Equal(t, "petstore", registered.Msg.GetApi().GetId())
	assert.Equal(t, []string{"petstore-host"}, registered.Msg.GetApi().GetServerIds(),
		"a description read from a live endpoint is bound to the server that served it")
}

func TestServiceRefusesADescribeWithNoSource(t *testing.T) {
	service := NewService(newTestStore(t), staticDirectory(t, serveProvider(t, &fakeProvider{}), api.ProviderParser))
	_, err := service.ParseApi(context.Background(), connect.NewRequest(&apitoolsv1.ParseApiRequest{Format: string(testFormat)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "document or a base url")
}

// The fixtures below are real servers. A test that proved invocation worked
// against a fake would pass whether or not a call could actually travel, and the
// failure this guards against is precisely a call that cannot.

// mountedSubsystem runs a real subsystem serving one contract over the framework's
// own transport, reflection included, and returns the endpoint it listens on. It
// is the shape of every endpoint a host seeds into a catalog.
func mountedSubsystem(t *testing.T) (*parserStub, string) {
	t.Helper()
	stub := &parserStub{}
	path, handler := apiv1connect.NewApiParserServiceHandler(stub)
	server, err := subsystem.NewServer(subsystem.Config{
		Name: "fixture",
		Services: []subsystem.Service{{
			Name:    apiv1connect.ApiParserServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return stub, server.Endpoint()
}

// parserStub answers the one method the fixture contract declares, and records
// what reached it so a test can prove the call travelled rather than assuming it.
type parserStub struct {
	apiv1connect.UnimplementedApiParserServiceHandler
	received []*apiv1.ParseApiRequest
}

func (s *parserStub) ParseApi(
	_ context.Context,
	request *connect.Request[apiv1.ParseApiRequest],
) (*connect.Response[apiv1.ParseApiResponse], error) {
	s.received = append(s.received, request.Msg)
	return connect.NewResponse(&apiv1.ParseApiResponse{
		Api: &apiv1.Api{
			Id:     "answered",
			Name:   "answered",
			Format: "grpc",
			Source: &apiv1.ApiSource{Kind: "reflection", Location: request.Msg.GetBaseUrl()},
		},
	}), nil
}

// seedMounted registers a mounted endpoint in a catalog the way a host does: the
// subsystem's own contract described from its own reflection, the server bound to
// that description, and the transport the framework serves.
func seedMounted(t *testing.T, store *Memory, endpoint string) (string, string) {
	t.Helper()
	described, warnings, err := protocontract.NewDescriptor(protocontract.Descriptor{}).FromEndpoint(t.Context(), endpoint)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.NotEmpty(t, described.Services)

	described.ID = "mounted-api"
	described.Transport = api.Transport(protocontract.TransportDescriptors()[0].ID)
	server, _, err := store.RegisterServer(api.Server{
		ID:        "mounted",
		Name:      "Mounted subsystem",
		BaseURL:   endpoint,
		Format:    "grpc",
		Transport: described.Transport,
	}, false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(described, server.ID, false)
	require.NoError(t, err)

	// Registration namespaces every operation identifier with its API, so the
	// identifier a caller uses is the stored one rather than the one reflection
	// produced.
	stored, err := store.GetAPI(described.ID)
	require.NoError(t, err)
	var operationID string
	for _, service := range stored.Services {
		if service.Name == apiv1connect.ApiParserServiceName && len(service.Operations) > 0 {
			operationID = service.Operations[0].ID
			break
		}
	}
	require.NotEmpty(t, operationID)
	_, err = store.SetExposed(described.ID, operationID, true)
	require.NoError(t, err)
	return described.ID, operationID
}

// A host seeds the operations of every subsystem it serves, and it seeds them
// whether or not an invoker provider happens to be deployed. Requiring a provider
// to call the framework's own transport made the catalog describe tools that did
// not work on any deployment that left the provider out.
func TestCallOperationReachesAMountedServiceWithNoInvokerProvider(t *testing.T) {
	stub, endpoint := mountedSubsystem(t)
	store := newTestStore(t)
	apiID, operationID := seedMounted(t, store, endpoint)

	// No provider directory at all: nothing is deployed beside this catalog.
	service := NewService(store, nil)

	response, err := service.CallOperation(context.Background(), connect.NewRequest(&apitoolsv1.CallOperationRequest{
		ApiId:         apiID,
		OperationId:   operationID,
		ArgumentsJson: json.RawMessage(`{"format":"grpc"}`),
	}))
	require.NoError(t, err, "a seeded operation must be callable on the deployment that seeded it")
	assert.Equal(t, int32(200), response.Msg.GetStatus())
	assert.Equal(t, "application/json", response.Msg.GetContentType())
	assert.Empty(t, response.Msg.GetAdapterId(),
		"no provider executed the call, so there is no provider to name")

	// The call reached the server rather than being satisfied by the catalog, and
	// what came back is the response the server actually sent.
	require.Len(t, stub.received, 1)
	assert.Equal(t, "grpc", stub.received[0].GetFormat())
	var payload struct {
		API struct {
			ID string `json:"id"`
		} `json:"api"`
	}
	require.NoError(t, json.Unmarshal(response.Msg.GetBodyJson(), &payload))
	assert.Equal(t, "answered", payload.API.ID)
}

// The framework answering is a fallback, not a replacement. A provider registered
// for the transport is still the one that runs, because it is a separately
// deployable subsystem and a deployment that chose it chose it deliberately.
func TestCallOperationPrefersARegisteredInvokerOverTheFrameworksOwn(t *testing.T) {
	stub, endpoint := mountedSubsystem(t)
	directory := staticDirectory(t, endpoint, api.ProviderInvoker)
	store := newTestStore(t)
	apiID, operationID := seedMounted(t, store, endpoint)

	// The registered provider claims "http", which is not what the mounted
	// endpoint speaks, so the framework answers — and a provider is not consulted
	// for a transport it did not claim.
	service := NewService(store, directory)
	response, err := service.CallOperation(context.Background(), connect.NewRequest(&apitoolsv1.CallOperationRequest{
		ApiId:         apiID,
		OperationId:   operationID,
		ArgumentsJson: json.RawMessage(`{"format":"grpc"}`),
	}))
	require.NoError(t, err)
	assert.Empty(t, response.Msg.GetAdapterId())
	assert.Len(t, stub.received, 1)
}

// A caller that names an invoker gets that invoker or an error naming why. The
// framework's fallback is never a way to ignore what was asked for.
func TestCallOperationRefusesAnInvokerTheCallerNamedAndDoesNotExist(t *testing.T) {
	_, endpoint := mountedSubsystem(t)
	store := newTestStore(t)
	apiID, operationID := seedMounted(t, store, endpoint)
	service := NewService(store, nil)

	response, err := service.CallOperation(context.Background(), connect.NewRequest(&apitoolsv1.CallOperationRequest{
		ApiId:         apiID,
		OperationId:   operationID,
		ArgumentsJson: json.RawMessage(`{"format":"grpc"}`),
		InvokerId:     "not-deployed",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err),
		"with no directory at all the named invoker cannot exist, and that is what is said")
	assert.Nil(t, response,
		"the framework does not quietly answer a call that named an invoker")

	// With a directory, a name that is not in it is a not-found, which is the
	// answer a caller can act on.
	_, otherEndpoint := mountedSubsystem(t)
	_, _, err = store.RegisterServer(api.Server{
		ID: "other", Name: "Other", BaseURL: otherEndpoint, Format: "grpc",
		Transport: api.Transport(protocontract.TransportDescriptors()[0].ID),
	}, false)
	require.NoError(t, err)
	withDirectory := NewService(store, staticDirectory(t, otherEndpoint, api.ProviderInvoker))
	_, err = withDirectory.CallOperation(context.Background(), connect.NewRequest(&apitoolsv1.CallOperationRequest{
		ApiId:         apiID,
		OperationId:   operationID,
		ArgumentsJson: json.RawMessage(`{"format":"grpc"}`),
		InvokerId:     "not-deployed",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a named invoker the directory does not hold is reported, not replaced")
}

// A transport the framework does not speak still needs a provider, and the error
// says so — naming both what is missing and what the framework could have done.
func TestCallOperationRefusesATransportNeitherTheFrameworkNorAProviderHandles(t *testing.T) {
	store := newTestStore(t)
	server := testServer("shop") // transport "http"
	_, _, err := store.RegisterServer(server, false)
	require.NoError(t, err)
	_, _, err = store.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)
	_, err = store.SetExposed("shop-api", "shop-api/pets/getPetById", true)
	require.NoError(t, err)

	service := NewService(store, nil)
	_, err = service.CallOperation(context.Background(), connect.NewRequest(&apitoolsv1.CallOperationRequest{
		ApiId:       "shop-api",
		OperationId: "shop-api/pets/getPetById",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorIs(t, err, ErrNoProvider)
	assert.Contains(t, err.Error(), `"http"`, "the error names the transport nothing handles")
}

// The exposure report must not claim an operation is uncallable on a deployment
// that calls it perfectly well, which is what counting only providers did.
func TestOperationExposureReportsAnInvokerForATransportTheFrameworkSpeaks(t *testing.T) {
	_, endpoint := mountedSubsystem(t)
	store := newTestStore(t)
	apiID, _ := seedMounted(t, store, endpoint)
	service := NewService(store, nil)

	response, err := service.OperationExposure(context.Background(), connect.NewRequest(&apitoolsv1.OperationExposureRequest{ApiId: apiID}))
	require.NoError(t, err)
	assert.True(t, response.Msg.GetInvokersAvailable())

	// A transport the framework cannot speak is still reported as unavailable
	// when nothing is deployed to serve it.
	shopStore := newTestStore(t)
	_, _, err = shopStore.RegisterServer(testServer("shop"), false)
	require.NoError(t, err)
	_, _, err = shopStore.RegisterAPI(testAPI("shop-api"), "shop", false)
	require.NoError(t, err)
	report, err := NewService(shopStore, nil).OperationExposure(
		context.Background(), connect.NewRequest(&apitoolsv1.OperationExposureRequest{ApiId: "shop-api"}))
	require.NoError(t, err)
	assert.False(t, report.Msg.GetInvokersAvailable())
}
