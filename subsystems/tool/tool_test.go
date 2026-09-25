package tool

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	toolv1 "github.com/Manu343726/toolbox/subsystems/tool/toolv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolHandlerRegistrationAndInvocation(t *testing.T) {
	handler := NewHandler(Options{Tools: []*toolv1.ToolDescriptor{{Name: "echo", InputSchemaJson: `{"type":"object"}`}}})
	err := handler.Register(&toolv1.ToolDescriptor{Name: "add", Mutating: true}, func(_ context.Context, args json.RawMessage) (any, error) {
		var value map[string]int
		if err := json.Unmarshal(args, &value); err != nil {
			return nil, err
		}
		return value["a"] + value["b"], nil
	})
	require.NoError(t, err)
	tools, err := handler.ListTools(context.Background(), connect.NewRequest(&toolv1.ListToolsRequest{}))
	require.NoError(t, err)
	assert.Len(t, tools.Msg.GetTools(), 2)
	result, err := handler.InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{Name: "add", ArgumentsJson: `{"a":2,"b":3}`}))
	require.NoError(t, err)
	assert.JSONEq(t, "5", result.Msg.GetResultJson())
}

func TestToolHandlerErrors(t *testing.T) {
	handler := NewHandler(Options{})
	ctx := context.Background()
	_, err := handler.InvokeTool(ctx, connect.NewRequest(&toolv1.InvokeToolRequest{Name: "missing"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = handler.InvokeTool(ctx, connect.NewRequest(&toolv1.InvokeToolRequest{Name: "missing", ArgumentsJson: "not-json"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Error(t, handler.Register(&toolv1.ToolDescriptor{}, nil))
}
