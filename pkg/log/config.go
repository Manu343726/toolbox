package log

import (
	"fmt"
	"sort"
	"strings"
)

// Config is a deployment's fanout: which handlers exist, and which entries go to which of
// them.
//
// It is one structure read from one place — the configuration file, the environment, and the
// logger subsystem's RPC all produce this value, and the router is built from it and nothing
// else. Two configurations that could differ in shape would let a deployment believe it had
// one fanout when it had two.
type Config struct {
	// Level is the minimum an entry must reach to be routed at all. It is the cheapest way to
	// turn a deployment's logging up without editing every handler.
	Level Level
	// Handlers are the backends, by the name routes refer to them.
	Handlers []HandlerConfig
	// Routes decide which handler receives which entry, evaluated in order.
	Routes []Route
}

// HandlerConfig declares one backend.
type HandlerConfig struct {
	// Name is what a route refers to this handler by. Two handlers with one name would make
	// a route ambiguous, so the router refuses the second.
	Name string
	// Provider is the backend that produces it: stderr, null, or a provider subsystem.
	Provider string
	// Level is the minimum entry this handler receives, independent of the router's.
	//
	// It is per handler because "everything to the file, only errors to the aggregator" is a
	// normal thing to want and a router-wide level cannot express it.
	Level Level
	// Options are the provider's own settings, and their shape is the provider's business.
	Options map[string]any
}

// Route decides which handlers receive an entry.
type Route struct {
	// Name identifies the route in a diagnostic and in the configuration it came from.
	Name string
	// When the entry must have to match. All of a route's conditions must hold.
	When Match
	// Handlers are the names this route sends to.
	//
	// Every matching route contributes, so several routes may name the same handler and it
	// receives the entry once. A route with no handlers is a way of saying "this entry goes
	// nowhere", and a configuration with no default route is a deployment that has decided
	// not to record.
	Handlers []string
	// Add are attributes attached to whatever the route matches.
	//
	// This is what makes one project's logs findable among a machine's: a route matching the
	// project and adding its name tags every line the project produced, whichever process or
	// which caller produced it.
	Add map[string]string
}

// Default reports whether the route matches everything, which makes it the fallback.
//
// A configuration is expected to end with one. A configuration that does not is not an error
// — a deployment may genuinely want to route nothing — but the router says so on a line, so
// "my logs are empty" has an explanation in the output rather than only in the reader's
// head.
func (r Route) Default() bool { return len(r.When.Attributes) == 0 && r.When.Level == nil }

// Matches reports whether an entry satisfies the route's conditions.
func (r Route) Matches(entry Entry) bool {
	if r.When.Level != nil && entry.Level < *r.When.Level {
		return false
	}
	for key, want := range r.When.Attributes {
		got, present := entry.Attributes[key]
		if !present && key == WorkspaceKey {
			got, present = entry.Workspace, entry.Workspace != ""
		}
		if !present || got != want {
			return false
		}
	}
	return true
}

// Apply attaches the route's attributes to an entry.
//
// A workspace attribute is also set on the entry's own field, so a backend that knows how to
// index a project does not have to dig through attributes to find it.
func (r Route) Apply(entry *Entry) {
	for key, value := range r.Add {
		if entry.Attributes == nil {
			entry.Attributes = make(map[string]string, len(r.Add))
		}
		entry.Attributes[key] = value
		if key == WorkspaceKey {
			entry.Workspace = value
		}
	}
}

func (r Route) describe() string {
	if r.Name != "" {
		return r.Name
	}
	if r.Default() {
		return "the default route"
	}
	return "an unnamed route"
}

// WorkspaceKey is the attribute a route matches and adds to name the project an entry belongs
// to.
//
// It is a reserved key: an entry carries the workspace as its own field, and a route that
// adds it is tagging entries rather than overwriting a field a caller set. Everything else
// is an ordinary attribute.
const WorkspaceKey = "workspace"

// Match is a route's condition: a minimum level, and attributes an entry must carry.
type Match struct {
	// Level is the minimum severity, or nil for no condition.
	Level *Level
	// Attributes must all be present on the entry with these values. An empty map means no
	// condition, which is what makes a route the default.
	Attributes map[string]string
}

// AtLeast returns a match for a minimum level.
func AtLeast(level Level) Match { return Match{Level: &level} }

// WithAttribute returns a match for an attribute value.
func WithAttribute(key, value string) Match {
	return Match{Attributes: map[string]string{key: value}}
}

// With returns a copy of the match with an added attribute, so conditions compose without a
// caller rebuilding the map.
func (m Match) With(key, value string) Match {
	attributes := make(map[string]string, len(m.Attributes)+1)
	for existing, current := range m.Attributes {
		attributes[existing] = current
	}
	attributes[key] = value
	m.Attributes = attributes
	return m
}

// ParseConfig reads a fanout from a generic map, which is what a configuration file, an
// environment and an RPC payload all reduce to.
//
// The shapes are plain maps rather than a struct per source because the sources are not the
// interesting part: one reader, one structure, and a key nobody understands refused by name —
// the same rule the deployment's own configuration follows, and for the same reason.
func ParseConfig(raw map[string]any) (Config, error) {
	cfg := Config{Level: LevelInfo}
	if raw == nil {
		return cfg, nil
	}
	level, ok := raw["level"]
	if !ok {
		return Config{}, fmt.Errorf("the logging configuration needs a level")
	}
	parsed, err := ParseLevel(fmt.Sprint(level))
	if err != nil {
		return Config{}, err
	}
	cfg.Level = parsed

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
	provider, ok := table["provider"]
	if !ok {
		return HandlerConfig{}, fmt.Errorf("the %q handler needs a provider: which backend writes to it", name)
	}
	declared := HandlerConfig{Name: name, Provider: fmt.Sprint(provider), Level: LevelInfo}
	if level, present := table["level"]; present {
		parsed, err := ParseLevel(fmt.Sprint(level))
		if err != nil {
			return HandlerConfig{}, fmt.Errorf("the %q handler: %w", name, err)
		}
		declared.Level = parsed
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
	route := Route{}
	if name, present := table["name"]; present {
		route.Name = fmt.Sprint(name)
	}
	if match, present := table["match"]; present {
		conditions, ok := match.(map[string]any)
		if !ok {
			return Route{}, fmt.Errorf("route %d's match must be a mapping", index)
		}
		for key, wanted := range conditions {
			if key == "level" {
				parsed, err := ParseLevel(fmt.Sprint(wanted))
				if err != nil {
					return Route{}, fmt.Errorf("route %d: %w", index, err)
				}
				route.When.Level = &parsed
				continue
			}
			if route.When.Attributes == nil {
				route.When.Attributes = make(map[string]string, len(conditions))
			}
			route.When.Attributes[key] = fmt.Sprint(wanted)
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
			route.Add[key] = fmt.Sprint(value)
		}
	}
	return route, nil
}

// Format renders a handler's options for a diagnostic, sorted so two runs of a deployment
// describe the same thing in the same order.
func Format(options map[string]any) string {
	if len(options) == 0 {
		return ""
	}
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, options[key]))
	}
	return strings.Join(parts, " ")
}

func sortStrings(values []string) { sort.Strings(values) }

func joinOr(values []string, empty string) string {
	if len(values) == 0 {
		return empty
	}
	return strings.Join(values, ", ")
}
