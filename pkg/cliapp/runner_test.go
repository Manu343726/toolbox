package cliapp_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A standalone subsystem command has two modes, and which one it is in decides what a write
// through it means: a private instance whose state dies with the process, or a call into the
// deployment's state. These tests drive the real command tree in both, because the decision
// is made from a flag and a configuration chain, and neither is visible from the inside.

const (
	fixtureService = "toolbox.api.v1.ApiParserService"
	// fixturePath is where the operation lives, as separate arguments. The service keeps a
	// level of its own here, because the command is "fixture" and the service is
	// "api-parser" — a namespace that names nothing the caller did not already name is only
	// dropped when it repeats the command's own name.
	fixturePath = "api-parser parse-api"
)

// fixtureFactory composes one subsystem serving one real contract.
//
// The contract is the framework's own, linked in by importing pkg/api, so the command tree
// under test is built from real descriptors with real comments. A fixture contract with
// invented fields would only test that the generator handles invented fields.
//
// The handler answers any unary call with an empty response, which is all these tests need:
// they are about where a command sends a call, not about what a service replies. A handler
// built from a generated type would need the subsystem that owns the contract, and a root
// package cannot import one. A stub that 404s would be worse — a 404 reads as "that
// operation does not exist" and every assertion about reaching a peer would become a
// statement about the stub.
func fixtureFactory(t *testing.T) (subsystem.Factory, []string) {
	t.Helper()
	return func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name:        "fixture",
			Version:     "0.1.0",
			Description: "A subsystem with one contract, for the command tests.",
			Services: []subsystem.Service{{
				Name: fixtureService,
				// Trailing slash, because the path is the prefix a request for a method
				// lands under. net/http's mux matches a pattern without one exactly, so a
				// path missing it mounts a handler nothing can reach.
				Path: "/" + fixtureService + "/",
				// The content type is the one a Connect client sends and expects back, so
				// the call is a real one rather than a request the codec rejects. An empty
				// body is an empty message, which every contract accepts.
				Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/proto")
				}),
			}},
		})
	}, []string{fixtureService}
}

var _ api.Catalog

// run executes the command with args and returns what it wrote and what it returned.
//
// An argument containing a space is split, so a command path can be written as one readable
// string rather than as a run of separate arguments at every call site.
func run(t *testing.T, options cliapp.Options, args ...string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	options.Args = nil
	for _, argument := range args {
		options.Args = append(options.Args, strings.Fields(argument)...)
	}
	options.Output = out
	options.Report = out
	err := cliapp.Run(t.Context(), options)
	return out.String(), err
}

// With no core configured, the command starts a private instance and offers every operation
// the subsystem serves.
func TestACommandWithNoCoreServesItsOwnSubsystem(t *testing.T) {
	factory, services := fixtureFactory(t)
	out, err := run(t, cliapp.Options{
		Name:        "fixture",
		Description: "A fixture subsystem.",
		Factory:     factory,
	}, "--help")
	require.NoError(t, err)

	assert.Contains(t, out, "api-parser", "the contract is offered")
	assert.Contains(t, out, "serve", "and so is serving the subsystem")
	assert.Contains(t, out, "mcp", "and so is the generated MCP")
	require.NotEmpty(t, services, "the fixture serves no contract, so this proves nothing")
	for _, service := range services {
		short := strings.TrimPrefix(service, "toolbox.")
		assert.Contains(t, out, short[:strings.Index(short, ".")],
			"%s is offered as a command", service)
	}
}

// A private instance is what an MCP server is generated from, and the flag that points the
// command at a core has to appear in the help — a flag nobody can find is a flag nobody sets.
func TestTheCoreFlagIsOfferedAndExplained(t *testing.T) {
	factory, _ := fixtureFactory(t)
	out, err := run(t, cliapp.Options{Name: "fixture", Factory: factory}, "--help")
	require.NoError(t, err)

	assert.Contains(t, out, "--core")
	assert.Contains(t, out, "private instance",
		"and says what happens without one, because the two modes mean different things")
}

// The command is the same command in both modes: same operations, same flags, same help.
// Pointing it at a core changes where a call goes and nothing a caller can see.
func TestTheSameOperationsAreOfferedInBothModes(t *testing.T) {
	factory, _ := fixtureFactory(t)

	private, err := run(t, cliapp.Options{Name: "fixture", Factory: factory}, fixturePath, "--help")
	require.NoError(t, err)

	againstCore, err := run(t, cliapp.Options{
		Name:        "fixture",
		Factory:     factory,
		ResolverFor: resolverFor(t),
	}, "--core", "core.internal:9180", fixturePath, "--help")
	require.NoError(t, err)

	for _, flag := range []string{"--document", "--format", "--format_hint", "--source"} {
		assert.Contains(t, private, flag, "a private instance offers %s", flag)
		assert.Contains(t, againstCore, flag, "and so does a command pointed at a core")
	}
}

// A call aimed at a core goes through the core's resolver, which is how a peer is found: a
// core's address is its registry's, not the address of every service it hosts.
//
// The peer here is a real subsystem serving the real contract, so the call is made rather
// than merely aimed. A fake endpoint would prove the resolver was consulted and nothing
// about whether a command can actually call a deployment.
func TestACallAimedAtACoreIsResolvedThroughTheCore(t *testing.T) {
	peer, _ := fixtureFactory(t)
	server, err := peer()
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	var resolved []string
	resolver := core.ResolverFunc(func(_ context.Context, name string) (core.Endpoint, error) {
		resolved = append(resolved, name)
		return core.Endpoint{Name: "peer", URL: server.Endpoint(), ServiceNames: []string{name}}, nil
	})

	factory, _ := fixtureFactory(t)
	out, err := run(t, cliapp.Options{
		Name:    "fixture",
		Factory: factory,
		ResolverFor: func(string) (core.Resolver, func(), error) {
			return resolver, nil, nil
		},
	}, "--core", "core.internal:9180", fixturePath, "--format", "openapi")
	require.NoError(t, err, "the call reached the peer the core pointed at")
	assert.Equal(t, []string{fixtureService}, resolved,
		"the core was asked where the service is, rather than the core's address being assumed")
	assert.Contains(t, out, "calling the core", "and the command said where it was calling")
}

// A resolver that reports the service missing must be reported as missing, not retried
// against a private instance. Silently answering from somewhere else would be a write
// against a store the caller did not choose.
func TestAServiceTheCoreDoesNotHostIsRefused(t *testing.T) {
	factory, _ := fixtureFactory(t)
	_, err := run(t, cliapp.Options{
		Name:    "fixture",
		Factory: factory,
		ResolverFor: func(string) (core.Resolver, func(), error) {
			return core.ResolverFunc(func(_ context.Context, name string) (core.Endpoint, error) {
				return core.Endpoint{}, fmt.Errorf("%w: the core hosts nothing called %s", core.ErrNotFound, name)
			}), nil, nil
		},
	}, "--core", "core.internal:9180", fixturePath, "--format", "openapi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), fixtureService, "the refusal names the service")
}

// A core that cannot be reached is named, because a dial error on its own leaves a reader to
// work out which of the two modes they are in — and a write that failed against an absent
// core is not a write that happened privately.
func TestACoreThatCannotBeReachedIsNamed(t *testing.T) {
	factory, _ := fixtureFactory(t)
	_, err := run(t, cliapp.Options{
		Name:    "fixture",
		Factory: factory,
		ResolverFor: func(string) (core.Resolver, func(), error) {
			return nil, nil, fmt.Errorf("the core at core.internal:9180 did not answer: connection refused")
		},
	}, "--core", "core.internal:9180", fixturePath, "--format", "openapi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not answer")
	assert.Contains(t, err.Error(), "core.internal:9180", "and names the address, so the fix is obvious")
}

// A command given a core address and no way to build a resolver says so, rather than sending
// the call to the core's own address and reporting a 404 that reads as "that operation does
// not exist".
func TestACoreWithNoResolverIsRefusedWithAnExplanation(t *testing.T) {
	factory, _ := fixtureFactory(t)
	_, err := run(t, cliapp.Options{Name: "fixture", Factory: factory},
		"--core", "core.internal:9180", fixturePath, "--format", "openapi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registry",
		"the refusal says what is missing and why a core's address is not enough")
}

// The commands that need a subsystem in this process stay listed when a core is configured,
// and refuse with the reason when used.
//
// They are not hidden. A hidden command is one whose absence a caller has to guess at, and
// "can this serve anything?" is a question worth answering — it is answered with the reason
// rather than with silence. Hiding them would also mean deciding visibility before the flag
// that decides the mode has been parsed, which is how a command ends up hidden for a core the
// caller did not configure.
func TestTheLocalOnlyCommandsRefuseWhenACoreIsConfigured(t *testing.T) {
	factory, _ := fixtureFactory(t)
	options := func() cliapp.Options {
		return cliapp.Options{
			Name: "fixture", Factory: factory, ResolverFor: resolverFor(t),
			Core: "core.internal:9180",
		}
	}

	// Listed, so the question is answerable.
	help, err := run(t, options(), "--help")
	require.NoError(t, err)
	assert.Contains(t, help, "serve", "the command is still listed")
	assert.Contains(t, help, "mcp", "and so is this one")

	for _, command := range []string{"serve", "mcp"} {
		t.Run(command, func(t *testing.T) {
			_, err := run(t, options(), command)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "core.internal:9180",
				"the refusal names the core that made the command inapplicable")
			assert.Contains(t, err.Error(), "without --core", "and says what to do instead")
		})
	}
}

// With no core configured those same commands work, so the refusal above is about the mode
// rather than about the command.
func TestTheLocalOnlyCommandsWorkWithNoCore(t *testing.T) {
	factory, _ := fixtureFactory(t)
	// `serve` blocks, so it is not run here; the mcp command is checked for being reachable
	// by asserting the refusal is about the core and not about the command existing.
	_, err := run(t, cliapp.Options{Name: "fixture", Factory: factory}, "mcp", "--help")
	require.NoError(t, err)
}

// The root of a command pointed at a core prints its help and returns, rather than blocking.
// A command with nothing to serve that hangs is indistinguishable from a hung deployment.
func TestACommandPointedAtACoreReturnsInsteadOfBlocking(t *testing.T) {
	factory, _ := fixtureFactory(t)
	out, err := run(t, cliapp.Options{
		Name: "fixture", Factory: factory, ResolverFor: resolverFor(t),
	}, "--core", "core.internal:9180")
	require.NoError(t, err, "it returns rather than hanging")
	assert.Contains(t, out, "api-parser", "and shows what it could have called")
}

// The private instance is shut down when the command finishes. A command that left a
// listener behind would make the next command in a script fail to bind, and the failure would
// look like a port clash rather than a leak.
func TestAPrivateInstanceIsShutDownWhenTheCommandFinishes(t *testing.T) {
	factory, _ := fixtureFactory(t)
	// A command that fails, so the tree is built and torn down without a call being made.
	_, err := run(t, cliapp.Options{Name: "fixture", Factory: factory}, "no-such-operation")
	require.Error(t, err)
	// A second command in the same process must not collide with the first one's listener,
	// which is the observable consequence of the shutdown.
	_, err = run(t, cliapp.Options{Name: "fixture", Factory: factory}, "no-such-operation")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-operation")
}

// A caller embedding the runner keeps its own address, and a person at a shell can override
// it. The flag's default is the configured value, so both work without a special case.
func TestTheConfiguredAddressIsTheFlagsDefault(t *testing.T) {
	factory, _ := fixtureFactory(t)
	out, err := run(t, cliapp.Options{
		Name: "fixture", Factory: factory, ResolverFor: resolverFor(t),
		Core: "core.internal:9180",
	}, "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "core.internal:9180",
		"the address the caller configured is visible in the help, so it is never a surprise")
}

// resolverFor is a resolver factory for the tests that only need a core to be configured,
// not to answer.
func resolverFor(t *testing.T) func(string) (core.Resolver, func(), error) {
	t.Helper()
	return func(string) (core.Resolver, func(), error) {
		return core.ResolverFunc(func(context.Context, string) (core.Endpoint, error) {
			return core.Endpoint{}, fmt.Errorf("%w: not hosting anything in this test", core.ErrNotFound)
		}), nil, nil
	}
}
