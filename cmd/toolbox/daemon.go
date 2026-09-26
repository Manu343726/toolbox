package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/host"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/spf13/cobra"
)

// The daemon is the same core the CLI runs in process, kept alive and reachable at
// one address. Two kinds of client use that address, and they are why it is worth
// having rather than just running the process again:
//
//   - a subsystem that did not start inside the daemon registers with it, so
//     membership is a decision a deployment makes rather than a list the binary was
//     compiled with;
//   - an agent connects to the MCP endpoint over the network, instead of launching a
//     subprocess and paying a start-up per session.
//
// The core is the registry: it is what a subsystem registers with, and it owns the
// address. The MCP endpoint is mounted on the registry's own server, because the MCP
// handler is not a protobuf service and putting it in reflection would be a lie — and
// because one address is the whole point.

// MCPPath is where a daemon serves the Model Context Protocol.
const MCPPath = "/mcp"

// deferredHandler is a mount that answers once something is ready.
//
// The MCP surface is built after the host starts, because it reads the catalog the
// seeder fills, and the mount has to exist at composition time, before the host
// starts. Until the handler is set the mount says so rather than serving an empty
// one, so a request that arrives too early is a diagnosable refusal and not a silent
// empty tool list.
type deferredHandler struct{ handler atomic.Pointer[http.Handler] }

// set installs the handler. A later call replaces an earlier one.
func (d *deferredHandler) set(handler http.Handler) {
	if handler != nil {
		d.handler.Store(&handler)
	}
}

// ServeHTTP implements http.Handler.
func (d *deferredHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler := d.handler.Load()
	if handler == nil {
		http.Error(w, "the core is still starting; its Model Context Protocol endpoint is not ready", http.StatusServiceUnavailable)
		return
	}
	(*handler).ServeHTTP(w, r)
}

// runDaemon runs the core until it is interrupted.
//
// A deployment whose launch mode is "disabled" cannot run one, and the subcommand is
// refused rather than hidden: somebody who typed it is asking for something this
// installation has said it does not do, and the answer is the reason, not an absent command.
func runDaemon(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveConfig(cmd)
	if err != nil {
		return err
	}
	if resolved.Launch == config.LaunchDisabled {
		return fmt.Errorf(
			"daemon.launch is %q, so this installation does not run a daemon: "+
				"set it to %q or %q to have one", config.LaunchDisabled, config.LaunchAuto, config.LaunchExplicit)
	}
	reportConfig(cmd, resolved)
	out := cmd.ErrOrStderr()

	components, err := cmd.Flags().GetStringSlice("component")
	if err != nil {
		return err
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}
	if !all && len(components) == 0 {
		all = true
	}

	// The core is the registry, and it owns the daemon address. Every other subsystem keeps
	// an ephemeral loopback port: a subsystem this process started needs no address to be
	// found at, and giving one would imply it could be found by a peer.
	//
	// The Model Context Protocol endpoint binds an address of its own. When that address is
	// the daemon's, the endpoint is mounted on the registry's server and the deployment is
	// one port; when it is not, it gets a second listener. Either way it is a mount rather
	// than a service, because it is not a protobuf contract and putting it in reflection
	// would be a lie.
	mcp := &deferredHandler{}
	sameAddress := config.SameAddress(resolved.Daemon, resolved.MCP)
	var mounts []subsystem.Mount
	if sameAddress {
		mounts = []subsystem.Mount{{
			Path:        MCPPath,
			Handler:     mcp,
			Description: "The core's Model Context Protocol endpoint.",
		}}
	}
	h, catalog, err := buildHost(resolved, resolved.Daemon.Addr(), mounts...)
	if err != nil {
		return err
	}
	if !all {
		if err := h.Select(components...); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := h.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = h.Shutdown(context.Background()) }()

	if err := registerStartedSubsystems(ctx, h, true); err != nil {
		return err
	}
	// Everything the daemon started is registered before the gateway is built, so
	// the first tool list a client sees already covers the whole core.
	if _, err := catalog.registerSubsystems(ctx, false); err != nil {
		return err
	}

	bridge, err := toolboxmcp.NewFromAPICatalog(ctx, catalog.service.Catalog(), catalog.service.Invoker(),
		toolboxmcp.APICatalogOptions{Options: toolboxmcp.Options{
			Name:            "toolbox",
			Description:     "Model Context Protocol server for Toolbox subsystems, served by the core.",
			Policy:          catalog.policy,
			InitialExposure: toolboxmcp.ExposeAllowedFeatures,
		}})
	if err != nil {
		return err
	}
	mcp.set(bridge.HTTPHandler())

	registryServer, ok := h.Servers()["registry"]
	if !ok {
		return fmt.Errorf("the core needs its registry; run it with --component registry")
	}
	// Endpoint is already a URL, scheme included, because that is what a client dials.
	coreURL := registryServer.Endpoint()
	fmt.Fprintf(out, "toolbox: core listening on %s\n", coreURL)

	// The second listener, when the endpoint has an address of its own. It is started after
	// the handler is installed, so the endpoint never answers with an empty tool list, and
	// it is shut down with the signal so a stopped daemon releases both ports rather than
	// leaving one that answers and one that does not.
	var endpointServer *http.Server
	if !sameAddress {
		listener, err := net.Listen("tcp", resolved.MCP.Addr())
		if err != nil {
			return fmt.Errorf("the Model Context Protocol endpoint cannot listen on %s: %w", resolved.MCP.Addr(), err)
		}
		mux := http.NewServeMux()
		mux.Handle(MCPPath, bridge.HTTPHandler())
		endpointServer = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if serveErr := endpointServer.Serve(listener); serveErr != nil &&
				!errors.Is(serveErr, http.ErrServerClosed) {
				fmt.Fprintf(out, "toolbox: the Model Context Protocol endpoint stopped: %v\n", serveErr)
			}
		}()
		defer func() {
			bounded, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = endpointServer.Shutdown(bounded)
		}()
		fmt.Fprintf(out, "toolbox: Model Context Protocol on %s%s\n", resolved.MCP.URL(), MCPPath)
	} else {
		fmt.Fprintf(out, "toolbox: Model Context Protocol on %s%s\n", coreURL, MCPPath)
	}
	fmt.Fprintf(out, "toolbox: point a client at it with --core %s\n", resolved.Daemon.Addr())

	// The registry's server is already serving, so the daemon's job is to stay alive and
	// let the signal through.
	<-ctx.Done()
	fmt.Fprintf(out, "toolbox: core stopping\n")
	return shutdownDaemon(ctx, h)
}

// shutdownDaemon stops the host in reverse start order, with a bound so a subsystem
// that will not stop does not hold the process.
func shutdownDaemon(ctx context.Context, h *host.Host) error {
	// The signal already cancelled the caller's context, so a fresh one is used for
	// the shutdown itself: stopping is the last thing that should be cut short.
	bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return h.Shutdown(bounded)
}
