package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The host builds its fanout from the configuration file before anything starts, so these
// tests are about the file: what it may say, where a relative path in it lands, and what
// happens when it says something impossible.

// writeDeploymentConfig writes a configuration file the loader will find for a directory.
func writeDeploymentConfig(t *testing.T, dir, contents string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, config.ProjectDir), 0o750))
	path := filepath.Join(dir, config.ProjectDir, config.FileName)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func resolveFor(t *testing.T, dir string) config.Config {
	t.Helper()
	resolved, err := config.New(config.Options{WorkDir: dir}).Resolve("")
	require.NoError(t, err)
	return resolved
}

func TestAConfiguredFanoutIsBuiltAndInstalled(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	writeDeploymentConfig(t, project, `
daemon:
  host: 127.0.0.1
  port: 9180
logging:
  level: warn
  handlers:
    file:
      provider: json
      options:
        path: logs/deployment.log
  routes:
    - name: everything
      handlers: [file]
`)
	resolved := resolveFor(t, project)

	built, err := buildFanout(resolved)
	require.NoError(t, err)

	// The level the file stated is the deployment's, not a default this function chose.
	assert.Equal(t, slog.LevelWarn, built.Router.Level())
	assert.Contains(t, built.Registry.Providers(), "logfile",
		"a backend a subsystem provides should be nameable from the host's configuration")

	// A relative path means a path in the project, because a deployment's log file belongs to
	// the deployment rather than to whichever directory the command happened to start in.
	previous := slog.Default()
	slog.SetDefault(slog.New(built.Router))
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.Info("stored a source")
	slog.Warn("cache is stale")

	written, err := os.ReadFile(filepath.Join(project, "logs", "deployment.log"))
	require.NoError(t, err)
	assert.NotContains(t, string(written), "stored a source")
	assert.Contains(t, string(written), "cache is stale")
}

func TestADeploymentWithNoLoggingSectionStillRecordsSomewhere(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeDeploymentConfig(t, dir, "daemon:\n  host: 127.0.0.1\n  port: 9180\n")

	built, err := buildFanout(resolveFor(t, dir))
	require.NoError(t, err)

	previous := slog.Default()
	slog.SetDefault(slog.New(built.Router))
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.Info("stored a source")

	// A tool that silently records nothing is a tool nobody can debug, so a deployment that
	// said nothing still gets a file it can read.
	written, err := os.ReadFile(filepath.Join(dir, "toolbox.log"))
	require.NoError(t, err)
	assert.Contains(t, string(written), "stored a source")
}

func TestAnUnknownLoggingKeyStopsTheDeployment(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeDeploymentConfig(t, dir, `
daemon:
  host: 127.0.0.1
  port: 9180
logging:
  level: info
  routs: []
`)

	_, err := buildFanout(resolveFor(t, dir))

	// Refused before a subsystem starts, because a fanout that is not the one written is
	// worse than one that did not load.
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown setting "routs"`)
}

func TestABackendNobodyProvidesStopsTheDeployment(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeDeploymentConfig(t, dir, `
daemon:
  host: 127.0.0.1
  port: 9180
logging:
  level: info
  handlers:
    file:
      provider: loki
  routes:
    - handlers: [file]
`)

	_, err := buildFanout(resolveFor(t, dir))

	// The available backends are listed, because a typo that recorded nothing would
	// otherwise be a line nobody could explain.
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no logging provider for "loki"`)
	assert.Contains(t, err.Error(), "logfile")
}

func TestTheHostCanNameARotatingFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeDeploymentConfig(t, dir, `
daemon:
  host: 127.0.0.1
  port: 9180
logging:
  level: info
  handlers:
    file:
      provider: logfile
      options:
        path: logs/deployment.log
        max_backups: 2
  routes:
    - handlers: [file]
`)

	built, err := buildFanout(resolveFor(t, dir))
	require.NoError(t, err)

	slog.New(built.Router).Info("stored a source", "padding", strings.Repeat("x", 64))

	// The backend is lumberjack's, reached through the same registry any other provider is,
	// which is what makes a deployment's configuration independent of how many backends the
	// framework ships.
	written, err := os.ReadFile(filepath.Join(dir, "logs", "deployment.log"))
	require.NoError(t, err)
	assert.Contains(t, string(written), "stored a source")
}
