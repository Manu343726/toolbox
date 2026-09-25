package registry_test

import (
	"context"
	"testing"
	"time"

	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/subsystems/registry"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The point of a directory is that it is right without being asked. These tests are
// about a resolution being answered from a table the change stream keeps current,
// and about the directory saying so when it cannot hear the core.

// serving describes a subsystem that advertises one fully-qualified service, which
// is what a resolution is by service name.
func serving(name, endpoint, serviceName string) *registryv1.ServiceDescriptor {
	described := descriptor(name, endpoint)
	described.ServiceNames = []string{serviceName}
	return described
}

const knowledgeService = "toolbox.knowledge.v1.KnowledgeService"
const workflowService = "toolbox.workflow.v1.WorkflowService"

func startDirectory(t *testing.T, endpoint string) *registry.Directory {
	t.Helper()
	directory, err := registry.NewDirectory(registry.DirectoryOptions{Endpoint: endpoint})
	require.NoError(t, err)
	t.Cleanup(directory.Close)
	return directory
}

func TestADirectoryResolvesFromATableItKeepsCurrent(t *testing.T) {
	store, endpoint, _ := serve(t)
	_, err := store.Register(serving("knowledge", "http://127.0.0.1:9001", knowledgeService), time.Minute)
	require.NoError(t, err)

	directory := startDirectory(t, endpoint)
	require.NoError(t, directory.Start(t.Context()))

	resolved, err := directory.Resolve(t.Context(), knowledgeService)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9001", resolved.URL,
		"the table answers, so a call path does not wait for the registry")

	// A subsystem that appears after the directory started becomes resolvable
	// without restarting anything, which is the whole point of following the stream.
	_, err = store.Register(serving("workflow", "http://127.0.0.1:9002", workflowService), time.Minute)
	require.NoError(t, err)
	requireEventually(t, func() bool {
		_, resolveErr := directory.Resolve(t.Context(), workflowService)
		return resolveErr == nil
	}, "a subsystem registered after start-up becomes resolvable")
}

func TestADirectoryFollowsAMovedEndpoint(t *testing.T) {
	// A subsystem that restarts on a new port announces itself as an update. A
	// resolver that kept the old address would send every later call to a process
	// that is gone.
	store, endpoint, _ := serve(t)
	_, err := store.Register(serving("knowledge", "http://127.0.0.1:9001", knowledgeService), time.Minute)
	require.NoError(t, err)

	directory := startDirectory(t, endpoint)
	require.NoError(t, directory.Start(t.Context()))

	_, err = store.Register(serving("knowledge", "http://127.0.0.1:9999", knowledgeService), time.Minute)
	require.NoError(t, err)
	requireEventually(t, func() bool {
		resolved, resolveErr := directory.Resolve(t.Context(), knowledgeService)
		return resolveErr == nil && resolved.URL == "http://127.0.0.1:9999"
	}, "a moved endpoint replaces the old one")
}

func TestADirectoryForgetsADeregisteredSubsystem(t *testing.T) {
	// A client must learn that a peer is gone, or it keeps dialling an endpoint
	// that no longer answers and reports a transport failure instead of a missing
	// dependency.
	store, endpoint, _ := serve(t)
	_, err := store.Register(serving("knowledge", "http://127.0.0.1:9001", knowledgeService), time.Minute)
	require.NoError(t, err)

	directory := startDirectory(t, endpoint)
	require.NoError(t, directory.Start(t.Context()))

	require.True(t, store.Deregister("knowledge"))
	requireEventually(t, func() bool {
		_, resolveErr := directory.Resolve(t.Context(), knowledgeService)
		return resolveErr != nil
	}, "a deregistered subsystem stops resolving")

	_, err = directory.Resolve(t.Context(), knowledgeService)
	assert.ErrorIs(t, err, core.ErrNotFound,
		"a synced directory knows what the registry has, so a name it does not hold is one nobody serves")
	assert.NotErrorIs(t, err, core.ErrUnreachable)
}

func TestADirectoryThatCannotReachTheCoreSaysSoAtStartUp(t *testing.T) {
	// A subsystem whose core is down should find out at start-up, not on its first
	// call — and the answer must be "unreachable", because a deployment may
	// legitimately run without a peer and the two are not the same problem.
	directory, err := registry.NewDirectory(registry.DirectoryOptions{
		Endpoint:    "http://127.0.0.1:1",
		SyncTimeout: 250 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(directory.Close)

	err = directory.Start(t.Context())
	require.Error(t, err, "an unreachable core is reported, not swallowed")
	assert.ErrorIs(t, err, core.ErrUnreachable)
	assert.False(t, directory.Synced())

	_, err = directory.Resolve(t.Context(), knowledgeService)
	assert.ErrorIs(t, err, core.ErrUnreachable)
	assert.NotErrorIs(t, err, core.ErrNotFound,
		"a directory that cannot hear the core must not claim the service does not exist")
}

func TestADirectoryRefusesToStartWithoutAnEndpoint(t *testing.T) {
	_, err := registry.NewDirectory(registry.DirectoryOptions{})
	assert.Error(t, err)
}

func TestADirectoryIsSafeForConcurrentUse(t *testing.T) {
	// The table is read on every peer call and written by a background goroutine, so
	// the interesting race is a reader against a table being replaced wholesale.
	store, endpoint, _ := serve(t)
	_, err := store.Register(serving("knowledge", "http://127.0.0.1:9001", knowledgeService), time.Minute)
	require.NoError(t, err)

	directory := startDirectory(t, endpoint)
	require.NoError(t, directory.Start(t.Context()))

	group := make(chan struct{}, 16)
	for index := 0; index < 16; index++ {
		go func() {
			defer func() { group <- struct{}{} }()
			for attempt := 0; attempt < 32; attempt++ {
				_, _ = directory.Resolve(context.Background(), knowledgeService)
				_ = directory.Endpoints()
				_ = directory.Synced()
				_ = directory.Generation()
			}
		}()
	}
	for index := 0; index < 8; index++ {
		_, _ = store.Register(descriptor("churn", "http://127.0.0.1:9100"), time.Minute)
		store.Deregister("churn")
	}
	for index := 0; index < 16; index++ {
		<-group
	}
	assert.NotEmpty(t, directory.Endpoints())
}

func requireEventually(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", message)
}
