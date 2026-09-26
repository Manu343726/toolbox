package logger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/log"
	"github.com/Manu343726/toolbox/subsystems/logger"
	loggerv1 "github.com/Manu343726/toolbox/subsystems/logger/loggerv1"
	"github.com/Manu343726/toolbox/subsystems/logger/loggerv1/loggerv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The subsystem is the RPC boundary, so most of what is worth testing is that the boundary
// does not change anything: an entry sent over the wire reaches the sinks a route names, and
// an entry written in process reaches the same ones. The tests read the files a real sink
// wrote, because a fanout is only observable where something was recorded.

// at is the clock every test's entries claim, so nothing asserts that a time is roughly now.
var at = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func TestAnEntrySentOverTheWireReachesTheSinksARouteNames(t *testing.T) {
	service, files := service(t, defaultFanout(t))

	response, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "stored a source",
		Level:   loggerv1.LogLevel_LOG_LEVEL_INFO,
	}))

	require.NoError(t, err)
	// The caller is owed an answer, and "no error" does not say whether anything recorded
	// its entry: a record matching no route is delivered successfully to nobody.
	assert.True(t, response.Msg.GetRouted())
	assert.Equal(t, []string{"file"}, response.Msg.GetHandlers())
	assert.Contains(t, readLines(t, files["file"])[0]["msg"], "stored a source")
}

func TestAnEntryFromTheWireLandsWhereAnInProcessEntryWould(t *testing.T) {
	router, files := router(t, defaultFanout(t))
	service := serve(t, router)

	// Both go through one router, which is the whole reason the deployment injects it: two
	// routers would be two fanouts, and a project whose routes were configured one way would
	// find the agent's entries going somewhere else.
	_, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "from the wire",
		Logger:  "knowledge",
	}))
	require.NoError(t, err)
	slog.New(router).Info("in process", "logger", "knowledge")

	lines := readLines(t, files["file"])
	require.Len(t, lines, 2)
	assert.Equal(t, "from the wire", lines[0]["msg"])
	assert.Equal(t, "knowledge", lines[0]["logger"])
	assert.Equal(t, "in process", lines[1]["msg"])
	assert.Equal(t, "knowledge", lines[1]["logger"])
}

func TestARouteMatchesOnTheComponentTheEntryNames(t *testing.T) {
	service, files := service(t, log.Config{
		Level: slog.LevelInfo,
		Handlers: []log.HandlerConfig{
			{Name: "file", Provider: log.ProviderJSON, Options: map[string]any{"path": "machine.log"}},
			{Name: "knowledge", Provider: log.ProviderJSON, Options: map[string]any{"path": "knowledge.log"}},
		},
		Routes: []log.Route{
			{Name: "one subsystem's own", When: log.Match{Attributes: map[string]string{"logger": "knowledge"}},
				Handlers: []string{"knowledge"}},
			{Name: "everything", Handlers: []string{"file"}},
		},
	})

	_, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "advanced a run", Logger: "workflow",
	}))
	require.NoError(t, err)
	_, err = service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "stored a source", Logger: "knowledge",
	}))
	require.NoError(t, err)

	// "Everything the knowledge subsystem said" is a route, not a special case in the
	// router, and it works the same whether the entry came over the wire or not.
	require.Len(t, readLines(t, files["knowledge"]), 1)
	assert.Equal(t, "stored a source", readLines(t, files["knowledge"])[0]["msg"])
	assert.Len(t, readLines(t, files["file"]), 2)
}

func TestARouteThatTagsMustSendSomewhere(t *testing.T) {
	_, err := log.NewRouter(log.RouterOptions{
		Level:    slog.LevelInfo,
		Handlers: map[string]slog.Handler{"file": discarding()},
		Routes: []log.Route{
			{Name: "everything", Handlers: []string{"file"}},
			{Name: "a project's own", When: log.Workspace("acme"),
				Handlers: []string{"file"}, Add: map[string]string{"project": "acme"}},
		},
	})

	// A route that tags entries and sends them nowhere is a route that does not do what it
	// says, and the only sign would be a project whose logs cannot be found among the
	// deployment's. The message says which route, so the fix is obvious.
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"a project's own"`)
	assert.Contains(t, err.Error(), "already sent to by an earlier route")
}

func TestAnEntryCarriesItsAttributes(t *testing.T) {
	service, files := service(t, defaultFanout(t))

	_, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "stored a source",
		Level:   loggerv1.LogLevel_LOG_LEVEL_ERROR,
		Attributes: []*loggerv1.LogValue{
			{Key: "id", Value: "42"},
			{Key: "attempt", Value: "3"},
		},
	}))
	require.NoError(t, err)

	entry := readLines(t, files["file"])[0]
	assert.Equal(t, "ERROR", entry["level"])
	assert.Equal(t, "42", entry["id"])
	assert.Equal(t, "3", entry["attempt"])
}

func TestAnEntryRecordsTheTimeTheCallerClaimed(t *testing.T) {
	service, files := service(t, defaultFanout(t))

	// The clock is injected, so the entry says when this test said it was written rather
	// than when the test happened to run.
	_, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "stored a source",
	}))
	require.NoError(t, err)

	assert.Equal(t, at.Format(time.RFC3339Nano), readLines(t, files["file"])[0]["time"])
}

func TestAnEntryThatMatchesNoRouteSaysSo(t *testing.T) {
	router, files := router(t, log.Config{
		Level: slog.LevelInfo,
		Handlers: []log.HandlerConfig{{
			Name: "file", Provider: log.ProviderJSON, Options: map[string]any{"path": "out.log"},
		}},
		Routes: []log.Route{{
			Name: "only the agent's entries", When: log.Workspace("acme"),
			Handlers: []string{"file"},
		}},
	})
	service := serve(t, router)

	response, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "someone else's work",
	}))
	require.NoError(t, err)

	// Not an error, and not silence either: the caller is told its entry reached nobody,
	// which is the one logging failure the log itself cannot report.
	assert.False(t, response.Msg.GetRouted())
	assert.Empty(t, response.Msg.GetHandlers())
	assert.Empty(t, files["file"].lines(t))
	assert.Equal(t, 1, router.Unrouted())
}

func TestAnEntryWithNoMessageIsRefused(t *testing.T) {
	service, _ := service(t, defaultFanout(t))

	for _, message := range []string{"", "   "} {
		_, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
			Message: message,
		}))

		require.Error(t, err)
		// A line with nothing in it is a line a reader cannot read, and it is the caller's
		// mistake rather than a deployment problem.
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
}

func TestAnAttributeWithNoKeyIsRefused(t *testing.T) {
	service, _ := service(t, defaultFanout(t))

	_, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message:    "stored a source",
		Attributes: []*loggerv1.LogValue{{Key: "  ", Value: "42"}},
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestTheRecordedProjectTagsLaterEntries(t *testing.T) {
	service, files := service(t, defaultFanout(t))

	confirmation, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
	}))
	require.NoError(t, err)
	// The answer names what was recorded, so a caller does not have to remember what it
	// asked for or wonder whether the next entry will be tagged with it.
	assert.Equal(t, "acme", confirmation.Msg.GetWorkspace())

	_, err = service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "stored a source",
	}))
	require.NoError(t, err)

	// The tag lands in the project's own file, which is the point of tagging: on a shared
	// machine this is how one project's lines are found among the deployment's.
	entry := readLines(t, files["project"])[0]
	assert.Equal(t, "acme", entry["workspace"])
	assert.Equal(t, "acme", entry["project"])
	// And the entry still reached the machine-wide file, because that route matched it too.
	assert.Equal(t, "acme", readLines(t, files["file"])[0]["workspace"])
}

func TestAnEntryMayNameItsOwnProjectOverTheRecordedOne(t *testing.T) {
	service, files := service(t, defaultFanout(t))

	_, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
	}))
	require.NoError(t, err)
	_, err = service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message:   "stored a source",
		Workspace: "globex",
	}))
	require.NoError(t, err)

	// Two agents on one deployment, each working for a different project, and neither
	// finding its entries filed under the other's.
	assert.Equal(t, "globex", readLines(t, files["file"])[0]["workspace"])
}

func TestGetConfigReportsTheFanoutInUse(t *testing.T) {
	router, _ := router(t, defaultFanout(t))
	service := serve(t, router)

	response, err := service.GetConfig(context.Background(), connect.NewRequest(&loggerv1.GetConfigRequest{}))
	require.NoError(t, err)

	assert.Equal(t, loggerv1.LogLevel_LOG_LEVEL_INFO, response.Msg.GetLevel())
	// What a caller is told is the fanout actually running, not a description of it: the
	// router keeps what it was built from, so the two cannot drift.
	require.Len(t, response.Msg.GetHandlers(), 2)
	assert.Equal(t, "file", response.Msg.GetHandlers()[0].GetName())
	assert.Equal(t, log.ProviderJSON, response.Msg.GetHandlers()[0].GetProvider())
	require.Len(t, response.Msg.GetRoutes(), 2)
	assert.Equal(t, "everything", response.Msg.GetRoutes()[0].GetName())
	assert.Equal(t, []string{"file"}, response.Msg.GetRoutes()[0].GetHandlers())
	// A route that named no level reports no level, rather than reporting info: the
	// difference between "matches everything the deployment accepts" and "matches info and
	// above" is the difference between a default and a decision.
	assert.Equal(t, loggerv1.LogLevel_LOG_LEVEL_UNSPECIFIED, response.Msg.GetRoutes()[0].GetLevel())
	assert.Equal(t, map[string]string{"workspace": "acme"}, response.Msg.GetRoutes()[1].GetAttributes())
	assert.Equal(t, map[string]string{"project": "acme"}, response.Msg.GetRoutes()[1].GetAdd())
}

func TestGetConfigReportsWhatTheDeploymentCanRouteTo(t *testing.T) {
	router, _ := router(t, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) {
		options.Providers = []string{log.ProviderJSON, "logfile"}
	})

	response, err := service.GetConfig(context.Background(), connect.NewRequest(&loggerv1.GetConfigRequest{}))
	require.NoError(t, err)

	// A caller has to know what it is allowed to send somewhere, or it is guessing.
	assert.Equal(t, []string{log.ProviderJSON, "logfile"}, response.Msg.GetProviders())
}

func TestGetConfigCountsWhatReachedNobody(t *testing.T) {
	router, _ := router(t, log.Config{
		Level: slog.LevelInfo,
		Handlers: []log.HandlerConfig{{
			Name: "file", Provider: log.ProviderJSON, Options: map[string]any{"path": "out.log"},
		}},
		Routes: []log.Route{{
			Name: "only the agent's entries", When: log.Workspace("acme"),
			Handlers: []string{"file"},
		}},
	})
	service := serve(t, router)
	_, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "someone else's work",
	}))
	require.NoError(t, err)

	response, err := service.GetConfig(context.Background(), connect.NewRequest(&loggerv1.GetConfigRequest{}))
	require.NoError(t, err)

	// A deployment that reports this can tell an empty log from a quiet period.
	assert.Equal(t, int64(1), response.Msg.GetUnrouted())
	assert.Equal(t, int64(0), response.Msg.GetFailures())
}

func TestAProjectMayConfigureItsOwnFanout(t *testing.T) {
	project := t.TempDir()
	// The route matches the project's own name, which is its directory's: a caller that says
	// where the project is is tagged with what it is called, not with where it was found.
	writeConfig(t, project, fmt.Sprintf(`
daemon:
  host: 127.0.0.1
  port: 9180
logging:
  level: debug
  handlers:
    project:
      provider: json
      options:
        path: logs/project.log
  routes:
    - name: the project's own
      match:
        workspace: "%s"
      handlers: [project]
      add:
        project: acme
`, filepath.Base(project)))
	router, deploymentFiles := router(t, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.WorkDir = project })

	response, err := service.GetConfig(context.Background(), connect.NewRequest(&loggerv1.GetConfigRequest{
		Workspace: project,
	}))
	require.NoError(t, err)

	// The project logs the way its own file says, and that file is a different file rather
	// than a flag on the same one.
	assert.Equal(t, loggerv1.LogLevel_LOG_LEVEL_DEBUG, response.Msg.GetLevel())
	// Reported as the project's name rather than its path, because a log line saying
	// `workspace: acme` is a fact about the entry and one saying
	// `workspace: /home/someone/src/acme` is a fact about the machine.
	assert.Equal(t, filepath.Base(project), response.Msg.GetWorkspace())
	require.Len(t, response.Msg.GetHandlers(), 1)
	assert.Equal(t, "project", response.Msg.GetHandlers()[0].GetName())

	// The project's file is not merely readable: the entry goes through it, into a file in
	// the project, and does not touch the deployment's fanout at all. A project's
	// configuration that was reported but inert would be the worst of both.
	_, err = service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "stored a source", Workspace: project,
	}))
	require.NoError(t, err)
	written, err := os.ReadFile(filepath.Join(project, "logs", "project.log"))
	require.NoError(t, err)
	assert.Contains(t, string(written), "stored a source")
	assert.Empty(t, deploymentFiles["file"].all(t))
}

func TestAProjectsFanoutIsBuiltOnce(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, project, fmt.Sprintf(`
daemon:
  host: 127.0.0.1
  port: 9180
logging:
  level: info
  handlers:
    project:
      provider: json
      options:
        path: project.log
  routes:
    - name: the project's own
      match:
        workspace: "%s"
      handlers: [project]
`, filepath.Base(project)))
	router, _ := router(t, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.WorkDir = project })

	for i := 0; i < 3; i++ {
		_, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
			Message: "stored a source", Workspace: project,
		}))
		require.NoError(t, err)
	}

	// A router holds open files and a rotation policy, so building one per entry would
	// reopen everything on every call.
	assert.Len(t, readLines(t, &lines{dir: project, name: "project.log"}), 3)
}

func TestAProjectThatNeverMentionedLoggingUsesTheDeploymentsFanout(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, project, "daemon:\n  host: 127.0.0.1\n  port: 9180\n")
	router, _ := router(t, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.WorkDir = project })

	response, err := service.GetConfig(context.Background(), connect.NewRequest(&loggerv1.GetConfigRequest{
		Workspace: project,
	}))
	require.NoError(t, err)

	// Which is what it would get without this service, and so what it should not lose by
	// using it.
	assert.Equal(t, loggerv1.LogLevel_LOG_LEVEL_INFO, response.Msg.GetLevel())
	require.Len(t, response.Msg.GetHandlers(), 2)
	assert.Equal(t, "file", response.Msg.GetHandlers()[0].GetName())
}

func TestAProjectWithABrokenFanoutIsRefused(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, project, `
daemon:
  host: 127.0.0.1
  port: 9180
logging:
  level: info
  handlers:
    project:
      provider: nosuchbackend
  routes:
    - handlers: [project]
`)
	router, _ := router(t, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.WorkDir = project })

	_, err := service.GetConfig(context.Background(), connect.NewRequest(&loggerv1.GetConfigRequest{
		Workspace: project,
	}))

	// Reported rather than defaulted: a project that named a backend nobody has would
	// otherwise log nowhere and say nothing.
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "nosuchbackend")
}

func TestAProjectWithAnUnknownLoggingKeyIsRefused(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, project, `
daemon:
  host: 127.0.0.1
  port: 9180
logging:
  level: info
  routs: []
`)
	router, _ := router(t, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.WorkDir = project })

	_, err := service.GetConfig(context.Background(), connect.NewRequest(&loggerv1.GetConfigRequest{
		Workspace: project,
	}))

	// A key nobody understands is refused by name, by the reader that owns the section: a
	// misspelled "routs" silently dropped would be a fanout that is not the one written.
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), `unknown setting "routs"`)
}

func TestAHandlerWithNoRouterIsRefused(t *testing.T) {
	_, err := logger.NewHandler(logger.Options{})

	// A logger that routes nothing is not a logger, and failing at start-up says so rather
	// than accepting entries and dropping them.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a router")
}

func TestTheSubsystemDeclaresItselfAsAProvider(t *testing.T) {
	declared := logger.Providers("127.0.0.1:9100")

	require.Len(t, declared, 1)
	assert.Equal(t, logger.Name, declared[0].ID)
	assert.Equal(t, logger.Name, declared[0].Subsystem)
	assert.Equal(t, api.ProviderRole("logger"), declared[0].Role)
	assert.Equal(t, "127.0.0.1:9100", declared[0].Endpoint)
}

func TestTheSubsystemComposes(t *testing.T) {
	router, _ := router(t, defaultFanout(t))

	server, err := logger.New(logger.Options{Router: router, ListenAddress: "127.0.0.1:0"})
	require.NoError(t, err)

	descriptor := server.Descriptor()
	assert.Equal(t, logger.Name, descriptor.SubsystemName)
	assert.Equal(t, logger.Version, descriptor.ImplementationVersion)
	assert.Equal(t, []string{loggerv1connect.LoggerServiceName}, descriptor.ServiceNames)
}

// defaultFanout is the fanout most tests want: a machine-wide file everything reaches, and a
// project's own file that the project's entries are tagged into.
//
// The two are separate sinks on purpose, because a route that tags entries has to send them
// somewhere of its own — a tag on a sink an earlier route already claims would never be
// written, and the router refuses that rather than pretending.
func defaultFanout(t *testing.T) log.Config {
	t.Helper()
	return log.Config{
		Level: slog.LevelInfo,
		Handlers: []log.HandlerConfig{
			{Name: "file", Provider: log.ProviderJSON, Options: map[string]any{"path": "machine.log"}},
			{Name: "project", Provider: log.ProviderJSON, Options: map[string]any{"path": "project.log"}},
		},
		Routes: []log.Route{
			{Name: "everything", Handlers: []string{"file"}},
			{
				Name:     "one project's own",
				When:     log.Workspace("acme"),
				Handlers: []string{"project"},
				Add:      map[string]string{"project": "acme"},
			},
		},
	}
}

// router builds a deployment's fanout over a directory, resolving the relative paths in it
// there so a test can read the files back by the names routes refer to them.
func router(t *testing.T, fanout log.Config) (*log.Router, map[string]*lines) {
	t.Helper()
	dir := t.TempDir()
	registry := log.NewRegistry()
	registry.BaseDir = dir
	built, err := registry.Build(fanout)
	require.NoError(t, err)
	files := make(map[string]*lines, len(fanout.Handlers))
	for _, handler := range fanout.Handlers {
		name, _ := handler.Options["path"].(string)
		if name == "" {
			name = "out.log"
		}
		files[handler.Name] = &lines{dir: dir, name: name}
	}
	return built, files
}

// lines reads the entries a JSON sink wrote, one per line.
type lines struct {
	dir  string
	name string
}

func (l *lines) all(t *testing.T) []map[string]any {
	t.Helper()
	written, err := os.ReadFile(filepath.Join(l.dir, l.name))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(written)), "\n") {
		if line == "" {
			continue
		}
		var decoded map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &decoded))
		out = append(out, decoded)
	}
	return out
}

func (l *lines) lines(t *testing.T) []map[string]any { return l.all(t) }

// serve exposes a handler over a real HTTP endpoint, because the boundary under test is a
// transport and testing it in process would test a conversion rather than a service.
func serve(t *testing.T, router *log.Router, adjust ...func(*logger.Options)) loggerv1connect.LoggerServiceClient {
	t.Helper()
	options := logger.Options{Router: router, WorkDir: t.TempDir(), Clock: func() time.Time { return at }}
	for _, apply := range adjust {
		apply(&options)
	}
	handler, err := logger.NewHandler(options)
	require.NoError(t, err)
	path, service := loggerv1connect.NewLoggerServiceHandler(handler)
	mux := http.NewServeMux()
	mux.Handle(path, service)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return loggerv1connect.NewLoggerServiceClient(server.Client(), server.URL)
}

func service(t *testing.T, fanout log.Config) (loggerv1connect.LoggerServiceClient, map[string]*lines) {
	t.Helper()
	built, files := router(t, fanout)
	return serve(t, built), files
}

func readLines(t *testing.T, file *lines) []map[string]any {
	t.Helper()
	return file.all(t)
}

func discarding() slog.Handler {
	return slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
}

// writeConfig writes a deployment configuration file, which is where a project's fanout
// lives.
func writeConfig(t *testing.T, dir, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, config.ProjectDir), 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, config.ProjectDir, config.FileName), []byte(contents), 0o600))
}

func TestACallerCanConfigureTheFanoutForItsOwnWorkspace(t *testing.T) {
	base := t.TempDir()
	router, files := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) {
		options.BaseDir = base
		options.Providers = []string{log.ProviderJSON, log.ProviderText}
	})

	confirmation, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
		Level:     loggerv1.LogLevel_LOG_LEVEL_DEBUG,
		Handlers: []*loggerv1.LogHandler{{
			Name:     "agent",
			Provider: log.ProviderJSON,
			Level:    loggerv1.LogLevel_LOG_LEVEL_DEBUG,
			Options:  map[string]string{"path": "logs/agent.log"},
		}},
		Routes: []*loggerv1.LogRoute{{
			Name:     "the agent's own",
			Handlers: []string{"agent"},
			Add:      map[string]string{"sent_by": "agent"},
		}},
	}))
	require.NoError(t, err)

	// The answer says what the caller's entries will now do, so a caller that has redirected
	// its logging knows rather than discovering it from a log it cannot read.
	assert.True(t, confirmation.Msg.GetConfigured())
	assert.Equal(t, "acme", confirmation.Msg.GetWorkspace())
	assert.Equal(t, loggerv1.FanoutSource_FANOUT_SOURCE_CALLER, confirmation.Msg.GetSource())
	require.Len(t, confirmation.Msg.GetHandlers(), 1)
	assert.Equal(t, "agent", confirmation.Msg.GetHandlers()[0].GetName())

	// The entry lands in the caller's sink, in the directory it asked for, and is tagged by
	// the route the caller wrote.
	response, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "advanced a run", Logger: "agent",
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{"agent"}, response.Msg.GetHandlers())

	entry := readLines(t, &lines{dir: base, name: "logs/agent.log"})[0]
	assert.Equal(t, "advanced a run", entry["msg"])
	assert.Equal(t, "agent", entry["sent_by"])

	// And it did not go where it would have, which is the whole observable difference.
	assert.Empty(t, files["file"].all(t))
	assert.Empty(t, files["project"].all(t))
}

func TestACallersFanoutCannotBeEscapedByNamingTheDeployment(t *testing.T) {
	base := t.TempDir()
	router, _ := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) {
		options.BaseDir = base
		options.Providers = []string{log.ProviderJSON}
	})

	_, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Handlers: []*loggerv1.LogHandler{{
			Name: "agent", Provider: log.ProviderJSON,
			Options: map[string]string{"path": "logs/agent.log"},
		}},
		Routes: []*loggerv1.LogRoute{{Handlers: []string{"agent"}}},
	}))

	// A fanout that is not scoped to a workspace would apply to the deployment's own entries,
	// which is the one thing a caller must not be able to configure. The scope is what makes
	// this operation safe to expose, so a request without one is refused rather than guessed.
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "needs a workspace to be scoped to")
}

func TestACallerCannotRouteToABackendTheDeploymentLacks(t *testing.T) {
	base := t.TempDir()
	router, _ := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) {
		options.BaseDir = base
		options.Providers = []string{log.ProviderJSON}
	})

	_, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
		Handlers: []*loggerv1.LogHandler{{
			Name: "elsewhere", Provider: "loki",
			Options: map[string]string{"endpoint": "http://loki:3100"},
		}},
		Routes: []*loggerv1.LogRoute{{Handlers: []string{"elsewhere"}}},
	}))

	// A caller's fanout may only name a backend this installation has, so it cannot use the
	// service to reach something that was never part of this deployment.
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "loki")
	assert.Contains(t, err.Error(), "json")
}

func TestAnEntryForAnotherWorkspaceIsNotRedirected(t *testing.T) {
	base := t.TempDir()
	router, files := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) {
		options.BaseDir = base
		options.Providers = []string{log.ProviderJSON}
	})

	_, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
		Handlers: []*loggerv1.LogHandler{{
			Name: "agent", Provider: log.ProviderJSON,
			Options: map[string]string{"path": "logs/agent.log"},
		}},
		Routes: []*loggerv1.LogRoute{{
			Name: "only acme", Handlers: []string{"agent"},
			// The caller is not configuring itself, because the scope is the caller's claim
			// about which entries are its own — so a route can narrow that and nothing can
			// widen it.
		}},
	}))
	require.NoError(t, err)

	// A different workspace's entry goes to the deployment's own routes, untouched.
	response, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "globex's work", Workspace: "globex",
	}))
	require.NoError(t, err)
	assert.NotEqual(t, []string{"agent"}, response.Msg.GetHandlers())
	assert.Equal(t, []string{"file"}, response.Msg.GetHandlers())
	assert.Empty(t, readLines(t, &lines{dir: base, name: "logs/agent.log"}))
	assert.Len(t, files["file"].all(t), 1)
}

func TestAConfigurationIsReplacedNotAccumulated(t *testing.T) {
	base := t.TempDir()
	router, _ := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) {
		options.BaseDir = base
		options.Providers = []string{log.ProviderJSON}
	})

	first := func(name string) *connect.Request[loggerv1.SetConfigRequest] {
		return connect.NewRequest(&loggerv1.SetConfigRequest{
			Workspace: "acme",
			Handlers: []*loggerv1.LogHandler{{Name: name, Provider: log.ProviderJSON,
				Options: map[string]string{"path": "logs/" + name + ".log"}}},
			Routes: []*loggerv1.LogRoute{{Handlers: []string{name}}},
		})
	}
	_, err := service.SetConfig(context.Background(), first("first"))
	require.NoError(t, err)
	_, err = service.SetConfig(context.Background(), first("second"))
	require.NoError(t, err)

	response, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "advanced a run",
	}))
	require.NoError(t, err)

	// A fanout is a whole plan, so a second one replaces the first rather than adding to it:
	// otherwise a caller that retunes its logging would find entries still going to a sink it
	// has just stopped asking for, with no way to know.
	assert.Equal(t, []string{"second"}, response.Msg.GetHandlers())
}

func TestNamingAProjectWithoutAConfigurationStillJustNamesIt(t *testing.T) {
	router, _ := router(t, defaultFanout(t))
	service := serve(t, router)

	confirmation, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
	}))

	require.NoError(t, err)
	// A caller with a project but no opinions about its routing sends an empty request, and
	// is told it supplied nothing rather than being left to think it retuned something.
	assert.False(t, confirmation.Msg.GetConfigured())
	assert.Equal(t, "acme", confirmation.Msg.GetWorkspace())
	assert.Equal(t, loggerv1.FanoutSource_FANOUT_SOURCE_DEPLOYMENT, confirmation.Msg.GetSource())
}

func TestGetConfigSaysWhereTheFanoutCameFrom(t *testing.T) {
	project := t.TempDir()
	name := filepath.Base(project)
	writeConfig(t, project, fmt.Sprintf(`
daemon:
  host: 127.0.0.1
  port: 9180
logging:
  level: warn
  handlers:
    project:
      provider: json
      options:
        path: project.log
  routes:
    - name: the project's own
      handlers: [project]
`))
	router, _ := router(t, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.WorkDir = project })
	_ = name

	deployment, err := service.GetConfig(context.Background(),
		connect.NewRequest(&loggerv1.GetConfigRequest{}))
	require.NoError(t, err)
	assert.Equal(t, loggerv1.FanoutSource_FANOUT_SOURCE_DEPLOYMENT, deployment.Msg.GetSource())

	fromFile, err := service.GetConfig(context.Background(),
		connect.NewRequest(&loggerv1.GetConfigRequest{Workspace: project}))
	require.NoError(t, err)
	// A caller entitled to know that its project's file is what would otherwise have answered,
	// so it can tell that from a fanout it configured itself.
	assert.Equal(t, loggerv1.FanoutSource_FANOUT_SOURCE_PROJECT_FILE, fromFile.Msg.GetSource())
	assert.Equal(t, loggerv1.LogLevel_LOG_LEVEL_WARN, fromFile.Msg.GetLevel())
}

func TestAFanoutWithNoHandlerIsRefused(t *testing.T) {
	base := t.TempDir()
	router, _ := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.BaseDir = base })

	_, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
		Routes:    []*loggerv1.LogRoute{{Handlers: []string{"nowhere"}}},
	}))

	// A fanout with no sinks describes where entries go by not saying, and a route naming a
	// sink that was not declared is exactly the typo that would record nothing.
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "at least one handler")
}

func TestAHandlerWithNoProviderIsRefused(t *testing.T) {
	base := t.TempDir()
	router, _ := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.BaseDir = base })

	_, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
		Handlers:  []*loggerv1.LogHandler{{Name: "agent"}},
		Routes:    []*loggerv1.LogRoute{{Handlers: []string{"agent"}}},
	}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a provider")
}

func TestAHandlerWithNoNameIsRefused(t *testing.T) {
	base := t.TempDir()
	router, _ := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.BaseDir = base })

	_, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
		Handlers:  []*loggerv1.LogHandler{{Provider: log.ProviderJSON}},
		Routes:    []*loggerv1.LogRoute{{Handlers: []string{"agent"}}},
	}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a name")
}

func TestARouteNamingNoHandlerIsRefused(t *testing.T) {
	base := t.TempDir()
	router, _ := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.BaseDir = base })

	_, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
		Handlers:  []*loggerv1.LogHandler{{Name: "agent", Provider: log.ProviderJSON}},
		Routes:    []*loggerv1.LogRoute{{Name: "nowhere"}},
	}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must name the handlers")
}

func TestAConfigurationThatCannotWorkIsNotInstalled(t *testing.T) {
	base := t.TempDir()
	router, _ := routerIn(t, base, defaultFanout(t))
	service := serve(t, router, func(options *logger.Options) { options.BaseDir = base })

	// Two routes, both tagging, the second naming a sink the first already claimed: the router
	// refuses the second, so the whole configuration is refused rather than half installed.
	_, err := service.SetConfig(context.Background(), connect.NewRequest(&loggerv1.SetConfigRequest{
		Workspace: "acme",
		Handlers: []*loggerv1.LogHandler{{Name: "agent", Provider: log.ProviderJSON,
			Options: map[string]string{"path": "logs/agent.log"}}},
		Routes: []*loggerv1.LogRoute{
			{Name: "everything", Handlers: []string{"agent"}},
			{Name: "and tagged again", Handlers: []string{"agent"}, Add: map[string]string{"x": "y"}},
		},
	}))
	require.Error(t, err)

	// The entry carries no workspace, so the deployment's own default route is the only one
	// that matches it — which is the proof that the refused configuration was never installed.
	response, err := service.Log(context.Background(), connect.NewRequest(&loggerv1.LogRequest{
		Message: "advanced a run",
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{"file"}, response.Msg.GetHandlers())
}

// routerIn is router with a chosen base directory, so a test can name a path it will read back.
func routerIn(t *testing.T, dir string, fanout log.Config) (*log.Router, map[string]*lines) {
	t.Helper()
	registry := log.NewRegistry()
	registry.BaseDir = dir
	built, err := registry.Build(fanout)
	require.NoError(t, err)
	files := make(map[string]*lines, len(fanout.Handlers))
	for _, handler := range fanout.Handlers {
		name, _ := handler.Options["path"].(string)
		if name == "" {
			name = "out.log"
		}
		files[handler.Name] = &lines{dir: dir, name: name}
	}
	return built, files
}
