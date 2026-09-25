package host_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/Manu343726/toolsbox/pkg/host"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostSelectsAndStartsIndependentSubsystems(t *testing.T) {
	h := host.New()
	factory := func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "example", Version: "test",
			Services: []subsystem.Service{{Name: "example.v1.ExampleService", Path: "/example.v1.ExampleService/", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}},
		})
	}
	require.NoError(t, h.Register("example", factory))
	var callbacks int
	h.OnStarted(func(context.Context, *subsystem.Descriptor) error { callbacks++; return nil })
	require.NoError(t, h.Select("example"))
	require.NoError(t, h.Start(context.Background()))
	assert.Equal(t, 1, callbacks)
	assert.Len(t, h.Servers(), 1)
	assert.Len(t, h.Descriptors(), 1)
	require.NoError(t, h.Shutdown(context.Background()))
	assert.Empty(t, h.Servers())
}

func TestHostRejectsUnknownAndDuplicateSelection(t *testing.T) {
	h := host.New()
	factory := func() (*subsystem.Server, error) { return nil, assert.AnError }
	require.NoError(t, h.Register("one", factory))
	assert.Error(t, h.Select("missing"))
	assert.Error(t, h.Select("one", "one"))
	assert.Error(t, h.Register("one", factory))
}

func TestHostStartsAllInNameOrder(t *testing.T) {
	h := host.New()
	for _, name := range []string{"z", "a"} {
		name := name
		require.NoError(t, h.Register(name, func() (*subsystem.Server, error) {
			return subsystem.NewServer(subsystem.Config{Name: name, Services: []subsystem.Service{{Name: name + ".v1.Service", Path: "/" + name + ".v1.Service/", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}}})
		}))
	}
	require.NoError(t, h.Start(context.Background()))
	descriptors := h.Descriptors()
	require.Len(t, descriptors, 2)
	assert.Equal(t, "a", descriptors[0].SubsystemName)
	assert.Equal(t, "z", descriptors[1].SubsystemName)
	require.NoError(t, h.Shutdown(context.Background()))
}
