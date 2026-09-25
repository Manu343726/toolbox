package core_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	endpoint string
}

func TestStaticResolverIndexesSubsystemAndServiceNames(t *testing.T) {
	resolver := core.NewStaticResolver(core.Endpoint{
		Name:         "weather",
		URL:          "http://127.0.0.1:9000",
		ServiceNames: []string{"weather.v1.WeatherService", "weather.v1.ForecastService"},
		Capabilities: []string{"weather.forecast"},
	})

	bySubsystem, err := resolver.Resolve(context.Background(), "weather")
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9000", bySubsystem.URL)
	byService, err := resolver.Resolve(context.Background(), "weather.v1.ForecastService")
	require.NoError(t, err)
	assert.Equal(t, "weather", byService.Name)
	assert.Equal(t, []string{"weather.v1.ForecastService", "weather.v1.WeatherService"}, byService.SortedServiceNames())

	resolver.Deregister("weather")
	_, err = resolver.Resolve(context.Background(), "weather.v1.WeatherService")
	assert.ErrorIs(t, err, core.ErrNotFound)
}

func TestTypedBindingFailsWhenServiceCannotBeResolved(t *testing.T) {
	client := core.NewClient(core.ClientOptions{Resolver: core.NewStaticResolver()})
	called := false
	constructor := func(_ connect.HTTPClient, _ string, _ ...connect.ClientOption) fakeClient {
		called = true
		return fakeClient{}
	}

	_, err := core.Bind(context.Background(), client, "missing.v1.Service", constructor)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNotFound)
	assert.False(t, called, "constructor must not be called for an unresolved service")
}

func TestTypedBindingUsesResolvedEndpoint(t *testing.T) {
	resolver := core.NewStaticResolver(core.Endpoint{
		Name: "weather", URL: "http://127.0.0.1:9010", ServiceNames: []string{"weather.v1.WeatherService"},
	})
	client := core.NewClient(core.ClientOptions{Resolver: resolver, HTTPClient: http.DefaultClient})
	bound, err := core.Bind(context.Background(), client, "weather.v1.WeatherService",
		func(_ connect.HTTPClient, endpoint string, _ ...connect.ClientOption) fakeClient {
			return fakeClient{endpoint: endpoint}
		})
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9010", bound.endpoint)
}

func TestMetadataHeaders(t *testing.T) {
	header := core.Metadata{
		RequestID: "req", TraceID: "trace", ActorID: "actor", RunID: "run",
		WorkspaceID: "workspace", PolicyID: "policy", Headers: map[string]string{"X-Test": "yes"},
	}.Header()
	assert.Equal(t, "req", header.Get("X-Toolsbox-Request-Id"))
	assert.Equal(t, "trace", header.Get("X-Toolsbox-Trace-Id"))
	assert.Equal(t, "yes", header.Get("X-Test"))
}

func TestEndpointValidation(t *testing.T) {
	assert.ErrorIs(t, core.ValidateEndpoint(core.Endpoint{}), core.ErrInvalidEndpoint)
	assert.NoError(t, core.ValidateEndpoint(core.Endpoint{Name: "x", URL: "http://localhost"}))
}
