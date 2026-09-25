package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The order is the contract: a flag beats the environment, the environment beats
// the file, and the file beats the built-in. Everything else follows from that, so
// these tests are about the order and not about the parsing.

func TestNothingConfiguredIsTheBuiltInDefault(t *testing.T) {
	// A deployment with no configuration is the common case and must not have to
	// create a file to start.
	clearEnvironment(t)
	resolved, err := Resolve(Overrides{}, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, DefaultHost, resolved.Core.Host)
	assert.Equal(t, DefaultPort, resolved.Core.Port)
	assert.Equal(t, "http://"+DefaultHost+":"+"9180", resolved.Core.URL())
	assert.Empty(t, resolved.Path, "no file was read")
	assert.Empty(t, resolved.Scope)
}

func TestAConfigurationFileIsRead(t *testing.T) {
	clearEnvironment(t)
	dir := t.TempDir()
	writeConfig(t, dir, "core:\n  host: core.internal\n  port: 7000\npolicy: ./ops.policy\nscope: acme\n")

	resolved, err := Resolve(Overrides{}, dir)
	require.NoError(t, err)
	assert.Equal(t, "core.internal", resolved.Core.Host)
	assert.Equal(t, 7000, resolved.Core.Port)
	assert.Equal(t, "http://core.internal:7000", resolved.Core.URL())
	assert.Equal(t, "./ops.policy", resolved.PolicyPath)
	assert.Equal(t, "acme", resolved.Scope)
	assert.Equal(t, filepath.Join(dir, ".toolbox", FileName), resolved.Path)
}

func TestTheEnvironmentBeatsTheFile(t *testing.T) {
	clearEnvironment(t)
	dir := t.TempDir()
	writeConfig(t, dir, "core:\n  host: from-file\n  port: 7000\n")

	t.Setenv(EnvCore, "from-env:7100")
	resolved, err := Resolve(Overrides{}, dir)
	require.NoError(t, err)
	assert.Equal(t, "from-env", resolved.Core.Host)
	assert.Equal(t, 7100, resolved.Core.Port)
}

func TestTheEnvironmentPortActsAlone(t *testing.T) {
	// A host with no port is a common thing to set, and it must not throw away the
	// port the file supplied.
	clearEnvironment(t)
	dir := t.TempDir()
	writeConfig(t, dir, "core:\n  host: from-file\n  port: 7000\n")

	t.Setenv(EnvCore, "from-env")
	resolved, err := Resolve(Overrides{}, dir)
	require.NoError(t, err)
	assert.Equal(t, "from-env", resolved.Core.Host)
	assert.Equal(t, 7000, resolved.Core.Port, "the file's port survives a host-only environment value")
}

func TestAFlagBeatsEverything(t *testing.T) {
	clearEnvironment(t)
	dir := t.TempDir()
	writeConfig(t, dir, "core:\n  host: from-file\n  port: 7000\n")
	t.Setenv(EnvCore, "from-env:7100")

	resolved, err := Resolve(Overrides{Core: "from-flag:7200"}, dir)
	require.NoError(t, err)
	assert.Equal(t, "from-flag", resolved.Core.Host)
	assert.Equal(t, 7200, resolved.Core.Port)

	// A port on its own leaves the host where the rest of the chain put it.
	portOnly, err := Resolve(Overrides{Port: 7300}, dir)
	require.NoError(t, err)
	assert.Equal(t, "from-env", portOnly.Core.Host)
	assert.Equal(t, 7300, portOnly.Core.Port)
}

func TestAProjectFileWinsOverTheUsers(t *testing.T) {
	// A project that pins its own address must not be silently overridden by a
	// machine-wide default, because that is the failure a project owner cannot see.
	clearEnvironment(t)
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	require.NoError(t, os.MkdirAll(filepath.Join(home, "config", "toolbox"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(home, "config", "toolbox", FileName),
		[]byte("core:\n  port: 8000\n"), 0o644,
	))

	project := t.TempDir()
	writeConfig(t, project, "core:\n  port: 9000\n")

	resolved, err := Resolve(Overrides{}, project)
	require.NoError(t, err)
	assert.Equal(t, 9000, resolved.Core.Port)
	assert.Equal(t, filepath.Join(project, ".toolbox", FileName), resolved.Path)
}

func TestAnExplicitlyNamedFileMustExist(t *testing.T) {
	// A configuration that was named and not read is a deployment running on
	// settings nobody chose, which is the worst of the three outcomes.
	clearEnvironment(t)
	_, err := Resolve(Overrides{ConfigFile: filepath.Join(t.TempDir(), "absent.yaml")}, t.TempDir())
	assert.Error(t, err)

	t.Setenv(EnvConfigFile, filepath.Join(t.TempDir(), "also-absent.yaml"))
	_, err = Resolve(Overrides{}, t.TempDir())
	assert.ErrorContains(t, err, EnvConfigFile)
}

func TestAMalformedFileIsAnError(t *testing.T) {
	// Falling back to a default after failing to parse a policy or an address
	// would be running on a configuration nobody chose, silently.
	clearEnvironment(t)
	dir := t.TempDir()
	writeConfig(t, dir, "core: [not, a, mapping]\n")

	_, err := Resolve(Overrides{}, dir)
	assert.Error(t, err)
}

func TestAnUnusableAddressIsRefused(t *testing.T) {
	clearEnvironment(t)
	_, err := Resolve(Overrides{Port: 70000}, t.TempDir())
	assert.ErrorContains(t, err, "not a port")

	_, err = Resolve(Overrides{Core: " "}, t.TempDir())
	require.NoError(t, err, "a blank override is no override")
}

func TestTheSearchPathTellsAUserWhereToPutAFile(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	path := SearchPath(dir)
	require.Len(t, path, 2)
	assert.Equal(t, filepath.Join(dir, ".toolbox", FileName), path[0],
		"the project's file is searched first")
	assert.Equal(t, filepath.Join(home, "config", "toolbox", FileName), path[1])
}

func writeConfig(t *testing.T, dir, contents string) {
	t.Helper()
	target := filepath.Join(dir, ".toolbox")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, FileName), []byte(contents), 0o644))
}

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{EnvCore, EnvPort, EnvConfigFile} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
}
