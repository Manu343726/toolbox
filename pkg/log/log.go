// Package log builds a slog handler from a deployment's configuration.
//
// The Go API is the standard library's. A caller writes
//
//	logger := slog.New(log.Installed())
//	logger.Info("stored a source", "id", id)
//
// and everything below is the standard library's too: an entry is a slog.Record, a sink is a
// slog.Handler, severity is a slog.Level, the line format is slog.NewTextHandler or
// slog.NewJSONHandler, the fanout is slog.MultiHandler, and attribute nesting and levels are
// each sink's own slog.HandlerOptions and WithAttrs. This package contributes one thing the
// standard library does not have — deciding which handlers an entry reaches, and what it is
// tagged with on the way — and it does that in slog's own types.
//
// Backends are existing slog handlers: the standard library's text and JSON handlers,
// lumberjack for rotation, and handlers written for syslog, Loki and the rest. See
// docs/decisions/0012-logging.md.
package log

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
)

// Router decides which handlers an entry reaches, and what it is tagged with on the way.
//
// It is a slog.Handler in front of a set of slog.MultiHandlers, one per configured route, so a
// project sharing a machine can send its own entries to its own file while everything still
// reaches the machine-wide destination. The fanout within a route is the standard library's.
type Router struct {
	mu sync.RWMutex
	// routes is the delivery plan, in order, each already reduced to the handlers it
	// introduces — the ones no earlier route claimed. A handler named by two routes therefore
	// receives an entry once, attributed to the first route that claimed it, and "once" is the
	// useful reading: a line duplicated because two routes overlapped is a line nobody can
	// count.
	routes []delivery
	// handlers are the sinks, by the name routes refer to them.
	handlers map[string]slog.Handler
	// level is the deployment's minimum, applied before any route is consulted.
	level slog.Level
	// bound are the attributes a caller attached with slog.With.
	//
	// They are kept as well as delegated, because a route has to be able to see them to
	// route on them: the sinks already hold them by the time the entry arrives, but a
	// decision made here cannot read a sink's state. Holding them costs one slice per
	// slog.With and changes nothing about how they are rendered — that is still each sink's
	// own WithAttrs.
	bound []slog.Attr
	// failures counts entries a sink refused, so a deployment whose logging is broken can be
	// told on a line rather than by noticing its log is empty.
	failures int
}

// delivery is one route reduced to what it actually sends.
type delivery struct {
	// name identifies the route in a diagnostic.
	name string
	// match decides whether an entry is this route's.
	match Match
	// handlers is the fanout this route sends to, or nil when the route introduced nothing
	// because an earlier route already claimed every handler it named.
	handlers slog.Handler
}

// RouterOptions configure a Router.
type RouterOptions struct {
	// Level is the minimum an entry must reach to be routed at all. It is the cheapest way to
	// turn a deployment's logging up without editing every sink.
	Level slog.Level
	// Handlers are the sinks, by the name routes refer to them.
	Handlers map[string]slog.Handler
	// Routes are the delivery plan, in order.
	Routes []Route
}

// NewRouter builds a router from a resolved configuration's sinks and routes.
//
// Every sink is bound to its routes, and every route's tags are bound to it, before the
// router exists: from then on nothing here formats a value, nests a group or checks a level.
// Those are the sinks' jobs and they are the standard library's.
func NewRouter(options RouterOptions) (*Router, error) {
	byName := make(map[string]slog.Handler, len(options.Handlers))
	for name, handler := range options.Handlers {
		if name == "" {
			return nil, fmt.Errorf("a logging handler needs a name")
		}
		if handler == nil {
			return nil, fmt.Errorf("the %q handler is nil", name)
		}
		byName[name] = handler
	}

	router := &Router{handlers: byName, level: options.Level}
	claimed := make(map[string]bool, len(byName))
	for index, route := range options.Routes {
		if err := router.addRoute(index, route, claimed); err != nil {
			return nil, err
		}
	}
	return router, nil
}

func (r *Router) addRoute(index int, route Route, claimed map[string]bool) error {
	plan := delivery{name: route.describe(index), match: route.When}
	introduced := make([]slog.Handler, 0, len(route.Handlers))
	for _, name := range route.Handlers {
		handler, ok := r.handlers[name]
		if !ok {
			return fmt.Errorf("the route %q sends entries to %q, which no handler is declared for",
				plan.name, name)
		}
		if claimed[name] {
			continue
		}
		claimed[name] = true
		introduced = append(introduced, handler)
	}
	if len(introduced) == 0 {
		r.routes = append(r.routes, plan)
		return nil
	}
	// The fanout and the tags are both the standard library's. WithAttrs is how a handler
	// carries attributes, so binding the route's tags here means the entry reaches the sinks
	// shaped exactly as any other slog entry would be, and the sinks group and nest it
	// themselves.
	plan.handlers = slog.NewMultiHandler(introduced...)
	if len(route.Add) > 0 {
		tags := make([]slog.Attr, 0, len(route.Add))
		for key, value := range route.Add {
			tags = append(tags, slog.String(key, value))
		}
		plan.handlers = plan.handlers.WithAttrs(tags)
	}
	r.routes = append(r.routes, plan)
	return nil
}

// Enabled reports whether an entry at a level would be routed.
//
// It asks the sinks rather than answering from the deployment's level alone, so a caller
// deciding whether to format an expensive attribute is not told yes and then has the entry
// dropped.
func (r *Router) Enabled(ctx context.Context, level slog.Level) bool {
	if level < r.Level() {
		return false
	}
	for _, plan := range r.snapshot() {
		if plan.handlers != nil && plan.handlers.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle routes one record to every route that matches it.
//
// The record reaches the sinks exactly as slog built it — nothing here copies, renders or
// rewrites it — and the error is never returned. A log line is not worth failing a call over,
// and a sink that cannot write must not be able to fail the thing it is describing. A sink's
// error is reported on standard error and counted, so a deployment whose logging is broken is
// told so on a line rather than by noticing its log is empty.
func (r *Router) Handle(ctx context.Context, record slog.Record) error {
	// A project carried on the context is put on the entry before any sink sees it. A route
	// can already match it, but a route is not the only reader: a log collector filtering by
	// project reads the line, and a caller deep in a call should not have to add an
	// attribute to every log call for that to work.
	delivered := record
	if workspace := WorkspaceFrom(ctx); workspace != "" && !carries(r.bound, record, WorkspaceKey, workspace) {
		delivered.AddAttrs(slog.String(WorkspaceKey, workspace))
	}
	for _, plan := range r.snapshot() {
		if plan.handlers == nil || !plan.match.matches(ctx, r.bound, delivered) {
			continue
		}
		if err := plan.handlers.Handle(ctx, delivered); err != nil {
			r.reportFailure(plan.name, err)
		}
	}
	return nil
}

// WithAttrs binds attributes to every route's fanout.
//
// It is the standard library's own mechanism, delegated to each sink, so what a
// slog.With("component", "knowledge") ends up looking like in a line is the sink's business
// and not this package's.
func (r *Router) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return r
	}
	return r.rewired(attrs, func(handler slog.Handler) slog.Handler { return handler.WithAttrs(attrs) })
}

// WithGroup nests attributes under a group, again by each sink's own WithGroup.
func (r *Router) WithGroup(name string) slog.Handler {
	if name == "" {
		return r
	}
	return r.rewired(nil, func(handler slog.Handler) slog.Handler { return handler.WithGroup(name) })
}

// rewired returns a router whose routes' fanouts have had a transformation applied, and which
// remembers added attributes for routing. slog handlers are immutable — WithAttrs and WithGroup
// return new ones — so binding a slog.With to a router produces a router, and the original
// keeps working.
func (r *Router) rewired(added []slog.Attr, transform func(slog.Handler) slog.Handler) *Router {
	bound := &Router{handlers: r.handlers, level: r.level}
	if len(added) > 0 {
		bound.bound = make([]slog.Attr, 0, len(r.bound)+len(added))
		bound.bound = append(bound.bound, r.bound...)
		bound.bound = append(bound.bound, added...)
	}
	for _, plan := range r.snapshot() {
		if plan.handlers != nil {
			plan.handlers = transform(plan.handlers)
		}
		bound.routes = append(bound.routes, plan)
	}
	return bound
}

// Level is the deployment's minimum.
func (r *Router) Level() slog.Level {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.level
}

// Failures reports how many entries a sink refused.
func (r *Router) Failures() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.failures
}

func (r *Router) reportFailure(where string, err error) {
	r.mu.Lock()
	r.failures++
	r.mu.Unlock()
	fmt.Fprintf(os.Stderr, "toolbox: the %q log route could not write an entry: %v\n", where, err)
}

func (r *Router) snapshot() []delivery {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.routes
}

// WorkspaceKey is the attribute a route matches and adds to name the project an entry belongs
// to.
const WorkspaceKey = "workspace"

// WithWorkspace returns a context carrying the project an entry belongs to.
//
// The workspace is read when the record is handled, so a caller deep in a call does not have
// to thread the project through every signature to log which project it was serving. An
// operator filtering by project gets the right lines without a single call site knowing that
// filtering exists.
func WithWorkspace(ctx context.Context, workspace string) context.Context {
	if workspace == "" {
		return ctx
	}
	return context.WithValue(ctx, workspaceKey{}, workspace)
}

// WorkspaceFrom returns the project a context is working in, or empty.
func WorkspaceFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if workspace, ok := ctx.Value(workspaceKey{}).(string); ok {
		return workspace
	}
	return ""
}

type workspaceKey struct{}
