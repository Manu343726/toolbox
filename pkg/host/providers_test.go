package host

import (
	"context"
	"net/http"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
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

func TestProviderDirectoryReportsWhenNoResolverIsConfigured(t *testing.T) {
	// The directory knows the provider — it derived it from the capabilities — but
	// nothing has told it where the provider is, so binding is refused with a
	// message that says which half is missing.
	directory := (&Host{}).ProviderDirectory()
	directory.host = nil
	_, err := directory.Parser(context.Background(), "apiopenapi-parser")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no provider")

	// A host that has derived a provider but has no resolver yet reports the
	// missing half, rather than reporting the provider as unknown.
	// A host that has started a provider but has no resolver yet reports the
	// missing half: the provider exists, what is missing is its placement.
	h := New()
	require.NoError(t, h.Register("apiopenapi", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "apiopenapi",
			Services: []subsystem.Service{{
				Name:         "toolbox.api.v1.ApiParserService",
				Path:         "/toolbox.api.v1.ApiParserService/",
				Handler:      http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
				Capabilities: []string{api.ParseCapability("openapi")},
			}},
		})
	}))
	require.NoError(t, h.Start(context.Background()))
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	directory = h.ProviderDirectory()
	_, err = directory.Parser(context.Background(), "apiopenapi-parser")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no resolver configured",
		"the provider exists; what is missing is its placement")
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
