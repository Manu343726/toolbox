package workflow

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	workflowv1 "github.com/Manu343726/toolbox/subsystems/workflow/workflowv1"
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
	assert.Equal(t, "toolbox.workflow.v1.WorkflowService", services[0].Name)
}

// A workflow with no version named is the latest one, and "latest" is a number
// rather than a piece of text: comparing versions as strings served the ninth
// revision of a definition that had been revised ten times, because "10" sorts
// before "9".
func TestTheLatestWorkflowIsTheGreatestNotTheLastAlphabetical(t *testing.T) {
	store := NewStore()
	for _, at := range []string{"1", "2", "9", "10", "11"} {
		definition := validDefinition()
		definition.Version = at
		_, err := store.Put(definition, false)
		require.NoError(t, err)
	}

	latest, err := store.Get("review", "")
	require.NoError(t, err)
	assert.Equal(t, "11", latest.GetVersion(), "the greatest version, not the last in text order")
}

func TestListWorkflowsIsOrderedByIdentifierThenVersion(t *testing.T) {
	store := NewStore()
	for _, entry := range []struct{ id, version string }{
		{"zebra", "1"}, {"review", "2"}, {"review", "10"}, {"review", "1"}, {"middle", "1"},
	} {
		definition := validDefinition()
		definition.Id, definition.Version = entry.id, entry.version
		_, err := store.Put(definition, false)
		require.NoError(t, err)
	}

	all := store.List("")
	got := make([]string, 0, len(all))
	for _, definition := range all {
		got = append(got, definition.GetId()+"@"+definition.GetVersion())
	}
	assert.Equal(t, []string{"middle@1", "review@1", "review@2", "review@10", "zebra@1"}, got,
		"versions of one definition are ordered by value, not as text")
	assert.Len(t, store.List("review"), 3)
	assert.Empty(t, store.List("nobody"))
}

// A stored definition is a copy, so a caller that edits what it wrote or what it
// was handed back cannot change what the store holds.
func TestAStoredWorkflowIsNotReachableThroughItsMessage(t *testing.T) {
	store := NewStore()
	written := validDefinition()
	stored, err := store.Put(written, false)
	require.NoError(t, err)

	written.GraphJson = `{"nodes":["rewritten"]}`
	stored.GraphJson = `{"nodes":["also rewritten"]}`

	read, err := store.Get("review", "1")
	require.NoError(t, err)
	assert.Equal(t, `{"nodes":[]}`, read.GetGraphJson())
}

// An identifier of only whitespace names nothing, and accepting one handed the
// store a key nothing can be looked up by — which answered not-found and sent the
// caller looking for a definition that was right there.
func TestTheHandlerRefusesAnIdentifierThatNamesNothing(t *testing.T) {
	handler := NewHandler(NewStore())
	for _, request := range []*connect.Request[workflowv1.GetWorkflowRequest]{
		nil,
		connect.NewRequest(&workflowv1.GetWorkflowRequest{}),
		connect.NewRequest(&workflowv1.GetWorkflowRequest{Id: "  "}),
	} {
		_, err := handler.GetWorkflow(context.Background(), request)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
			"an identifier that names nothing is the caller's mistake, not a missing definition")
	}
}

// Validation is the reason this subsystem exists rather than being a map, so what
// it refuses is the behaviour worth pinning: a definition that cannot be run is
// refused at the boundary rather than failing whoever tries to run it.
func TestPutRefusesADefinitionThatCouldNotBeRun(t *testing.T) {
	store := NewStore()
	graph := validDefinition()
	graph.GraphJson = "not json at all"
	_, err := store.Put(graph, false)
	require.Error(t, err, "a graph that is not JSON cannot be run")

	missing := validDefinition()
	missing.GraphJson = ""
	_, err = store.Put(missing, false)
	require.Error(t, err, "a definition with no graph cannot be run")

	_, err = store.Put(nil, false)
	require.Error(t, err)

	_, err = store.Put(&workflowv1.WorkflowDefinition{Id: "review", Version: "1", GraphJson: `{"nodes":[]}`}, false)
	require.Error(t, err, "a definition with no name cannot be addressed")
}

func TestTheStoreIsSafeUnderConcurrentUse(t *testing.T) {
	store := NewStore()
	_, err := store.Put(validDefinition(), false)
	require.NoError(t, err)

	const workers = 12
	var group sync.WaitGroup
	failures := make(chan error, workers)
	for worker := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			definition := validDefinition()
			definition.Id = fmt.Sprintf("flow-%d", worker)
			for range 10 {
				if _, err := store.Put(definition, false); err != nil {
					failures <- err
					return
				}
				if _, err := store.Get(definition.GetId(), "1"); err != nil {
					failures <- err
					return
				}
				store.List("flow-")
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("using the store concurrently: %v", err)
	}
	assert.Len(t, store.List("flow-"), workers)
}
