package workflow

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	workflowv1 "github.com/Manu343726/toolsbox/subsystems/workflow/workflowv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validDefinition() *workflowv1.WorkflowDefinition {
	return &workflowv1.WorkflowDefinition{
		Id: "review", Name: "Review", Version: "1", GraphJson: `{"nodes":[]}`,
	}
}

func TestStorePutGetLatestAndList(t *testing.T) {
	store := NewStore()
	first := validDefinition()
	stored, err := store.Put(first, false)
	require.NoError(t, err)
	assert.Equal(t, first.GetId(), stored.GetId())
	first.GraphJson = "mutated"
	assert.Equal(t, `{"nodes":[]}`, stored.GetGraphJson())

	second := validDefinition()
	second.Version = "2"
	_, err = store.Put(second, false)
	require.NoError(t, err)
	latest, err := store.Get("review", "")
	require.NoError(t, err)
	assert.Equal(t, "2", latest.GetVersion())
	all := store.List("rev")
	require.Len(t, all, 2)
	assert.Equal(t, "1", all[0].GetVersion())
	assert.Equal(t, "2", all[1].GetVersion())
}

func TestStoreRejectsDuplicateAndInvalidDefinitions(t *testing.T) {
	store := NewStore()
	_, err := store.Put(validDefinition(), true)
	require.NoError(t, err)
	_, err = store.Put(validDefinition(), true)
	assert.Error(t, err)

	invalid := validDefinition()
	invalid.GraphJson = "not-json"
	_, err = store.Put(invalid, false)
	assert.Error(t, err)
	_, err = store.Get("missing", "")
	assert.Error(t, err)
}

func TestWorkflowHandlerValidationAndErrors(t *testing.T) {
	handler := NewHandler(NewStore())
	ctx := context.Background()

	put, err := handler.PutWorkflow(ctx, connect.NewRequest(&workflowv1.PutWorkflowRequest{Definition: validDefinition()}))
	require.NoError(t, err)
	assert.Equal(t, "review", put.Msg.GetDefinition().GetId())

	get, err := handler.GetWorkflow(ctx, connect.NewRequest(&workflowv1.GetWorkflowRequest{Id: "review"}))
	require.NoError(t, err)
	assert.Equal(t, "Review", get.Msg.GetDefinition().GetName())

	_, err = handler.GetWorkflow(ctx, connect.NewRequest(&workflowv1.GetWorkflowRequest{Id: "missing"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	invalid := validDefinition()
	invalid.GraphJson = "[]"
	validation, err := handler.ValidateWorkflow(ctx, connect.NewRequest(&workflowv1.ValidateWorkflowRequest{Definition: invalid}))
	require.NoError(t, err)
	assert.False(t, validation.Msg.GetValid())
	assert.NotEmpty(t, validation.Msg.GetIssues())
}

func TestNewServerExposesWorkflowService(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)
	services := server.Services()
	require.Len(t, services, 1)
	assert.Equal(t, "toolsbox.workflow.v1.WorkflowService", services[0].Name)
	assert.Contains(t, services[0].Capabilities, "workflow.validate")
}
