package prompt

import (
	"context"
	"fmt"
	"sync"
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

// A prompt with no version named is the latest one, and "latest" is a number
// rather than a piece of text: comparing versions as strings served the ninth
// revision of a template that had been revised ten times, because "10" sorts
// before "9".
func TestTheLatestPromptIsTheGreatestNotTheLastAlphabetical(t *testing.T) {
	store := NewStore()
	for _, at := range []string{"1", "2", "9", "10", "11"} {
		entry := template()
		entry.Version = at
		_, err := store.Put(entry, false)
		require.NoError(t, err)
	}

	latest, err := store.Get("greeting", "")
	require.NoError(t, err)
	assert.Equal(t, "11", latest.GetVersion(), "the greatest version, not the last in text order")
}

func TestListPromptsIsOrderedByIdentifierThenVersion(t *testing.T) {
	store := NewStore()
	for _, entry := range []struct{ id, version string }{
		{"zebra", "1"}, {"greeting", "2"}, {"greeting", "10"}, {"greeting", "1"}, {"middle", "1"},
	} {
		stored := template()
		stored.Id, stored.Version = entry.id, entry.version
		_, err := store.Put(stored, false)
		require.NoError(t, err)
	}

	all := store.List("")
	got := make([]string, 0, len(all))
	for _, entry := range all {
		got = append(got, entry.GetId()+"@"+entry.GetVersion())
	}
	assert.Equal(t, []string{"greeting@1", "greeting@2", "greeting@10", "middle@1", "zebra@1"}, got,
		"versions of one template are ordered by value, not as text")
	assert.Len(t, store.List("greeting"), 3)
	assert.Empty(t, store.List("nobody"))
}

// A stored template is a copy, so a caller that edits what it wrote or what it
// was handed back cannot change what the store holds.
func TestAStoredPromptIsNotReachableThroughItsMessage(t *testing.T) {
	store := NewStore()
	written := template()
	stored, err := store.Put(written, false)
	require.NoError(t, err)

	written.Template = "Rewritten by the caller"
	written.Variables = append(written.Variables, "invented")
	stored.Variables[0] = "rewritten"

	read, err := store.Get("greeting", "1")
	require.NoError(t, err)
	assert.Equal(t, "Hello {{name}}", read.GetTemplate())
	assert.Equal(t, []string{"name"}, read.GetVariables())
}

// An identifier of only whitespace names nothing, and accepting one handed the
// store a key nothing can be looked up by — which answered not-found and sent the
// caller looking for a template that was right there.
func TestTheHandlerRefusesAnIdentifierThatNamesNothing(t *testing.T) {
	handler := NewHandler(NewStore())
	for _, request := range []*connect.Request[promptv1.GetPromptRequest]{
		nil,
		connect.NewRequest(&promptv1.GetPromptRequest{}),
		connect.NewRequest(&promptv1.GetPromptRequest{Id: "  "}),
	} {
		_, err := handler.GetPrompt(context.Background(), request)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
			"an identifier that names nothing is the caller's mistake, not a missing template")
	}

	_, err := NewStore().Put(&promptv1.PromptTemplate{Id: "  ", Name: "n", Version: "1", Template: "t"}, false)
	require.Error(t, err)
}

// Rendering is the operation that makes a template worth storing, so what it does
// with a variable the template does not use is behaviour rather than an accident.
func TestRenderSubstitutesAndLeavesUnknownVariablesAlone(t *testing.T) {
	handler := NewHandler(NewStore())
	_, err := handler.PutPrompt(context.Background(), connect.NewRequest(&promptv1.PutPromptRequest{Prompt: template()}))
	require.NoError(t, err)

	rendered, err := handler.RenderPrompt(context.Background(), connect.NewRequest(&promptv1.RenderPromptRequest{
		Id:        "greeting",
		Version:   "1",
		Variables: map[string]string{"name": "Ada", "unused": "x"},
	}))
	require.NoError(t, err)
	assert.Contains(t, rendered.Msg.GetText(), "Ada")
	assert.NotContains(t, rendered.Msg.GetText(), "{{name}}", "a supplied value is substituted")
	assert.NotContains(t, rendered.Msg.GetText(), "unused", "a value the template does not use adds nothing")
}

func TestTheStoreIsSafeUnderConcurrentUse(t *testing.T) {
	store := NewStore()
	_, err := store.Put(template(), false)
	require.NoError(t, err)

	const workers = 12
	var group sync.WaitGroup
	failures := make(chan error, workers)
	for worker := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			stored := template()
			stored.Id = fmt.Sprintf("prompt-%d", worker)
			for range 10 {
				if _, err := store.Put(stored, false); err != nil {
					failures <- err
					return
				}
				if _, err := store.Get(stored.GetId(), "1"); err != nil {
					failures <- err
					return
				}
				store.List("prompt-")
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("using the store concurrently: %v", err)
	}
	assert.Len(t, store.List("prompt-"), workers)
}
