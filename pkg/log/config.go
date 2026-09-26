package log

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

// Config is a deployment's fanout: which sinks exist, and which entries go to which of them.
//
// It is one structure read from one place — a configuration file, the environment, and the
// logger subsystem's RPC all produce this value, and the router is built from it and nothing
// else. Two configurations that could differ in shape would let a deployment believe it had
// one fanout when it had two.
type Config struct {
	// Level is the minimum an entry must reach to be routed at all.
	Level slog.Level
	// Handlers are the sinks, by the name routes refer to them.
	Handlers []HandlerConfig
	// Routes decide which sink receives which entry, in order.
	Routes []Route
}

// HandlerConfig declares one sink.
type HandlerConfig struct {
	// Name is what a route refers to this sink by. Two sinks with one name would make a route
	// ambiguous, so the second is refused.
	Name string
	// Provider is the backend that produces it: text, json, null, or a provider subsystem.
	Provider string
	// Level is the minimum this sink receives, independent of the router's, or nil when the
	// configuration did not say — which means the sink's own default, so that a deployment
	// saying "level: debug" and then not narrowing a sink is not quietly narrowed to info by
	// a default it never wrote.
	//
	// It is per sink because "everything to the file, only errors to the aggregator" is a
	// normal thing to want, and one router-wide level cannot express it. A level reaches the
	// provider as an option, which the provider passes to its slog.HandlerOptions, so the
	// filtering is the sink's own Enabled and not a filter wrapped around it.
	Level *slog.Level
	// Options are the provider's own settings, and their shape is the provider's business.
	Options map[string]any
}

// LevelOrInfo is the sink's minimum, or info when the configuration named none.
//
// A sink that named none is reporting that it has no opinion, and info is the severity a
// reader should assume in that case: it is the standard library's own zero value.
func (h HandlerConfig) LevelOrInfo() slog.Level {
	if h.Level == nil {
		return slog.LevelInfo
	}
	return *h.Level
}

// Route decides which sinks receive an entry.
type Route struct {
	// Name identifies the route in a diagnostic and in the configuration it came from.
	Name string
	// When the entry must have to match. All of a route's conditions must hold.
	When Match
	// Handlers are the names this route sends to. Every matching route contributes, so
	// "everything to the file, and this project's errors also to the aggregator" is
	// expressible; a sink named by two routes receives an entry once.
	Handlers []string
	// Add are attributes attached to whatever the route matches.
	//
	// This is what makes one project's logs findable among a machine's: a route matching the
	// project and adding its name tags every line the project produced, whichever process
	// produced it.
	Add map[string]string
}

// Match is a route's condition: a minimum level, and attributes an entry must carry.
//
// Conditions are matched against an entry's top-level attributes, which is what a caller
// writes and what a route names. A value nested inside a slog group is not matched: routing
// on it would mean walking group values here, and a route that needs the project it is
// filtering is better off naming the project.
type Match struct {
	// Level is the minimum severity, or nil when the route said none.
	//
	// It is a pointer because slog.LevelInfo is a real severity and not a zero value, and a
	// route that did not name a level must match every severity the router accepted rather
	// than quietly dropping the debug lines below info.
	Level *slog.Level
	// Attributes must all be present on the entry with these values. An empty map means no
	// condition, which is what makes a route the default.
	Attributes map[string]string
}

// Empty reports whether the match has no condition, which makes the route the default.
func (m Match) Empty() bool { return len(m.Attributes) == 0 && m.Level == nil }

// matches reports whether an entry satisfies the route, looking at the attributes bound to a
// logger with slog.With as well as the ones on the entry itself: both are attributes of the
// entry, and a caller writing slog.With("logger", "knowledge") means exactly what
// logger.Info(msg, "logger", "knowledge") means.
func (m Match) matches(ctx context.Context, bound []slog.Attr, record slog.Record) bool {
	if m.Level != nil && record.Level < *m.Level {
		return false
	}
	for key, want := range m.Attributes {
		if carries(bound, record, key, want) {
			continue
		}
		// A project may be carried on the context rather than as an attribute, because a
		// caller deep in a call should not have to pass it through every signature to log
		// which project it was serving. To a route the two spellings are the same entry.
		if key == WorkspaceKey && want != "" && want == WorkspaceFrom(ctx) {
			continue
		}
		return false
	}
	return true
}

// carries reports whether an entry has an attribute with that key and value. An entry with
// two attributes of the same key and the wanted one anywhere is a match: slog permits
// duplicate keys and the last one written is what a reader sees, so refusing would drop an
// entry over which of two writers ran last.
func carries(bound []slog.Attr, record slog.Record, key, want string) bool {
	found := false
	for _, attr := range bound {
		if attr.Key == key && attr.Value.String() == want {
			found = true
		}
	}
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == key && attr.Value.String() == want {
			found = true
			return false
		}
		return true
	})
	return found
}

// AtLeast returns a match for a minimum severity.
func AtLeast(level slog.Level) Match { return Match{Level: &level} }

// With returns a copy of the match with an added attribute condition, so conditions compose
// without a caller rebuilding the map.
func (m Match) With(key, value string) Match {
	attributes := make(map[string]string, len(m.Attributes)+1)
	for existing, current := range m.Attributes {
		attributes[existing] = current
	}
	attributes[key] = value
	m.Attributes = attributes
	return m
}

// Workspace returns a match for one project, so a route reads as the question it asks.
func Workspace(workspace string) Match {
	return Match{Attributes: map[string]string{WorkspaceKey: workspace}}
}

func (r Route) describe(index int) string {
	switch {
	case r.Name != "":
		return r.Name
	case r.When.Empty():
		return fmt.Sprintf("route %d (the default one)", index)
	default:
		return fmt.Sprintf("route %d", index)
	}
}

// text reads a condition or tag value, which is a string and only a string.
//
// A configuration file can write a bare number, and reading it as a number and rendering it
// as text is how a project whose directory is called "001" ends up with a route that can never
// match itself: the file says 001, YAML reads it as the integer 1, and the entry carries the
// string "001". Refusing it by name is the difference between a configuration that will not
// load and one that quietly routes nothing.
func text(value any, where string) (string, error) {
	written, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is %T, and a condition or a tag is text: write it in "+
			"quotes, or the route will never match the entry it was written for", where, value)
	}
	return written, nil
}

// refuseUnknown reports a key the reader does not understand, naming it and listing what it
// does accept.
//
// A misspelled key that is ignored is a fanout quietly not being the one that was written:
// a route called "routs" that was dropped leaves every entry going somewhere the deployment
// did not choose, with nothing to say so.
func refuseUnknown(where string, got map[string]any, accepted ...string) error {
	permitted := make(map[string]bool, len(accepted))
	for _, key := range accepted {
		permitted[key] = true
	}
	var unknown []string
	for key := range got {
		if !permitted[key] {
			unknown = append(unknown, strconv.Quote(key))
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	keys := append([]string(nil), accepted...)
	sort.Strings(keys)
	return fmt.Errorf("%s: unknown setting %s; it takes: %s",
		where, strings.Join(unknown, ", "), strings.Join(keys, ", "))
}

// LevelOf reads a level from a configuration value, which is a string in a file and a
// slog.Level in code.
//
// It refuses anything else rather than defaulting, because a sink configured with a
// misspelled level would silently record everything or nothing, and both are worse than a
// configuration that will not load.
func LevelOf(value any) (slog.Level, error) {
	switch typed := value.(type) {
	case slog.Level:
		return typed, nil
	case slog.Leveler:
		return typed.Level(), nil
	case nil:
		return slog.LevelInfo, nil
	case string:
		return ParseLevel(typed)
	default:
		return slog.LevelInfo, fmt.Errorf(
			"%v is not a level; it must be one of: debug, info, warn, error", value)
	}
}

// ParseLevel reads a level name, refusing anything else by name.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf(
			"%q is not a level; it must be one of: debug, info, warn, error", value)
	}
}

// ParseConfig reads a fanout from a generic map, which is what a configuration file, an
// environment and an RPC payload all reduce to.
//
// The shapes are plain maps rather than a struct per source because the sources are not the
// interesting part: one reader, one structure, and a key nobody understands refused by name —
// the same rule the deployment's own configuration follows, and for the same reason.
func ParseConfig(raw map[string]any) (Config, error) {
	if raw == nil {
		return Config{Level: slog.LevelInfo}, nil
	}
	if err := refuseUnknown("the logging configuration", raw, "level", "handlers", "routes"); err != nil {
		return Config{}, err
	}
	level, present := raw["level"]
	if !present {
		return Config{}, fmt.Errorf("the logging configuration needs a level")
	}
	parsed, err := LevelOf(level)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{Level: parsed}

	if handlers, present := raw["handlers"]; present {
		table, ok := handlers.(map[string]any)
		if !ok {
			return Config{}, fmt.Errorf("logging.handlers must be a mapping of names to handlers")
		}
		for name, value := range table {
			declared, err := parseHandler(name, value)
			if err != nil {
				return Config{}, err
			}
			cfg.Handlers = append(cfg.Handlers, declared)
		}
		sort.Slice(cfg.Handlers, func(i, j int) bool { return cfg.Handlers[i].Name < cfg.Handlers[j].Name })
	}

	if routes, present := raw["routes"]; present {
		list, ok := routes.([]any)
		if !ok {
			return Config{}, fmt.Errorf("logging.routes must be a list")
		}
		for i, value := range list {
			route, err := parseRoute(i, value)
			if err != nil {
				return Config{}, err
			}
			cfg.Routes = append(cfg.Routes, route)
		}
	}
	return cfg, nil
}

func parseHandler(name string, value any) (HandlerConfig, error) {
	table, ok := value.(map[string]any)
	if !ok {
		return HandlerConfig{}, fmt.Errorf("the %q handler must be a mapping", name)
	}
	provider, present := table["provider"]
	if !present {
		return HandlerConfig{}, fmt.Errorf("the %q handler needs a provider: which backend writes to it", name)
	}
	if err := refuseUnknown(fmt.Sprintf("the %q handler", name), table, "provider", "level", "options"); err != nil {
		return HandlerConfig{}, err
	}
	declared := HandlerConfig{Name: name, Provider: fmt.Sprint(provider)}
	if level, present := table["level"]; present {
		parsed, err := LevelOf(level)
		if err != nil {
			return HandlerConfig{}, fmt.Errorf("the %q handler: %w", name, err)
		}
		declared.Level = &parsed
	}
	if options, present := table["options"]; present {
		settings, ok := options.(map[string]any)
		if !ok {
			return HandlerConfig{}, fmt.Errorf("the %q handler's options must be a mapping", name)
		}
		declared.Options = settings
	}
	return declared, nil
}

func parseRoute(index int, value any) (Route, error) {
	table, ok := value.(map[string]any)
	if !ok {
		return Route{}, fmt.Errorf("route %d must be a mapping", index)
	}
	if err := refuseUnknown(fmt.Sprintf("route %d", index), table, "name", "match", "handlers", "add"); err != nil {
		return Route{}, err
	}
	route := Route{}
	if name, present := table["name"]; present {
		route.Name = fmt.Sprint(name)
	}
	if conditions, present := table["match"]; present {
		raw, ok := conditions.(map[string]any)
		if !ok {
			return Route{}, fmt.Errorf("route %d's match must be a mapping", index)
		}
		for key, wanted := range raw {
			if key == "level" {
				parsed, err := LevelOf(wanted)
				if err != nil {
					return Route{}, fmt.Errorf("route %d: %w", index, err)
				}
				route.When.Level = &parsed
				continue
			}
			value, err := text(wanted, fmt.Sprintf("route %d's match on %q", index, key))
			if err != nil {
				return Route{}, err
			}
			if route.When.Attributes == nil {
				route.When.Attributes = make(map[string]string, len(raw))
			}
			route.When.Attributes[key] = value
		}
	}
	handlers, present := table["handlers"]
	if !present {
		return Route{}, fmt.Errorf("route %d must name the handlers it sends entries to", index)
	}
	names, ok := handlers.([]any)
	if !ok {
		return Route{}, fmt.Errorf("route %d's handlers must be a list of names", index)
	}
	for _, name := range names {
		route.Handlers = append(route.Handlers, fmt.Sprint(name))
	}
	if add, present := table["add"]; present {
		attributes, ok := add.(map[string]any)
		if !ok {
			return Route{}, fmt.Errorf("route %d's add must be a mapping", index)
		}
		route.Add = make(map[string]string, len(attributes))
		for key, value := range attributes {
			text, err := text(value, fmt.Sprintf("route %d's add of %q", index, key))
			if err != nil {
				return Route{}, err
			}
			route.Add[key] = text
		}
	}
	return route, nil
}

// Provider produces a sink for one backend.
//
// A provider is what a subsystem implements to offer a logging backend, and it declares itself
// to the framework's catalog under the role loghandler — the way a parser or an adapter
// declares itself. The interface is one method returning the standard library's handler,
// because that is all a backend is: a sink the standard library already knows how to call.
//
// A provider must honour the level option, passing it to its slog.HandlerOptions, so that
// filtering happens inside the sink's own Enabled rather than in something wrapped around it.
// Most providers are a few lines. A deployment does not need a subsystem to write to a file, a
// socket or a pipe, because the standard library's handlers already do that over any
// io.Writer. A provider earns a subsystem when the backend is something only it can do.
type Provider interface {
	// ProviderID is the identifier a configuration names to select this backend.
	ProviderID() string
	// NewHandler builds a sink from the options the configuration gave it.
	NewHandler(options map[string]any) (slog.Handler, error)
}

// ProviderFunc adapts a function to a Provider, so a backend that is a single expression does
// not need a type.
type ProviderFunc struct {
	ID      string
	BuildFn func(map[string]any) (slog.Handler, error)
}

// ProviderID implements Provider.
func (p ProviderFunc) ProviderID() string { return p.ID }

// NewHandler implements Provider.
func (p ProviderFunc) NewHandler(options map[string]any) (slog.Handler, error) {
	return p.BuildFn(options)
}
