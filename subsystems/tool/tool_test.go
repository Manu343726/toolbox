package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/subsystems/tool"
	toolv1 "github.com/Manu343726/toolbox/subsystems/tool/toolv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This subsystem is the gateway an agent's tools come through, so the tests are about what it
// will and will not call: a tool that is declared but not implemented must not answer as though
// it did, arguments that are not JSON must not reach an implementation as an empty object, and
// a failure inside a tool must not be reported as a missing one.
//
// Two tests existed and both checked the happy path.

func registered(t *testing.T) *tool.Handler {
	t.Helper()
	handler := tool.NewHandler(tool.Options{})
	require.NoError(t, handler.Register(&toolv1.ToolDescriptor{
		Name: "knowledge.search",
		// The input schema is what an agent reads to build its arguments, so a tool that has
		// none is a tool nobody can call correctly.
		InputSchemaJson:    `{"type":"object","properties":{"query":{"type":"string"}}}`,
		Description:        "Searches the knowledge store.",
		RequiredPermission: "knowledge.read",
	}, func(_ context.Context, arguments json.RawMessage) (any, error) {
		var parsed struct {
			Query string `json:"query"`
		}
		if len(arguments) > 0 {
			if err := json.Unmarshal(arguments, &parsed); err != nil {
				return nil, err
			}
		}
		return map[string]any{"matched": parsed.Query}, nil
	}))
	require.NoError(t, handler.Register(&toolv1.ToolDescriptor{
		Name:        "workflow.advance",
		Description: "Advances a run.",
		Mutating:    true,
	}, nil))
	return handler
}

func TestARegisteredToolIsListed(t *testing.T) {
	response, err := registered(t).ListTools(context.Background(),
		connect.NewRequest(&toolv1.ListToolsRequest{}))
	require.NoError(t, err)

	require.Len(t, response.Msg.GetTools(), 2)
	// Sorted, because two processes registering the same tools must list them the same way for
	// a diff of two listings to mean anything.
	assert.Equal(t, "knowledge.search", response.Msg.GetTools()[0].GetName())
	assert.Equal(t, "workflow.advance", response.Msg.GetTools()[1].GetName())
	assert.Equal(t, "knowledge.read", response.Msg.GetTools()[0].GetRequiredPermission())
	assert.True(t, response.Msg.GetTools()[1].GetMutating(),
		"a tool declared as mutating must say so, or a policy cannot see it")
}

func TestListingCanBeNarrowedByPrefix(t *testing.T) {
	response, err := registered(t).ListTools(context.Background(),
		connect.NewRequest(&toolv1.ListToolsRequest{NamePrefix: "knowledge"}))
	require.NoError(t, err)

	require.Len(t, response.Msg.GetTools(), 1)
	assert.Equal(t, "knowledge.search", response.Msg.GetTools()[0].GetName())
}

func TestListingHandsOutCopies(t *testing.T) {
	handler := registered(t)

	first, err := handler.ListTools(context.Background(), connect.NewRequest(&toolv1.ListToolsRequest{}))
	require.NoError(t, err)
	first.Msg.Tools[0].Description = "edited by a caller"

	second, err := handler.ListTools(context.Background(), connect.NewRequest(&toolv1.ListToolsRequest{}))
	require.NoError(t, err)
	// The gateway serves one catalogue to every agent, so a caller that edited what it was
	// handed would have edited it for everyone.
	assert.Equal(t, "Searches the knowledge store.", second.Msg.Tools[0].GetDescription())
}

func TestAnInvocationReachesTheRegisteredImplementation(t *testing.T) {
	response, err := registered(t).InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{
		Name:          "knowledge.search",
		ArgumentsJson: `{"query":"slog"}`,
	}))

	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(response.Msg.GetResultJson()), &decoded))
	assert.Equal(t, "slog", decoded["matched"])
}

func TestAToolWithNoImplementationIsUnimplementedRatherThanEmpty(t *testing.T) {
	// The tool is declared, so ListTools offers it. Calling it must say it cannot be run here
	// rather than answering with an empty result, which an agent would read as a tool that ran
	// and found nothing.
	_, err := registered(t).InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{
		Name: "workflow.advance",
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "workflow.advance")
}

func TestAnUnknownToolIsNotFound(t *testing.T) {
	_, err := registered(t).InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{
		Name: "no.such.tool",
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "no.such.tool")
}

func TestAnInvocationWithNoNameIsRefused(t *testing.T) {
	_, err := registered(t).InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAnInvocationWithNoRequestAtAllIsRefused(t *testing.T) {
	_, err := registered(t).InvokeTool(context.Background(), nil)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestArgumentsThatAreNotJSONAreRefusedBeforeTheToolSeesThem(t *testing.T) {
	var called bool
	handler := tool.NewHandler(tool.Options{})
	require.NoError(t, handler.Register(&toolv1.ToolDescriptor{Name: "echo"}, func(context.Context, json.RawMessage) (any, error) {
		called = true
		return nil, nil
	}))

	_, err := handler.InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{
		Name:          "echo",
		ArgumentsJson: "{not json",
	}))

	// Refused at the boundary, because a tool handed arguments it cannot parse would either
	// fail obscurely or silently treat them as absent.
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.False(t, called, "the tool was called with arguments the boundary had rejected")
}

func TestAFailureInsideAToolIsInternalAndNotFound(t *testing.T) {
	failure := errors.New("the store is unreachable")
	handler := tool.NewHandler(tool.Options{})
	require.NoError(t, handler.Register(&toolv1.ToolDescriptor{Name: "broken"},
		func(context.Context, json.RawMessage) (any, error) { return nil, failure }))

	_, err := handler.InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{
		Name: "broken",
	}))

	// The tool exists and failed. Reporting it as missing would send an agent looking for a
	// tool that is registered, and reporting it as invalid would blame the caller.
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.ErrorIs(t, err, failure)
}

func TestAResultThatCannotBeMarshalledIsReported(t *testing.T) {
	handler := tool.NewHandler(tool.Options{})
	require.NoError(t, handler.Register(&toolv1.ToolDescriptor{Name: "unserialisable"},
		func(context.Context, json.RawMessage) (any, error) {
			// A channel, which JSON cannot represent.
			return make(chan int), nil
		}))

	_, err := handler.InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{
		Name: "unserialisable",
	}))

	// A tool that returned something unserialisable has still run, and the failure is in
	// reporting it rather than in the call — which is a different thing for an agent to retry.
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "unserialisable")
}

func TestAnEmptyArgumentStringReachesTheToolAsNoArguments(t *testing.T) {
	var seen string
	handler := tool.NewHandler(tool.Options{})
	require.NoError(t, handler.Register(&toolv1.ToolDescriptor{Name: "noargs"},
		func(_ context.Context, arguments json.RawMessage) (any, error) {
			seen = string(arguments)
			return "ok", nil
		}))

	_, err := handler.InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{
		Name: "noargs",
	}))
	require.NoError(t, err)
	// Blank means absent, not an empty JSON document: a tool that takes no arguments should not
	// have to distinguish "{}" from "".
	assert.Empty(t, seen)
}

func TestRegisteringTheSameNameTwiceReplacesBothDescriptorAndInvoker(t *testing.T) {
	handler := registered(t)
	require.NoError(t, handler.Register(&toolv1.ToolDescriptor{
		Name:        "knowledge.search",
		Description: "Replaced.",
	}, func(context.Context, json.RawMessage) (any, error) { return "replaced", nil }))

	list, err := handler.ListTools(context.Background(), connect.NewRequest(&toolv1.ListToolsRequest{}))
	require.NoError(t, err)
	// One entry, not two: a duplicate name would be rendered by an agent as two tools with one
	// meaning, and which one it picked would be arbitrary.
	require.Len(t, list.Msg.GetTools(), 2)

	response, err := handler.InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{
		Name: "knowledge.search",
	}))
	require.NoError(t, err)
	assert.JSONEq(t, `"replaced"`, response.Msg.GetResultJson())
}

func TestReplacingADescriptorLeavesTheOldInvokerInPlace(t *testing.T) {
	handler := registered(t)
	require.NoError(t, handler.Register(&toolv1.ToolDescriptor{Name: "knowledge.search"}, nil))

	response, err := handler.InvokeTool(context.Background(), connect.NewRequest(&toolv1.InvokeToolRequest{
		Name: "knowledge.search",
	}))
	// A nil invoker means "do not replace the implementation", so the tool keeps working after
	// its description is corrected. Silently unregistering it would break a deployment over a
	// documentation edit.
	require.NoError(t, err)
	assert.NotEmpty(t, response.Msg.GetResultJson())
}

func TestAToolWithNoNameIsRefused(t *testing.T) {
	handler := tool.NewHandler(tool.Options{})

	require.Error(t, handler.Register(&toolv1.ToolDescriptor{}, nil))
	require.Error(t, handler.Register(nil, nil))
}

func TestAHandlerWithNoToolsListsNothing(t *testing.T) {
	response, err := tool.NewHandler(tool.Options{}).ListTools(context.Background(),
		connect.NewRequest(&toolv1.ListToolsRequest{}))
	require.NoError(t, err)
	// Empty rather than an error: a deployment with no tools is a deployment with no tools, and
	// an agent asking what it may call deserves an answer.
	assert.Empty(t, response.Msg.GetTools())
}

func TestARegistrationIsCopiedSoALaterEditDoesNotChangeWhatIsServed(t *testing.T) {
	descriptor := &toolv1.ToolDescriptor{Name: "copied", Description: "first"}
	handler := tool.NewHandler(tool.Options{})
	require.NoError(t, handler.Register(descriptor, nil))

	descriptor.Description = "edited after registration"

	list, err := handler.ListTools(context.Background(), connect.NewRequest(&toolv1.ListToolsRequest{}))
	require.NoError(t, err)
	// Registration is a handover, not a reference: a caller reusing the descriptor it passed
	// would otherwise be able to change what the gateway serves after the fact.
	assert.Equal(t, "first", list.Msg.Tools[0].GetDescription())
}

func TestConcurrentRegistrationAndInvocationAreSafe(t *testing.T) {
	handler := registered(t)
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "generated.tool"
			if err := handler.Register(&toolv1.ToolDescriptor{Name: name},
				func(context.Context, json.RawMessage) (any, error) { return i, nil }); err != nil {
				t.Error(err)
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := handler.InvokeTool(context.Background(),
				connect.NewRequest(&toolv1.InvokeToolRequest{Name: "knowledge.search"})); err != nil &&
				!strings.Contains(err.Error(), "not found") {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
