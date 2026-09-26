// Package log routes log entries from this process to the handlers a deployment
// configured.
//
// The API Go code uses is the standard library's. A caller writes
//
//	logger := log.Logger("knowledge")
//	logger.Info("stored a source", "id", id)
//
// and this package's slog handler decides where the entry goes. Nothing in the framework's
// API is a logging interface, so a dependency that logs through slog lands in the same
// fanout without knowing this package exists — which is the whole reason for choosing slog
// over a facade.
//
// See docs/decisions/0012-logging.md.
package log

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
)

// Entry is one log record in the transport-neutral form every handler receives.
//
// It is deliberately not a slog.Record: a handler is a backend, and a backend should not have
// to know how the standard library represents a record to be able to write one somewhere.
// Attributes are flattened to strings because that is what every backend can represent — a
// typed value a backend cannot hold would be dropped, and dropping it silently is worse than
// rendering it.
type Entry struct {
	// Time is when the entry was produced. A backend that batches or files may write it
	// later, so it is the entry's own time and not the write's.
	Time Time
	// Level is how severe the entry is.
	Level Level
	// Message is the entry's text.
	Message string
	// Logger is the name the caller logged under, such as a subsystem's name.
	Logger string
	// Attributes are the key/value pairs, rendered as strings.
	Attributes map[string]string
	// Source is where the call was made, when the caller asked for it.
	Source Source
	// Workspace is the project this entry belongs to, when the caller carried one.
	//
	// It is an attribute like any other, and it is called out here because it is the one an
	// operator filters by: entries from every project share one stream, and this is what
	// tells them apart.
	Workspace string
}

// Source is the call site of a log entry.
type Source struct {
	File string
	Line int
	// Function is the function the call was in, when the runtime reports it.
	Function string
}

// String renders a source as "file:line", which is how every backend wants it.
func (s Source) String() string {
	if s.File == "" {
		return ""
	}
	if s.Line <= 0 {
		return s.File
	}
	return fmt.Sprintf("%s:%d", s.File, s.Line)
}

// Level is a severity, ordered so a handler can state a minimum and a router can compare.
type Level int

// Levels, in the order of severity.
//
// The values are spaced and centred on zero so that a Level nobody set is LevelInfo — the
// default a caller means when they do not say. Deriving them with iota from -4 put the zero
// value above LevelError, so a handler declared without a level accepted nothing at all and
// recorded nothing anywhere, with no error to explain it. That is the whole class of defect
// this package exists to avoid, caused by the numbering.
const (
	LevelDebug Level = -4
	LevelInfo  Level = 0
	LevelWarn  Level = 4
	LevelError Level = 8
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return fmt.Sprintf("level(%d)", int(l))
	}
}

// ParseLevel reads a level name, refusing anything else by name.
//
// A level that is not one of the four is refused rather than defaulted, because a handler
// configured with a misspelled level would silently record everything or nothing, and both
// are worse than a configuration that will not load.
func ParseLevel(value string) (Level, error) {
	switch value {
	case "debug", "DEBUG", "Debug":
		return LevelDebug, nil
	case "info", "INFO", "Info", "":
		return LevelInfo, nil
	case "warn", "WARN", "Warn", "warning", "WARNING":
		return LevelWarn, nil
	case "error", "ERROR", "Error":
		return LevelError, nil
	default:
		return LevelInfo, fmt.Errorf("%q is not a level; it must be one of: debug, info, warn, error", value)
	}
}

// slogLevel converts to the standard library's level, which is what a Go caller uses.
func (l Level) slogLevel() slog.Level {
	switch l {
	case LevelDebug:
		return slog.LevelDebug
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// levelFromSlog converts from the standard library's level, clamping anything outside the
// four the framework names so a caller using a custom level cannot produce an entry that
// compares strangely against a handler's minimum.
func levelFromSlog(level slog.Level) Level {
	switch {
	case level <= slog.LevelDebug:
		return LevelDebug
	case level >= slog.LevelError:
		return LevelError
	case level >= slog.LevelWarn:
		return LevelWarn
	default:
		return LevelInfo
	}
}

// Handler is one logging backend: a named sink that accepts entries.
//
// A handler is held by the router and called directly. It is not reached over the network —
// see the decision record for why, which is that a sink in the same process is not a peer,
// and a network hop per log line would make logging able to fail for reasons unrelated to
// the log.
type Handler interface {
	// Name is the handler's identifier, as the configuration names it. Two handlers with one
	// name would make a route ambiguous, so the router refuses to register the second.
	Name() string
	// Handle records one entry. An error is reported and the entry is dropped: a log line is
	// not worth failing a call over, and a handler that cannot write must not be able to take
	// the process down with it.
	Handle(Entry) error
	// Close releases whatever the handler holds, flushing what it has buffered. It is called
	// when the router closes, which is when the process is shutting down.
	Close() error
}

// Provider produces a handler for one backend.
//
// A provider is what a subsystem implements to offer a logging backend, and it declares
// itself to the framework's catalog with the role loghandler — the same way a parser or an
// adapter declares itself. The interface lives here rather than in the subsystem because the
// router is what consumes it, and a subsystem that has to import the router to say it can
// write logs would be coupled to the whole fanout to provide one sink.
type Provider interface {
	// ProviderID is the identifier the configuration names to select this backend.
	ProviderID() string
	// NewHandler builds a handler from the options the configuration gave it.
	NewHandler(options map[string]any) (Handler, error)
}

// Router holds the handlers a deployment configured and the routes that decide which entry
// goes to which of them.
//
// It implements slog.Handler, so it is the whole of the standard library's involvement: a
// caller uses slog.Logger and this decides.
type Router struct {
	mu       sync.RWMutex
	handlers map[string]*routedHandler
	order    []string
	routes   []Route
	level    Level
	// providers are the backends a deployment can name but has not configured. Kept so a
	// configuration that names one can be reported as a typo rather than as a backend that
	// silently records nothing.
	providers map[string]Provider
	// now is injectable so a test can state an entry's time rather than the clock's.
	now func() Time
	// failures counts entries a handler refused, so a deployment whose logging is broken can
	// be told so on a line rather than by noticing the log is empty.
	failures int64
}

// NewRouter creates a router with no handlers and no routes.
//
// A router with no configuration records nothing, which is deliberate: a deployment that has
// said nothing about logging has not asked for any, and inventing a destination would send a
// reader looking for an explanation that is not written down anywhere. The host adds the
// built-in stderr handler when it wants the default behaviour.
func NewRouter() *Router {
	return &Router{
		handlers:  make(map[string]*routedHandler),
		providers: make(map[string]Provider),
		now:       Now,
		level:     LevelInfo,
	}
}

// Now is the clock the router stamps entries with. It is a variable so a test can state an
// entry's time; production code never sets it.
var Now = func() Time { return TimeNow() }

// RegisterProvider adds a backend the configuration may name.
//
// A provider registered after a configuration has been applied is not used until the
// configuration is applied again. Silently picking it up would mean a fanout that changes
// because a subsystem started, which is not something a reader of the configuration could
// predict.
func (r *Router) RegisterProvider(provider Provider) error {
	if provider == nil {
		return fmt.Errorf("a logging provider is required")
	}
	id := provider.ProviderID()
	if id == "" {
		return fmt.Errorf("a logging provider needs an identifier")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[id]; exists {
		return fmt.Errorf("a logging provider for %q is already registered", id)
	}
	r.providers[id] = provider
	return nil
}

// Providers returns the identifiers of the backends this router can name, sorted.
func (r *Router) Providers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sortStrings(ids)
	return ids
}

// Apply replaces the router's handlers and routes with a configuration.
//
// The whole configuration is applied or none of it: a router half-way through a new fanout
// would route some entries by the old rules and some by the new, and an entry's destination
// would depend on when it was written.
func (r *Router) Apply(cfg Config) error {
	built := make(map[string]*routedHandler, len(cfg.Handlers))
	order := make([]string, 0, len(cfg.Handlers))
	var closing []Handler

	for _, declared := range cfg.Handlers {
		if _, exists := built[declared.Name]; exists {
			closeAll(closing)
			return fmt.Errorf("two handlers are named %q; a route naming it would be ambiguous", declared.Name)
		}
		if declared.Name == "" {
			closeAll(closing)
			return fmt.Errorf("a logging handler needs a name")
		}
		r.mu.RLock()
		provider, known := r.providers[declared.Provider]
		r.mu.RUnlock()
		if !known {
			closeAll(closing)
			return fmt.Errorf("no logging provider for %q; this deployment has: %s",
				declared.Provider, joinOr(r.Providers(), "none"))
		}
		handler, err := provider.NewHandler(declared.Options)
		if err != nil {
			closeAll(closing)
			return fmt.Errorf("the %q handler: %w", declared.Name, err)
		}
		built[declared.Name] = &routedHandler{name: declared.Name, handler: handler, level: declared.Level}
		order = append(order, declared.Name)
		closing = append(closing, handler)
	}
	if err := validateRoutes(cfg.Routes, built); err != nil {
		closeAll(closing)
		return err
	}

	r.mu.Lock()
	previous := make([]Handler, 0, len(r.order))
	for _, name := range r.order {
		previous = append(previous, r.handlers[name].handler)
	}
	r.handlers = built
	r.order = order
	r.routes = cfg.Routes
	r.level = cfg.Level
	r.mu.Unlock()

	// The old handlers are released after the swap, so an entry written during the change goes
	// to a handler that is still open.
	closeAll(previous)
	return nil
}

// validateRoutes refuses a route naming a handler the configuration does not declare.
//
// A route naming nothing would be accepted and would record the entry nowhere, which is the
// one outcome a reader cannot diagnose: no error, no line, and no way to tell a
// misconfiguration from a quiet day.
func validateRoutes(routes []Route, handlers map[string]*routedHandler) error {
	seenDefault := false
	for _, route := range routes {
		if route.Default() {
			if seenDefault {
				return fmt.Errorf("two default routes; the second would never be reached")
			}
			seenDefault = true
		}
		for _, name := range route.Handlers {
			if _, ok := handlers[name]; !ok {
				return fmt.Errorf("the route %q sends entries to %q, which no handler is declared for",
					route.describe(), name)
			}
		}
	}
	return nil
}

// Config returns the fanout currently in force.
func (r *Router) Config() Config { //nolint:revive // a router always has a fanout, even an empty one
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg := Config{Level: r.level, Routes: append([]Route(nil), r.routes...)}
	for _, name := range r.order {
		h := r.handlers[name]
		cfg.Handlers = append(cfg.Handlers, HandlerConfig{
			Name:    h.name,
			Level:   h.level,
			Options: h.options(),
		})
	}
	return cfg
}

// Failures reports how many entries a handler refused since the router started.
func (r *Router) Failures() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return int(r.failures)
}

// Close releases every handler.
func (r *Router) Close() error {
	r.mu.Lock()
	handlers := make([]Handler, 0, len(r.order))
	for _, name := range r.order {
		handlers = append(handlers, r.handlers[name].handler)
	}
	r.handlers = make(map[string]*routedHandler)
	r.order = nil
	r.mu.Unlock()
	return closeAll(handlers)
}

// Dispatch routes one entry.
//
// A handler that refuses is counted and the entry is dropped rather than returned as an
// error. Failing the caller because a log could not be written makes logging able to break
// the thing it is describing, and a deployment that loses requests under logging pressure is
// in a worse position than one that loses log lines.
func (r *Router) Dispatch(entry Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry.Time.IsZero() {
		entry.Time = r.now()
	}
	if entry.Level < r.level {
		return
	}
	// Every matching route contributes, rather than the first one winning.
	//
	// "Everything to the file, and this project's errors also to the aggregator" is the
	// ordinary thing to want, and first-match-wins cannot express it: whichever route came
	// first would swallow the entry and the other would never see it. A fanout that can only
	// send an entry to one place is not a fanout.
	//
	// A handler receives an entry at most once however many routes named it, because two
	// routes naming the same handler means one destination, not two writes to it.
	delivered := make(map[string]bool, 4)
	for _, route := range r.routes {
		if !route.Matches(entry) {
			continue
		}
		route.Apply(&entry)
		for _, name := range route.Handlers {
			if delivered[name] {
				continue
			}
			h, ok := r.handlers[name]
			if !ok {
				continue
			}
			delivered[name] = true
			if entry.Level < h.level {
				continue
			}
			if err := h.handler.Handle(entry); err != nil {
				r.failures++
				fmt.Fprintf(os.Stderr, "toolbox: the %q log handler refused an entry: %v\n", name, err)
			}
		}
	}
}

// routedHandler is one handler plus the minimum level the configuration gave it.
type routedHandler struct {
	name    string
	handler Handler
	level   Level
}

func (h *routedHandler) options() map[string]any { return nil }

func closeAll(handlers []Handler) error {
	var firstErr error
	for _, handler := range handlers {
		if handler == nil {
			continue
		}
		if err := handler.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Logger returns a logger that tags its entries with a name.
//
// The name is what a route matches on, so it is the thing a deployment filters by to answer
// "which subsystem said this". It is the standard library's own convention — a name for the
// logger — so a caller reading it is not learning a second thing.
func Logger(name string) *slog.Logger {
	return slog.Default().With(slog.String("logger", name))
}

// WithWorkspace returns a context carrying the project an entry belongs to.
//
// The workspace is read from the context when an entry is handled, so a caller deep in a call
// does not have to thread it through every call to be able to log which project it was
// serving. An operator filtering by workspace gets the right lines without any of the call
// sites knowing that filtering exists.
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
