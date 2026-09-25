package registry_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/registry"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	"github.com/Manu343726/toolbox/subsystems/registry/registryv1/registryv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A watch is only worth having if a client on the other end of a socket gets it, so
// these tests run against a served registry and a generated client rather than
// against the store.

func serve(t *testing.T) (*registry.Memory, registryv1connect.RegistryServiceClient) {
	t.Helper()
	store := registry.NewMemory(registry.MemoryOptions{})
	path, handler := registryv1connect.NewRegistryServiceHandler(registry.NewService(store))
	server, err := subsystem.NewServer(subsystem.Config{
		Name:     "registry",
		Services: []subsystem.Service{{Name: registryv1connect.RegistryServiceName, Path: path, Handler: handler}},
	})
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return store, registryv1connect.NewRegistryServiceClient(http.DefaultClient, server.Endpoint())
}

func descriptor(name, endpoint string) *registryv1.ServiceDescriptor {
	return &registryv1.ServiceDescriptor{
		SubsystemName: name,
		Endpoint:      endpoint,
		ServiceNames:  []string{"toolbox.fixture.v1.FixtureService"},
	}
}

func TestAWatchOpensWithWhatIsAlreadyRegistered(t *testing.T) {
	// A client that learns nothing until the next change cannot answer a question
	// asked in the meantime, so the stream opens with the current set.
	store, client := serve(t)
	_, err := store.Register(descriptor("first", "http://127.0.0.1:9001"), time.Minute)
	require.NoError(t, err)
	_, err = store.Register(descriptor("second", "http://127.0.0.1:9002"), time.Minute)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream, err := client.WatchServices(ctx, connect.NewRequest(&registryv1.WatchServicesRequest{}))
	require.NoError(t, err)
	require.True(t, stream.Receive())

	sync := stream.Msg()
	require.Equal(t, registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_SYNC, sync.GetType())
	names := make([]string, 0, len(sync.GetServices()))
	for _, service := range sync.GetServices() {
		names = append(names, service.GetSubsystemName())
	}
	assert.Equal(t, []string{"first", "second"}, names, "the sync carries the current set, ordered")
}

func TestAWatchReportsChangesAsTheyHappen(t *testing.T) {
	store, client := serve(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream, err := client.WatchServices(ctx, connect.NewRequest(&registryv1.WatchServicesRequest{}))
	require.NoError(t, err)
	require.True(t, stream.Receive())
	require.Equal(t, registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_SYNC, stream.Msg().GetType())

	_, err = store.Register(descriptor("arriving", "http://127.0.0.1:9003"), time.Minute)
	require.NoError(t, err)
	require.True(t, stream.Receive())
	assert.Equal(t, registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_REGISTERED, stream.Msg().GetType())
	assert.Equal(t, "arriving", stream.Msg().GetDescriptor_().GetSubsystemName())

	// A subsystem that moved announces itself as an update, which is how a client
	// learns an endpoint changed without treating it as a new subsystem.
	moved := descriptor("arriving", "http://127.0.0.1:9999")
	_, err = store.Register(moved, time.Minute)
	require.NoError(t, err)
	require.True(t, stream.Receive())
	assert.Equal(t, registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_UPDATED, stream.Msg().GetType())
	assert.Equal(t, "http://127.0.0.1:9999", stream.Msg().GetDescriptor_().GetEndpoint())

	require.True(t, store.Deregister("arriving"))
	require.True(t, stream.Receive())
	assert.Equal(t, registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_DEREGISTERED, stream.Msg().GetType())
}

func TestALapsedLeaseIsReportedAsARemoval(t *testing.T) {
	// A subsystem that dies without deregistering is the case a watch exists for. A
	// silent sweep would leave a client resolving an endpoint that is gone, and the
	// client would have no way to know.
	now := time.Now()
	store := registry.NewMemory(registry.MemoryOptions{
		DefaultLease: time.Millisecond,
		Now:          func() time.Time { return now },
	})
	path, handler := registryv1connect.NewRegistryServiceHandler(registry.NewService(store))
	server, err := subsystem.NewServer(subsystem.Config{
		Name:     "registry",
		Services: []subsystem.Service{{Name: registryv1connect.RegistryServiceName, Path: path, Handler: handler}},
	})
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	client := registryv1connect.NewRegistryServiceClient(http.DefaultClient, server.Endpoint())

	_, err = store.Register(descriptor("doomed", "http://127.0.0.1:9004"), time.Millisecond)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream, err := client.WatchServices(ctx, connect.NewRequest(&registryv1.WatchServicesRequest{}))
	require.NoError(t, err)
	require.True(t, stream.Receive())
	require.Equal(t, registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_SYNC, stream.Msg().GetType())
	require.Len(t, stream.Msg().GetServices(), 1)

	// Time passes, and something reads the registry, which is when the sweep runs.
	now = now.Add(time.Hour)
	store.List("", nil, false)
	require.NoError(t, err)

	require.True(t, stream.Receive())
	assert.Equal(t, registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_DEREGISTERED, stream.Msg().GetType(),
		"a lease that lapsed is a removal, and a watcher has to hear about it")
	assert.Equal(t, "doomed", stream.Msg().GetDescriptor_().GetSubsystemName())
}

func TestAWatchCanBeNarrowedToOneSubsystem(t *testing.T) {
	store, client := serve(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream, err := client.WatchServices(ctx, connect.NewRequest(&registryv1.WatchServicesRequest{
		SubsystemName: "watched",
	}))
	require.NoError(t, err)
	require.True(t, stream.Receive())
	require.Empty(t, stream.Msg().GetServices())

	_, err = store.Register(descriptor("unwatched", "http://127.0.0.1:9005"), time.Minute)
	require.NoError(t, err)
	_, err = store.Register(descriptor("watched", "http://127.0.0.1:9006"), time.Minute)
	require.NoError(t, err)

	// The unwatched registration is not delivered, so the next event is the watched
	// one. A client that received both would have to filter them itself, which is
	// the work the request asked the server to do.
	require.True(t, stream.Receive())
	assert.Equal(t, "watched", stream.Msg().GetDescriptor_().GetSubsystemName())
}

func TestAWatchEndsWhenTheClientGoesAway(t *testing.T) {
	_, client := serve(t)

	ctx, cancel := context.WithCancel(t.Context())
	stream, err := client.WatchServices(ctx, connect.NewRequest(&registryv1.WatchServicesRequest{}))
	require.NoError(t, err)
	require.True(t, stream.Receive(), "the sync arrives before the client leaves")

	cancel()
	// Draining to the end must terminate rather than block, which is what a handler
	// that ignored its context would fail to do.
	for stream.Receive() {
	}
}
