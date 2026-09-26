package log

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The built-in providers are the standard library's own handlers, named so a configuration can
// select them. They are here rather than in subsystems because a deployment must be able to
// log before any subsystem has started, and because the standard library already provides
// them: a provider wrapping slog.NewTextHandler would add nothing.

// Built-in provider identifiers.
const (
	// ProviderText is the standard library's text handler, which is what a person reads at a
	// terminal.
	ProviderText = "text"
	// ProviderJSON is the standard library's JSON handler, which is what a machine reads.
	ProviderJSON = "json"
	// ProviderNull writes to io.Discard, so a configuration can switch a destination off
	// without deleting the entry that names it.
	ProviderNull = "null"
)

// BuiltinProviders returns the providers every router has, so a deployment can name them
// without a subsystem for each.
//
// The two real ones differ in who reads the output, which is why both exist: a person at a
// terminal wants the text handler's layout, and a log collector wants JSON it can parse. That
// choice belongs to a configuration and not to this package.
func BuiltinProviders() []Provider {
	return []Provider{
		ProviderFunc{ID: ProviderText, BuildFn: func(options map[string]any) (slog.Handler, error) {
			writer, err := openPath("", options)
			if err != nil {
				return nil, err
			}
			if writer == nil {
				writer = os.Stderr
			}
			return NewStdlib(options, writer, false)
		}},
		ProviderFunc{ID: ProviderJSON, BuildFn: func(options map[string]any) (slog.Handler, error) {
			writer, err := openPath("", options)
			if err != nil {
				return nil, err
			}
			if writer == nil {
				writer = os.Stderr
			}
			return NewStdlib(options, writer, true)
		}},
		ProviderFunc{ID: ProviderNull, BuildFn: func(options map[string]any) (slog.Handler, error) {
			// The standard library's handler over io.Discard, with a level nothing reaches.
			// That way switching a destination off is a setting rather than a type that
			// implements slog.Handler and does nothing, which could drift from how the
			// standard library handles a record.
			settings := &slog.HandlerOptions{Level: slog.Level(math.MaxInt)}
			return slog.NewTextHandler(io.Discard, settings), nil
		}},
	}
}

// NewStdlib builds one of the standard library's two handlers over a writer.
//
// json selects which. Every setting it reads is a slog.HandlerOptions field, so a configured
// level, source and attribute renaming all take effect through the standard library's own
// code — and a provider that brings its own writer, such as a rotating file, uses this rather
// than parsing the same configuration shape a second time and drifting from it.
func NewStdlib(options map[string]any, out io.Writer, json bool) (slog.Handler, error) {
	settings := &slog.HandlerOptions{Level: slog.LevelDebug}
	for key, value := range options {
		switch key {
		case "path":
			// The writer was chosen by the caller, so a path here is a setting this provider
			// has already acted on. It is not an error, because a configuration names a
			// handler the same way whichever backend it turns out to be.
		case "level":
			// The deployment's minimum goes to the standard library's own option, so the
			// filtering is the handler's Enabled and a caller deciding whether to format an
			// expensive attribute learns the answer from the same call slog makes.
			level, err := LevelOf(value)
			if err != nil {
				return nil, err
			}
			settings.Level = level
		case "source":
			settings.AddSource = truthy(value)
		case "replace_attr":
			renames, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("replace_attr must be a mapping of old attribute to new name")
			}
			// One function over the whole mapping rather than a chain built in map order, so
			// two attributes renamed onto the same name cannot resolve differently between
			// runs.
			renamed := make(map[string]string, len(renames))
			for old, to := range renames {
				renamed[old] = fmt.Sprint(to)
			}
			settings.ReplaceAttr = func(groups []string, attr slog.Attr) slog.Attr {
				if len(groups) != 0 {
					return attr
				}
				if to, ok := renamed[attr.Key]; ok {
					attr.Key = to
				}
				return attr
			}
		default:
			return nil, fmt.Errorf(
				"this provider has no setting %q; it takes: level, path, replace_attr, source", key)
		}
	}
	if json {
		return slog.NewJSONHandler(out, settings), nil
	}
	return slog.NewTextHandler(out, settings), nil
}

// openPath opens the file a configuration named, creating the directory it lives in.
//
// It returns a nil writer when the configuration named none, and the caller supplies the
// default. Creating the directory is deliberate: a project saying its logs go to
// "logs/project.log" means a directory called logs in that project, and a deployment that
// refuses to start over a directory it could have created is harder to run than one that
// makes it.
func openPath(baseDir string, options map[string]any) (io.Writer, error) {
	value, present := options["path"]
	if !present {
		return nil, nil
	}
	path := fmt.Sprint(value)
	if path == "" || path == "-" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create the directory for %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return file, nil
}
func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "true" || typed == "yes" || typed == "1"
	default:
		return false
	}
}

// Registry holds the backends a deployment can name, and builds the sinks a configuration
// declares.
//
// It is the gathering point: the built-in providers plus whatever subsystems the deployment
// started, in one place, so a configuration names a backend and does not care which kind it
// is.
type Registry struct {
	// BaseDir is what a relative path in a configuration is resolved against — the project
	// or directory the declaring file belongs to.
	//
	// It is what makes "this project's logs go to a file in this project" mean that. The
	// alternatives both put a project's log somewhere it has nothing to do with: relative to
	// wherever the process happened to start, or relative to the configuration directory
	// rather than the project it configures.
	BaseDir string

	providers map[string]Provider
}

// NewRegistry creates a registry holding the built-in providers.
func NewRegistry() *Registry {
	registry := &Registry{providers: make(map[string]Provider, 4)}
	for _, provider := range BuiltinProviders() {
		_ = registry.Register(provider)
	}
	return registry
}

// Register adds a backend the configuration may name.
func (r *Registry) Register(provider Provider) error {
	if provider == nil {
		return fmt.Errorf("a logging provider is required")
	}
	id := strings.TrimSpace(provider.ProviderID())
	if id == "" {
		return fmt.Errorf("a logging provider needs an identifier")
	}
	if r.providers == nil {
		r.providers = make(map[string]Provider, 4)
	}
	if _, exists := r.providers[id]; exists {
		return fmt.Errorf("a logging provider for %q is already registered", id)
	}
	r.providers[id] = provider
	return nil
}

// Providers returns the identifiers this registry can name, sorted.
func (r *Registry) Providers() []string {
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Build turns a configuration into a router, building every sink it declares.
//
// A provider named that nobody registered is refused, with the available ones listed. A typo
// that recorded nothing would be a line nobody could explain, and the only sign would be the
// absence of the logs a deployment expected.
func (r *Registry) Build(cfg Config) (*Router, error) {
	handlers := make(map[string]slog.Handler, len(cfg.Handlers))
	for _, declared := range cfg.Handlers {
		if _, exists := handlers[declared.Name]; exists {
			return nil, fmt.Errorf("two handlers are named %q; a route naming it would be ambiguous", declared.Name)
		}
		provider, ok := r.providers[declared.Provider]
		if !ok {
			available := r.Providers()
			if len(available) == 0 {
				available = []string{"none"}
			}
			return nil, fmt.Errorf("no logging provider for %q; this deployment has: %s",
				declared.Provider, strings.Join(available, ", "))
		}
		options, err := r.resolve(declared)
		if err != nil {
			return nil, err
		}
		handler, err := provider.NewHandler(options)
		if err != nil {
			return nil, fmt.Errorf("the %q handler: %w", declared.Name, err)
		}
		if handler == nil {
			return nil, fmt.Errorf("the %q provider returned no handler", declared.Provider)
		}
		handlers[declared.Name] = handler
	}
	router, err := NewRouter(RouterOptions{Level: cfg.Level, Handlers: handlers, Routes: cfg.Routes})
	if err != nil {
		return nil, err
	}
	router.config = cfg
	return router, nil
}

// resolve gives a provider its own options, with the two the framework owns applied: the
// deployment's relative path made absolute against the file that declared it, and the sink's
// minimum as a level the provider hands to its slog.HandlerOptions.
func (r *Registry) resolve(declared HandlerConfig) (map[string]any, error) {
	options := make(map[string]any, len(declared.Options)+2)
	for key, value := range declared.Options {
		options[key] = value
	}
	// A level the provider's own options set wins: an explicit setting inside the provider's
	// block is the more specific statement about that sink.
	if _, present := options["level"]; !present && declared.Level != nil {
		options["level"] = *declared.Level
	}
	// The path is made absolute here rather than in each provider, so a provider that brings
	// its own writer — a rotating file, say — resolves "a file in this project" the same way
	// the built-in ones do without knowing about the directory a configuration was read from.
	if name, isString := options["path"].(string); isString && name != "" && name != "-" && !filepath.IsAbs(name) {
		options["path"] = filepath.Join(r.BaseDir, name)
	}
	return options, nil
}
