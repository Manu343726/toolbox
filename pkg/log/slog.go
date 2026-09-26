package log

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"
)

// Time is when an entry was produced. It is a type of its own so a test can state one, and so
// a backend is handed a value rather than a shape it has to know.
type Time = time.Time

// TimeNow reads the clock. It exists so a test can replace the whole clock by replacing this
// one function, rather than every handler taking a clock it has to be given.
var TimeNow = time.Now

// HandlerOption configures how a Router is installed as the standard library's handler.
type HandlerOption func(*slog.HandlerOptions)

// WithSource reports the file and line an entry was logged from.
//
// Off by default, because it costs a stack walk on every entry and most deployments read
// their logs by message and attribute. A deployment debugging one subsystem turns it on there
// rather than for everything.
func WithSource(on bool) HandlerOption {
	return func(options *slog.HandlerOptions) { options.AddSource = on }
}

// WithLevel maps the standard library's levels onto this package's, so a caller who wants
// everything at debug does not have to know this package's numbering.
func WithLevel(level slog.Leveler) HandlerOption {
	return func(options *slog.HandlerOptions) { options.Level = level }
}

// AsSlogHandler returns the router as the standard library's handler, with options applied.
//
// This is the whole of the standard library's involvement: a caller uses slog.Logger and this
// decides where an entry goes. Making the handler obtainable this way, rather than exposing a
// bespoke Logger type, is what lets a dependency that logs through slog reach the same fanout
// without knowing this package exists.
func (r *Router) AsSlogHandler(options ...HandlerOption) slog.Handler {
	settings := &slog.HandlerOptions{Level: slog.LevelDebug}
	for _, apply := range options {
		apply(settings)
	}
	return &slogHandler{router: r, options: settings}
}

// Install makes the router the process-wide default logger and returns a function that puts
// the previous default back.
//
// It is the one call that makes the framework's dependencies log into the deployment's fanout:
// slog.SetDefault is what any library in the process ends up using, whether or not it knows
// the framework is here. The returned function exists because a test that installs a router
// must not leave it installed — a global that outlives its test is how one test changes
// another's behaviour.
func (r *Router) Install(options ...HandlerOption) func() {
	previous := slog.Default()
	slog.SetDefault(slog.New(r.AsSlogHandler(options...)))
	return func() { slog.SetDefault(previous) }
}

// slogHandler is the slog.Handler in front of a Router.
type slogHandler struct {
	router  *Router
	options *slog.HandlerOptions
	// carried are attributes added to every entry, from WithAttrs.
	carried []slog.Attr
	// groups is the group path this handler nests under, from WithGroup.
	groups []string
}

// Enabled reports whether an entry at a level would be routed.
//
// It consults the router's own minimum as well as the handler options, so a caller asking
// whether logging is enabled is told the truth about the deployment rather than about this
// handler's defaults. A handler that said yes and then dropped everything would make a caller
// format entries that are discarded.
func (h *slogHandler) Enabled(_ context.Context, level slog.Level) bool {
	if level < h.options.Level.Level() {
		return false
	}
	return levelFromSlog(level) >= h.router.minimum()
}

// Handle turns one record into an entry and routes it.
func (h *slogHandler) Handle(ctx context.Context, record slog.Record) error {
	entry := Entry{
		Time:    record.Time,
		Level:   levelFromSlog(record.Level),
		Message: record.Message,
		// Read from the context, so a caller deep in a call does not have to thread the
		// project through every signature to log which project it was serving. An operator
		// filtering by project gets the right lines without any call site knowing that
		// filtering exists.
		Workspace: WorkspaceFrom(ctx),
	}
	if entry.Time.IsZero() {
		entry.Time = TimeNow()
	}
	prefix := ""
	if len(h.groups) > 0 {
		prefix = strings.Join(h.groups, ".") + "."
	}
	for _, attr := range h.carried {
		entry.set(prefix+attr.Key, attr.Value)
	}
	record.Attrs(func(attr slog.Attr) bool {
		entry.set(prefix+attr.Key, attr.Value)
		return true
	})
	if record.PC != 0 {
		entry.Source = sourceOf(record.PC)
	}
	h.router.Dispatch(entry)
	// An error is never returned. A log line is not worth failing a call over, and a handler
	// that cannot write must not be able to fail the thing it is describing. The router has
	// already counted the failure and said so.
	return nil
}

// WithAttrs returns a handler that adds attributes to every entry.
//
// The attributes are applied when the entry is written rather than merged into a record
// later, so an entry's attributes are the ones it was logged with. Merging at write time
// would make them depend on when the entry was dispatched.
func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return &slogHandler{
		router:  h.router,
		options: h.options,
		carried: append(append([]slog.Attr(nil), h.carried...), attrs...),
		groups:  h.groups,
	}
}

// WithGroup returns a handler that nests attributes under a group.
//
// Groups are flattened with dots rather than nested maps, because every backend here writes
// flat key/value pairs and a backend that had to understand nesting would be a worse backend.
func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &slogHandler{
		router:  h.router,
		options: h.options,
		carried: h.carried,
		groups:  append(append([]string(nil), h.groups...), name),
	}
}

// WithName is slog's own hook for a logger name, and it is an attribute here.
//
// The name is what a route matches on to answer "which subsystem said this", so it is the
// standard library's convention rather than a second thing to learn.
func (h *slogHandler) WithName(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.WithAttrs([]slog.Attr{slog.String("logger", name)})
}

func (e *Entry) set(key string, value slog.Value) {
	if e.Attributes == nil {
		e.Attributes = make(map[string]string, 8)
	}
	e.Attributes[key] = renderValue(value)
}

// renderValue turns a value into something a backend can write.
//
// A structured value is rendered as JSON rather than dropped, because dropping it loses the
// part of a log line a caller most often wanted. Anything the value's own formatting cannot
// express falls back to Go's, which is lossy but never empty.
func renderValue(value slog.Value) string {
	switch value.Kind() {
	case slog.KindGroup:
		parts := make([]string, 0, len(value.Group()))
		for _, attr := range value.Group() {
			parts = append(parts, attr.Key+"="+renderValue(attr.Value))
		}
		return "{" + strings.Join(parts, " ") + "}"
	case slog.KindAny:
		return fmt.Sprint(value.Any())
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindTime:
		return value.Time().Format(time.RFC3339)
	default:
		return value.String()
	}
}

// sourceOf reads the call site from a program counter, skipping this package's own frames.
//
// Without the skip, every entry would name this file rather than the caller's, which would
// make a source-annotated log useless for the only thing anybody turns it on for.
func sourceOf(pc uintptr) Source {
	frames := runtime.CallersFrames([]uintptr{pc})
	for {
		frame, more := frames.Next()
		if frame.File != "" && !strings.HasSuffix(frame.File, "pkg/log/slog.go") {
			return Source{File: frame.File, Line: frame.Line, Function: frame.Function}
		}
		if !more {
			return Source{}
		}
	}
}

// minimum is the router's own level.
func (r *Router) minimum() Level {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.level
}
