package host_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Manu343726/toolbox/pkg/host"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A contributor adds a subsystem to the composition, and the added subsystem runs. This is
// the whole mechanism: a thing that configures the deployment it is part of, rather than only
// serving one.
func TestAContributorAddsASubsystemAndItRuns(t *testing.T) {
	h := host.New()
	// A holder is a stand-in for whatever the contributor keeps to itself: the test is
	// about what the composition does with a contribution, and the value reaching the
	// factory is what makes that observable.
	var contributed string

	require.NoError(t, h.Register("configures", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "configures", Version: "0.1.0",
			Configure: func(_ context.Context, into subsystem.Compositor) error {
				return into.Compose("supplied", func() (*subsystem.Server, error) {
					contributed = "reached"
					return subsystem.NewServer(subsystem.Config{
						Name: "supplied", Version: "0.1.0",
						Services: []subsystem.Service{{Name: "supplied.v1.Supplied", Path: "/", Handler: nothing()}},
					})
				})
			},
		})
	}))

	require.NoError(t, h.Start(t.Context()))
	assert.Equal(t, "reached", contributed, "the contributed factory ran")
	assert.Contains(t, h.Servers(), "supplied", "and what it built is part of the deployment")
}

// A subsystem that only contributes is not started: no listener, so no port, no handshake, no
// registry entry. That is what makes "may not expose features itself" true rather than
// aspirational — a contributor that still opened a port would appear in every listing of what
// the deployment runs, and a thing whose only job is to decide how the rest is wired has
// nothing to be reached for.
func TestAContributorThatServesNothingIsNotStarted(t *testing.T) {
	h := host.New()
	require.NoError(t, h.Register("configures", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "configures", Version: "0.1.0",
			Configure: func(_ context.Context, into subsystem.Compositor) error {
				return into.Compose("supplied", func() (*subsystem.Server, error) {
					return subsystem.NewServer(subsystem.Config{
						Name: "supplied", Version: "0.1.0",
						Services: []subsystem.Service{{Name: "supplied.v1.Supplied", Path: "/", Handler: nothing()}},
					})
				})
			},
		})
	}))

	require.NoError(t, h.Start(t.Context()))
	assert.NotContains(t, h.Servers(), "configures",
		"it contributed and is finished, so there is nothing to be reached for")
	assert.Contains(t, h.Servers(), "supplied")
}

// The contribution is complete before anything is asked to work, so there is no window in
// which a subsystem is up and what it contributes is not yet in place.
//
// The claim is asserted from inside the contributed factory, which runs in the configure
// phase on the same goroutine as `Start` — so there is nothing to synchronise and nothing to
// race. Reading it from a background goroutine instead would have proved less and been a data
// race, since the write and the assertion would not be ordered.
func TestAContributionIsCompleteBeforeAnythingIsAskedToWork(t *testing.T) {
	h := host.New()
	startedWhenComposed := -1

	require.NoError(t, h.Register("configures", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "configures", Version: "0.1.0",
			Configure: func(_ context.Context, into subsystem.Compositor) error {
				return into.Compose("supplied", func() (*subsystem.Server, error) {
					// Whatever the deployment has running, at the moment this contribution
					// is applied.
					startedWhenComposed = len(h.Servers())
					return subsystem.NewServer(subsystem.Config{
						Name: "supplied", Version: "0.1.0",
						Services: []subsystem.Service{{
							Name: "supplied.v1.Supplied", Path: "/", Handler: nothing(),
						}},
					})
				})
			},
		})
	}))
	// A subsystem that existed before the contribution, so "nothing is running yet" is a
	// statement about the phase and not about there being only one subsystem.
	require.NoError(t, h.Register("serves", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "serves", Version: "0.1.0",
			Services: []subsystem.Service{{
				Name: "serves.v1.Serves", Path: "/", Handler: nothing(),
			}},
		})
	}))

	require.NoError(t, h.Start(t.Context()))
	assert.Equal(t, 0, startedWhenComposed,
		"a contribution is applied before anything is started, so nothing was half-configured "+
			"and then asked to work")
	assert.Len(t, h.Servers(), 2)
}

// A contributor that fails aborts the start. A deployment that could not be fully configured
// does not come up partly configured, because a project naming a reference that resolves to
// nothing is the one state a project's list must never be in, and a startup failure is how a
// person hears about it.
func TestAFailedContributionStopsTheDeployment(t *testing.T) {
	h := host.New()
	require.NoError(t, h.Register("configures", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "configures", Version: "0.1.0",
			Configure: func(context.Context, subsystem.Compositor) error {
				return assert.AnError
			},
		})
	}))
	require.NoError(t, h.Register("serves", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "serves", Version: "0.1.0",
			Services: []subsystem.Service{{Name: "serves.v1.Serves", Path: "/", Handler: nothing()}},
		})
	}))

	err := h.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configures",
		"the error names what failed, because a startup failure a person cannot act on is only "+
			"slightly better than a silent one")
	assert.Empty(t, h.Servers(), "and nothing is left half-started")
}

// A contribution may add a subsystem and never replace one. Rewriting a registered subsystem's
// options would mean two sources of the same settings with a precedence rule between them, and
// "which wins, the file or the code" is a question with no good answer — a person editing a
// file that silently loses to code they cannot see is worse than either winning.
func TestAContributionMayNotReplaceASubsystem(t *testing.T) {
	h := host.New()
	require.NoError(t, h.Register("already", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "already", Version: "0.1.0",
			Services: []subsystem.Service{{Name: "already.v1.Already", Path: "/", Handler: nothing()}},
		})
	}))

	var composeErr error
	require.NoError(t, h.Register("configures", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "configures", Version: "0.1.0",
			Configure: func(_ context.Context, into subsystem.Compositor) error {
				composeErr = into.Compose("already", func() (*subsystem.Server, error) {
					t.Error("the factory must not run, because the name was refused")
					return nil, nil
				})
				return nil
			},
		})
	}))

	require.NoError(t, h.Start(t.Context()))
	require.Error(t, composeErr)
	assert.Contains(t, composeErr.Error(), "already")
	assert.Contains(t, composeErr.Error(), "another name",
		"the refusal says what to do, because a contributor that cannot compose anything has "+
			"no way to guess why")
	// One subsystem runs, and it is the deployment's own. The contributor is absent because
	// it serves nothing, which is the same rule as everywhere else.
	assert.Len(t, h.Servers(), 1)
	assert.Contains(t, h.Servers(), "already", "and it is untouched")
}

// Two contributions naming one subsystem is a deployment that cannot say which content a
// reference resolves to, so it is refused rather than resolved by whichever happened to be
// built last.
//
// The two here are in *different waves*, which is the case that is easy to get wrong: a
// duplicate check that lives on one wave's compositor forgets everything the previous wave
// contributed, so the second one is accepted and the deployment has two subsystems under one
// name with nothing to say so.
func TestTwoContributionsInDifferentWavesMayNotShareAName(t *testing.T) {
	h := host.New()
	var second error

	require.NoError(t, h.Register("outer", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "outer", Version: "0.1.0",
			Configure: func(_ context.Context, into subsystem.Compositor) error {
				// The shared name, in the first wave.
				return into.Compose("shared", func() (*subsystem.Server, error) {
					return subsystem.NewServer(subsystem.Config{
						Name: "shared", Version: "0.1.0",
						// Which is itself a contributor, so the phase iterates.
						Configure: func(_ context.Context, next subsystem.Compositor) error {
							// And again, in the second wave, which is a duplicate.
							second = next.Compose("shared", func() (*subsystem.Server, error) {
								t.Error("the factory must not run, because the name was refused")
								return nil, nil
							})
							return nil
						},
					})
				})
			},
		})
	}))

	require.NoError(t, h.Start(t.Context()))
	require.Error(t, second, "a name composed in an earlier wave is still taken")
	assert.Contains(t, second.Error(), "one thing")
}

// A contributor that composes a subsystem which also contributes gets its contribution
// applied. The phase iterates while contributors keep contributing, and terminates because a
// name can be composed once.
func TestAContributionMayComposeAContributor(t *testing.T) {
	h := host.New()
	require.NoError(t, h.Register("outer", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "outer", Version: "0.1.0",
			Configure: func(_ context.Context, into subsystem.Compositor) error {
				return into.Compose("inner", func() (*subsystem.Server, error) {
					return subsystem.NewServer(subsystem.Config{
						Name: "inner", Version: "0.1.0",
						Configure: func(_ context.Context, next subsystem.Compositor) error {
							return next.Compose("supplied", func() (*subsystem.Server, error) {
								return subsystem.NewServer(subsystem.Config{
									Name: "supplied", Version: "0.1.0",
									Services: []subsystem.Service{{
										Name: "supplied.v1.Supplied", Path: "/", Handler: nothing(),
									}},
								})
							})
						},
					})
				})
			},
		})
	}))

	// Bounded, because the thing this test is most able to regress is the phase failing to
	// settle. An unbounded wait would turn a mistake in the loop into a test that never
	// returns and allocates a subsystem per pass — which is exactly how a version of this
	// loop behaved, and why the wait here has a message rather than a shrug.
	settled := make(chan error, 1)
	go func() { settled <- h.Start(t.Context()) }()
	select {
	case err := <-settled:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("applying contributions did not settle: a pass is building subsystems the " +
			"composition has already built, so the phase cannot finish")
	}

	assert.Contains(t, h.Servers(), "supplied",
		"the second wave's contribution was applied, which is the whole claim")
	assert.NotContains(t, h.Servers(), "inner")
	assert.NotContains(t, h.Servers(), "outer",
		"and neither contributor is started, because neither serves anything")
}

// A subsystem with a mount or background work still runs even with no contract, because it
// does have something to serve. The rule is about having nothing to do, not about having no
// protobuf.
func TestASubsystemWithSomethingToDoIsStarted(t *testing.T) {
	h := host.New()
	served := make(chan struct{})
	require.NoError(t, h.Register("works", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "works", Version: "0.1.0",
			Background: func(ctx context.Context) error {
				close(served)
				<-ctx.Done()
				return nil
			},
		})
	}))

	require.NoError(t, h.Start(t.Context()))
	defer func() { _ = h.Shutdown(t.Context()) }()
	assert.Contains(t, h.Servers(), "works")
	select {
	case <-served:
	case <-t.Context().Done():
		t.Fatal("the subsystem never ran")
	}
}

// A deployment with no contributors behaves exactly as it did before they existed, which is
// the property that makes adding them safe.
func TestADeploymentWithNoContributorsIsUnchanged(t *testing.T) {
	h := host.New()
	for _, name := range []string{"one", "two"} {
		require.NoError(t, h.Register(name, func() (*subsystem.Server, error) {
			return subsystem.NewServer(subsystem.Config{
				Name: name, Version: "0.1.0",
				Services: []subsystem.Service{{Name: name + ".v1.S", Path: "/", Handler: nothing()}},
			})
		}))
	}

	require.NoError(t, h.Start(t.Context()))
	assert.Len(t, h.Servers(), 2)
	assert.Equal(t, []string{"one", "two"}, h.StartOrder(),
		"and the start order is the selection order, as it was")
}

// The contributed subsystem is registered like any other, so a registry entry, a provider
// record and a command name all work for it without knowing it was contributed.
func TestAContributedSubsystemIsAnOrdinarySubsystem(t *testing.T) {
	h := host.New()
	var endpoints []string
	h.OnStarted(func(_ context.Context, descriptor *subsystem.Descriptor) error {
		endpoints = append(endpoints, descriptor.SubsystemName+"="+descriptor.Endpoint)
		return nil
	})
	require.NoError(t, h.Register("configures", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "configures", Version: "0.1.0",
			Configure: func(_ context.Context, into subsystem.Compositor) error {
				return into.Compose("supplied", func() (*subsystem.Server, error) {
					return subsystem.NewServer(subsystem.Config{
						Name: "supplied", Version: "0.1.0",
						Services: []subsystem.Service{{Name: "supplied.v1.Supplied", Path: "/", Handler: nothing()}},
					})
				})
			},
		})
	}))

	require.NoError(t, h.Start(t.Context()))
	require.Len(t, endpoints, 1, "only the subsystem that serves is registered")
	assert.True(t, strings.HasPrefix(endpoints[0], "supplied=http"),
		"with a real endpoint of its own: %s", endpoints[0])
}

// nothing is a handler for a service that exists only to be registered. The framework refuses
// a service with no implementation behind it, which is right, and these tests are about
// composition rather than about serving anything.
func nothing() http.Handler { return http.NotFoundHandler() }

// A subsystem that reports itself under a name other than the one it was composed under would
// put two subsystems in one name in the registry, which is the collision `Compose` refuses and
// which would otherwise happen one layer down. The composition decides the name.
func TestAContributedSubsystemMustReportTheNameItWasComposedUnder(t *testing.T) {
	h := host.New()
	require.NoError(t, h.Register("configures", func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "configures", Version: "0.1.0",
			Configure: func(_ context.Context, into subsystem.Compositor) error {
				return into.Compose("as-composed", func() (*subsystem.Server, error) {
					return subsystem.NewServer(subsystem.Config{
						// A different name from the one it was composed under.
						Name: "as-declared", Version: "0.1.0",
						Services: []subsystem.Service{{
							Name: "x.v1.X", Path: "/", Handler: nothing(),
						}},
					})
				})
			},
		})
	}))

	err := h.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "as-composed")
	assert.Contains(t, err.Error(), "as-declared",
		"the error names both, because a contributor fixing this needs to know which of the "+
			"two names it has to change")
	assert.Contains(t, err.Error(), "the composition decides the name")
	assert.Empty(t, h.Servers(), "and nothing is left half-registered")
}
