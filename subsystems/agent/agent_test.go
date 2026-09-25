package agent

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	agentv1 "github.com/Manu343726/toolbox/subsystems/agent/agentv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func profile() *agentv1.AgentProfile {
	return &agentv1.AgentProfile{Id: "developer", Name: "Developer", Version: "1", ToolRefs: []string{"shell.execute"}}
}

func TestStoreVersionsProfiles(t *testing.T) {
	store := NewStore()
	first, err := store.Put(profile(), false)
	require.NoError(t, err)
	first.ToolRefs[0] = "mutated"
	got, err := store.Get("developer", "1")
	require.NoError(t, err)
	assert.Equal(t, "shell.execute", got.GetToolRefs()[0])

	second := profile()
	second.Version = "2"
	_, err = store.Put(second, false)
	require.NoError(t, err)
	latest, err := store.Get("developer", "")
	require.NoError(t, err)
	assert.Equal(t, "2", latest.GetVersion())
	assert.Len(t, store.List("dev"), 2)
}

func TestAgentHandlerCRUDAndValidation(t *testing.T) {
	handler := NewHandler(nil)
	ctx := context.Background()
	put, err := handler.PutAgent(ctx, connect.NewRequest(&agentv1.PutAgentRequest{Profile: profile()}))
	require.NoError(t, err)
	assert.Equal(t, "developer", put.Msg.GetProfile().GetId())

	_, err = handler.PutAgent(ctx, connect.NewRequest(&agentv1.PutAgentRequest{Profile: &agentv1.AgentProfile{Id: "bad"}}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = handler.GetAgent(ctx, connect.NewRequest(&agentv1.GetAgentRequest{Id: "missing"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestNewServerMetadata(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)
	assert.Equal(t, Name, server.Descriptor().SubsystemName)
}
