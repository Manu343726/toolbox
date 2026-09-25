package host

import (
	"context"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProviderDirectoryDerivesRolesFromCapabilities proves the extension-point
// discovery a catalog depends on: a subsystem participates by declaring a
// capability, and nothing in the framework has to know the subsystem exists.
func TestProviderDirectoryDerivesRolesFromCapabilities(t *testing.T) {
	directory := (&Host{}).ProviderDirectory()
	providers := directory.ProvidersOf([]ProviderEndpoint{
		{
			Subsystem:             "apiopenapi",
			Endpoint:              "http://127.0.0.1:9001",
			ImplementationVersion: "0.1.0",
			Capabilities: []string{
				api.ParseCapability("openapi"),
				api.RenderCapability("openapi"),
				api.InvokeCapability("http"),
			},
		},
		{
			Subsystem: "apigrpc",
			Endpoint:  "http://127.0.0.1:9002",
			Capabilities: []string{
				api.ParseCapability("grpc"),
				api.InvokeCapability("connectrpc"),
			},
		},
		{
			// A subsystem with no extension-point capability is not a provider,
			// whatever else it offers.
			Subsystem:    "workflow",
			Endpoint:     "http://127.0.0.1:9003",
			Capabilities: []string{"workflow.definition.read"},
		},
	})

	byID := make(map[string]api.Provider, len(providers))
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	require.Len(t, byID, 5, "two subsystems implementing three contracts between them")

	parser := byID["apiopenapi-parser"]
	assert.Equal(t, api.ProviderParser, parser.Role)
	assert.Equal(t, []api.Format{"openapi"}, parser.Formats)
	assert.Equal(t, []string{apiv1connect.ApiParserServiceName}, parser.ServiceNames)

	adapter := byID["apiopenapi-adapter"]
	assert.Equal(t, api.ProviderAdapter, adapter.Role)
	assert.Equal(t, []string{"openapi"}, adapter.Targets)
	assert.Equal(t, []string{apiv1connect.ApiAdapterServiceName}, adapter.ServiceNames)

	invoker := byID["apiopenapi-invoker"]
	assert.Equal(t, api.ProviderInvoker, invoker.Role)
	assert.Equal(t, []api.Transport{"http"}, invoker.Transports)
	assert.Equal(t, []string{apiv1connect.ApiInvokerServiceName}, invoker.ServiceNames)

	assert.Contains(t, byID, "apigrpc-parser")
	assert.Contains(t, byID, "apigrpc-invoker")
	assert.NotContains(t, byID, "apigrpc-adapter", "a subsystem that implements no adapter contract claims no target")
	assert.NotContains(t, byID, "workflow-parser")
}

func TestProviderDirectoryDerivesUserDefinedIdentifiers(t *testing.T) {
	directory := (&Host{}).ProviderDirectory()
	providers := directory.ProvidersOf([]ProviderEndpoint{{
		Subsystem: "apimystery",
		Endpoint:  "http://127.0.0.1:9004",
		Capabilities: []string{
			api.ParseCapability("mystery-idl"),
			api.RenderCapability("mystery-target"),
			api.InvokeCapability("mystery-transport"),
		},
	}})
	require.Len(t, providers, 3)
	byRole := make(map[string]api.Provider, len(providers))
	for _, provider := range providers {
		byRole[provider.Role] = provider
	}
	assert.True(t, byRole[api.ProviderParser].HandlesFormat("mystery-idl"))
	assert.True(t, byRole[api.ProviderAdapter].HandlesTarget("mystery-target"))
	assert.True(t, byRole[api.ProviderInvoker].HandlesTransport("mystery-transport"),
		"an identifier the framework has never seen resolves like any other")
}

func TestProviderDirectoryRefusesUnknownProvider(t *testing.T) {
	directory := (&Host{}).ProviderDirectory()
	_, err := directory.Parser(context.Background(), "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no provider")
}

func TestProviderDirectoryBindsAHostProviderWithoutAResolver(t *testing.T) {
	// A provider the host started knows its own endpoint, so it is reachable
	// without a registry, a resolver, or any configuration a deployment has to get
	// right. That is what makes a single-process deployment work out of the box.
	path, handler := apiv1connect.NewApiInvokerServiceHandler(&noOpInvoker{})
	server, err := subsystem.NewServer(subsystem.Config{
		Name: "apimystery",
		Services: []subsystem.Service{{
			Name:         apiv1connect.ApiInvokerServiceName,
			Path:         path,
			Handler:      handler,
			Capabilities: []string{api.InvokeCapability("mystery")},
		}},
	})
	require.NoError(t, err)
	h := New()
	require.NoError(t, h.Register("apimystery", func() (*subsystem.Server, error) { return server, nil }))
	require.NoError(t, h.Select("apimystery"))
	require.NoError(t, h.Start(context.Background()))
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	directory := h.ProviderDirectory()
	assert.Nil(t, directory.clients, "no resolver has been installed, and none is needed")
	client, err := directory.Invoker(context.Background(), "apimystery-invoker")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

// noOpInvoker is a contract implementation that answers nothing, which is enough
// for a test that binds a client and does not call it.
type noOpInvoker struct {
	apiv1connect.UnimplementedApiInvokerServiceHandler
}

func TestProviderDirectoryDescribeIsStable(t *testing.T) {
	directory := (&Host{}).ProviderDirectory()
	providers := directory.ProvidersOf([]ProviderEndpoint{{
		Subsystem:    "apigrpc",
		Endpoint:     "http://127.0.0.1:9002",
		Capabilities: []string{api.ParseCapability("grpc")},
	}})
	require.Len(t, providers, 1)
	assert.Equal(t, "apigrpc-parser", providers[0].ID)
}

func TestProviderEndpointCarriesCapabilityNames(t *testing.T) {
	// The directory's inputs are Go values supplied by a deployment's own
	// composition; the capability names are the only contract involved, and they
	// are the same strings the registry advertises.
	endpoint := ProviderEndpoint{
		Subsystem:    "s",
		Endpoint:     "http://x",
		Capabilities: []string{api.ParseCapability("f"), api.InvokeCapability("t")},
	}
	directory := (&Host{}).ProviderDirectory()
	providers := directory.ProvidersOf([]ProviderEndpoint{endpoint})
	require.Len(t, providers, 2)
	assert.Contains(t, endpoint.Capabilities, "api.parse.f")
	assert.Contains(t, endpoint.Capabilities, "api.invoke.t")
}

// TestProviderDirectoryBindsEachProviderToItsOwnEndpoint covers the case two
// providers create by existing: several subsystems serve the same contract,
// because serving it is how a format or a transport is contributed. A client
// bound by service name would reach whichever endpoint claimed the name first, so
// a catalog asking for one provider would silently get another's behaviour.
func TestProviderDirectoryBindsEachProviderToItsOwnEndpoint(t *testing.T) {
	h := New()
	for _, name := range []string{"alpha", "beta"} {
		name := name
		path, handler := apiv1connect.NewApiInvokerServiceHandler(&noOpInvoker{})
		server, err := subsystem.NewServer(subsystem.Config{
			Name: name,
			Services: []subsystem.Service{{
				Name:    apiv1connect.ApiInvokerServiceName,
				Path:    path,
				Handler: handler,
				// Both providers claim an invocation transport, which is what makes
				// the catalog able to choose between them.
				Capabilities: []string{api.InvokeCapability("connectrpc")},
			}},
		})
		require.NoError(t, err)
		require.NoError(t, h.Register(name, func() (*subsystem.Server, error) { return server, nil }))
	}
	require.NoError(t, h.Start(context.Background()))
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	directory := h.ProviderDirectory()
	directory.SetResolver(core.NewStaticResolver(core.Endpoint{
		// One endpoint claims the contract on behalf of both providers, which is
		// exactly the ambiguity this test exists to rule out.
		Name:         "any",
		URL:          h.Servers()["alpha"].Endpoint(),
		ServiceNames: []string{apiv1connect.ApiInvokerServiceName},
	}))

	alpha, err := directory.Invoker(context.Background(), "alpha-invoker")
	require.NoError(t, err, "the first provider binds")
	beta, err := directory.Invoker(context.Background(), "beta-invoker")
	require.NoError(t, err, "the second provider binds")
	assert.NotSame(t, alpha, beta,
		"two providers serving one contract are two clients, one per endpoint")

	// A client is memoized per provider, so a second request costs nothing and
	// cannot pick a different endpoint.
	again, err := directory.Invoker(context.Background(), "alpha-invoker")
	require.NoError(t, err)
	assert.Same(t, alpha, again)
}
