package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The four layers are the whole of the design: a file, the environment, a flag, and a
// built-in default underneath. Each test below changes exactly one of them and asserts what
// the others did to it, because a precedence bug is invisible until two layers disagree and
// the wrong one wins.
//
// Everything runs in a temporary working directory with a cleared environment, so a test
// cannot be decided by the machine it runs on.

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// The project file is looked for by walking up, and a temporary directory may be under
	// a checkout that has one. A working directory that cannot escape the temporary tree is
	// what keeps a test's result its own.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	for _, name := range []string{
		config.EnvCore, config.EnvPort, config.EnvConfigFile,
		"TOOLBOX_DAEMON_HOST", "TOOLBOX_DAEMON_PORT", "TOOLBOX_DAEMON_LAUNCH",
		"TOOLBOX_MCP_HOST", "TOOLBOX_MCP_PORT", "TOOLBOX_POLICY", "TOOLBOX_SCOPE",
	} {
		t.Setenv(name, "")
	}
	return dir
}

// write puts a configuration file where a project would keep one.
func write(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, config.ProjectDir, config.FileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

// flags builds a flag set with the same declarations the command uses, so a test resolves
// through the same binding a caller would.
func flags() *pflag.FlagSet {
	set := pflag.NewFlagSet("test", pflag.ContinueOnError)
	set.String("daemon-host", "", "")
	set.Int("daemon-port", 0, "")
	set.String("mcp-host", "", "")
	set.Int("mcp-port", 0, "")
	set.String("launch", "", "")
	set.String("policy", "", "")
	set.String("core", "", "")
	set.Int("port", 0, "")
	return set
}

func resolve(t *testing.T, set *pflag.FlagSet, workDir string) (config.Config, error) {
	t.Helper()
	return config.New(config.Options{Flags: set, WorkDir: workDir}).Resolve("")
}

// A deployment that has said nothing is on the loopback default with the daemon managed for
// the caller, which is what makes a fresh installation work with no configuration at all.
func TestNothingConfiguredIsTheBuiltInDefault(t *testing.T) {
	dir := isolate(t)
	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)

	assert.Equal(t, config.DefaultHost, resolved.Daemon.Host)
	assert.Equal(t, config.DefaultPort, resolved.Daemon.Port)
	assert.Equal(t, config.LaunchAuto, resolved.Launch)
	assert.Empty(t, resolved.Path)
	assert.Equal(t, config.SourceDefault, resolved.SourceOf(config.KeyDaemonPort),
		"and it says so, rather than leaving a reader to guess")
}

// The Model Context Protocol endpoint defaults to the daemon's address, so a deployment that
// says nothing is one port rather than two.
func TestTheEndpointDefaultsToTheDaemonsAddress(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  port: 9400\n")
	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:9400", resolved.MCP.Addr(),
		"one port, so a small deployment is not two things to configure")
}

func TestTheEndpointCanHaveAnAddressOfItsOwn(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  port: 9400\nmcp:\n  port: 9401\n")
	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:9400", resolved.Daemon.Addr())
	assert.Equal(t, "127.0.0.1:9401", resolved.MCP.Addr(),
		"a deployment that exposes agent tools to a network gives them their own address")
}

// The three layers, in order. Each test changes one and asserts the other two did not win.
func TestAConfigurationFileIsRead(t *testing.T) {
	dir := isolate(t)
	path := write(t, dir, "daemon:\n  host: 10.0.0.5\n  port: 9500\n  launch: explicit\npolicy: ops.policy\n")
	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)

	assert.Equal(t, "10.0.0.5", resolved.Daemon.Host)
	assert.Equal(t, 9500, resolved.Daemon.Port)
	assert.Equal(t, config.LaunchExplicit, resolved.Launch)
	assert.Equal(t, "ops.policy", resolved.PolicyPath)
	assert.Equal(t, path, resolved.Path)
	assert.Equal(t, config.SourceFile, resolved.SourceOf(config.KeyDaemonHost))
}

func TestTheEnvironmentBeatsTheFile(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  port: 9500\n")
	t.Setenv("TOOLBOX_DAEMON_PORT", "9600")
	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)

	assert.Equal(t, 9600, resolved.Daemon.Port, "the environment is later than the file")
	assert.Equal(t, config.SourceEnvironment, resolved.SourceOf(config.KeyDaemonPort))
}

func TestAFlagBeatsEverything(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  port: 9500\n")
	t.Setenv("TOOLBOX_DAEMON_PORT", "9600")

	set := flags()
	require.NoError(t, set.Set("daemon-port", "9700"))
	resolved, err := resolve(t, set, dir)
	require.NoError(t, err)

	assert.Equal(t, 9700, resolved.Daemon.Port, "a flag the caller gave wins over both")
	assert.Equal(t, config.SourceFlag, resolved.SourceOf(config.KeyDaemonPort))
}

// A flag left at its default contributes nothing, so a caller who did not choose a value is
// not recorded as having chosen one.
func TestAFlagNobodyGaveContributesNothing(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  port: 9500\n")
	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)

	assert.Equal(t, 9500, resolved.Daemon.Port, "the file is not overridden by an unset flag")
	assert.Equal(t, config.SourceFile, resolved.SourceOf(config.KeyDaemonPort))
}

// TOOLBOX_CORE carries two facts, so it cannot be a key. It is split, and it yields to the
// variables that do map to keys, so a stale one in a shell profile cannot override a
// deployment that has adopted the specific names.
func TestTheCoreVariableIsAShortWayToNameTheDaemon(t *testing.T) {
	dir := isolate(t)
	t.Setenv(config.EnvCore, "core.internal:9800")
	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)

	assert.Equal(t, "core.internal", resolved.Daemon.Host)
	assert.Equal(t, 9800, resolved.Daemon.Port)
}

func TestTheSpecificVariablesBeatThePair(t *testing.T) {
	dir := isolate(t)
	t.Setenv(config.EnvCore, "stale.internal:9800")
	t.Setenv("TOOLBOX_DAEMON_HOST", "chosen.internal")
	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)

	assert.Equal(t, "chosen.internal", resolved.Daemon.Host,
		"a deployment that adopted the specific names is not surprised by a stale pair")
}

// --core is the flag spelling of the same pair, and it yields to the specific flags for the
// same reason.
func TestTheCoreFlagIsSplitIntoTheAddress(t *testing.T) {
	dir := isolate(t)
	set := flags()
	require.NoError(t, set.Set("core", "flag.internal:9900"))
	resolved, err := resolve(t, set, dir)
	require.NoError(t, err)

	assert.Equal(t, "flag.internal", resolved.Daemon.Host)
	assert.Equal(t, 9900, resolved.Daemon.Port)
}

func TestTheSpecificFlagsBeatThePair(t *testing.T) {
	dir := isolate(t)
	set := flags()
	require.NoError(t, set.Set("core", "stale.internal:9900"))
	require.NoError(t, set.Set("daemon-port", "9950"))
	resolved, err := resolve(t, set, dir)
	require.NoError(t, err)

	assert.Equal(t, 9950, resolved.Daemon.Port)
}

// A project file wins over the user's, because a project that pins its own address must not
// be silently overridden by a machine-wide default. That is the direction people do not
// expect, which is why the explicit name exists.
func TestAProjectFileWinsOverTheUsers(t *testing.T) {
	dir := isolate(t)
	userDir := filepath.Join(dir, "config", "toolbox")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, config.FileName),
		[]byte("daemon:\n  port: 8000\n"), 0o600))

	project := t.TempDir()
	write(t, project, "daemon:\n  port: 8001\n")

	loader := config.New(config.Options{Flags: flags(), WorkDir: project})
	resolved, err := loader.Resolve("")
	require.NoError(t, err)
	assert.Equal(t, 8001, resolved.Daemon.Port)
	assert.Contains(t, resolved.Path, config.ProjectDir)
}

// A project file is found from a subdirectory, which is what makes it work the same
// everywhere in a project rather than only at its root.
func TestAProjectFileIsFoundFromASubdirectory(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  port: 8002\n")
	nested := filepath.Join(dir, "services", "api")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	resolved, err := resolve(t, flags(), nested)
	require.NoError(t, err)
	assert.Equal(t, 8002, resolved.Daemon.Port)
}

func TestAnExplicitlyNamedFileMustExist(t *testing.T) {
	dir := isolate(t)
	loader := config.New(config.Options{Flags: flags(), WorkDir: dir})
	_, err := loader.Resolve(filepath.Join(dir, "absent.yaml"))
	require.Error(t, err, "somebody asked for a configuration and there is none")
	assert.Contains(t, err.Error(), "absent.yaml")
}

func TestAMalformedFileIsAnError(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  port: [not a number\n")
	_, err := resolve(t, flags(), dir)
	require.Error(t, err, "a deployment running on a configuration nobody chose is worse than one that refuses to start")
}

// A key the file contains that this package does not understand is refused, by name. A file
// that silently ignores what it does not understand cannot be trusted to state what it does,
// and a misspelled key is otherwise indistinguishable from a deliberate default.
func TestAnUnknownKeyIsRefusedByName(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  prot: 9500\n")
	_, err := resolve(t, flags(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prot", "the refusal names the key that was not understood")
}

// The workspace selector is per invocation, not per installation, so a file must not carry
// it. A machine-wide file that held it would make every project share one workspace.
func TestAFileCannotCarryTheWorkspaceSelector(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "scope: a-workspace\n")
	_, err := resolve(t, flags(), dir)
	require.Error(t, err, "the workspace is not a thing an installation states")
	assert.Contains(t, err.Error(), "scope")
}

// The launch mode is an enum: a value that is not one of the three is refused by name,
// because a deployment that meant "explicit" and got "auto" would start a daemon somebody
// manages with systemd, and the two would fight over the address.
func TestAnUnknownLaunchModeIsRefusedWithTheThree(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  launch: sometimes\n")
	_, err := resolve(t, flags(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sometimes")
	assert.Contains(t, err.Error(), "auto")
	assert.Contains(t, err.Error(), "explicit")
	assert.Contains(t, err.Error(), "disabled")
}

func TestEveryLaunchModeIsAccepted(t *testing.T) {
	for _, mode := range config.LaunchModes {
		t.Run(string(mode), func(t *testing.T) {
			dir := isolate(t)
			contents := "daemon:\n  host: named.internal\n  port: 9600\n  launch: " + string(mode) + "\n"
			write(t, dir, contents)
			resolved, err := resolve(t, flags(), dir)
			require.NoError(t, err)
			assert.Equal(t, mode, resolved.Launch)
		})
	}
}

// A deployment that disabled the daemon and named no core has said nothing about where
// anything is: there is no daemon to ask, and peers outside this process are found through
// one. Reported as an incomplete configuration rather than as a failure on some later call.
func TestDisablingTheDaemonRequiresANamedCore(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  launch: disabled\n")
	_, err := resolve(t, flags(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "which core to connect to")
}

func TestDisablingTheDaemonWithANamedCoreIsAccepted(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  host: named.internal\n  port: 9600\n  launch: disabled\n")
	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)
	assert.Equal(t, config.LaunchDisabled, resolved.Launch)
	assert.Equal(t, "named.internal:9600", resolved.Daemon.Addr())
}

// Naming the core on the command line satisfies the requirement too, because a deployment
// that has said nothing in a file can still say it per invocation.
func TestDisablingTheDaemonAcceptsTheAddressFromAFlag(t *testing.T) {
	dir := isolate(t)
	set := flags()
	require.NoError(t, set.Set("launch", "disabled"))
	require.NoError(t, set.Set("daemon-host", "named.internal"))
	resolved, err := resolve(t, set, dir)
	require.NoError(t, err)
	assert.Equal(t, config.LaunchDisabled, resolved.Launch)
}

func TestAnUnusablePortIsRefused(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  port: 70000\n")
	_, err := resolve(t, flags(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "70000")
}

// The report states where every value came from, because "which of four places did this
// port come from" is otherwise a question only the resolving process can answer.
func TestTheReportSaysWhereEveryValueCameFrom(t *testing.T) {
	dir := isolate(t)
	write(t, dir, "daemon:\n  port: 9500\n  launch: explicit\n")
	t.Setenv("TOOLBOX_POLICY", "from-env.policy")

	resolved, err := resolve(t, flags(), dir)
	require.NoError(t, err)
	report := " " + joinLines(resolved.Report()) + " "

	assert.Contains(t, report, "config file", "the file is named as the source")
	assert.Contains(t, report, "environment", "and so is the environment")
	assert.Contains(t, report, "default", "and the built-in default for what nobody set")
	assert.Contains(t, report, "explicit", "the launch mode is stated")
	assert.Contains(t, report, resolved.Path, "and the file that was read is named")
}

func TestTheSearchPathTellsAUserWhereToPutAFile(t *testing.T) {
	dir := isolate(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	path := config.SearchPath(dir)
	require.NotEmpty(t, path)

	assert.Contains(t, path[0], config.ProjectDir, "the project's file comes first")
	assert.Contains(t, path[len(path)-1], filepath.Join("toolbox", config.FileName),
		"and the user's is the fallback")
}

func TestSameAddressDecidesWhetherTheEndpointCanBeMounted(t *testing.T) {
	daemon := config.Listen{Host: "127.0.0.1", Port: 9180}
	assert.True(t, config.SameAddress(daemon, config.Listen{Host: "127.0.0.1", Port: 9180}))
	assert.False(t, config.SameAddress(daemon, config.Listen{Host: "127.0.0.1", Port: 9181}),
		"a second address needs a second listener, and one port cannot be two")
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}
