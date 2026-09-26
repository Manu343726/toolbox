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
	// WorkDir is where this deployment's own project is. A caller naming a project by
	// directory overrides it; a caller naming no project, or a project that is not a
	// directory, is answered by this deployment's own fanout.
	//
	// It defaults to the process's working directory, so a daemon running inside a project
	// serves that project without being told which one it is.
	WorkDir string
	// BaseDir is what a relative handler path in a caller's own configuration resolves
	// against, because the caller is not in this process and cannot know where it runs. It is
	// the deployment's own configuration directory; a caller that wants its logs somewhere
	// particular says an absolute path.
	BaseDir string
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
	baseDir   string
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
	// fanouts are the routers built for individual projects, by where their configuration
	// came from.
	//
	// They are cached because a router holds open files and a rotation policy, and building
	// one per entry would reopen everything on every call. Cached is safe because a project's
	// configuration is a file that does not change under a running deployment; changing it
	// means restarting, which is true of every other part of a deployment too. A caller's own
	// configuration is replaced explicitly rather than by expiry, because it can change while
	// the deployment runs.
	fanouts map[fanoutKey]*log.Router
	// broken names configurations that could not be built, by where they came from, so the
	// failure is reported the same way every time rather than retried into a new one.
	broken map[fanoutKey]error
}

// Where a fanout came from, named once so the cache keys and the reports cannot disagree.
const (
	deploymentSource  = loggerv1.FanoutSource_FANOUT_SOURCE_DEPLOYMENT
	projectFileSource = loggerv1.FanoutSource_FANOUT_SOURCE_PROJECT_FILE
	callerSource      = loggerv1.FanoutSource_FANOUT_SOURCE_CALLER
)

// fanoutKey is where a fanout came from, and for a caller's own configuration, which
// workspace it was for.
type fanoutKey struct {
	source loggerv1.FanoutSource
	// location is the directory a project's configuration file was read from, or the workspace
	// a caller's own configuration is scoped to. It is empty only for the deployment's own,
	// which is the router this process was given rather than anything that was read.
	location string
}

// callerKey is the cache slot for a caller's own configuration for one workspace.
//
// The workspace is part of the key, and that is the whole safety property of the feature: a
// caller that configures where its own entries go cannot redirect another workspace's, because
// another workspace's entries look up a different slot and find nothing. A single slot would
// have made every configured workspace capture every other one, which is the exact escalation
// the scope exists to prevent.
func callerKey(workspace string) fanoutKey {
	return fanoutKey{source: callerSource, location: workspace}
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
		baseDir:   options.BaseDir,
		clock:     clock,
		fanouts:   map[fanoutKey]*log.Router{},
		broken:    map[fanoutKey]error{},
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
	requested := strings.TrimSpace(req.Msg.GetWorkspace())
	// The same resolution Log uses, so a caller is told the fanout its entries actually go
	// through. Reporting one configuration and routing by another would leave a caller
	// unable to answer "where did my entry go" from anything this service said.
	project, err := h.resolveProject(requested)
	if err != nil {
		return nil, err
	}
	fanout := project.router.Config()
	return connect.NewResponse(&loggerv1.GetConfigResponse{
		Level:     levelToProto(fanout.Level),
		Handlers:  handlersToProto(fanout.Handlers),
		Routes:    routesToProto(fanout.Routes),
		Workspace: project.name,
		Source:    project.source,
		Providers: h.providers,
		Failures:  int64(h.router.Failures()),
		Unrouted:  int64(h.router.Unrouted()),
	}), nil
}

// SetConfig records the project a caller is acting for.
func (h *Handler) SetConfig(ctx context.Context, req *connect.Request[loggerv1.SetConfigRequest]) (*connect.Response[loggerv1.SetConfigResponse], error) {
	workspace := strings.TrimSpace(req.Msg.GetWorkspace())
	supplied := len(req.Msg.GetHandlers()) > 0 || len(req.Msg.GetRoutes()) > 0 ||
		req.Msg.GetLevel() != loggerv1.LogLevel_LOG_LEVEL_UNSPECIFIED

	if supplied {
		if workspace == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
				"a fanout needs a workspace to be scoped to: a configuration that is not "+
					"scoped would apply to the deployment's own entries, which is the one "+
					"thing a caller cannot configure"))
		}
		fanout, err := fanoutFromProto(req.Msg.GetLevel(), req.Msg.GetHandlers(), req.Msg.GetRoutes())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("the fanout for the workspace %q: %w", workspace, err))
		}
		if err := h.checkProviders(workspace, fanout); err != nil {
			return nil, err
		}
		router, err := h.build(fanout)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("the fanout for the workspace %q: %w", workspace, err))
		}
		// Built before anything is stored, so a configuration that cannot work is refused
		// rather than installed and discovered by the entries that reach it.
		key := callerKey(workspace)
		h.mu.Lock()
		h.fanouts[key] = router
		delete(h.broken, key)
		h.scope = workspace
		h.mu.Unlock()
	} else {
		h.mu.Lock()
		h.scope = workspace
		h.mu.Unlock()
	}

	effective, err := h.resolveProject(workspace)
	if err != nil {
		return nil, err
	}
	inForce := effective.router.Config()
	// The answer says what a caller's entries will now do, and whether this call supplied a
	// configuration or only named a project — so a caller that sent an empty request is not
	// left believing it retuned anything.
	return connect.NewResponse(&loggerv1.SetConfigResponse{
		Workspace:  effective.name,
		Level:      levelToProto(inForce.Level),
		Handlers:   handlersToProto(inForce.Handlers),
		Routes:     routesToProto(inForce.Routes),
		Source:     effective.source,
		Configured: supplied,
	}), nil
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

	// A project with a configuration file of its own is logged into by its own fanout, which
	// is what "this project configures its routing differently" means for a caller that is
	// not in this process. Without this the project's file would be readable but inert.
	project, err := h.resolveProject(workspace)
	if err != nil {
		return nil, err
	}
	if project.name != "" {
		ctx = log.WithWorkspace(ctx, project.name)
	}

	// Deliver reports which sinks the entry reached, which Handle cannot. A caller in another
	// process is owed an answer, and "no error" does not say whether anything recorded it.
	reached := project.router.Deliver(ctx, record)
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

// project is a resolved fanout and the name entries for it are tagged with.
type project struct {
	router *log.Router
	// source says where the fanout came from, so a caller can tell its own configuration
	// from its project's file and from the deployment's. It is reported rather than assumed:
	// a caller that has redirected its logging is entitled to know that, and to know that its
	// project's file is what would otherwise have answered.
	source loggerv1.FanoutSource
	// name is the project an entry belongs to, as a route matches it and as the line reads.
	//
	// It is the project's directory name rather than its path, because a log line saying
	// `workspace: acme` is a fact about the entry and one saying `workspace:
	// /home/someone/src/acme` is a fact about the machine.
	name string
}

// resolveProject decides which fanout an entry for a project belongs to.
//
// The order is a caller's own configuration, then the project's configuration file, then the
// deployment's. A caller that has been told what it wants runs first because it is the most
// recent and most specific statement about those entries; a project's file is next because
// the deployment author wrote it; and the deployment's answers when neither has an opinion,
// which is what a caller that named no project gets.
//
// A workspace that names a directory is a location: the project there, its own configuration
// file, and its entries tagged with the directory's name, so a route written as
// `match: {workspace: acme}` matches whether the caller said "acme" or said where acme lives.
//
// A workspace that is only a name is a label, not a location. It can still carry a caller's
// own configuration — that is the common case, since a caller usually knows its project by
// name and not by where it lives — but no file is looked up for it: a name says which project
// a caller is serving, not where that project lives on this machine, and guessing a directory
// from a name would put one project's log in another's.
func (h *Handler) resolveProject(workspace string) (project, error) {
	if workspace == "" {
		return project{router: h.router, source: deploymentSource}, nil
	}
	// A caller's own configuration answers first, and only for the workspace it was given for:
	// another workspace's entry looks up a different slot and reaches the deployment's routes,
	// which is what keeps a client's configuration to the client's own entries.
	if router, err := h.cached(callerKey(workspace)); router != nil || err != nil {
		return project{router: router, name: workspace, source: callerSource}, err
	}

	dir, isLocation := h.projectDir(workspace)
	if !isLocation {
		// A label with no configuration of its own: the deployment's fanout, and the entry
		// carries the name so the deployment's routes can match on it.
		return project{router: h.router, name: workspace, source: deploymentSource}, nil
	}
	name := filepath.Base(dir)

	fileKey := fanoutKey{source: projectFileSource, location: dir}
	if router, err := h.cached(fileKey); router != nil || err != nil {
		return project{router: router, name: name, source: projectFileSource}, err
	}

	resolved, err := config.New(config.Options{WorkDir: dir}).Resolve("")
	if err != nil {
		return project{}, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("the configuration for the project %q: %w", name, err))
	}
	if len(resolved.Logging) == 0 {
		// A project that never mentioned logging logs into the deployment's fanout, which is
		// what it would get without this service and so what it should not lose by using it.
		return project{
			router: h.router,
			name:   name,
			source: deploymentSource,
		}, nil
	}
	fanout, err := log.ParseConfig(resolved.Logging)
	if err != nil {
		return project{}, h.rememberFailure(fileKey, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("the logging section of the project %q: %w", name, err)))
	}
	if err := h.checkProviders(name, fanout); err != nil {
		return project{}, h.rememberFailure(fileKey, err)
	}

	// The base directory is the project's own, so a relative path in its configuration means
	// a path in that project rather than wherever this process started.
	router, err := h.buildIn(resolved.LoggingBaseDir, fanout)
	if err != nil {
		return project{}, h.rememberFailure(fileKey, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("the fanout of the project %q: %w", name, err)))
	}

	h.mu.Lock()
	h.fanouts[fileKey] = router
	h.mu.Unlock()
	return project{router: router, name: name, source: projectFileSource}, nil
}

// cached returns a fanout already built for a source, or the error it failed with.
//
// A nil router and a nil error together mean "not built yet", which is different from "built
// and refused": a refusal is remembered so a caller that fixed its configuration gets the new
// answer, and a caller that did not is not told it works.
func (h *Handler) cached(key fanoutKey) (*log.Router, error) {
	h.mu.RLock()
	router, found := h.fanouts[key]
	failure, failed := h.broken[key]
	h.mu.RUnlock()
	switch {
	case found:
		return router, nil
	case failed:
		return nil, failure
	default:
		return nil, nil
	}
}

// build constructs a caller's own fanout, resolving a relative path against the deployment's
// configuration directory.
//
// That base is the deployment's rather than the caller's working directory, because the
// caller is not in this process and cannot know where it runs. A caller that wants its logs in
// a particular place says an absolute path, which is unambiguous.
func (h *Handler) build(fanout log.Config) (*log.Router, error) {
	return h.buildIn(h.baseDir, fanout)
}

func (h *Handler) buildIn(baseDir string, fanout log.Config) (*log.Router, error) {
	registry := *h.registry
	registry.BaseDir = baseDir
	return registry.Build(fanout)
}

func (h *Handler) rememberFailure(key fanoutKey, err error) error {
	h.mu.Lock()
	h.broken[key] = err
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

// projectDir reports the directory a requested workspace names, if it names one.
func (h *Handler) projectDir(workspace string) (string, bool) {
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return "", false
	}
	if absolute, absErr := filepath.Abs(workspace); absErr == nil {
		return absolute, true
	}
	return workspace, true
}
