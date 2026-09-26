package log_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Manu343726/toolbox/pkg/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The engine's whole job is deciding where an entry goes, so almost every test here writes an
// entry and asks which handlers saw it.
//
// The handlers are the standard library's own JSON handler over a buffer rather than a fake
// that keeps records: a test then reads the lines a real sink would have written, so a routing
// assertion and a formatting assertion are the same assertion, and nothing here has to be
// kept in step with how slog renders a record.

// captured is a buffer a sink writes to, safe to read while entries are being written.
type captured struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *captured) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// lines returns what was written, one entry per line, as the key/value pairs a JSON sink
// emitted.
func (c *captured) lines(t *testing.T) []map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(c.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var decoded map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &decoded),
			"a sink wrote %q, which is not a JSON entry", line)
		out = append(out, decoded)
	}
	return out
}

// messages returns just the messages, which is what most routing assertions care about.
func (c *captured) messages(t *testing.T) []string {
	t.Helper()
	lines := c.lines(t)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, fmt.Sprint(line["msg"]))
	}
	return out
}

// recording is a test provider handing out a JSON sink per declared handler name, so one
// provider serves a fanout with several destinations and a test can still tell them apart.
type recording struct {
	mu     sync.Mutex
	byName map[string]*captured
	// brokenOn names the sinks that cannot write, so a test can break one destination of a
	// fanout and leave the rest working.
	brokenOn map[string]error
}

func (p *recording) ProviderID() string { return "recording" }

func (p *recording) NewHandler(options map[string]any) (slog.Handler, error) {
	name, _ := options["name"].(string)
	if name == "" {
		name = "unnamed"
	}
	level := slog.LevelDebug
	if options["level"] != nil {
		parsed, err := log.LevelOf(options["level"])
		if err != nil {
			return nil, err
		}
		level = parsed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byName == nil {
		p.byName = map[string]*captured{}
	}
	sink, ok := p.byName[name]
	if !ok {
		sink = &captured{}
		p.byName[name] = sink
	}
	inner := slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: level})
	if err, ok := p.brokenOn[name]; ok {
		return brokenHandler{inner: inner, err: err}, nil
	}
	return slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: level}), nil
}

// brokenHandler is a sink that cannot write. A logging backend failing is a real condition and
// the only honest way to test what happens is to make one fail.
type brokenHandler struct {
	inner slog.Handler
	err   error
}

func (h brokenHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h brokenHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.err
}

func (h brokenHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return brokenHandler{inner: h.inner.WithAttrs(attrs), err: h.err}
}

func (h brokenHandler) WithGroup(name string) slog.Handler {
	return brokenHandler{inner: h.inner.WithGroup(name), err: h.err}
}

// fanout builds a router from YAML whose sinks are individually addressable, which is what a
// test about routing needs: a test that cannot tell two sinks apart cannot say where an entry
// went.
func fanout(t *testing.T, configuration string) (*log.Router, map[string]*captured) {
	t.Helper()
	registry, provider := recordingRegistry()
	return build(t, registry, provider, configuration)
}

func recordingRegistry() (*log.Registry, *recording) {
	provider := &recording{}
	registry := log.NewRegistry()
	if err := registry.Register(provider); err != nil {
		panic(err)
	}
	return registry, provider
}

func build(t *testing.T, registry *log.Registry, provider *recording, configuration string) (*log.Router, map[string]*captured) {
	t.Helper()
	cfg, err := log.ParseConfig(decodeFanout(t, configuration))
	require.NoError(t, err)
	// Each declared handler tells the provider its own name, so one provider serves them all
	// and a test can still address them individually.
	for i := range cfg.Handlers {
		if cfg.Handlers[i].Options == nil {
			cfg.Handlers[i].Options = map[string]any{}
		}
		cfg.Handlers[i].Options["name"] = cfg.Handlers[i].Name
	}
	router, err := registry.Build(cfg)
	require.NoError(t, err)
	return router, provider.byName
}

func TestAnEntryReachesEveryHandlerItsRouteNames(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  audit:
    provider: recording
  file:
    provider: recording
routes:
  - name: everything
    handlers: [audit, file]
`)

	slog.New(router).Info("stored a source")

	assert.Equal(t, []string{"stored a source"}, handlers["audit"].messages(t))
	assert.Equal(t, []string{"stored a source"}, handlers["file"].messages(t))
}

func TestEachHandlerHasItsOwnMinimumLevel(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
  errors:
    provider: recording
    level: error
routes:
  - name: everything to the file
    handlers: [file]
  - name: only problems to the operator
    handlers: [errors]
`)

	logger := slog.New(router)
	logger.Debug("connecting")
	logger.Info("stored a source")
	logger.Warn("cache is stale")
	logger.Error("source is unreadable")

	// The file took everything, the operator only the problem: the one router-wide level
	// cannot say that, and the level is each sink's own Enabled.
	assert.Equal(t, []string{
		"connecting", "stored a source", "cache is stale", "source is unreadable",
	}, handlers["file"].messages(t))
	assert.Equal(t, []string{"source is unreadable"}, handlers["errors"].messages(t))
}

func TestEveryMatchingRouteContributes(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
  pager:
    provider: recording
routes:
  - name: everything to the file
    handlers: [file]
  - name: and problems to the operator
    match:
      level: error
    handlers: [pager]
`)

	slog.New(router).Error("source is unreadable")

	// Both routes match, so the entry goes to both. First-match-wins could not express
	// "everything to the file and this project's problems also to the pager".
	assert.Equal(t, []string{"source is unreadable"}, handlers["file"].messages(t))
	assert.Equal(t, []string{"source is unreadable"}, handlers["pager"].messages(t))
}

func TestARouteAddsAttributesToWhatItMatches(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  aggregate:
    provider: recording
routes:
  - name: the operator's view
    match:
      level: warn
    handlers: [aggregate]
    add:
      team: platform
`)

	slog.New(router).Info("stored a source")
	slog.New(router).Warn("cache is stale")

	lines := handlers["aggregate"].lines(t)
	require.Len(t, lines, 1)
	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, "platform", lines[0]["team"])
}

func TestARouteMayTagTheWorkspaceItMatched(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  all:
    provider: recording
  acme:
    provider: recording
routes:
  - name: everything
    handlers: [all]
  - name: one project's own file
    match:
      workspace: acme
    handlers: [acme]
    add:
      project: acme
`)

	ctx := log.WithWorkspace(context.Background(), "acme")
	slog.New(router).InfoContext(ctx, "stored a source")

	// The tag is the point: on a shared machine this is how a project's lines are found, and
	// it is attached by the route that matched, not by the caller knowing tags exist.
	acme := handlers["acme"].lines(t)
	require.Len(t, acme, 1)
	assert.Equal(t, "acme", acme[0]["project"])
	assert.Equal(t, "acme", acme[0]["workspace"])
	assert.Len(t, handlers["all"].messages(t), 1)
}

func TestARouteMatchesOnAnOrdinaryAttribute(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  knowledge:
    provider: recording
  everything:
    provider: recording
routes:
  - name: one subsystem's own
    match:
      logger: knowledge
    handlers: [knowledge]
  - name: and everywhere
    handlers: [everything]
`)

	slog.New(router).With("logger", "knowledge").Info("stored a source")
	slog.New(router).With("logger", "workflow").Info("advanced a run")

	assert.Equal(t, []string{"stored a source"}, handlers["knowledge"].messages(t))
	assert.Equal(t, []string{"stored a source", "advanced a run"}, handlers["everything"].messages(t))
}

func TestAHandlerNamedByTwoRoutesReceivesAnEntryOnce(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
routes:
  - name: everything
    handlers: [file]
  - name: and problems again
    match:
      level: error
    handlers: [file]
`)

	slog.New(router).Error("source is unreadable")

	// A line duplicated because two routes overlapped is a line nobody can count.
	assert.Equal(t, []string{"source is unreadable"}, handlers["file"].messages(t))
}

func TestARouteMatchesOnSeverity(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  problems:
    provider: recording
routes:
  - name: warn and above
    match:
      level: warn
    handlers: [problems]
`)

	logger := slog.New(router)
	logger.Debug("connecting")
	logger.Info("stored a source")
	logger.Warn("cache is stale")
	logger.Error("source is unreadable")

	assert.Equal(t, []string{"cache is stale", "source is unreadable"}, handlers["problems"].messages(t))
}

func TestTheRouterLevelAppliesBeforeAnyRoute(t *testing.T) {
	router, handlers := fanout(t, `
level: warn
handlers:
  file:
    provider: recording
routes:
  - name: everything
    handlers: [file]
`)

	logger := slog.New(router)
	logger.Info("stored a source")
	logger.Warn("cache is stale")

	assert.Equal(t, []string{"cache is stale"}, handlers["file"].messages(t))
}

func TestAFailingHandlerDoesNotFailTheCaller(t *testing.T) {
	registry, provider := recordingRegistry()
	provider.brokenOn = map[string]error{"broken": assert.AnError}
	router, handlers := build(t, registry, provider, `
level: debug
handlers:
  broken:
    provider: recording
  file:
    provider: recording
routes:
  - name: everything
    handlers: [broken, file]
`)

	// A sink that cannot write must not be able to fail the thing it is describing: the
	// answer to Log is whether the work happened, and the work happened.
	logger := slog.New(router)
	require.NotPanics(t, func() { logger.Info("stored a source") })

	// The failure is counted rather than swallowed silently, so a deployment whose logging is
	// broken hears about it on standard error instead of by noticing an empty log.
	assert.Equal(t, 1, router.Failures())
	assert.Empty(t, handlers["broken"].messages(t))
	assert.Equal(t, []string{"stored a source"}, handlers["file"].messages(t))
}

func TestAnUnknownBackendIsRefusedWithTheOnesAvailable(t *testing.T) {
	registry := log.NewRegistry()

	_, err := registry.Build(log.Config{
		Level:    slog.LevelInfo,
		Handlers: []log.HandlerConfig{{Name: "file", Provider: "rotatting"}},
	})

	require.Error(t, err)
	// The available ones are listed, because a typo that recorded nothing would otherwise be
	// a line nobody could explain.
	assert.Contains(t, err.Error(), `no logging provider for "rotatting"`)
	assert.Contains(t, err.Error(), "json")
	assert.Contains(t, err.Error(), "null")
	assert.Contains(t, err.Error(), "text")
}

func TestARouteNamingNoHandlerIsRefused(t *testing.T) {
	_, err := log.NewRouter(log.RouterOptions{
		Level:    slog.LevelInfo,
		Handlers: map[string]slog.Handler{"file": discarding()},
		Routes:   []log.Route{{Name: "typo", Handlers: []string{"files"}}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"files"`)
	assert.Contains(t, err.Error(), `the route "typo"`)
}

func TestTwoHandlersWithOneNameAreRefused(t *testing.T) {
	registry := log.NewRegistry()

	_, err := registry.Build(log.Config{
		Level: slog.LevelInfo,
		Handlers: []log.HandlerConfig{
			{Name: "file", Provider: log.ProviderText, Options: map[string]any{}},
			{Name: "file", Provider: log.ProviderJSON, Options: map[string]any{}},
		},
		Routes: []log.Route{{Handlers: []string{"file"}}},
	})

	// Two sinks with one name would make a route naming it ambiguous, and an entry going to
	// one of them by accident is a log nobody can trust.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
}

func TestAConfigurationIsAppliedWholeOrNotAtAll(t *testing.T) {
	registry, _ := recordingRegistry()

	// The second handler's provider is not registered, so building must produce no router at
	// all. A router half way through a new fanout would send some entries by the old rules
	// and some by the new, and where a line ended up would depend on when it was written.
	router, err := registry.Build(log.Config{
		Level: slog.LevelInfo,
		Handlers: []log.HandlerConfig{
			{Name: "file", Provider: "recording"},
			{Name: "elsewhere", Provider: "not-registered"},
		},
		Routes: []log.Route{{Handlers: []string{"file"}}},
	})

	require.Error(t, err)
	assert.Nil(t, router)
}

func TestAnEntryWrittenThroughSlogReachesTheFanout(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
routes:
  - name: everything
    handlers: [file]
`)

	// The whole point of slog being the API: a caller holds a *slog.Logger and nothing here,
	// and its entry is shaped exactly as any other slog entry is.
	slog.New(router).Error("source is unreadable", "id", 42, "attempt", 3)

	lines := handlers["file"].lines(t)
	require.Len(t, lines, 1)
	assert.Equal(t, "source is unreadable", lines[0]["msg"])
	assert.Equal(t, "ERROR", lines[0]["level"])
	assert.Equal(t, float64(42), lines[0]["id"])
	assert.Equal(t, float64(3), lines[0]["attempt"])
}

func TestAnEntryKeepsItsOwnShapeApartFromTheRoutesTags(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
routes:
  - name: everything
    handlers: [file]
    add:
      deployment: laptop
`)

	slog.New(router).With("subsystem", "knowledge").Info("stored a source", "id", 7)

	lines := handlers["file"].lines(t)
	require.Len(t, lines, 1)
	assert.Equal(t, "stored a source", lines[0]["msg"])
	assert.Equal(t, "knowledge", lines[0]["subsystem"])
	assert.Equal(t, float64(7), lines[0]["id"])
	assert.Equal(t, "laptop", lines[0]["deployment"])
	// slog renders the time itself, which is the point of using its handler: the standard
	// library decides what a record looks like on the wire, not this package.
	assert.NotEmpty(t, lines[0]["time"])
}

func TestAWorkspaceOnTheContextReachesTheEntry(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  acme:
    provider: recording
routes:
  - name: one project
    match:
      workspace: acme
    handlers: [acme]
`)

	// A caller deep in a call logs which project it was serving without threading the project
	// through every signature to do it.
	ctx := log.WithWorkspace(context.Background(), "acme")
	slog.New(router).InfoContext(ctx, "stored a source")

	assert.Equal(t, []string{"stored a source"}, handlers["acme"].messages(t))
}

func TestEnabledTellsTheTruthAboutTheDeployment(t *testing.T) {
	router, _ := fanout(t, `
level: warn
handlers:
  file:
    provider: recording
routes:
  - name: everything
    handlers: [file]
`)

	ctx := context.Background()
	// A caller deciding whether to format an expensive attribute asks this, so a yes that
	// then drops the entry wastes work and a no that keeps it is worse.
	assert.False(t, router.Enabled(ctx, slog.LevelInfo))
	assert.True(t, router.Enabled(ctx, slog.LevelWarn))
	assert.True(t, router.Enabled(ctx, slog.LevelError))
}

func TestTheDefaultLoggersLogsReachTheFanout(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
routes:
  - name: everything
    handlers: [file]
`)

	// slog.SetDefault is the whole integration: a dependency the framework does not control
	// logs through the standard library's package-level logger, and lands in this
	// deployment's fanout, having never heard of Toolbox.
	previous := slog.Default()
	slog.SetDefault(slog.New(router))
	t.Cleanup(func() { slog.SetDefault(previous) })

	slog.Default().Info("a dependency said this")
	slog.With("logger", "third-party").Info("another one")

	lines := handlers["file"].lines(t)
	require.Len(t, lines, 2)
	assert.Equal(t, "a dependency said this", lines[0]["msg"])
	assert.Equal(t, "third-party", lines[1]["logger"])
}

func TestBoundAttributesNestTheWaySlogNestsThem(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
routes:
  - name: everything
    handlers: [file]
`)

	// WithAttrs and WithGroup are delegated to each sink, so a slog.With nests exactly as it
	// would with a plain JSON handler — including a group inside a group, which is the
	// standard library's nesting and not this package's reconstruction of it.
	slog.New(router).
		With("subsystem", "knowledge").
		WithGroup("source").
		With("id", 7).
		Info("stored a source")

	lines := handlers["file"].lines(t)
	require.Len(t, lines, 1)
	assert.Equal(t, "knowledge", lines[0]["subsystem"])
	source, ok := lines[0]["source"].(map[string]any)
	require.True(t, ok, "a group should nest as an object, got %#v", lines[0]["source"])
	assert.Equal(t, float64(7), source["id"])
}

func TestTheBuiltInProvidersWrite(t *testing.T) {
	// The two built-ins exist because a person and a collector read the output differently,
	// and neither format is this package's to invent.
	for _, testCase := range []struct {
		provider string
		contains []string
	}{
		{provider: log.ProviderText, contains: []string{"level=INFO", "msg=\"stored a source\"", "id=7"}},
		{provider: log.ProviderJSON, contains: []string{`"level":"INFO"`, `"msg":"stored a source"`, `"id":7`}},
	} {
		t.Run(testCase.provider, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "output.log")
			registry := log.NewRegistry()
			router, err := registry.Build(log.Config{
				Level: slog.LevelInfo,
				Handlers: []log.HandlerConfig{{
					Name:     "file",
					Provider: testCase.provider,
					Options:  map[string]any{"path": path},
				}},
				Routes: []log.Route{{Handlers: []string{"file"}}},
			})
			require.NoError(t, err)

			slog.New(router).Info("stored a source", "id", 7)

			written, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, want := range testCase.contains {
				assert.Contains(t, string(written), want)
			}
		})
	}
}

func TestTheNullProviderWritesNothing(t *testing.T) {
	registry := log.NewRegistry()
	router, err := registry.Build(log.Config{
		Level: slog.LevelDebug,
		Handlers: []log.HandlerConfig{{
			Name:     "quiet",
			Provider: log.ProviderNull,
		}},
		Routes: []log.Route{{Handlers: []string{"quiet"}}},
	})
	require.NoError(t, err)

	// A configuration can name a destination and switch it off without editing the routes
	// that reference it, which is the difference between turning one off and discovering the
	// route pointing at it was the only reason anything was recorded.
	logger := slog.New(router)
	require.NotPanics(t, func() {
		logger.Info("stored a source")
		logger.Error("source is unreadable")
	})
	assert.False(t, router.Enabled(context.Background(), slog.LevelError))
}

func TestARelativePathResolvesAgainstTheDeclaringConfigDirectory(t *testing.T) {
	project := t.TempDir()
	registry := log.NewRegistry()
	registry.BaseDir = project

	router, err := registry.Build(log.Config{
		Level: slog.LevelInfo,
		Handlers: []log.HandlerConfig{{
			Name:     "project",
			Provider: log.ProviderJSON,
			Options:  map[string]any{"path": "logs/project.log"},
		}},
		Routes: []log.Route{{Handlers: []string{"project"}}},
	})
	require.NoError(t, err)

	slog.New(router).Info("stored a source")

	// "A file local to the project" means a path relative to the file that declared it, not
	// to wherever the process happened to start.
	written, err := os.ReadFile(filepath.Join(project, "logs", "project.log"))
	require.NoError(t, err)
	assert.Contains(t, string(written), "stored a source")
}

func TestTheSinkLevelReachesTheProvidersOwnOptions(t *testing.T) {
	registry, provider := recordingRegistry()
	router, handlers := build(t, registry, provider, `
level: debug
handlers:
  file:
    provider: recording
    level: error
    options:
      level: warn
routes:
  - name: everything
    handlers: [file]
`)

	// An option the provider's own configuration set wins over the handler's level, because
	// an explicit per-handler option is the more specific statement.
	logger := slog.New(router)
	logger.Info("stored a source")
	logger.Warn("cache is stale")
	logger.Error("source is unreadable")

	assert.Equal(t, []string{"cache is stale", "source is unreadable"}, handlers["file"].messages(t))
}

func TestConcurrentEntriesReachEveryRoute(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
  acme:
    provider: recording
routes:
  - name: everything
    handlers: [file]
  - name: one project
    match:
      workspace: acme
    handlers: [acme]
`)

	const writers, each = 8, 25
	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger := slog.New(router).With("subsystem", "knowledge")
			for i := 0; i < each; i++ {
				logger.Info("stored a source")
				// A bound router and the one it came from are used at once, because that is
				// what slog.With in a long-lived component does.
				slog.New(router.WithAttrs([]slog.Attr{slog.String("logger", "plain")})).Info("plain entry")
			}
		}()
	}
	wg.Wait()

	assert.Len(t, handlers["file"].messages(t), writers*each*2)
}

func TestAnUnknownSettingIsRefusedByName(t *testing.T) {
	registry := log.NewRegistry()

	_, err := registry.Build(log.Config{
		Level: slog.LevelInfo,
		Handlers: []log.HandlerConfig{{
			Name:     "file",
			Provider: log.ProviderJSON,
			Options:  map[string]any{"rotation": "daily"},
		}},
		Routes: []log.Route{{Handlers: []string{"file"}}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `no setting "rotation"`)
}

func TestAConditionValueMustBeText(t *testing.T) {
	_, err := log.ParseConfig(decodeFanout(t, `
level: info
handlers:
  file:
    provider: text
routes:
  - name: a project whose directory is a number
    match:
      workspace: 001
    handlers: [file]
`))

	// A configuration file can write a bare number, and reading it as a number and rendering
	// it as text is how a project called "001" ends up with a route that can never match
	// itself: the file says 001, the reader says 1, and the entry carries "001".
	require.Error(t, err)
	assert.Contains(t, err.Error(), `match on "workspace" is int`)
	assert.Contains(t, err.Error(), "quotes")
}

func TestAParseLevelRefusesAnythingElse(t *testing.T) {
	for _, name := range []string{"debug", "info", "warn", "warning", "error", " INFO "} {
		_, err := log.ParseLevel(name)
		assert.NoError(t, err, "%q is a level", name)
	}
	for _, name := range []string{"trace", "critical", "3", "off"} {
		_, err := log.ParseLevel(name)
		// A sink configured with a misspelled level would silently record everything or
		// nothing, and both are worse than a configuration that will not load.
		assert.Error(t, err, "%q is not a level", name)
	}
}

func TestAnUnknownConfigurationKeyIsRefused(t *testing.T) {
	_, err := log.ParseConfig(decodeFanout(t, `
level: info
handlers:
  file:
    provider: text
routs:
  - handlers: [file]
`))

	// A misspelled key that is ignored is a fanout quietly not being the one that was
	// written, with nothing to say so.
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown setting "routs"`)
	assert.Contains(t, err.Error(), "handlers, level, routes")
}

func TestAHandlerWithoutAProviderIsRefused(t *testing.T) {
	_, err := log.ParseConfig(decodeFanout(t, `
level: info
handlers:
  file:
    level: info
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), `the "file" handler needs a provider`)
}

func TestARouteWithoutHandlersIsRefused(t *testing.T) {
	_, err := log.ParseConfig(decodeFanout(t, `
level: info
handlers:
  file:
    provider: text
routes:
  - name: nowhere
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "route 0 must name the handlers")
}

func TestAMatchComposes(t *testing.T) {
	// Conditions compose without a caller rebuilding the map, and the result is the question
	// the route asks.
	match := log.AtLeast(slog.LevelWarn).With("logger", "knowledge").With("subsystem", "knowledge")

	assert.False(t, match.Empty())
	require.NotNil(t, match.Level)
	assert.Equal(t, slog.LevelWarn, *match.Level)
	assert.Equal(t, map[string]string{"logger": "knowledge", "subsystem": "knowledge"}, match.Attributes)
	// A route that said no conditions is the default one, and a route that said "at least
	// info" has a condition, even though slog.LevelInfo is the zero value of a level.
	assert.True(t, log.Match{}.Empty())
	assert.False(t, log.AtLeast(slog.LevelInfo).Empty())
	assert.False(t, log.Workspace("acme").Empty())
}

func TestARouterIsNotMutatedBySlogWith(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
routes:
  - name: everything
    handlers: [file]
`)

	// slog handlers are immutable — WithAttrs returns a new one — so a component holding a
	// bound logger must not change what every other component's entries look like.
	bound := slog.New(router).With("subsystem", "knowledge")
	slog.New(router).Info("unbound entry")
	bound.Info("bound entry")

	lines := handlers["file"].lines(t)
	require.Len(t, lines, 2)
	_, firstIsBound := lines[0]["subsystem"]
	assert.False(t, firstIsBound, "the unbound logger must not pick up the bound attributes")
	assert.Equal(t, "knowledge", lines[1]["subsystem"])
}

func TestARouteWithNoIntroducedHandlerRoutesNothing(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  file:
    provider: recording
routes:
  - name: everything
    handlers: [file]
  - name: the same file again
    match:
      level: debug
    handlers: [file]
  - name: and nothing of its own
    handlers: [file]
`)

	slog.New(router).Info("stored a source")

	assert.Equal(t, []string{"stored a source"}, handlers["file"].messages(t))
}

func TestASinkNoRouteNamesReceivesNothing(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  used:
    provider: recording
  unused:
    provider: recording
routes:
  - name: the fanout as configured
    handlers: [used]
`)

	// A sink that no route names is not part of the fanout, whatever it is — a deployment
	// cannot say where its entries go and find one destination nobody asked for.
	slog.New(router).Info("stored a source")

	assert.Equal(t, []string{"stored a source"}, handlers["used"].messages(t))
	assert.Empty(t, handlers["unused"].messages(t))
}

func decodeFanout(t *testing.T, fanout string) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(fanout), &decoded), "the test's own fanout is valid YAML")
	return decoded
}

func discarding() slog.Handler {
	return slog.NewTextHandler(discard{}, &slog.HandlerOptions{Level: slog.LevelDebug})
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
