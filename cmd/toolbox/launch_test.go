package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/config"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	"github.com/Manu343726/toolbox/subsystems/registry/registryv1/registryv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The launch mode is a statement about who owns the state, so it decides what happens in
// exactly one moment: when no core answers. These tests hold that moment for all three
// modes, against a real address that nothing is listening on, and they hold the two things
// that follow from it — whether the daemon subcommand is available, and whether the
// configuration is complete.

// deadAddress is an address on loopback that no core is bound to. Port 1 requires privileges
// to bind, so nothing else can be holding it, which is what makes it a reliable "no core
// answering" without depending on what else the machine happens to be running.
const deadAddress = "127.0.0.1:1"

// A deployment that manages its own core refuses the call rather than serving it from this
// process. A write that cannot reach the deployment must fail; a write that lands in a
// store nobody is managing reports success and is gone.
func TestExplicitRefusesRatherThanServingLocally(t *testing.T) {
	_, _, err := runRoot(t, "health", "check",
		"--launch", "explicit", "--daemon-host", "127.0.0.1", "--daemon-port", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "will not start one")
	assert.Contains(t, err.Error(), "will not serve the call from this process instead",
		"and says the other half of the mode, because a reader who sees one without the other "+
			"would think it fell back")
}

// A deployment with the daemon disabled runs nothing, so a missing core is not a problem to
// report as a failure. It is reported, and the call is served here.
func TestDisabledServesLocallyWhenNoCoreAnswers(t *testing.T) {
	_, stderr, err := runRoot(t, "health", "check",
		"--launch", "disabled", "--daemon-host", "127.0.0.1", "--daemon-port", "1")
	require.NoError(t, err, "a deployment that runs no core does not need one to work")
	assert.Contains(t, stderr, "no core at 127.0.0.1:1")
	assert.Contains(t, stderr, "from this process")
}

// The default mode starts one. What is asserted is that it says so, and names the address:
// a caller who did not know a daemon was being started would not know where the state they
// just wrote went.
func TestAutoAnnouncesThatItIsStartingACore(t *testing.T) {
	previous := coreStartBudget
	coreStartBudget = 150 * time.Millisecond
	t.Cleanup(func() { coreStartBudget = previous })

	_, stderr, _ := runRoot(t, "health", "check",
		"--launch", "auto", "--daemon-host", "127.0.0.1", "--daemon-port", "1")
	assert.Contains(t, stderr, "no core at 127.0.0.1:1")
	assert.Contains(t, stderr, "starting one")
}

// A daemon is started only for an address on this machine. An address elsewhere is somebody
// else's daemon, and starting one here would put a second core on a network pretending to be
// the first — so the fallback and the reason are reported instead.
func TestAutoDoesNotStartACoreForSomebodyElsesAddress(t *testing.T) {
	_, stderr, err := runRoot(t, "health", "check",
		"--launch", "auto", "--daemon-host", "core.invalid", "--daemon-port", "9180")
	require.NoError(t, err, "a remote core that is down is not a reason to refuse")
	assert.Contains(t, stderr, "not on this machine",
		"and says why nothing was started here")
	assert.NotContains(t, stderr, "starting one")
}

// The daemon subcommand is refused by a deployment that has disabled it. Refused rather than
// absent, because somebody who typed it is asking for something this installation has said
// it does not do, and the answer is the reason.
func TestTheDaemonSubcommandIsRefusedWhenDisabled(t *testing.T) {
	_, _, err := runRoot(t, "daemon", "--launch", "disabled",
		"--daemon-host", "127.0.0.1", "--daemon-port", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
	assert.Contains(t, err.Error(), "auto", "and names the modes that would allow one")
}

// The refusal happens before anything binds, so a deployment that has said it runs no core
// cannot end up running one.
func TestADisabledDeploymentNeverBinds(t *testing.T) {
	address := freeAddress(t)
	_, _, err := runRoot(t, "daemon", "--launch", "disabled",
		"--daemon-host", "127.0.0.1", "--daemon-port", portFlag(t, address))
	require.Error(t, err)

	// Nothing is listening, which is the observable consequence of refusing before binding.
	assert.True(t, nothingListening(t, address), "and the address was never taken")
}

// The daemon binds the two addresses separately when they differ, and each surface answers
// only on its own. A deployment that exposes agent tools to a network wants that on an
// address it can firewall apart from an internal directory, and one address cannot be given
// two policies.
func TestTheDaemonBindsTwoAddressesWhenTheyDiffer(t *testing.T) {
	core := freeAddress(t)
	endpoint := freeAddress(t)

	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go func() {
		_, _, _ = runRootInContext(t, ctx, "daemon",
			"--component", "registry",
			"--daemon-host", "127.0.0.1", "--daemon-port", portFlag(t, core),
			"--mcp-host", "127.0.0.1", "--mcp-port", portFlag(t, endpoint),
			"--launch", "explicit")
	}()

	// The daemon address serves the directory.
	// The two surfaces come up at different moments, and the difference is real rather than
	// incidental: the daemon binds its directory as soon as the registry is ready, while the
	// protocol endpoint is built from the catalog the seeder fills, which is work that
	// happens afterwards. So each is waited for on its own terms — asking the protocol before
	// it is ready is answered with "still starting", not with a tool list.
	client := registryv1connect.NewRegistryServiceClient(http.DefaultClient, "http://"+core)
	requireEventually(t, func() bool {
		_, err := client.ListServices(t.Context(), listServicesRequest())
		return err == nil
	}, "the daemon answers its own address")
	response := requireEndpoint(t, endpoint)
	assert.Contains(t, response, "serverInfo",
		"the endpoint answers the protocol on its own address")

	// Asking the directory for the protocol, and the protocol for the directory, finds
	// neither — which is the point of two addresses rather than one address twice.
	status := postStatus(t, core, MCPPath)
	assert.Equal(t, http.StatusNotFound, status,
		"the protocol is not on the directory's address")
	status = postStatus(t, endpoint, "/toolbox.registry.v1.RegistryService/ListServices")
	assert.Equal(t, http.StatusNotFound, status,
		"and the directory is not on the protocol's address")
}

// When the two addresses are the same the deployment is one port, which is the default and
// the reason the two are configurable separately rather than separately required.
func TestTheSameAddressGivesOnePort(t *testing.T) {
	address := freeAddress(t)

	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go func() {
		_, _, _ = runRootInContext(t, ctx, "daemon",
			"--component", "registry",
			"--daemon-host", "127.0.0.1", "--daemon-port", portFlag(t, address),
			"--mcp-host", "127.0.0.1", "--mcp-port", portFlag(t, address),
			"--launch", "explicit")
	}()

	client := registryv1connect.NewRegistryServiceClient(http.DefaultClient, "http://"+address)
	requireEventually(t, func() bool {
		_, err := client.ListServices(t.Context(), listServicesRequest())
		return err == nil
	}, "the daemon answers")

	response := requireEndpoint(t, address)
	assert.Contains(t, response, "serverInfo", "and the protocol is on the same port")

	status := postStatus(t, address, "/toolbox.registry.v1.RegistryService/ListServices")
	assert.NotEqual(t, http.StatusNotFound, status, "one port carries both")
}

// A configuration file is read by the daemon too, and it reports the same values the client
// would, so the two agree about the deployment by construction rather than by coincidence.
func TestTheDaemonReportsItsResolvedConfiguration(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeFile(t, dir, "daemon:\n  port: 1\n  launch: explicit\nmcp:\n  port: 2\n"))

	// The ports here are the two this test has always used, and whether the daemon
	// goes on to serve depends on who is running this: an unprivileged process cannot
	// bind port 1 and the command fails, while a privileged one can and the daemon
	// serves until interrupted. Rather than rely on either, the report is read as
	// soon as it is written and the daemon is then asked to stop — so the test is the
	// same assertion on a developer's machine and on a root runner in a container.
	//
	// The report goes to stderr through a locked buffer, because it is read from
	// another goroutine than the one running the command.
	reported := &lockedBuffer{}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runRootInto(t, ctx, reported, reported, "daemon", "--config", fileIn(dir), "--component", "registry")
	}()
	requireEventually(t, func() bool {
		return strings.Contains(reported.String(), "launch is")
	}, "the daemon never reported its resolved configuration")
	stop()
	<-done

	stderr := reported.String()
	assert.Contains(t, stderr, "daemon at 127.0.0.1:1")
	assert.Contains(t, stderr, "Model Context Protocol at 127.0.0.1:2")
	assert.Contains(t, stderr, "launch is explicit")
	assert.Contains(t, stderr, "config file", "and where each value came from")
}

// lockedBuffer collects command output for a test reading it from another goroutine.
//
// The bytes.Buffer the command helpers use is not safe to read while the command is
// still writing, and the tests that need to read early are exactly the ones whose
// command may go on to serve. A mutex is the whole of it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The launch mode reaches the daemon as well as the client, so a daemon started by a client
// does not itself try to start one.
func TestTheDaemonDoesNotTryToStartItself(t *testing.T) {
	// The spawned daemon is passed "explicit" precisely so this holds. The observable
	// consequence is that a daemon whose own address is free binds it and says so, rather
	// than reporting that it could not reach a core and starting another.
	address := freeAddress(t)
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go func() {
		_, _, _ = runRootInContext(t, ctx, "daemon",
			"--component", "registry",
			"--daemon-host", "127.0.0.1", "--daemon-port", portFlag(t, address),
			"--launch", "explicit")
	}()
	client := registryv1connect.NewRegistryServiceClient(http.DefaultClient, "http://"+address)
	requireEventually(t, func() bool {
		_, err := client.ListServices(t.Context(), listServicesRequest())
		return err == nil
	}, "the daemon bound its own address without looking for another core")
}

// portFlag renders a reserved port for a flag, which takes a string rather than an int.
func portFlag(t *testing.T, address string) string {
	t.Helper()
	return fmt.Sprintf("%d", portOf(t, address))
}

func listServicesRequest() *connect.Request[registryv1.ListServicesRequest] {
	return connect.NewRequest(&registryv1.ListServicesRequest{})
}

func postMCPInitialize(address string) (string, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	request, err := http.NewRequest(http.MethodPost, "http://"+address+MCPPath, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	// Both media types, because the protocol's streamable transport requires a client to be
	// willing to take either: one for a single response, one for the stream that follows. A
	// request naming only one is refused, which is the endpoint being strict rather than
	// broken.
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	text := string(raw)
	// A streamable response may arrive as one event per "data:" line rather than as a bare
	// object, so the first one is taken rather than the whole body.
	for _, line := range strings.Split(text, "\n") {
		if payload, found := strings.CutPrefix(strings.TrimSpace(line), "data:"); found {
			text = strings.TrimSpace(payload)
			break
		}
	}
	// Two shapes are correct protocol here and both are answered in practice: a bare JSON
	// object, and — when the server is configured to answer with JSON rather than a stream —
	// a JSON string containing the object. A test that assumed one would fail against a
	// perfectly good server, so both are read.
	if decoded, ok := decodeJSONObject(text); ok {
		encoded, _ := json.Marshal(decoded)
		return string(encoded), nil
	}
	var wrapped string
	if err := json.Unmarshal([]byte(text), &wrapped); err == nil {
		if decoded, ok := decodeJSONObject(wrapped); ok {
			encoded, _ := json.Marshal(decoded)
			return string(encoded), nil
		}
	}
	return text, fmt.Errorf("the endpoint answered with neither a JSON object nor an event: %.200q", text)
}

// decodeJSONObject parses text as a JSON object, reporting whether it was one.
func decodeJSONObject(text string) (map[string]any, bool) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

// requireEndpoint waits for a Model Context Protocol endpoint to answer and returns its
// initialize response.
//
// It waits rather than asking once, because an endpoint that is not built yet answers "still
// starting" — which is a correct answer to a request that arrived too early, and not a
// failure. A test that asked once would be testing when it ran rather than what it built.
func requireEndpoint(t *testing.T, address string) string {
	t.Helper()
	var response string
	requireEventually(t, func() bool {
		answered, err := postMCPInitialize(address)
		if err != nil {
			return false
		}
		response = answered
		return true
	}, "the Model Context Protocol endpoint answers at "+address)
	return response
}

// postStatus asks an address a path and reports the status, which is how a test sees which
// surface a port is carrying without depending on either one's success.
func postStatus(t *testing.T, address, path string) int {
	t.Helper()
	response, err := http.Post("http://"+address+path, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("post %s%s: %v", address, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode
}

// writeFile puts a configuration file where a project would keep one.
func writeFile(t *testing.T, dir, contents string) error {
	t.Helper()
	path := filepath.Join(dir, config.ProjectDir, config.FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents), 0o600)
}

func fileIn(dir string) string {
	return filepath.Join(dir, config.ProjectDir, config.FileName)
}

// requireEventually waits for a condition, so a test can assert that something came up
// without sleeping for a fixed time and hoping. A fixed sleep makes a suite either slow or
// flaky, and both are worse than a bound with a message.
func requireEventually(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", message)
}
