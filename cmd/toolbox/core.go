package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Manu343726/toolbox/pkg/config"
	registrydir "github.com/Manu343726/toolbox/subsystems/registry"
	"github.com/spf13/cobra"
)

// How a client reaches the core, and what it does when the core is not there.
//
// ADR-0011 says the launch mode is a statement about who owns the state, so the three modes
// differ exactly where that ownership shows: in what happens when no core answers. The rest
// of the resolution — which address, which endpoint, whether the address is one this
// machine can serve — is the same in all three.

// reachCore returns a resolver for the configured core, or nil when this process should
// serve the call itself.
//
// A nil directory with a nil error means "serve locally", and it is a different answer from
// an error: an error is a deployment that cannot do what it was asked, and a caller who
// cannot tell those apart is back to guessing.
func reachCore(ctx context.Context, resolved config.Config, out io.Writer) (*registrydir.Directory, error) {
	switch resolved.Launch {
	case config.LaunchDisabled:
		// This installation runs no daemon and requires none. Peers that are not in this
		// process are still found through the configured address, so the directory is
		// opened when one answers — and a deployment that named no address has already been
		// refused by the configuration, so there is nothing to fall back to here.
		directory, err := openCoreDirectory(resolved.Daemon.Addr())
		if err != nil {
			fmt.Fprintf(out,
				"toolbox: no core at %s; serving this call from this process (%v)\n",
				resolved.Daemon.Addr(), err)
			return nil, nil
		}
		return directory, nil

	case config.LaunchExplicit:
		// The lifecycle belongs to an init system. Starting one here would fight it, and
		// serving locally would hide a stopped service behind a working command — so a core
		// that does not answer stops the command.
		directory, err := openCoreDirectory(resolved.Daemon.Addr())
		if err != nil {
			return nil, fmt.Errorf(
				"the core at %s did not answer, and daemon.launch is %q so this command will not start one "+
					"and will not serve the call from this process instead: %w",
				resolved.Daemon.Addr(), config.LaunchExplicit, err)
		}
		return directory, nil

	default:
		return reachCoreAutomatically(ctx, resolved, out)
	}
}

// reachCoreAutomatically is the tmux model: no core answering means one is started.
//
// The daemon is detached so it outlives the command that started it, and the caller waits
// for the *address* rather than for its own child — so two clients starting at the same
// moment do not have to coordinate. One binds the port, the other's daemon exits because it
// cannot, and both proceed against the daemon that won.
//
// A daemon is started only for an address on this machine. An address elsewhere is
// somebody else's daemon, and starting one here would put a second core on a network
// pretending to be the first.
func reachCoreAutomatically(ctx context.Context, resolved config.Config, out io.Writer) (*registrydir.Directory, error) {
	address := resolved.Daemon.Addr()

	directory, err := openCoreDirectory(address)
	if err == nil {
		return directory, nil
	}
	if !isLocalAddress(resolved.Daemon) {
		fmt.Fprintf(out,
			"toolbox: no core at %s; it is not on this machine, so none is started here; "+
				"serving this call from this process instead (%v)\n", address, err)
		return nil, nil
	}

	fmt.Fprintf(out, "toolbox: no core at %s; starting one\n", address)
	if startErr := spawnDaemon(ctx, resolved); startErr != nil {
		return nil, fmt.Errorf("the core at %s did not answer and one could not be started: %w", address, startErr)
	}
	started, waitErr := waitForCore(ctx, address, out)
	if waitErr != nil {
		return nil, waitErr
	}
	if !started {
		return nil, fmt.Errorf(
			"the core at %s did not answer and did not come up; start it yourself with `toolbox daemon`", address)
	}
	directory, err = openCoreDirectory(address)
	if err != nil {
		return nil, fmt.Errorf("the core at %s came up but would not answer: %w", address, err)
	}
	return directory, nil
}

// coreStartBudget is how long a client waits for a daemon it started to answer.
//
// Long enough for a daemon to start every subsystem it was asked for, which is the work
// between binding the registry and answering a directory read. It is a variable rather than
// a constant so a test can shorten it: a suite that waits twenty seconds to observe a
// failure is a suite nobody runs, and the behaviour under test is the decision to start one,
// not how long the decision takes.
var coreStartBudget = 20 * time.Second

// waitForCore polls an address until a core answers or the bound passes.
//
// Bounded because a client that cannot reach a core should be told quickly rather than
// waiting on it, and a caller who asked for a deployment is better served by an answer than
// by a hang.
func waitForCore(ctx context.Context, address string, out io.Writer) (bool, error) {
	const attempt = 100 * time.Millisecond
	deadline := time.Now().Add(coreStartBudget)
	for {
		if directory, err := openCoreDirectory(address); err == nil {
			directory.Close()
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(attempt):
		}
	}
}

// spawnDaemon starts a detached daemon serving the same resolved configuration.
//
// Detached, with its own session and its output to a log file, because it has to outlive the
// command that started it: a child that died with its parent would make the "auto" mode a
// daemon that only exists while something is invoking a command, which is not a daemon.
//
// The configuration is passed on explicitly rather than inherited, because the child is
// started with a different working directory in some environments and a project file
// found by walking up would then be a different one.
func spawnDaemon(ctx context.Context, resolved config.Config) error {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	args := []string{"daemon",
		"--daemon-host", resolved.Daemon.Host,
		"--daemon-port", fmt.Sprintf("%d", resolved.Daemon.Port),
		"--mcp-host", resolved.MCP.Host,
		"--mcp-port", fmt.Sprintf("%d", resolved.MCP.Port),
		"--launch", string(config.LaunchExplicit),
	}
	if resolved.PolicyPath != "" {
		args = append(args, "--policy", resolved.PolicyPath)
	}
	if resolved.Path != "" {
		// The file is named rather than inherited, so the daemon reads the one this client
		// read even if it is started somewhere else.
		args = append(args, "--config", resolved.Path)
	}

	logPath, err := daemonLogPath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open the daemon log at %s: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()

	command := exec.Command(self, args...)
	command.Stdout = logFile
	command.Stderr = logFile
	command.Stdin = nil
	// A new session, so the daemon is not in the terminal's process group and a ctrl-c at
	// the command that started it does not take the core down with it.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// The environment is kept, including any TOOLBOX_* that configured this client, because
	// the daemon is meant to be the same deployment.
	command.Env = os.Environ()
	command.Dir = ""
	_ = ctx

	if err := command.Start(); err != nil {
		return fmt.Errorf("start %s: %w", self, err)
	}
	// Released rather than waited on: the daemon is the point, and this process is about to
	// use it. A zombie would be a tidiness problem, and a reaper goroutine that outlived
	// every command in a shell session would be worse.
	go func() { _ = command.Wait() }()
	return nil
}

// daemonLogPath is where a daemon this client started writes its output, and creates the
// directory it needs.
//
// The directory is created rather than assumed. A cache directory is exactly the kind of
// thing a machine has cleaned out, and a client whose "auto" mode could never start a
// daemon because a log directory was missing would report the failure as "the core did not
// answer and one could not be started" — which sends a reader to the network rather than
// to their own cache.
func daemonLogPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "toolbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return filepath.Join(dir, "daemon.log"), nil
}

// isLocalAddress reports whether an address is one this machine can serve a daemon on.
//
// Loopback and an unspecified bind address are local because they name this machine. A
// resolvable hostname is not, without resolving it: a deployment that names a host is
// naming a machine, and the question of whether that machine is this one is not answered by
// comparing strings.
func isLocalAddress(address config.Listen) bool {
	host := strings.TrimSpace(address.Host)
	switch strings.ToLower(host) {
	case "", "localhost", "0.0.0.0", "::", "[::]":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// openCoreDirectory connects to a core's registry and follows its changes.
//
// A directory that cannot sync is an error rather than an empty directory: a directory that
// has never synced knows nothing, and would report every service as missing — which reads as
// "this deployment has no operations" rather than as "the core is down".
func openCoreDirectory(endpoint string) (*registrydir.Directory, error) {
	address := strings.TrimSpace(endpoint)
	if address == "" {
		return nil, fmt.Errorf("no core address is configured")
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	directory, err := registrydir.NewDirectory(registrydir.DirectoryOptions{
		Endpoint: address,
		// A directory used to decide whether a core exists must not wait long to find out:
		// the answer is what a command is about to branch on.
		SyncTimeout: 2 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if err := directory.Start(context.Background()); err != nil {
		return nil, err
	}
	return directory, nil
}

// ensureCore is what the Model Context Protocol command does before reflecting, so that a
// client asked for the deployment's surface gets the deployment's surface rather than a
// private copy of it.
func ensureCore(cmd *cobra.Command) (*registrydir.Directory, error) {
	resolved, err := resolveConfig(cmd)
	if err != nil {
		return nil, err
	}
	reportConfig(cmd, resolved)
	return reachCore(cmd.Context(), resolved, cmd.ErrOrStderr())
}

// probeCore reports whether a core answers at an address, without keeping a directory.
func probeCore(ctx context.Context, address string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+address+"/", nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	// Any answer at all means something is listening. A 404 is the core saying it serves no
	// root path, which is not the same as nothing being there.
	return true
}
