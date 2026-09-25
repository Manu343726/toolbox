package main

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	registryv1 "github.com/Manu343726/toolsbox/subsystems/registry/registryv1"
	registryv1connect "github.com/Manu343726/toolsbox/subsystems/registry/registryv1/registryv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildHostRegistersIndependentSubsystems(t *testing.T) {
	h, err := buildHost()
	require.NoError(t, err)
	assert.NoError(t, h.Select("workflow"))
	assert.NoError(t, h.Select("agent", "knowledge"))
}

func TestAllModeRegistersEndpoints(t *testing.T) {
	h, err := buildHost()
	require.NoError(t, err)
	require.NoError(t, h.Start(context.Background()))
	defer func() { require.NoError(t, h.Shutdown(context.Background())) }()
	require.NoError(t, registerStartedSubsystems(context.Background(), h))

	registryServer := h.Servers()["registry"]
	require.NotNil(t, registryServer)
	client := registryv1connect.NewRegistryServiceClient(http.DefaultClient, registryServer.Endpoint())
	response, err := client.ListServices(context.Background(), connect.NewRequest(&registryv1.ListServicesRequest{}))
	require.NoError(t, err)
	assert.Len(t, response.Msg.GetServices(), len(h.Servers()))
}

func TestRootCommandFlags(t *testing.T) {
	command := newRootCommand()
	assert.NotNil(t, command.Flags().Lookup("component"))
	assert.NotNil(t, command.Flags().Lookup("all"))
}
