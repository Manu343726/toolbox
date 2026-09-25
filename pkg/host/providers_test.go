package host

import (
	"context"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A provider declares itself. These tests pin what that means: the directory holds
// what a deployment handed it, the role a provider plays is the record's own field
// rather than something inferred from a capability string, and a subsystem nothing
// registered is not a provider however much it serves.

func providerFixtures() []api.Provider {
	return []api.Provider{{
		ID:           "apiopenapi-parser",
		Subsystem:    "apiopenapi",
		Role:         api.ProviderParser,
		Endpoint:     "http://127.0.0.1:9001",
		Formats:      []api.Format{"openapi"},
		ServiceNames: []string{apiv1connect.ApiParserServiceName},
	}, {
		ID:           "apiopenapi-adapter",
		Subsystem:    "apiopenapi",
		Role:         api.ProviderAdapter,
		Endpoint:     "http://127.0.0.1:9001",
		Targets:      []string{"openapi"},
		ServiceNames: []string{apiv1connect.ApiAdapterServiceName},
	}, {
		ID:           "apigrpc-parser",
		Subsystem:    "apigrpc",
		Role:         api.ProviderParser,
		Endpoint:     "http://127.0.0.1:9002",
		Formats:      []api.Format{"grpc"},
		ServiceNames: []string{apiv1connect.ApiParserServiceName},
	}}
}

func TestProviderDirectoryHoldsWhatTheDeploymentRegistered(t *testing.T) {
	directory := (&Host{}).ProviderDirectory()
	require.NoError(t, directory.Register(providerFixtures()...))

	providers, err := directory.Providers(context.Background())
	require.NoError(t, err)
	require.Len(t, providers, 3, "two subsystems implementing three contracts between them")

	byID := make(map[string]api.Provider, len(providers))
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	parser := byID["apiopenapi-parser"]
	assert.Equal(t, api.ProviderParser, parser.Role)
	assert.Equal(t, []api.Format{"openapi"}, parser.Formats)
	assert.Equal(t, []string{apiv1connect.ApiParserServiceName}, parser.ServiceNames)
	assert.Equal(t, api.ServerStatusServing, parser.Status,
		"a registered provider is serving until something reports otherwise")

	adapter := byID["apiopenapi-adapter"]
	assert.Equal(t, api.ProviderAdapter, adapter.Role)
	assert.Equal(t, []string{"openapi"}, adapter.Targets)

	// A subsystem nobody registered is not a provider, whatever else it serves.
	assert.NotContains(t, byID, "workflow-parser")
}

func TestRegisteringAProviderTwiceReplacesIt(t *testing.T) {
	// A subsystem restarted on a new port is the same provider at a new address.
	directory := (&Host{}).ProviderDirectory()
	require.NoError(t, directory.Register(providerFixtures()...))
	moved := providerFixtures()[0]
	moved.Endpoint = "http://127.0.0.1:9999"
	require.NoError(t, directory.Register(moved))

	providers, err := directory.Providers(context.Background())
	require.NoError(t, err)
	require.Len(t, providers, 3, "the identifier is the provider's, not the endpoint's")
	byID := map[string]api.Provider{}
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	assert.Equal(t, "http://127.0.0.1:9999", byID["apiopenapi-parser"].Endpoint)
}

func TestAProviderNeedsAnIdentifier(t *testing.T) {
	directory := (&Host{}).ProviderDirectory()
	assert.Error(t, directory.Register(api.Provider{Role: api.ProviderParser}),
		"an identifier is what a caller binds a provider by")
}

func TestDirectoryBindsAProviderByItsOwnIdentity(t *testing.T) {
	// Two providers serve one contract on purpose, and a caller naming the contract
	// would reach whichever answered first. Naming the provider reaches the one
	// meant, which is why binding is by identifier and never by service name.
	directory := (&Host{}).ProviderDirectory()
	require.NoError(t, directory.Register(
		api.Provider{
			ID:           "apigrpc-parser",
			Role:         api.ProviderParser,
			Endpoint:     "http://127.0.0.1:9002",
			ServiceNames: []string{apiv1connect.ApiParserServiceName},
		},
		api.Provider{
			ID:           "apimcp-parser",
			Role:         api.ProviderParser,
			Endpoint:     "http://127.0.0.1:9003",
			ServiceNames: []string{apiv1connect.ApiParserServiceName},
		},
	))
	directory.SetResolver(core.NewStaticResolver(
		core.Endpoint{
			Name:         "apigrpc",
			URL:          "http://127.0.0.1:9002",
			ServiceNames: []string{apiv1connect.ApiParserServiceName},
		},
		core.Endpoint{
			Name:         "apimcp",
			URL:          "http://127.0.0.1:9003",
			ServiceNames: []string{apiv1connect.ApiParserServiceName},
		},
	))

	grpc, err := directory.Parser(context.Background(), "apigrpc-parser")
	require.NoError(t, err)
	require.NotNil(t, grpc)
	mcp, err := directory.Parser(context.Background(), "apimcp-parser")
	require.NoError(t, err)
	require.NotNil(t, mcp)
	assert.NotSame(t, grpc, mcp, "two providers, two clients")

	_, err = directory.Parser(context.Background(), "apimcp")
	assert.Error(t, err, "a subsystem name is not a provider identifier")
}
