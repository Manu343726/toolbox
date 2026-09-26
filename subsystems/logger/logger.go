// Package logger is the subsystem that exposes a deployment's log fanout over ConnectRPC.
//
// An agent, or a service it started, is not this process. It cannot call slog.New against a
// router that lives in another process, and it should not have to open the deployment's log
// files itself to write into them. This subsystem is the boundary: it turns an RPC message
// into a slog.Record and hands it to the same router the in-process handlers use, so an entry
// sent over the wire lands wherever a route says an entry of that shape lands, and is tagged
// the same way.
//
// Everything past that boundary is the standard library's. The conversion is the only part
// here that has a shape of its own, and it is the conversion that has to.
package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/log"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	loggerv1 "github.com/Manu343726/toolbox/subsystems/logger/loggerv1"
	"github.com/Manu343726/toolbox/subsystems/logger/loggerv1/loggerv1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "logger"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Role is the provider role this subsystem plays.
//
// It is an open identifier, as every role is: a role is a convention, not a closed set. A
// deployment says it has a logger by the same call that lists its parsers and adapters.
const Role api.ProviderRole = "logger"

// LoggerKey is the attribute naming the component that produced an entry.
//
// It is the key slog itself uses for a logger's name, so a route written against an
// in-process entry matches an entry that arrived over this boundary too — which is the whole
// reason the boundary is cheap.
const LoggerKey = "logger"

// Options configure the subsystem.
type Options struct {
	// Router is the deployment's fanout, shared with the in-process handlers.
	//
	// It is injected rather than built here so an entry sent over the wire and an entry
	// written in process go through one router. Two routers would be two fanouts, and a
	// project whose routes were configured one way would find the agent's entries going
	// somewhere else.
	Router *log.Router
	// Providers are the backends a configuration may name, reported by GetConfig so a caller
	// can find out where it is allowed to send entries.
	//
	// A logger reads entries, it does not create sinks, so a deployment that has configured
	// none reports none rather than a default that would record nothing. A project's own
	// configuration is checked against this list, because a project naming a backend the
	// deployment does not have would log nowhere and say nothing.
	Providers []string
	// Registry builds a project's fanout from its own configuration file.
	//
	// It is supplied by whoever composed the deployment, and it is a registry rather than a
	// parsed configuration because a project's sinks have to be built the same way the
	// deployment's were: the same providers, the same base directory, the same refusals.
	Registry *log.Registry
	// WorkDir is where a project's configuration file is looked for. It defaults to the
	// process's working directory, which is what makes a project in a subdirectory find its
	// own file by walking up.
	WorkDir string
	// Clock supplies the time an entry claims to have been written at. It is a field so a
	// test can state the time instead of asserting one is roughly now.
	Clock func() time.Time
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// Handler implements LoggerService.
type Handler struct {
	router    *log.Router
	providers []string
	workDir   string
	clock     func() time.Time

	// mu guards scope, the project the caller has declared it is acting for.
	//
	// It is per handler rather than per deployment on purpose: two agents working on
	// different projects share one deployment, and an entry that inherited the wrong project
	// would be filed under the wrong project. A caller that wants a particular project says
	// so with the entry, and this is the default for the ones that do not.
	mu    sync.RWMutex
	scope string

	// registry builds a project's fanout. It is nil when the deployment did not supply one,
	// in which case a project's own configuration can still be read and reported but not
	// built — and that is said rather than silently ignored.
	registry *log.Registry
	// projects are the routers built for individual projects, by name.
	//
	// They are cached because a router holds open files and a rotation policy, and building
	// one per entry would reopen everything on every call. Cached is safe because a
	// project's configuration is a file that does not change under a running deployment;
	// changing it means restarting, which is true of every other part of a deployment too.
	projects map[string]*log.Router
	// broken names projects whose configuration could not be built, with the reason, so the
	// failure is reported the same way every time rather than retried into a new one.
	broken map[string]error
}

// NewHandler creates a logger handler.
func NewHandler(options Options) (*Handler, error) {
	if options.Router == nil {
		return nil, fmt.Errorf("the %s subsystem needs a router: it is the deployment's fanout, "+
			"and a logger that routes nothing is not one", Name)
	}
	workDir := options.WorkDir
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	registry := options.Registry
	if registry == nil {
		// Built from the built-in providers, so a project that names one of them still works
		// and a project that names a subsystem's backend is refused rather than ignored.
		registry = log.NewRegistry()
	}
	providers := options.Providers
	if providers == nil {
		providers = []string{}
	}
	return &Handler{
		router:    options.Router,
		providers: providers,
		registry:  registry,
		workDir:   workDir,
		clock:     clock,
		projects:  map[string]*log.Router{},
		broken:    map[string]error{},
	}, nil
}

// New is the programmatic entrypoint for the logger service.
func New(options Options) (*subsystem.Server, error) {
	if options.Version == "" {
		options.Version = Version
	}
	handler, err := NewHandler(options)
	if err != nil {
		return nil, err
	}
	path, service := loggerv1connect.NewLoggerServiceHandler(handler)
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       options.Version,
		Description:   "A deployment's log fanout, over ConnectRPC, for callers that are not this process.",
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:    loggerv1connect.LoggerServiceName,
			Path:    path,
			Handler: service,
		}},
	})
}

// Providers declares this subsystem as a provider at an endpoint.
func Providers(endpoint string) []api.Provider {
	return []api.Provider{{
		ID:                    Name,
		Subsystem:             Name,
		Role:                  Role,
		Endpoint:              endpoint,
		ImplementationVersion: Version,
	}}
}

// GetConfig returns the deployment's fanout, or a project's.
func (h *Handler) GetConfig(ctx context.Context, req *connect.Request[loggerv1.GetConfigRequest]) (*connect.Response[loggerv1.GetConfigResponse], error) {
	workspace := strings.TrimSpace(req.Msg.GetWorkspace())
	// The same resolution Log uses, so a caller is told the fanout its entries actually go
	// through. Reporting one configuration and routing by another would leave a caller
	// unable to answer "where did my entry go" from anything this service said.
	destination, err := h.routerFor(workspace)
	if err != nil {
		return nil, err
	}
	fanout := destination.Config()
	return connect.NewResponse(&loggerv1.GetConfigResponse{
		Level:     levelToProto(fanout.Level),
		Handlers:  handlersToProto(fanout.Handlers),
		Routes:    routesToProto(fanout.Routes),
		Workspace: workspace,
		Providers: h.providers,
		Failures:  int64(h.router.Failures()),
		Unrouted:  int64(h.router.Unrouted()),
	}), nil
}

// SetConfig records the project a caller is acting for.
func (h *Handler) SetConfig(ctx context.Context, req *connect.Request[loggerv1.SetConfigRequest]) (*connect.Response[loggerv1.SetConfigResponse], error) {
	workspace := strings.TrimSpace(req.Msg.GetWorkspace())
	h.mu.Lock()
	h.scope = workspace
	h.mu.Unlock()
	// The answer names what was recorded, so a caller does not have to remember what it asked
	// for or wonder whether the entry it sends next will be tagged with it.
	return connect.NewResponse(&loggerv1.SetConfigResponse{Workspace: workspace}), nil
}

// Log records one entry.
func (h *Handler) Log(ctx context.Context, req *connect.Request[loggerv1.LogRequest]) (*connect.Response[loggerv1.LogResponse], error) {
	msg := req.Msg
	if strings.TrimSpace(msg.GetMessage()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("an entry needs a message: there is nothing for a reader to read"))
	}
	record := slog.NewRecord(h.clock(), levelFromProto(msg.GetLevel()), msg.GetMessage(), 0)

	// The component is an attribute rather than a field of its own, because that is where a
	// route matches it: "everything the knowledge subsystem said" is a route, not a special
	// case in the router.
	if component := strings.TrimSpace(msg.GetLogger()); component != "" {
		record.AddAttrs(slog.String(LoggerKey, component))
	}
	for index, value := range msg.GetAttributes() {
		if strings.TrimSpace(value.GetKey()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("attribute %d needs a key: a value with no key is not a value", index+1))
		}
		record.AddAttrs(slog.String(value.GetKey(), value.GetValue()))
	}

	workspace := strings.TrimSpace(msg.GetWorkspace())
	if workspace == "" {
		workspace = h.currentScope()
	}
	if workspace != "" {
		ctx = log.WithWorkspace(ctx, workspace)
	}

	// A project with a configuration file of its own is logged into by its own fanout, which
	// is what "this project configures its routing differently" means for a caller that is
	// not in this process. Without this the project's file would be readable but inert.
	destination, err := h.routerFor(workspace)
	if err != nil {
		return nil, err
	}

	// Deliver reports which sinks the entry reached, which Handle cannot. A caller in another
	// process is owed an answer, and "no error" does not say whether anything recorded it.
	reached := destination.Deliver(ctx, record)
	return connect.NewResponse(&loggerv1.LogResponse{
		Routed:   len(reached) > 0,
		Handlers: reached,
	}), nil
}

func (h *Handler) currentScope() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.scope
}

// routerFor returns the fanout an entry for a project goes through: the project's own if it
// configured one, and the deployment's otherwise.
func (h *Handler) routerFor(workspace string) (*log.Router, error) {
	if workspace == "" {
		return h.router, nil
	}
	h.mu.RLock()
	cached, built := h.projects[workspace]
	failure, failed := h.broken[workspace]
	h.mu.RUnlock()
	switch {
	case built:
		return cached, nil
	case failed:
		// The same message every time, because a caller that fixed its configuration should
		// get the new answer — but a caller that did not should not be told it works.
		return nil, failure
	}

	resolved, err := config.New(config.Options{WorkDir: h.workDirFor(workspace)}).Resolve("")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("the configuration for the project %q: %w", workspace, err))
	}
	if len(resolved.Logging) == 0 {
		// A project that never mentioned logging logs into the deployment's fanout, which is
		// what it would get without this service and so what it should not lose by using it.
		return h.router, nil
	}
	fanout, err := log.ParseConfig(resolved.Logging)
	if err != nil {
		return nil, h.rememberFailure(workspace, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("the logging section of the project %q: %w", workspace, err)))
	}
	if err := h.checkProviders(workspace, fanout); err != nil {
		return nil, h.rememberFailure(workspace, err)
	}

	// The base directory is the file's own, so a relative path in a project's configuration
	// means a path in that project rather than wherever this process started.
	registry := *h.registry
	registry.BaseDir = resolved.LoggingBaseDir
	project, err := registry.Build(fanout)
	if err != nil {
		return nil, h.rememberFailure(workspace, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("the fanout of the project %q: %w", workspace, err)))
	}

	h.mu.Lock()
	h.projects[workspace] = project
	h.mu.Unlock()
	return project, nil
}

func (h *Handler) rememberFailure(workspace string, err error) error {
	h.mu.Lock()
	h.broken[workspace] = err
	h.mu.Unlock()
	return err
}

// checkProviders refuses a project that named a backend this deployment does not have.
//
// It is reported rather than defaulted, because a project whose logs went nowhere because it
// spelled a backend wrongly is the failure a log cannot report: there is no log.
func (h *Handler) checkProviders(workspace string, fanout log.Config) error {
	if len(h.providers) == 0 {
		// The deployment did not say what it has, which means it is not claiming a list —
		// and a claim it did not make is not one to refuse against. The registry is still
		// asked to build the fanout, and it refuses a provider nobody registered.
		return nil
	}
	available := make(map[string]bool, len(h.providers))
	for _, provider := range h.providers {
		available[provider] = true
	}
	for _, handler := range fanout.Handlers {
		if available[handler.Provider] {
			continue
		}
		names := h.providers
		if len(names) == 0 {
			names = []string{"none"}
		}
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"the project %q sends to the %q handler, which needs the %q backend; this "+
				"deployment has: %s", workspace, handler.Name, handler.Provider,
			strings.Join(names, ", ")))
	}
	return nil
}

// workDirFor is where a project's configuration file is looked for.
//
// A workspace that names a directory is that directory's project. One that does not is still
// the working directory's, because a caller saying "acme" is saying which project it is
// serving, not claiming to know where that project lives on this machine.
func (h *Handler) workDirFor(workspace string) string {
	if info, err := os.Stat(workspace); err == nil && info.IsDir() {
		if absolute, absErr := filepath.Abs(workspace); absErr == nil {
			return absolute
		}
		return workspace
	}
	return h.workDir
}
