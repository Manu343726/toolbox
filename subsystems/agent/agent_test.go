package agent

import (
	"context"
	"fmt"
	"sync"
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

// A profile with no version named is the latest one, and "latest" is a number
// rather than a piece of text: comparing versions as strings served the ninth
// revision of a profile that had been revised ten times, because "10" sorts
// before "9".
func TestTheLatestProfileIsTheGreatestNotTheLastAlphabetical(t *testing.T) {
	store := NewStore()
	for _, at := range []string{"1", "2", "9", "10", "11"} {
		entry := profile()
		entry.Version = at
		_, err := store.Put(entry, false)
		require.NoError(t, err)
	}

	latest, err := store.Get("developer", "")
	require.NoError(t, err)
	assert.Equal(t, "11", latest.GetVersion(), "the greatest version, not the last in text order")
}

// A listing is what a client chooses from, so its order is behaviour rather than
// whatever the map happened to yield.
func TestListAgentsIsOrderedByIdentifierThenVersion(t *testing.T) {
	store := NewStore()
	for _, entry := range []struct{ id, version string }{
		{"zebra", "1"}, {"developer", "2"}, {"developer", "10"}, {"developer", "1"}, {"middle", "1"},
	} {
		stored := profile()
		stored.Id, stored.Version = entry.id, entry.version
		_, err := store.Put(stored, false)
		require.NoError(t, err)
	}

	all := store.List("")
	got := make([]string, 0, len(all))
	for _, entry := range all {
		got = append(got, entry.GetId()+"@"+entry.GetVersion())
	}
	assert.Equal(t, []string{"developer@1", "developer@2", "developer@10", "middle@1", "zebra@1"}, got,
		"versions of one profile are ordered by value, not as text")

	assert.Len(t, store.List("developer"), 3, "a prefix narrows the listing")
	assert.Empty(t, store.List("nobody"), "a prefix that matches nothing is empty, not a failure")
}

// A stored profile is a copy. A caller that kept the message it wrote, or edited
// the one it was handed back, must not be able to change what the store holds.
func TestAStoredProfileIsNotReachableThroughItsMessage(t *testing.T) {
	store := NewStore()
	written := profile()
	stored, err := store.Put(written, false)
	require.NoError(t, err)

	written.ToolRefs = append(written.ToolRefs, "invented.capability")
	stored.ToolRefs[0] = "rewritten"

	read, err := store.Get("developer", "1")
	require.NoError(t, err)
	assert.Equal(t, []string{"shell.execute"}, read.GetToolRefs())
}

// An identifier of only whitespace names nothing, and accepting one handed the
// store a key nothing can be looked up by — which answered not-found and sent the
// caller looking for a profile that was right there.
func TestTheHandlerRefusesAnIdentifierThatNamesNothing(t *testing.T) {
	handler := NewHandler(NewStore())
	for _, request := range []*connect.Request[agentv1.GetAgentRequest]{
		nil,
		connect.NewRequest(&agentv1.GetAgentRequest{}),
		connect.NewRequest(&agentv1.GetAgentRequest{Id: "  "}),
		connect.NewRequest(&agentv1.GetAgentRequest{Id: "\t\n"}),
	} {
		_, err := handler.GetAgent(context.Background(), request)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
			"an identifier that names nothing is the caller's mistake, not a missing profile")
	}

	// The store refuses one too, so an embedded caller is held to the same rule.
	_, err := NewStore().Put(&agentv1.AgentProfile{Id: "  ", Name: "n", Version: "1"}, false)
	require.Error(t, err)
}

// The store is shared by every client of a running subsystem.
func TestTheStoreIsSafeUnderConcurrentUse(t *testing.T) {
	store := NewStore()
	_, err := store.Put(profile(), false)
	require.NoError(t, err)

	const workers = 12
	var group sync.WaitGroup
	failures := make(chan error, workers)
	for worker := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			stored := profile()
			stored.Id = fmt.Sprintf("agent-%d", worker)
			for range 10 {
				if _, err := store.Put(stored, false); err != nil {
					failures <- err
					return
				}
				if _, err := store.Get(stored.GetId(), "1"); err != nil {
					failures <- err
					return
				}
				store.List("agent-")
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("using the store concurrently: %v", err)
	}
	assert.Len(t, store.List("agent-"), workers)
}
