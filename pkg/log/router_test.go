package log_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Manu343726/toolbox/pkg/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The engine's whole job is deciding where an entry goes, so almost every test here writes an
// entry and asks which handlers saw it. The handler is a recording fake rather than a file,
// because a test about routing should fail on routing and not on a path.

// recorder is a handler that keeps what it was given.
type recorder struct {
	name    string
	mu      sync.Mutex
	entries []log.Entry
	err     error
	closed  bool
}

func (r *recorder) Name() string { return r.name }

func (r *recorder) Handle(entry log.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	return r.err
}

func (r *recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *recorder) seen() []log.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]log.Entry(nil), r.entries...)
}

func (r *recorder) messages() []string {
	entries := r.seen()
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Message)
	}
	return out
}

// The recorders are keyed by provider, so three handlers of one provider collide. The test
// above needs distinct recorders per handler name, which is what the provider below gives.
type namedProvider struct {
	mu       sync.Mutex
	byOption map[string]*recorder
}

func (p *namedProvider) ProviderID() string { return "recording" }

func (p *namedProvider) NewHandler(options map[string]any) (log.Handler, error) {
	name, _ := options["name"].(string)
	if name == "" {
		name = "unnamed"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byOption == nil {
		p.byOption = map[string]*recorder{}
	}
	if existing, ok := p.byOption[name]; ok {
		return existing, nil
	}
	handler := &recorder{name: name}
	p.byOption[name] = handler
	return handler, nil
}

// fanout builds a router whose handlers are individually addressable, which is what a test
// about routing needs: a test that cannot tell two handlers apart cannot say where an entry
// went.
func fanout(t *testing.T, configuration string) (*log.Router, map[string]*recorder) {
	t.Helper()
	router := log.NewRouter()
	provider := &namedProvider{}
	require.NoError(t, router.RegisterProvider(provider))
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
	require.NoError(t, router.Apply(cfg))
	return router, provider.byOption
}

func TestAnEntryReachesEveryHandlerItsRouteNames(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  first: {provider: recording, level: debug}
  second: {provider: recording, level: debug}
  unused: {provider: recording, level: debug}
routes:
  - handlers: [first, second]
`)
	require.Len(t, handlers, 3, "every declared handler is built, because a later route may name it")

	router.Dispatch(log.Entry{Message: "one", Level: log.LevelInfo})

	assert.Equal(t, []string{"one"}, handlers["first"].messages())
	assert.Equal(t, []string{"one"}, handlers["second"].messages())
	assert.Empty(t, handlers["unused"].messages(), "and a handler no route names receives nothing")
}

// A handler's own level is independent of the router's, because "everything to the file, only
// errors to the aggregator" is a normal thing to want.
func TestEachHandlerHasItsOwnMinimumLevel(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  everything: {provider: recording, level: debug}
  errors: {provider: recording, level: error}
routes:
  - handlers: [everything, errors]
`)
	router.Dispatch(log.Entry{Message: "a warning", Level: log.LevelWarn})
	router.Dispatch(log.Entry{Message: "a failure", Level: log.LevelError})

	assert.Equal(t, []string{"a warning", "a failure"}, handlers["everything"].messages())
	assert.Equal(t, []string{"a failure"}, handlers["errors"].messages(),
		"the handler that asked for errors got only errors")
}

// Every matching route contributes, so a specific route and the default both receive an entry
// that matches both. This is the difference between a fanout and a switch: an entry that is
// both "acme's" and "a warning" reaches acme's handler and the general one, and a deployment
// does not have to choose which of the two facts matters more.
func TestEveryMatchingRouteContributes(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  project: {provider: recording, level: debug}
  general: {provider: recording, level: debug}
routes:
  - name: one-project
    match: {workspace: acme}
    handlers: [project]
  - handlers: [general]
`)
	router.Dispatch(log.Entry{Message: "acme's", Level: log.LevelInfo, Workspace: "acme"})
	router.Dispatch(log.Entry{Message: "another's", Level: log.LevelInfo, Workspace: "other"})

	assert.Equal(t, []string{"acme's"}, handlers["project"].messages())
	assert.Equal(t, []string{"acme's", "another's"}, handlers["general"].messages(),
		"so the specific route and the default both received it, which is what makes this a fanout")
}

// A route adds attributes, which is the whole mechanism for making one project's logs
// findable among a machine's. The added attribute is on the entry that reaches the handler, not
// only on the configuration.
func TestARouteAddsAttributesToWhatItMatches(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  tagged: {provider: recording, level: debug}
routes:
  - name: tag-the-project
    match: {workspace: acme}
    add: {project: acme, tier: backend}
    handlers: [tagged]
`)
	router.Dispatch(log.Entry{Message: "stored", Level: log.LevelInfo, Workspace: "acme"})

	entries := handlers["tagged"].seen()
	require.Len(t, entries, 1)
	assert.Equal(t, "acme", entries[0].Attributes["project"],
		"so an operator filtering by project finds it, whichever process wrote it")
	assert.Equal(t, "backend", entries[0].Attributes["tier"])
}

// A route may add the workspace itself, and that reaches the entry's own field as well as its
// attributes — a backend that knows how to index a project should not have to dig for it.
func TestARouteMayTagTheWorkspaceItMatched(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  tagged: {provider: recording, level: debug}
routes:
  - match: {workspace: acme}
    add: {workspace: labelled}
    handlers: [tagged]
`)
	router.Dispatch(log.Entry{Message: "x", Level: log.LevelInfo, Workspace: "acme"})

	entries := handlers["tagged"].seen()
	require.Len(t, entries, 1)
	assert.Equal(t, "labelled", entries[0].Workspace,
		"the route relabels the entry, because that is what tagging a project means")
	assert.Equal(t, "labelled", entries[0].Attributes[log.WorkspaceKey])
}

// A route matches on any attribute, not only the workspace, because the interesting question
// is rarely "which subsystem" alone.
func TestARouteMatchesOnAnOrdinaryAttribute(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  slow: {provider: recording, level: debug}
  general: {provider: recording, level: debug}
routes:
  - match: {component: database}
    handlers: [slow]
  - handlers: [general]
`)
	router.Dispatch(log.Entry{Message: "a query", Level: log.LevelInfo,
		Attributes: map[string]string{"component": "database"}})
	router.Dispatch(log.Entry{Message: "a request", Level: log.LevelInfo,
		Attributes: map[string]string{"component": "http"}})

	assert.Equal(t, []string{"a query"}, handlers["slow"].messages())
	assert.Equal(t, []string{"a query", "a request"}, handlers["general"].messages(),
		"the query matched the specific route and the default, and both contributed")
}

// A handler named by two routes receives the entry once, because two routes naming one
// handler means one destination rather than two writes to it. Without this a line would be
// duplicated every time a deployment added a route that overlapped, which is exactly the kind
// of duplication nobody notices until a log is full of everything twice.
func TestAHandlerNamedByTwoRoutesReceivesAnEntryOnce(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  shared: {provider: recording, level: debug}
routes:
  - match: {workspace: acme}
    handlers: [shared]
  - handlers: [shared]
`)
	router.Dispatch(log.Entry{Message: "once", Level: log.LevelInfo, Workspace: "acme"})

	assert.Equal(t, []string{"once"}, handlers["shared"].messages())
}

// A route can state a minimum severity, so "only warnings from this project reach the
// aggregator" needs no separate handler.
func TestARouteMatchesOnSeverity(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  loud: {provider: recording, level: debug}
  general: {provider: recording, level: debug}
routes:
  - match: {level: warn}
    handlers: [loud]
  - handlers: [general]
`)
	router.Dispatch(log.Entry{Message: "chatter", Level: log.LevelInfo})
	router.Dispatch(log.Entry{Message: "trouble", Level: log.LevelError})

	assert.Equal(t, []string{"trouble"}, handlers["loud"].messages())
	assert.Equal(t, []string{"chatter", "trouble"}, handlers["general"].messages(),
		"and the error reached the general handler too, which first-match-wins could not express")
}

// The router's own level is the cheapest way to turn a deployment's logging up without editing
// every handler.
func TestTheRouterLevelAppliesBeforeAnyRoute(t *testing.T) {
	router, handlers := fanout(t, `
level: warn
handlers:
  everything: {provider: recording, level: debug}
routes:
  - handlers: [everything]
`)
	router.Dispatch(log.Entry{Message: "chatter", Level: log.LevelInfo})
	router.Dispatch(log.Entry{Message: "trouble", Level: log.LevelError})

	assert.Equal(t, []string{"trouble"}, handlers["everything"].messages())
}

// A handler that cannot write is counted and does not fail the caller. Logging that can break
// the thing it is describing is worse than logging that loses lines.
func TestAFailingHandlerDoesNotFailTheCaller(t *testing.T) {
	router := log.NewRouter()
	broken := &recorder{name: "broken", err: errors.New("the disk is full")}
	require.NoError(t, router.RegisterProvider(staticProvider{handler: broken}))

	require.NoError(t, router.Apply(log.Config{
		Level:    log.LevelDebug,
		Handlers: []log.HandlerConfig{{Name: "broken", Provider: "static", Level: log.LevelDebug}},
		Routes:   []log.Route{{Handlers: []string{"broken"}}},
	}))

	// No panic, no error, and the failure is counted so a deployment can be told.
	router.Dispatch(log.Entry{Message: "an entry", Level: log.LevelInfo})
	assert.Equal(t, 1, router.Failures())
}

// staticProvider always returns one handler, for the cases a test does not need to address by
// name.
type staticProvider struct{ handler log.Handler }

func (p staticProvider) ProviderID() string                             { return "static" }
func (p staticProvider) NewHandler(map[string]any) (log.Handler, error) { return p.handler, nil }

// A configuration naming a backend the deployment does not have is refused by name, with the
// ones it does have listed. A typo that recorded nothing would be a line nobody could explain.
func TestAnUnknownBackendIsRefusedWithTheOnesAvailable(t *testing.T) {
	router := log.NewRouter()
	require.NoError(t, router.RegisterProvider(&namedProvider{}))

	err := router.Apply(log.Config{
		Handlers: []log.HandlerConfig{{Name: "x", Provider: "syslog"}},
		Routes:   []log.Route{{Handlers: []string{"x"}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "syslog", "names what was asked for")
	assert.Contains(t, err.Error(), "recording", "and what is available")
}

// A route naming a handler no configuration declares is refused. A route naming nothing would
// record the entry nowhere, which is the one outcome a reader cannot diagnose.
func TestARouteNamingNoHandlerIsRefused(t *testing.T) {
	// Composed directly rather than through the helper, because the helper requires the
	// application to succeed and this test is about it failing.
	plain := log.NewRouter()
	require.NoError(t, plain.RegisterProvider(&namedProvider{}))
	err := plain.Apply(log.Config{
		Handlers: []log.HandlerConfig{{Name: "only", Provider: "recording"}},
		Routes:   []log.Route{{Handlers: []string{"only", "absent"}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absent", "and it names the handler nothing declares")
}

// A configuration is applied whole or not at all. A router half way through a new fanout
// would route some entries by the old rules and some by the new, and an entry's destination
// would depend on when it was written.
func TestAConfigurationIsAppliedWholeOrNotAtAll(t *testing.T) {
	router, _ := fanout(t, `
level: debug
handlers:
  first: {provider: recording, level: debug}
routes:
  - handlers: [first]
`)
	before := router.Config()

	require.Error(t, router.Apply(log.Config{
		Handlers: []log.HandlerConfig{{Name: "second", Provider: "absent"}},
		Routes:   []log.Route{{Handlers: []string{"second"}}},
	}))

	after := router.Config()
	assert.Equal(t, before.Handlers, after.Handlers, "the failed application changed nothing")
	assert.Equal(t, before.Routes, after.Routes)
}

// Replacing a fanout releases the handlers it replaced, and only after the swap, so an entry
// written during the change still reaches a handler that is open.
func TestReplacingAFanoutClosesTheOldHandlers(t *testing.T) {
	router := log.NewRouter()
	provider := &trackedProvider{}
	require.NoError(t, router.RegisterProvider(provider))
	require.NoError(t, router.Apply(log.Config{
		Handlers: []log.HandlerConfig{{Name: "first", Provider: "tracked"}},
		Routes:   []log.Route{{Handlers: []string{"first"}}},
	}))
	first := provider.last()
	require.False(t, first.isClosed(), "and it is open while it is the configured one")

	require.NoError(t, router.Apply(log.Config{
		Handlers: []log.HandlerConfig{{Name: "second", Provider: "tracked"}},
		Routes:   []log.Route{{Handlers: []string{"second"}}},
	}))
	assert.True(t, first.isClosed(), "so the replaced one is released")
	assert.False(t, provider.last().isClosed(), "and the new one is open")
}

// trackedProvider hands out handlers that remember whether they were closed.
type trackedProvider struct {
	mu      sync.Mutex
	created []*trackedHandler
}

func (p *trackedProvider) ProviderID() string { return "tracked" }

func (p *trackedProvider) NewHandler(map[string]any) (log.Handler, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	handler := &trackedHandler{}
	p.created = append(p.created, handler)
	return handler, nil
}

func (p *trackedProvider) last() *trackedHandler {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.created) == 0 {
		return nil
	}
	return p.created[len(p.created)-1]
}

type trackedHandler struct {
	mu     sync.Mutex
	closed bool
}

func (h *trackedHandler) Name() string { return "tracked" }
func (h *trackedHandler) Handle(log.Entry) error {
	return nil
}
func (h *trackedHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return nil
}
func (h *trackedHandler) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// Closing the router releases everything it holds, because a daemon that exits without closing
// its log file leaves a line buffered that a reader is waiting for.
func TestClosingTheRouterReleasesEveryHandler(t *testing.T) {
	router := log.NewRouter()
	provider := &trackedProvider{}
	require.NoError(t, router.RegisterProvider(provider))
	require.NoError(t, router.Apply(log.Config{
		Handlers: []log.HandlerConfig{{Name: "one", Provider: "tracked"}, {Name: "two", Provider: "tracked"}},
		Routes:   []log.Route{{Handlers: []string{"one", "two"}}},
	}))
	require.NoError(t, router.Close())
	assert.True(t, provider.created[0].isClosed())
	assert.True(t, provider.created[1].isClosed())
}

// The public Go API is slog. An entry written through a *slog.Logger reaches the fanout, with
// its message, its attributes, and its logger name — because a deployment's logs are one
// stream rather than one per library.
func TestAnEntryWrittenThroughSlogReachesTheFanout(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  all: {provider: recording, level: debug}
routes:
  - handlers: [all]
`)
	logger := slog.New(router.AsSlogHandler())
	logger.With(slog.String("logger", "knowledge")).Info("stored a source", "id", "n1", "bytes", 42)

	entries := handlers["all"].seen()
	require.Len(t, entries, 1)
	assert.Equal(t, "stored a source", entries[0].Message)
	assert.Equal(t, "knowledge", entries[0].Attributes["logger"],
		"the logger name is what a route matches to answer which subsystem said this")
	assert.Equal(t, "n1", entries[0].Attributes["id"])
	assert.Equal(t, "42", entries[0].Attributes["bytes"])
}

// A structured value is rendered rather than dropped, because dropping it loses the part of a
// log line a caller most often wanted.
func TestAStructuredValueIsRenderedRatherThanDropped(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  all: {provider: recording, level: debug}
routes:
  - handlers: [all]
`)
	slog.New(router.AsSlogHandler()).Info("a group", slog.Group("request",
		slog.String("method", "GET"), slog.Int("status", 200)))

	entries := handlers["all"].seen()
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Attributes["request"], "method=GET")
	assert.Contains(t, entries[0].Attributes["request"], "status=200")
}

// A workspace on the context reaches the entry, so a caller deep in a call does not have to
// thread the project through every signature to log which project it was serving.
func TestAWorkspaceOnTheContextReachesTheEntry(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  all: {provider: recording, level: debug}
routes:
  - match: {workspace: acme}
    handlers: [all]
`)
	ctx := log.WithWorkspace(context.Background(), "acme")
	slog.New(router.AsSlogHandler()).InfoContext(ctx, "served a request")

	entries := handlers["all"].seen()
	require.Len(t, entries, 1)
	assert.Equal(t, "acme", entries[0].Workspace)
}

// The handler reports honestly whether an entry would be routed, so a caller does not format
// entries that are discarded — and so the answer is about the deployment, not this handler's
// defaults.
func TestEnabledTellsTheTruthAboutTheDeployment(t *testing.T) {
	router, _ := fanout(t, `
level: warn
handlers:
  all: {provider: recording, level: debug}
routes:
  - handlers: [all]
`)
	handler := router.AsSlogHandler()

	assert.False(t, handler.Enabled(context.Background(), slog.LevelInfo),
		"the router's level is the deployment's, and it is warn")
	assert.True(t, handler.Enabled(context.Background(), slog.LevelError))
}

// Installing makes the router the process default, so a dependency that logs through slog
// lands in the same fanout without knowing the framework is here. The returned function puts
// the previous default back, because a global that outlives its test changes other tests.
func TestInstallingMakesTheRouterTheProcessDefault(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  all: {provider: recording, level: debug}
routes:
  - handlers: [all]
`)
	before := slog.Default()
	restore := router.Install()
	t.Cleanup(restore)

	// A caller that knows nothing about this framework, which is the whole claim: its
	// entries land in this deployment's fanout without it knowing the framework is here.
	slog.Info("from a dependency", "lib", "third-party")

	entries := handlers["all"].seen()
	require.Len(t, entries, 1)
	assert.Equal(t, "from a dependency", entries[0].Message)
	assert.Equal(t, "third-party", entries[0].Attributes["lib"])

	// The previous default is put back, and it is the previous one rather than merely a
	// different one: a global that outlives its test changes every test after it.
	restore()
	assert.Equal(t, before, slog.Default())
	assert.NotEqual(t, slog.New(router.AsSlogHandler()), slog.Default())
}

// A time that was not stated is stamped, so a handler never has to invent one and two entries
// are never written with a zero time that sorts to 1970.
func TestAnUnstampedEntryGetsTheClock(t *testing.T) {
	router, handlers := fanout(t, `
level: debug
handlers:
  all: {provider: recording, level: debug}
routes:
  - handlers: [all]
`)
	before := time.Now()
	router.Dispatch(log.Entry{Message: "x", Level: log.LevelInfo})

	entries := handlers["all"].seen()
	require.Len(t, entries, 1)
	assert.False(t, entries[0].Time.Before(before.Add(-time.Second)),
		"the entry carries a real time")
}

// The built-in providers are what a deployment has before it configures anything, and a
// deployment must be able to log before any subsystem has started.
func TestTheBuiltInProvidersWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "toolbox.log")

	router := log.NewRouter()
	require.NoError(t, router.RegisterProvider(log.NewStderr()))
	require.NoError(t, router.Apply(log.Config{
		Level:    log.LevelDebug,
		Handlers: []log.HandlerConfig{{Name: "file", Provider: "stderr", Options: map[string]any{"path": path}}},
		Routes:   []log.Route{{Handlers: []string{"file"}}},
	}))

	fixed := time.Date(2026, 9, 26, 10, 30, 0, 0, time.UTC)
	router.Dispatch(log.Entry{Time: fixed, Message: "a line", Level: log.LevelInfo,
		Logger: "knowledge", Workspace: "acme",
		Attributes: map[string]string{"id": "n1"}})
	require.NoError(t, router.Close())

	written, err := os.ReadFile(path)
	require.NoError(t, err)
	line := strings.TrimSpace(string(written))
	assert.Contains(t, line, "2026-09-26T10:30:00.000Z", "the entry's own time, not the write's")
	assert.Contains(t, line, "INFO")
	assert.Contains(t, line, "knowledge")
	assert.Contains(t, line, "a line")
	assert.Contains(t, line, "workspace=acme")
	assert.Contains(t, line, "id=n1")
}

// Attributes are sorted, so two runs of the same program produce lines a reader can diff.
//
// The time is fixed rather than read, because a line rendered a millisecond apart differs for
// reasons that have nothing to do with attribute order — and a test that fails for that
// reason is a test nobody trusts when it fails for the real one.
func TestAttributesAreSortedSoLinesCanBeDiffed(t *testing.T) {
	at := time.Date(2026, 9, 26, 10, 30, 0, 0, time.UTC)
	first := log.FormatEntry(log.Entry{
		Time: at, Level: log.LevelInfo, Message: "x",
		Attributes: map[string]string{"zebra": "1", "alpha": "2", "middle": "3"},
	}, nil)
	second := log.FormatEntry(log.Entry{
		Time: at, Level: log.LevelInfo, Message: "x",
		Attributes: map[string]string{"alpha": "2", "middle": "3", "zebra": "1"},
	}, nil)
	assert.Equal(t, first, second,
		"a line whose attribute order changed between runs is a line nobody can compare")
	assert.Contains(t, first, "alpha=2 middle=3 zebra=1", "and the order is the sorted one")
}

// decodeFanout reads the fanout a test writes as YAML, through the same reader a
// configuration file goes through, so the parser under test is the one being exercised rather
// than a map built to suit it.
func decodeFanout(t *testing.T, fanout string) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(fanout), &decoded), "the test's own fanout is valid YAML")
	return decoded
}
