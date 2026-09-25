package registry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/subsystems/registry"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryRegisterAndClone(t *testing.T) {
	now := time.Unix(100, 0)
	store := registry.NewMemory(registry.MemoryOptions{DefaultLease: time.Minute, Now: func() time.Time { return now }})
	descriptor := &registryv1.ServiceDescriptor{
		SubsystemName:         "workflow",
		Endpoint:              "http://127.0.0.1:9000",
		ImplementationVersion: "1.0.0",
		ServiceNames:          []string{"toolbox.workflow.v1.WorkflowService"},
		Capabilities:          []*registryv1.Capability{{Name: "workflow.run"}},
	}

	got, err := store.Register(descriptor, 0)
	require.NoError(t, err)
	assert.Equal(t, now.UnixNano(), got.GetRegisteredAtUnixNano())
	assert.Equal(t, now.Add(time.Minute).UnixNano(), got.GetLeaseExpiresAtUnixNano())
	assert.Equal(t, registryv1.RegistryStatus_REGISTRY_STATUS_SERVING, got.GetStatus())

	descriptor.Endpoint = "http://changed"
	fromStore, err := store.Get("workflow")
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9000", fromStore.GetEndpoint())

	fromStore.Endpoint = "http://mutated"
	again, err := store.Get("workflow")
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9000", again.GetEndpoint())
}

func TestMemoryRejectsInvalidRegistrations(t *testing.T) {
	store := registry.NewMemory(registry.MemoryOptions{})
	cases := []*registryv1.ServiceDescriptor{
		nil,
		{},
		{SubsystemName: "x"},
		{SubsystemName: "x", Endpoint: "not-a-url"},
		{SubsystemName: "x", Endpoint: "ftp://example.com"},
	}
	for _, descriptor := range cases {
		_, err := store.Register(descriptor, 0)
		assert.ErrorIs(t, err, registry.ErrInvalidRegistration)
	}
}

func TestMemoryHeartbeatExpiryAndDeregister(t *testing.T) {
	now := time.Unix(200, 0)
	store := registry.NewMemory(registry.MemoryOptions{Now: func() time.Time { return now }})
	_, err := store.Register(&registryv1.ServiceDescriptor{SubsystemName: "agent", Endpoint: "http://localhost:1"}, time.Second)
	require.NoError(t, err)

	_, err = store.Heartbeat("agent", 2*time.Second)
	require.NoError(t, err)
	now = now.Add(3 * time.Second)
	_, err = store.Get("agent")
	assert.ErrorIs(t, err, registry.ErrNotFound)

	_, err = store.Heartbeat("agent", 0)
	assert.ErrorIs(t, err, registry.ErrNotFound)
	assert.False(t, store.Deregister("agent"))
}

func TestMemoryListFiltersCapabilitiesAndSorts(t *testing.T) {
	store := registry.NewMemory(registry.MemoryOptions{DefaultLease: time.Hour})
	_, err := store.Register(&registryv1.ServiceDescriptor{
		SubsystemName: "zeta",
		Endpoint:      "http://localhost:1",
		Capabilities:  []*registryv1.Capability{{Name: "read"}},
	}, 0)
	require.NoError(t, err)
	_, err = store.Register(&registryv1.ServiceDescriptor{
		SubsystemName: "alpha",
		Endpoint:      "http://localhost:2",
		Capabilities:  []*registryv1.Capability{{Name: "read"}, {Name: "write"}},
	}, 0)
	require.NoError(t, err)

	all := store.List("", nil, false)
	require.Len(t, all, 2)
	assert.Equal(t, "alpha", all[0].GetSubsystemName())
	assert.Equal(t, "zeta", all[1].GetSubsystemName())

	read := store.List("", []string{"read"}, false)
	require.Len(t, read, 2)
	writes := store.List("", []string{"write"}, false)
	require.Len(t, writes, 1)
	assert.Equal(t, "alpha", writes[0].GetSubsystemName())
	assert.Empty(t, store.List("", []string{"missing"}, false))
}

func TestMemoryWatch(t *testing.T) {
	store := registry.NewMemory(registry.MemoryOptions{})
	watch := store.Watch(2)
	_, err := store.Register(&registryv1.ServiceDescriptor{SubsystemName: "skills", Endpoint: "http://localhost:1"}, 0)
	require.NoError(t, err)
	select {
	case event := <-watch:
		assert.Equal(t, registry.EventRegistered, event.Type)
		assert.Equal(t, "skills", event.Descriptor.GetSubsystemName())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for registry event")
	}
	store.CloseWatch(watch)
}

func TestRegistryServiceConnectErrors(t *testing.T) {
	service := registry.NewService(nil)
	ctx := context.Background()

	_, err := service.Register(ctx, connect.NewRequest(&registryv1.RegisterRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = service.GetService(ctx, connect.NewRequest(&registryv1.GetServiceRequest{SubsystemName: "missing"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	descriptor := &registryv1.ServiceDescriptor{SubsystemName: "workflow", Endpoint: "http://localhost:3"}
	resp, err := service.Register(ctx, connect.NewRequest(&registryv1.RegisterRequest{Descriptor_: descriptor, LeaseSeconds: 60}))
	require.NoError(t, err)
	assert.Equal(t, "workflow", resp.Msg.GetDescriptor_().GetSubsystemName())

	list, err := service.ListServices(ctx, connect.NewRequest(&registryv1.ListServicesRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetServices(), 1)

	heartbeat, err := service.Heartbeat(ctx, connect.NewRequest(&registryv1.HeartbeatRequest{SubsystemName: "workflow", LeaseSeconds: 60}))
	require.NoError(t, err)
	assert.NotZero(t, heartbeat.Msg.GetDescriptor_().GetLeaseExpiresAtUnixNano())

	removed, err := service.Deregister(ctx, connect.NewRequest(&registryv1.DeregisterRequest{SubsystemName: "workflow"}))
	require.NoError(t, err)
	assert.True(t, removed.Msg.GetRemoved())
}

func TestRegistryServiceNegativeLease(t *testing.T) {
	service := registry.NewService(nil)
	_, err := service.Register(context.Background(), connect.NewRequest(&registryv1.RegisterRequest{
		Descriptor_:  &registryv1.ServiceDescriptor{SubsystemName: "x", Endpoint: "http://localhost:1"},
		LeaseSeconds: -1,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.False(t, errors.Is(err, registry.ErrInvalidRegistration))
}
