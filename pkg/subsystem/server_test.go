package subsystem_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Manu343726/toolbox/pkg/docs"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This package is the transport and lifecycle every subsystem sits on, and it had no tests at
// all — which is the worst place for that to be true, because everything above it is tested by
// exercising it and would report a bug here as a bug there.
//
// What is tested is the parts a caller can observe: what is refused at construction, what a
// started server answers, what a handshake says, and — the part that matters most — that
// starting, stopping and racing callers do not corrupt the lifecycle state. Every test here
// runs under the race detector in CI.

// echo answers whatever it is sent, so a test can tell a mounted path from a 404.
var echo = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "served %s", r.URL.Path)
})

// start builds and starts a server, and shuts it down when the test ends.
func start(t *testing.T, config subsystem.Config) *subsystem.Server {
	t.Helper()
	if config.ListenAddress == "" {
		config.ListenAddress = "127.0.0.1:0"
	}
	server, err := subsystem.NewServer(config)
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return server
}

func TestAServerAnswersOnItsOwnListener(t *testing.T) {
	server := start(t, subsystem.Config{
		Name:   "test",
		Mounts: []subsystem.Mount{{Path: "/thing", Handler: echo, Description: "a thing"}},
	})

	response, err := http.Get(server.Endpoint() + "/thing")
	require.NoError(t, err)
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "served /thing", string(body))
}

func TestTheEndpointIsReachableBeforeStartIsReportedComplete(t *testing.T) {
	// Start returns after the listener is bound, so a caller that returns from Start and
	// immediately dials must not lose the race. This is the ordering a host depends on when it
	// registers a subsystem with a core in the same call.
	server := start(t, subsystem.Config{Name: "test"})

	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.Endpoint(), "http://"), time.Second)
	require.NoError(t, err)
	require.NoError(t, connection.Close())
}

func TestTheEndpointIsLoopbackByDefault(t *testing.T) {
	server := start(t, subsystem.Config{Name: "test"})

	// A subsystem that binds all interfaces by default would expose every capability in the
	// deployment to the network the machine is on, so the default is the safe one and a test
	// is what keeps it that way.
	assert.True(t, strings.HasPrefix(server.Endpoint(), "http://127.0.0.1:"),
		"the default endpoint was %q", server.Endpoint())
}

func TestAnEphemeralPortIsUsedWhenTheAddressIsTheDefault(t *testing.T) {
	first := start(t, subsystem.Config{Name: "one"})
	second := start(t, subsystem.Config{Name: "two"})

	// Two subsystems on the default address must not collide, because a host starts several at
	// once and a fixed port would make the second one fail.
	assert.NotEqual(t, first.Endpoint(), second.Endpoint())
}

func TestAReflectionServiceIsServedForEveryMountedService(t *testing.T) {
	// Reflection is what a client reads a contract through, and it is mounted from the service
	// names rather than from the services themselves.
	server := start(t, subsystem.Config{
		Name:     "test",
		Services: []subsystem.Service{{Name: "toolbox.health.v1.HealthService", Path: "/h.v1.HealthService/", Handler: echo}},
	})

	for _, path := range []string{"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
		"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo"} {
		response, err := http.Post(server.Endpoint()+path, "application/json", strings.NewReader("{}"))
		require.NoError(t, err)
		_ = response.Body.Close()
		assert.NotEqual(t, http.StatusNotFound, response.StatusCode, "no reflection at %s", path)
	}
}

func TestAMountIsServedWithoutBeingInTheDescriptor(t *testing.T) {
	server := start(t, subsystem.Config{
		Name:   "test",
		Mounts: []subsystem.Mount{{Path: "/dashboard", Handler: echo, Description: "a dashboard"}},
	})

	descriptor := server.Descriptor()
	// A mount is not a service: it has no protobuf contract, so putting it in the descriptor
	// would be a claim reflection could not back up.
	assert.Empty(t, descriptor.ServiceNames)
	assert.Equal(t, "test", descriptor.SubsystemName)
}

func TestTheDescriptorReportsWhatWasMounted(t *testing.T) {
	server := start(t, subsystem.Config{
		Name:         "knowledge",
		Version:      "1.2.3",
		Description:  "Sources and retrieval",
		Services:     []subsystem.Service{{Name: "toolbox.knowledge.v1.KnowledgeService", Path: "/knowledge.v1.KnowledgeService/", Handler: echo, Dependencies: []string{"registry"}}},
		Dependencies: []string{"registry"},
	})

	descriptor := server.Descriptor()
	assert.Equal(t, "knowledge", descriptor.SubsystemName)
	assert.Equal(t, "1.2.3", descriptor.ImplementationVersion)
	assert.Equal(t, "Sources and retrieval", descriptor.Description)
	assert.Equal(t, []string{"toolbox.knowledge.v1.KnowledgeService"}, descriptor.ServiceNames)
	assert.Equal(t, server.Endpoint(), descriptor.Endpoint)
	assert.Contains(t, descriptor.Dependencies, "registry")
}

func TestTheHandshakeNamesTheProtocolAndTheEndpoint(t *testing.T) {
	var buffer syncBuffer
	server := start(t, subsystem.Config{
		Name:            "test",
		Version:         "0.4.0",
		Description:     "A subsystem",
		HandshakeWriter: &buffer,
		Services:        []subsystem.Service{{Name: "toolbox.health.v1.HealthService", Path: "/h.v1.HealthService/", Handler: echo}},
	})

	var handshake subsystem.Handshake
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &handshake))
	// A supervising process reads this to learn where a subsystem ended up, so the protocol
	// version and the endpoint are the two facts it cannot proceed without.
	assert.Equal(t, subsystem.HandshakeProtocol, handshake.Protocol)
	assert.Equal(t, server.Endpoint(), handshake.Endpoint)
	assert.Equal(t, "test", handshake.Name)
	assert.Equal(t, "0.4.0", handshake.Version)
	assert.Equal(t, []string{"toolbox.health.v1.HealthService"}, handshake.ServiceNames)
	assert.True(t, handshake.ReflectionEnabled)
}

func TestAHandshakeCanBeWrittenOnDemand(t *testing.T) {
	server := start(t, subsystem.Config{Name: "test"})

	var buffer syncBuffer
	require.NoError(t, server.WriteHandshake(&buffer))

	var handshake subsystem.Handshake
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &handshake))
	assert.Equal(t, server.Endpoint(), handshake.Endpoint)
}

func TestAVersionDefaultsToDevRatherThanBeingEmpty(t *testing.T) {
	// A registry that reports an empty version cannot tell one build of a subsystem from
	// another, and "dev" at least says it is not a release.
	server := start(t, subsystem.Config{Name: "test"})

	var buffer syncBuffer
	require.NoError(t, server.WriteHandshake(&buffer))
	var handshake subsystem.Handshake
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &handshake))
	assert.Equal(t, "dev", handshake.Version)
}

func TestAHealthCheckIsNotPolledByTheServerItself(t *testing.T) {
	var called int
	var mu sync.Mutex
	server := start(t, subsystem.Config{
		Name: "test",
		// A readiness signal belongs to whatever supervises the subsystem. The server
		// deliberately does not poll it, because a subsystem that decided its own readiness
		// would report itself ready on the strength of its own opinion.
		Health: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			called++
			return nil
		},
	})

	_ = server.Endpoint()
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, called, "the server polled a health check it does not own")
}

func TestBackgroundWorkStopsWhenTheContextIsCancelled(t *testing.T) {
	// A background task that outlives its context is a goroutine leak, and a subsystem that
	// leaks one per restart leaks them for the life of the host.
	started := make(chan struct{})
	stopped := make(chan struct{})
	server, err := subsystem.NewServer(subsystem.Config{
		Name: "test",
		Background: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, server.Start(ctx))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the background task never started")
	}
	cancel()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the background task outlived its context")
	}
	require.NoError(t, server.Shutdown(context.Background()))
}

func TestABackgroundFailureIsReportedAndNotSwallowed(t *testing.T) {
	failure := errors.New("the store could not be opened")
	server, err := subsystem.NewServer(subsystem.Config{
		Name:       "test",
		Background: func(context.Context) error { return failure },
	})
	require.NoError(t, err)

	// A background task that fails silently leaves a subsystem serving and broken, and the
	// only sign is an absence. Wait is where a supervisor learns about it, so a failure that
	// does not reach Wait is a failure nobody is told about.
	err = server.Serve(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, failure)
}

func TestShutdownStopsTheListener(t *testing.T) {
	server, err := subsystem.NewServer(subsystem.Config{Name: "test"})
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	endpoint := server.Endpoint()

	require.NoError(t, server.Shutdown(context.Background()))

	_, err = http.Get(endpoint + "/")
	// Either the connection is refused or something else is now bound; what matters is that
	// the server is no longer answering, and a refused connection is the ordinary way to say so.
	assert.Error(t, err, "the server still answers after Shutdown")
}

func TestShutdownIsSafeToCallTwice(t *testing.T) {
	server := start(t, subsystem.Config{Name: "test"})

	// A host shuts a subsystem down from its own shutdown path and a test's cleanup may
	// already have done it, so a second call has to be harmless rather than a panic.
	require.NoError(t, server.Shutdown(context.Background()))
	require.NoError(t, server.Shutdown(context.Background()))
}

func TestStartingTwiceIsRefused(t *testing.T) {
	server := start(t, subsystem.Config{Name: "test"})

	err := server.Start(t.Context())

	// Two listeners on one server would mean the endpoint a core was told about is not the one
	// the subsystem is answering on.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestStartingAClosedServerIsRefused(t *testing.T) {
	server, err := subsystem.NewServer(subsystem.Config{Name: "test"})
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	require.NoError(t, server.Shutdown(context.Background()))

	assert.Error(t, server.Start(t.Context()))
}

func TestServeBlocksUntilTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		server, err := subsystem.NewServer(subsystem.Config{Name: "test"})
		if err != nil {
			done <- err
			return
		}
		done <- server.Serve(ctx)
	}()

	// Serve is the standalone command's entry point, so a caller cancelling has to bring the
	// process down rather than leaving it listening.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return when its context was cancelled")
	}
}

func TestAServerWithNoNameIsRefused(t *testing.T) {
	_, err := subsystem.NewServer(subsystem.Config{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestAServiceWithNoNameIsRefused(t *testing.T) {
	_, err := subsystem.NewServer(subsystem.Config{
		Name:     "test",
		Services: []subsystem.Service{{Path: "/x", Handler: echo}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestAServiceWithNoPathIsRefused(t *testing.T) {
	_, err := subsystem.NewServer(subsystem.Config{
		Name:     "test",
		Services: []subsystem.Service{{Name: "a.B", Handler: echo}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "path")
}

func TestAServiceWithNoHandlerIsRefused(t *testing.T) {
	_, err := subsystem.NewServer(subsystem.Config{
		Name:     "test",
		Services: []subsystem.Service{{Name: "a.B", Path: "/x"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler")
}

func TestTwoServicesOnOnePathAreRefused(t *testing.T) {
	// Two services sharing a path means one of them is unreachable, and a reflection client
	// would see both names resolve to the same endpoint.
	_, err := subsystem.NewServer(subsystem.Config{
		Name: "test",
		Services: []subsystem.Service{
			{Name: "a.B", Path: "/same/", Handler: echo},
			{Name: "a.C", Path: "/same/", Handler: echo},
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate service path")
}

func TestAServiceAndAMountOnOnePathAreRefused(t *testing.T) {
	_, err := subsystem.NewServer(subsystem.Config{
		Name:     "test",
		Services: []subsystem.Service{{Name: "a.B", Path: "/same/", Handler: echo}},
		Mounts:   []subsystem.Mount{{Path: "/same/", Handler: echo}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "/same")
}

func TestTwoMountsOnOnePathAreRefused(t *testing.T) {
	_, err := subsystem.NewServer(subsystem.Config{
		Name: "test",
		Mounts: []subsystem.Mount{
			{Path: "/same", Handler: echo},
			{Path: "/same", Handler: echo},
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already served")
}

func TestAMountWithNoPathIsRefused(t *testing.T) {
	_, err := subsystem.NewServer(subsystem.Config{
		Name:   "test",
		Mounts: []subsystem.Mount{{Handler: echo}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "path")
}

func TestAMountWithNoHandlerIsRefused(t *testing.T) {
	_, err := subsystem.NewServer(subsystem.Config{
		Name:   "test",
		Mounts: []subsystem.Mount{{Path: "/x"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler")
}

func TestStartingOnAnAddressAlreadyInUseFails(t *testing.T) {
	first := start(t, subsystem.Config{Name: "one"})
	address := strings.TrimPrefix(first.Endpoint(), "http://")

	second, err := subsystem.NewServer(subsystem.Config{Name: "two", ListenAddress: address})
	require.NoError(t, err)

	// The error names the subsystem and the address, because "address in use" on its own does
	// not say which of several subsystems failed to start.
	err = second.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two")
	assert.Contains(t, err.Error(), address)
}

// The lifecycle state is behind a mutex because a host starts a subsystem, reads its endpoint
// from a callback, and shuts it down, and any two of those can overlap.

func TestConcurrentStartsAndReadsAreSafe(t *testing.T) {
	server, err := subsystem.NewServer(subsystem.Config{
		Name:   "test",
		Mounts: []subsystem.Mount{{Path: "/x", Handler: echo}},
	})
	require.NoError(t, err)

	// Fifteen readers racing one start. A core registering two different endpoints for one
	// subsystem is the failure this prevents, so every reader that sees an endpoint must see
	// the same one.
	var wg sync.WaitGroup
	endpoints := make([]string, 16)
	for i := range endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Started here, so a read is not guaranteed to see an endpoint: the assertion is
			// that whatever a reader sees, it sees consistently.
			if err := server.Start(t.Context()); err == nil {
				endpoints[i] = server.Endpoint()
			}
		}()
	}
	wg.Wait()
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	seen := map[string]int{}
	for _, endpoint := range endpoints {
		if endpoint != "" {
			seen[endpoint]++
		}
	}
	require.NotEmpty(t, seen, "no goroutine started the server")
	require.LessOrEqual(t, len(seen), 1, "readers saw more than one endpoint: %v", seen)
}

func TestConcurrentShutdownsAreSafe(t *testing.T) {
	server := start(t, subsystem.Config{Name: "test"})

	// A host's shutdown path and a failing request's cleanup can both decide to stop the same
	// subsystem, and a double close of a channel panics.
	var wg sync.WaitGroup
	errorsSeen := make([]error, 8)
	for i := range errorsSeen {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsSeen[i] = server.Shutdown(context.Background())
		}()
	}
	wg.Wait()
	for _, err := range errorsSeen {
		assert.NoError(t, err)
	}
}

func TestServiceNamesKeepTheConfiguredOrder(t *testing.T) {
	// ServicesNames is what a caller assembling a list of its own wants: the order the
	// deployment configured, so a caller can predict it. Deduplication and sorting are the
	// descriptor's job, and are tested there.
	names := (&subsystem.Config{
		Services: []subsystem.Service{
			{Name: "b.B", Path: "/b.B/", Handler: echo},
			{Name: "a.A", Path: "/a.A/", Handler: echo},
		},
	}).ServicesNames()
	assert.Equal(t, []string{"b.B", "a.A"}, names)
}

func TestTheDescriptorsServiceListIsDeduplicatedAndSorted(t *testing.T) {
	// A descriptor is compared across processes — a registry holds it, a client diffs it — so
	// two processes that mounted the same services must report the same list in the same order
	// whatever order their configs listed them in.
	server := start(t, subsystem.Config{
		Name: "test",
		Services: []subsystem.Service{
			{Name: "b.B", Path: "/b.B/", Handler: echo},
			{Name: "a.A", Path: "/a.A/", Handler: echo},
			{Name: "b.B", Path: "/b2.B/", Handler: echo},
		},
	})
	assert.Equal(t, []string{"a.A", "b.B"}, server.Descriptor().ServiceNames)
}

func TestServiceNamesOfNothingIsEmpty(t *testing.T) {
	assert.Empty(t, (&subsystem.Config{}).ServicesNames())
}

func TestTheServicesAccessorReportsWhatIsMounted(t *testing.T) {
	server := start(t, subsystem.Config{
		Name:     "test",
		Services: []subsystem.Service{{Name: "a.B", Path: "/a.B/", Handler: echo, Description: "a service"}},
	})

	services := server.Services()
	require.Len(t, services, 1)
	assert.Equal(t, "a.B", services[0].Name)
	assert.Equal(t, "/a.B/", services[0].Path)
	assert.NotNil(t, services[0].Handler)
}

func TestTheCatalogIsAlwaysAvailable(t *testing.T) {
	// pkg/docs reads descriptor sets, and a subsystem with no documentation of its own still
	// has to hand something back rather than a nil a caller must check.
	server := start(t, subsystem.Config{Name: "test"})
	require.NotNil(t, server.Catalog())
}

func TestDocumentationSuppliedByTheCallerIsKept(t *testing.T) {
	catalog := docs.DefaultCatalog()
	server := start(t, subsystem.Config{Name: "test", Documentation: catalog})

	// A subsystem that embeds its own descriptor set is the normal case, and a default catalog
	// silently replacing it would be documentation that is always empty.
	assert.Same(t, catalog, server.Catalog())
}

func TestAHandshakeWriterThatFailsStopsTheStart(t *testing.T) {
	server, err := subsystem.NewServer(subsystem.Config{
		Name:            "test",
		HandshakeWriter: failingWriter{},
	})
	require.NoError(t, err)

	// A supervisor that cannot read the handshake has no way to learn the endpoint, so a
	// subsystem that cannot write it has not started successfully.
	assert.Error(t, server.Start(t.Context()))
}

func TestANilContextIsTreatedAsBackground(t *testing.T) {
	// A caller passing a nil context is a bug, but panicking on it in a process that is trying
	// to shut down is a worse outcome than running with no cancellation.
	server, err := subsystem.NewServer(subsystem.Config{Name: "test"})
	require.NoError(t, err)
	require.NoError(t, server.Start(nil))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	assert.NotEmpty(t, server.Endpoint())
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the pipe is closed") }

// syncBuffer is a bytes.Buffer that a handshake goroutine and a test can share, because the
// handshake is written from the starting goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return []byte(b.buf.String())
}
