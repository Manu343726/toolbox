package prompt

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	promptv1 "github.com/Manu343726/toolbox/subsystems/prompt/promptv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func template() *promptv1.PromptTemplate {
	return &promptv1.PromptTemplate{Id: "greeting", Name: "Greeting", Version: "1", Template: "Hello {{name}}", Variables: []string{"name"}}
}

func TestPromptRenderAndStore(t *testing.T) {
	handler := NewHandler(nil)
	ctx := context.Background()
	_, err := handler.PutPrompt(ctx, connect.NewRequest(&promptv1.PutPromptRequest{Prompt: template()}))
	require.NoError(t, err)
	rendered, err := handler.RenderPrompt(ctx, connect.NewRequest(&promptv1.RenderPromptRequest{Id: "greeting", Variables: map[string]string{"name": "Ada"}}))
	require.NoError(t, err)
	assert.Equal(t, "Hello Ada", rendered.Msg.GetText())
}

func TestPromptHandlerErrors(t *testing.T) {
	handler := NewHandler(NewStore())
	ctx := context.Background()
	_, err := handler.RenderPrompt(ctx, connect.NewRequest(&promptv1.RenderPromptRequest{Id: "missing"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = handler.PutPrompt(ctx, connect.NewRequest(&promptv1.PutPromptRequest{Prompt: &promptv1.PromptTemplate{Id: "x"}}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestPromptServerCapabilities(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", server.Descriptor().ImplementationVersion)
}
