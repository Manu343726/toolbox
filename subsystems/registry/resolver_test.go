package registry_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/core"
	registry "github.com/Manu343726/toolbox/subsystems/registry"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	registryv1connect "github.com/Manu343726/toolbox/subsystems/registry/registryv1/registryv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolverUsesTypedRegistryClientAndResolvesServiceNames(t *testing.T) {
	store := registry.NewMemory(registry.MemoryOptions{DefaultLease: time.Minute})
	server, err := registry.New(registry.Options{Store: store})
	require.NoError(t, err)
	require.NoError(t, server.Start(context.Background()))
	defer func() { require.NoError(t, server.Shutdown(context.Background())) }()

	_, err = store.Register(&registryv1.ServiceDescriptor{
		SubsystemName: "weather", Endpoint: server.Endpoint(),
		ServiceNames: []string{"weather.v1.WeatherService"},
		Capabilities: []*registryv1.Capability{{Name: "weather.forecast"}},
	}, 0)
	require.NoError(t, err)

	resolver := registry.NewResolver(server.Endpoint(), http.DefaultClient)
	endpoint, err := resolver.Resolve(context.Background(), "weather.v1.WeatherService")
	require.NoError(t, err)
	assert.Equal(t, "weather", endpoint.Name)

	_, err = resolver.Resolve(context.Background(), "missing.v1.Service")
	assert.ErrorIs(t, err, core.ErrNotFound)
}

func TestTypedRegistryClientCanRegisterAndResolve(t *testing.T) {
	store := registry.NewMemory(registry.MemoryOptions{})
	server, err := registry.New(registry.Options{Store: store})
	require.NoError(t, err)
	require.NoError(t, server.Start(context.Background()))
	defer func() { require.NoError(t, server.Shutdown(context.Background())) }()

	client := registryv1connect.NewRegistryServiceClient(http.DefaultClient, server.Endpoint())
	response, err := client.Register(context.Background(), connect.NewRequest(&registryv1.RegisterRequest{
		Descriptor_: &registryv1.ServiceDescriptor{SubsystemName: "skills", Endpoint: "http://127.0.0.1:1234"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "skills", response.Msg.GetDescriptor_().GetSubsystemName())
}
