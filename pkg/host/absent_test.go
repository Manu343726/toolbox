package host_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Manu343726/toolbox/pkg/host"
	"github.com/Manu343726/toolbox/pkg/subsystem"
)

// These tests are about a factory that reports there is nothing to start.
//
// That is a supported answer, not a failure: a subsystem that only contributes has no contract to
// serve, and a provider this deployment has not configured has nothing to offer. Both answer
// `(nil, nil)`. So a host that dereferences whatever a factory returned turns "this deployment
// does not use that subsystem" into a crash — and it crashes on paths that run while a command tree
// is being built, where the symptom is "cannot print help" and nothing points at the absent
// subsystem.

func serving(name, service string) subsystem.Factory {
	return func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: name, Version: "test",
			Services: []subsystem.Service{{
				Name: service, Path: "/" + service + "/",
				Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			}},
		})
	}
}

func TestASelectedFactoryThatReportsNothingToStartIsNotAFailure(t *testing.T) {
	t.Parallel()
	h := host.New()
	require.NoError(t, h.Register("configured", serving("configured", "configured.v1.Configured")))
	require.NoError(t, h.Register("unconfigured", func() (*subsystem.Server, error) { return nil, nil }))
	require.NoError(t, h.Start(context.Background()))

	// The one that serves is started. The one that does not is absent rather than present as
	// something broken, and it took nothing else down with it.
	assert.Len(t, h.Servers(), 1)
	assert.Contains(t, h.Servers(), "configured")
	assert.Len(t, h.Descriptors(), 1)
	require.NoError(t, h.Shutdown(context.Background()))
}

func TestServiceNamesSkipsAFactoryThatReportsNothingToStart(t *testing.T) {
	t.Parallel()
	h := host.New()
	require.NoError(t, h.Register("configured", serving("configured", "configured.v1.Configured")))
	require.NoError(t, h.Register("unconfigured", func() (*subsystem.Server, error) { return nil, nil }))

	// A command tree is built from this list, before anything has started, so this is the
	// path where an unconfigured provider would take the process down. Reading the list is
	// how `toolbox --help` decides what to offer.
	names, err := h.ServiceNames()
	require.NoError(t, err)
	assert.Equal(t, []string{"configured.v1.Configured"}, names)
}

func TestContributionsFromTheOthersStillApplyWhenOneReportsNothingToStart(t *testing.T) {
	t.Parallel()
	h := host.New()
	require.NoError(t, h.Register("unconfigured", func() (*subsystem.Server, error) { return nil, nil }))

	// A contributor has no contract of its own, so it answers exactly this way. A host that
	// treated the nil as a server would fail in the configure phase, before any of it started
	// — which is the phase that exists precisely so that a deployment's configuration is
	// complete before anything is reachable.
	contributed := false
	contributor := func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "contributor", Version: "test",
			Configure: func(ctx context.Context, c subsystem.Compositor) error {
				contributed = true
				return c.Compose("configured", serving("configured", "configured.v1.Configured"))
			},
		})
	}
	require.NoError(t, h.Register("contributor", contributor))
	require.NoError(t, h.Start(context.Background()))

	assert.True(t, contributed)
	assert.Contains(t, h.Servers(), "configured")
	require.NoError(t, h.Shutdown(context.Background()))
}
