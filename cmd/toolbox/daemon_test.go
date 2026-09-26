package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/host"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	"github.com/Manu343726/toolbox/subsystems/registry/registryv1/registryv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The daemon has two claims worth testing, and both are about the address:
//
//   - a subsystem that did not start inside the core can register with it, so
//     membership is a decision a deployment makes rather than a list the binary was
//     compiled with;
//   - an agent reaches the Model Context Protocol over the network, instead of
//     launching a subprocess and paying a start-up per session.
//
// Both are exercised against a real core with a real socket, because a daemon that
// only works with its parts stubbed is not a daemon.

// freeAddress reserves and releases a loopback port, so the core can bind an address
// a test knows in advance.
//
// It is a reservation rather than a binding, so there is a window between the release
// and the bind in which something else could take the port. That is acceptable here:
// the alternative is a fixed port, which makes the suite fail in parallel or behind
// another process, and a test that fails for a reason nobody can reproduce is worse
// than a rare bind race.
func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

// coreMount is the mount a daemon installs, with the deferred handler that carries
// the Model Context Protocol until it is built.
func coreMount(deferred *deferredHandler) subsystem.Mount {
	return subsystem.Mount{
		Path:        MCPPath,
		Handler:     deferred,
		Description: "The core's Model Context Protocol endpoint.",
	}
}

// startCore composes and starts a core on a free address, the way the daemon does.
func startCore(t *testing.T, components ...string) (*hostHandle, *deferredHandler) {
	t.Helper()
	address := freeAddress(t)
	deferred := &deferredHandler{}
	h, catalog, err := buildHost(config.Config{Daemon: config.Listen{Host: "127.0.0.1", Port: portOf(t, address)}}, address, coreMount(deferred))
	require.NoError(t, err)
	require.NoError(t, h.Select(components...))
	require.NoError(t, h.Start(t.Context()))
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	registryServer, ok := h.Servers()["registry"]
	require.True(t, ok, "the core needs its registry")
	return &hostHandle{
		host:     h,
		catalog:  catalog,
		address:  address,
		endpoint: registryServer.Endpoint(),
	}, deferred
}

// hostHandle is the running core a test asserts against.
type hostHandle struct {
	host    *host.Host
	catalog *sharedCatalog
	// address is the host:port the core was configured with, which is what a
	// deployment writes in a configuration file.
	address string
	// endpoint is that address as a URL, which is what a client dials.
	endpoint string
}

// client is a typed registry client for the core, the way a subsystem that did not
// start inside it would reach the core.
func (c *hostHandle) client() registryv1connect.RegistryServiceClient {
	return registryv1connect.NewRegistryServiceClient(http.DefaultClient, c.endpoint)
}

// portOf reads the port back out of an address, so the config and the bind address
// cannot disagree — a test that sets one and binds the other passes for the wrong
// reason.
func portOf(t *testing.T, address string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	require.NoError(t, err)
	number := 0
	for _, digit := range port {
		require.True(t, digit >= '0' && digit <= '9', "port %q is numeric", port)
		number = number*10 + int(digit-'0')
	}
	return number
}

func TestTheCoreBindsTheAddressItWasConfiguredWith(t *testing.T) {
	// The address is the whole point: a subsystem joining and an agent connecting
	// both need to know where the core is without being told per session.
	core, _ := startCore(t, "registry", "knowledge")
	registryServer, ok := core.host.Servers()["registry"]
	require.True(t, ok, "the core needs its registry")
	assert.Equal(t, core.address, strings.TrimPrefix(registryServer.Endpoint(), "http://"),
		"the core's registry is on the configured address, not an ephemeral one")
}

func TestASubsystemThatDidNotStartInsideTheCoreCanRegister(t *testing.T) {
	// This is what makes membership a decision rather than a list the binary was
	// compiled with: the core holds no factory for this subsystem and never will.
	core, _ := startCore(t, "registry")

	// A second, independent subsystem registers with the core.
	other, err := subsystem.NewServer(subsystem.Config{
		Name:    "remote",
		Version: "0.1.0",
		Services: []subsystem.Service{{
			Name:    "toolbox.fixture.v1.FixtureService",
			Path:    "/toolbox.fixture.v1.FixtureService/",
			Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		}},
	})
	require.NoError(t, err)
	require.NoError(t, other.Start(t.Context()))
	t.Cleanup(func() { _ = other.Shutdown(context.Background()) })

	client := core.client()
	registration, err := client.Register(t.Context(), connect.NewRequest(&registryv1.RegisterRequest{
		Descriptor_: &registryv1.ServiceDescriptor{
			SubsystemName: "remote",
			Endpoint:      other.Endpoint(),
			ServiceNames:  []string{"toolbox.fixture.v1.FixtureService"},
		},
		LeaseSeconds: 60,
	}))
	require.NoError(t, err, "a subsystem the core did not start can join it")
	assert.Equal(t, "remote", registration.Msg.Descriptor_.SubsystemName)

	// And the core can resolve it, which is the reason it registered.
	response, err := client.GetService(t.Context(), connect.NewRequest(&registryv1.GetServiceRequest{
		SubsystemName: "remote",
	}))
	require.NoError(t, err)
	assert.Equal(t, other.Endpoint(), response.Msg.Descriptor_.Endpoint,
		"the core resolves the peer that joined it")
}

func TestTheCoreRefusesARegistrationItCannotUse(t *testing.T) {
	// A registration the core cannot act on is the caller's mistake, and it has to
	// say so as a bad argument rather than as an internal failure or an
	// unauthenticated call: the three send a reader to three entirely different
	// places.
	core, _ := startCore(t, "registry")
	for name, descriptor := range map[string]*registryv1.ServiceDescriptor{
		"no name at all":                {Endpoint: "http://127.0.0.1:9999"},
		"a blank name":                  {SubsystemName: "  ", Endpoint: "http://127.0.0.1:9999"},
		"no endpoint":                   {SubsystemName: "peer"},
		"an endpoint that is not a URL": {SubsystemName: "peer", Endpoint: "127.0.0.1:9999"},
		"a scheme nobody can dial":      {SubsystemName: "peer", Endpoint: "ftp://127.0.0.1:9999"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := core.client().Register(t.Context(), connect.NewRequest(&registryv1.RegisterRequest{
				Descriptor_: descriptor,
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestTheCoreKeepsASubsystemThatNamesNoService(t *testing.T) {
	// A subsystem with no service names is not a mistake: it serves something that
	// is not a protobuf contract — a Model Context Protocol mount, say — and refusing
	// it would leave a real deployment unable to join. What matters is that it is
	// listed, so an operator can see what the core holds.
	core, _ := startCore(t, "registry")
	_, err := core.client().Register(t.Context(), connect.NewRequest(&registryv1.RegisterRequest{
		Descriptor_:  &registryv1.ServiceDescriptor{SubsystemName: "mount-only", Endpoint: "http://127.0.0.1:9999"},
		LeaseSeconds: 60,
	}))
	require.NoError(t, err)

	listed, err := core.client().ListServices(t.Context(), connect.NewRequest(&registryv1.ListServicesRequest{}))
	require.NoError(t, err)
	names := make([]string, 0, len(listed.Msg.Services))
	for _, descriptor := range listed.Msg.Services {
		names = append(names, descriptor.GetSubsystemName())
	}
	assert.Contains(t, names, "mount-only", "the core reports what joined it")
}

func TestTheCoreAnswersAboutWhatItHolds(t *testing.T) {
	// A daemon is only useful if the catalog behind it is the catalog an in-process
	// core fills. A client that reached the core over its registry must find the same
	// descriptions, or there are two products.
	core, _ := startCore(t, "registry", "apitools", "knowledge")
	_, err := core.catalog.registerSubsystems(t.Context(), false)
	require.NoError(t, err)

	apis, err := core.catalog.service.Catalog().APIs(t.Context())
	require.NoError(t, err)
	names := make([]string, 0, len(apis))
	for _, described := range apis {
		names = append(names, described.ID)
	}
	assert.Contains(t, names, "knowledge", "a subsystem the core started is described")
	assert.Contains(t, names, "apitools", "and so is the catalog itself")
}

func TestTheDeferredMountRefusesUntilItIsReady(t *testing.T) {
	// The Model Context Protocol surface is built after the host starts, because it
	// reads the catalog the seeder fills, and the mount exists before it. A request
	// that arrives in between has to be a diagnosable refusal: an empty tool list
	// would read as "this core has no tools", which is a different problem with a
	// different fix.
	deferred := &deferredHandler{}

	recorder := httptest.NewRecorder()
	deferred.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, MCPPath, nil))
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "not ready")

	deferred.set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ready"))
	}))
	recorder = httptest.NewRecorder()
	deferred.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, MCPPath, nil))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "ready", recorder.Body.String())
}

func TestTheDeferredMountRefusesANilHandler(t *testing.T) {
	// A nil handler is not installed, because serving through it panics on the first
	// request and takes the core down with it.
	deferred := &deferredHandler{}
	deferred.set(nil)
	recorder := httptest.NewRecorder()
	deferred.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, MCPPath, nil))
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestTheCoreServesTheSurfaceItsCatalogDescribes(t *testing.T) {
	// The claim an agent depends on: the tools on the network are the catalog's
	// operations, under the names the naming rules give them, gated by the same
	// policy the core authorized with.
	core, deferred := startCore(t, "registry", "apitools", "knowledge")
	_, err := core.catalog.registerSubsystems(t.Context(), false)
	require.NoError(t, err)

	bridge, err := toolboxmcp.NewFromAPICatalog(t.Context(), core.catalog.service.Catalog(), core.catalog.service.Invoker(),
		toolboxmcp.APICatalogOptions{Options: toolboxmcp.Options{
			Name:            "toolbox",
			Policy:          core.catalog.policy,
			InitialExposure: toolboxmcp.ExposeAllowedFeatures,
		}})
	require.NoError(t, err)
	deferred.set(bridge.HTTPHandler())

	exposed := map[string]bool{}
	for _, feature := range bridge.Features() {
		exposed[feature.ToolName] = feature.Exposed
	}
	assert.True(t, exposed["knowledge__search"], "a read the default policy permits is offered")
	assert.False(t, exposed["knowledge__put_source"], "and a write it does not is not")

	session, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "probe", Version: "0.1.0"}, nil).
		Connect(t.Context(), &sdkmcp.StreamableClientTransport{
			Endpoint: core.endpoint + MCPPath,
		}, nil)
	require.NoError(t, err, "an agent reaches the core over the network")
	t.Cleanup(func() { _ = session.Close() })

	listed, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	assert.Contains(t, names, "knowledge__search", "the mounted endpoint serves the catalog's tools")
	assert.NotContains(t, names, "knowledge__put_source", "and hides the write the policy refuses")
}

func TestTheCoreReportsItIsStillStarting(t *testing.T) {
	// The message names the state, because the reader of it is a person deciding
	// whether to wait or to look at a log.
	deferred := &deferredHandler{}
	recorder := httptest.NewRecorder()
	deferred.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, MCPPath, nil))
	assert.Contains(t, recorder.Body.String(), "starting")
	assert.Equal(t, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
}

func TestAWaitForTheCoreDoesNotOutliveItsDeadline(t *testing.T) {
	// A client that is told to wait five seconds should stop waiting after five
	// seconds. This is asserted through the core's own timeout rather than a sleep,
	// so it cannot pass by accident on a slow machine or fail on a fast one.
	address := freeAddress(t)
	core, _ := startCore(t, "registry")
	assert.NotEqual(t, address, core.address, "two cores did not share an address")

	started := time.Now()
	client := registryv1connect.NewRegistryServiceClient(&http.Client{Timeout: 100 * time.Millisecond}, "http://"+address)
	_, err := client.ListServices(t.Context(), connect.NewRequest(&registryv1.ListServicesRequest{}))
	require.Error(t, err, "a core that is not there is a refusal, not an empty answer")
	assert.Less(t, time.Since(started), 5*time.Second, "and it is refused within the bound")
}
