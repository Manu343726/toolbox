package main

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	registryv1connect "github.com/Manu343726/toolbox/subsystems/registry/registryv1/registryv1connect"
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

func TestRootCommandIncludesAggregatedMCP(t *testing.T) {
	command := newRootCommand()
	mcpCommand, _, err := command.Find([]string{"mcp"})
	require.NoError(t, err)
	assert.Equal(t, "mcp", mcpCommand.Name())
	assert.NotNil(t, mcpCommand.Flags().Lookup("component"))
	assert.NotNil(t, mcpCommand.Flags().Lookup("all"))
	assert.NotNil(t, mcpCommand.Flags().Lookup("minimal"))
}

func TestFilterDescriptorsByService(t *testing.T) {
	descriptors := []*subsystem.Descriptor{
		{SubsystemName: "one", ServiceNames: []string{"one.v1.A", "one.v1.B"}},
		{SubsystemName: "two", ServiceNames: []string{"two.v1.C"}},
	}
	filtered, err := filterDescriptorsByService(descriptors, []string{"one.v1.B"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "one", filtered[0].SubsystemName)
	assert.Equal(t, []string{"one.v1.B"}, filtered[0].ServiceNames)
	assert.Equal(t, []string{"one.v1.A", "one.v1.B"}, descriptors[0].ServiceNames)

	_, err = filterDescriptorsByService(descriptors, []string{"missing.v1.Service"})
	assert.Error(t, err)
}
