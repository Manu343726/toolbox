package model

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	modelv1 "github.com/Manu343726/toolsbox/subsystems/model/modelv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultModelCatalogAndGeneration(t *testing.T) {
	handler := NewHandler(Options{})
	ctx := context.Background()
	models, err := handler.ListModels(ctx, connect.NewRequest(&modelv1.ListModelsRequest{}))
	require.NoError(t, err)
	require.Len(t, models.Msg.GetModels(), 1)
	assert.Equal(t, "reference/echo", models.Msg.GetModels()[0].GetId())

	generated, err := handler.Generate(ctx, connect.NewRequest(&modelv1.GenerateRequest{ModelId: "reference/echo", Prompt: "hello"}))
	require.NoError(t, err)
	assert.Equal(t, "hello", generated.Msg.GetText())
	_, err = handler.Generate(ctx, connect.NewRequest(&modelv1.GenerateRequest{ModelId: "missing"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCustomProviderAndFilter(t *testing.T) {
	handler := NewHandler(Options{
		Models: []*modelv1.ModelDescriptor{{Id: "custom/model", Provider: "custom", DisplayName: "Custom"}},
		Generate: func(_ context.Context, req *modelv1.GenerateRequest) (string, error) {
			return req.GetPrompt() + "-custom", nil
		},
	})
	models, err := handler.ListModels(context.Background(), connect.NewRequest(&modelv1.ListModelsRequest{Provider: "custom"}))
	require.NoError(t, err)
	require.Len(t, models.Msg.GetModels(), 1)
	generated, err := handler.Generate(context.Background(), connect.NewRequest(&modelv1.GenerateRequest{ModelId: "custom/model", Prompt: "x"}))
	require.NoError(t, err)
	assert.Equal(t, "x-custom", generated.Msg.GetText())
}
