// Package config resolves how an installation is deployed and what a client should do
// about it.
//
// A configuration file describes the deployment: the address the daemon binds and clients
// dial, the address the Model Context Protocol endpoint binds, the policy the deployment
// authorises with, and how the daemon's lifecycle is managed. It does not describe a
// workspace — agents, skills, prompts and knowledge are not deployment — and it does not
// carry the workspace selector, which differs per invocation rather than per installation.
// See docs/decisions/0011-deployment-configuration.md.
//
// Four layers, in this order: a file, the environment, and a flag; and a built-in default
// underneath all of them. A flag that was given wins, because the person at the terminal is
// the one who knows what they meant. A configuration file that cannot be parsed is an error
// rather than a silent fall back to a default, because a deployment running on a
// configuration nobody chose is worse than one that refuses to start.
//
// There is no discovery protocol. A process that wants the core knows where it is. A core
// that is not there is a connection failure rather than a timeout waiting for something to
// appear, which is what a command wants: "no daemon is running" is an answer, not a hang.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Built-in defaults. A loopback default is deliberate: a core reached over the network
// without being asked for is a surprise, and every deployment that wants one configures it.
// A fixed port rather than a discovered one is what lets a subsystem find the core with no
// configuration at all.
const (
	DefaultHost = "127.0.0.1"
	DefaultPort = 9180
)

// FileName is the configuration file's name, in whichever directory it is looked for.
const FileName = "config.yaml"

// ProjectDir is the per-project directory a configuration file is looked for in, walking up
// from the working directory.
const ProjectDir = ".toolbox"

// EnvPrefix is prepended to a key to form its environment variable, so daemon.host is
// TOOLBOX_DAEMON_HOST.
const EnvPrefix = "TOOLBOX"

// Convenience variables that do not map to a single key.
const (
	// EnvCore is a "host:port" pair for the daemon's address. It is the short way to point
	// a command at somebody else's core, and it cannot be a configuration key because a
	// value carrying two facts is not one fact. TOOLBOX_DAEMON_HOST and TOOLBOX_DAEMON_PORT
	// are the ones a key maps to, and they win over this.
	EnvCore = "TOOLBOX_CORE"
	// EnvPort is a port number on its own.
	EnvPort = "TOOLBOX_PORT"
	// EnvConfigFile names a configuration file explicitly, bypassing the search.
	EnvConfigFile = "TOOLBOX_CONFIG"
)

// The configuration keys. They are named once and used everywhere, so a key cannot be
// spelled two ways in two places.
const (
	KeyDaemonHost   = "daemon.host"
	KeyDaemonPort   = "daemon.port"
	KeyDaemonLaunch = "daemon.launch"
	KeyMCPHost      = "mcp.host"
	KeyMCPPort      = "mcp.port"
	KeyPolicy       = "policy"
)

// Launch is how the daemon's lifecycle is managed. It is a Go enum rather than a protobuf
// one because it configures a client and is never served over the wire, and a contract
// nobody serves would be a contract with no reader.
//
// The value is not documentation. Each changes what a client does when the core does not
// answer, which is the only moment it matters.
type Launch string

const (
	// LaunchAuto starts a daemon when a client finds no core answering, the way a tmux
	// server starts when no session exists. The default.
	//
	// A daemon is started only for an address on this machine: an address elsewhere is
	// somebody else's daemon, and this one has no business launching anything for it.
	LaunchAuto Launch = "auto"

	// LaunchExplicit never starts a daemon and never falls back to a private instance when
	// the configured core does not answer. It says the core did not answer and stops.
	//
	// This is for a machine whose core is a systemd unit. The lifecycle belongs to the init
	// system, and a client that quietly served a private copy would report a working command
	// while the service that owns the state was down — and a write would go somewhere nobody
	// is managing.
	LaunchExplicit Launch = "explicit"

	// LaunchDisabled never starts a daemon and never requires one to be running: the client
	// serves from the instance in this process. Two things follow, and both are enforced.
	// The daemon subcommand is refused, because a core this installation does not run is not
	// something it can start. And the configuration must name the address to connect to,
	// because peers that are not in this process are found through a core, and a deployment
	// that disabled the daemon and named no core has said nothing about where anything is.
	LaunchDisabled Launch = "disabled"
)

// LaunchModes is every accepted value, in the order they are reported.
var LaunchModes = []Launch{LaunchAuto, LaunchExplicit, LaunchDisabled}

// ParseLaunch reads a launch mode, refusing anything else by name.
//
// A mode that is not one of the three is refused rather than defaulted, because a
// deployment that meant "explicit" and got "auto" would start a daemon somebody manages with
// systemd, and the two would fight over the address.
func ParseLaunch(value string) (Launch, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	for _, mode := range LaunchModes {
		if Launch(trimmed) == mode {
			return mode, nil
		}
	}
	names := make([]string, 0, len(LaunchModes))
	for _, mode := range LaunchModes {
		names = append(names, string(mode))
	}
	return "", fmt.Errorf("%q is not a way to launch the daemon; it must be one of: %s",
		value, strings.Join(names, ", "))
}

// Source is where a resolved value came from. It is reported rather than inferred by a
// reader, because "which of three places did this port come from" is otherwise a question
// only the process that resolved it can answer.
type Source string

const (
	// SourceDefault is a built-in default: nobody chose it, and it is right for a
	// deployment that has said nothing.
	SourceDefault Source = "default"
	// SourceFile is a configuration file.
	SourceFile Source = "config file"
	// SourceEnvironment is an environment variable.
	SourceEnvironment Source = "environment"
	// SourceFlag is a command-line flag the caller gave.
	SourceFlag Source = "flag"
)

// Listen is a resolved bind address: where something binds, and where a client dials.
type Listen struct {
	// Host is an address to bind or dial. A hostname, an IP, or a DNS name.
	Host string
	// Port is the TCP port.
	Port int
}

// Addr returns the address as "host:port".
func (l Listen) Addr() string { return net.JoinHostPort(l.Host, strconv.Itoa(l.Port)) }

// URL returns the address as an http URL, which is what a ConnectRPC client and a Model
// Context Protocol endpoint both take.
func (l Listen) URL() string { return "http://" + l.Addr() }

// Config is a resolved deployment configuration.
type Config struct {
	// Daemon is the address the daemon binds and clients dial.
	Daemon Listen
	// MCP is the address the Model Context Protocol endpoint binds. It defaults to the
	// daemon's address, so a small deployment is one port; a deployment that exposes agent
	// tools to a network gives them an address of their own, so that surface can be
	// firewalled and authorised separately from an internal directory.
	MCP Listen
	// Launch is how the daemon's lifecycle is managed.
	Launch Launch
	// PolicyPath is the policy document the in-process core reads. A daemon-backed client
	// has no use for it and refuses one, because the daemon's policy is authoritative for
	// everything it serves.
	PolicyPath string
	// Scope is the workspace this client works in. It is resolved from a flag or the
	// environment and never from a file: it differs per invocation, not per installation.
	Scope string
	// Path is the configuration file that was read, or empty when none was.
	Path string
	// sources records where each value came from, keyed by configuration key.
	sources map[string]Source
}

// SourceOf reports where a value came from. An unknown key is a default, because a value
// nobody supplied is the built-in one.
func (c Config) SourceOf(key string) Source {
	if source, ok := c.sources[key]; ok {
		return source
	}
	return SourceDefault
}

// Sources returns the provenance of every resolved value, keyed by configuration key, so a
// caller can report all of them.
func (c Config) Sources() map[string]Source {
	out := make(map[string]Source, len(c.sources))
	for key, source := range c.sources {
		out[key] = source
	}
	return out
}

// SameAddress reports whether two listen addresses are the same. The daemon uses it to
// decide whether the Model Context Protocol endpoint can be mounted on the daemon's own
// server, which is what keeps a one-port deployment one port.
func SameAddress(a, b Listen) bool { return a.Addr() == b.Addr() }

// knownKeys is every key a configuration file may contain.
//
// The set is stated rather than derived from a struct, because the check it exists for is
// "did this file say something nobody here understands" — and a struct would only be a second
// statement of the same list, free to drift from the constants above.
var knownKeys = map[string]bool{
	KeyDaemonHost:   true,
	KeyDaemonPort:   true,
	KeyDaemonLaunch: true,
	KeyMCPHost:      true,
	KeyMCPPort:      true,
	KeyPolicy:       true,
}

// Loader reads the configuration layers in order and reports what each value resolved to.
//
// It wraps one private viper instance rather than the package-level one. A global
// configuration singleton is shared by every consumer in the process, which is how a test
// that sets a value changes the behaviour of a test that did not.
type Loader struct {
	viper *viper.Viper
	flags map[string]*pflag.Flag
	// workDir is where the project file is searched from, walking up. It is stored rather
	// than read from the process at load time, because a caller that resolved from a
	// directory it chose must get the same answer as one that resolved from the working
	// directory — and a test must be able to say which directory it meant.
	workDir string
	// coreFlag is the "host:port" flag, kept so its value can be attributed to a source.
	coreFlag *pflag.Flag
	path     string
	loaded   bool
}

// Options configure a Loader.
type Options struct {
	// ConfigFile names a configuration file explicitly, bypassing the search. A name that
	// does not exist is an error: somebody asked for a configuration and there is none.
	ConfigFile string
	// WorkDir is where the project file is searched from, walking up.
	WorkDir string
	// Flags are the caller's flag set, bound so a flag the caller gave wins. A nil set, or
	// a flag that is absent, simply means that layer has nothing to say.
	Flags *pflag.FlagSet
}

// New creates a Loader.
//
// Nothing is read yet: the file is found and parsed on the first resolution, so a caller
// that only wants the search path can ask for it without touching the disk.
func New(options Options) *Loader {
	v := viper.New()
	v.SetEnvPrefix(EnvPrefix)
	// A key is a dotted path; its variable is the upper-cased path with dots as
	// underscores. daemon.launch is TOOLBOX_DAEMON_LAUNCH, which is what a person would
	// write without being told.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	// Defaults are declared rather than applied afterwards, because viper consults the
	// defaults to know which keys exist. Without them an environment-only key is invisible:
	// viper has never heard of it, so it never looks for its variable.
	v.SetDefault(KeyDaemonHost, DefaultHost)
	v.SetDefault(KeyDaemonPort, DefaultPort)
	v.SetDefault(KeyDaemonLaunch, string(LaunchAuto))
	v.SetDefault(KeyMCPHost, "")
	v.SetDefault(KeyMCPPort, 0)
	v.SetDefault(KeyPolicy, "")

	workDir := strings.TrimSpace(options.WorkDir)
	if workDir == "" {
		if dir, err := os.Getwd(); err == nil {
			workDir = dir
		}
	}
	loader := &Loader{viper: v, flags: make(map[string]*pflag.Flag), workDir: workDir}
	if options.Flags != nil {
		loader.bindFlags(options.Flags)
	}
	return loader
}

// bindFlags binds the caller's flags to keys.
//
// viper consults a bound flag only when it was Changed, which is exactly the precedence
// wanted: a flag the caller gave wins, and a flag left at its default contributes nothing
// and cannot be mistaken for a choice.
func (l *Loader) bindFlags(flags *pflag.FlagSet) {
	for key, name := range map[string]string{
		KeyDaemonHost:   "daemon-host",
		KeyDaemonPort:   "daemon-port",
		KeyDaemonLaunch: "launch",
		KeyMCPHost:      "mcp-host",
		KeyMCPPort:      "mcp-port",
		KeyPolicy:       "policy",
	} {
		flag := flags.Lookup(name)
		if flag == nil {
			continue
		}
		l.flags[key] = flag
		_ = l.viper.BindPFlag(key, flag)
	}
	// The port flag is the older spelling of the daemon's port. Bound to the same key so
	// there is one value with two names rather than two values with a precedence between
	// them.
	if flag := flags.Lookup("port"); flag != nil {
		if _, alreadyBound := l.flags[KeyDaemonPort]; !alreadyBound {
			l.flags[KeyDaemonPort] = flag
			_ = l.viper.BindPFlag(KeyDaemonPort, flag)
		}
	}
	// --core carries "host:port", which is two facts and so cannot be a key. It is split
	// here, into the two keys, before anything is read: from there on there is one value per
	// key and the precedence chain is viper's alone. Splitting in the command instead would
	// mean a caller that resolves through the loader and one that resolves through cobra
	// could disagree about what --core means.
	if flag := flags.Lookup("core"); flag != nil {
		l.coreFlag = flag
		l.splitCoreFlag(flags)
	}
}

// splitCoreFlag writes a "host:port" flag value into the two keys it means.
//
// The specific flags win, because somebody who adopted them should not be surprised by a
// stale --core in a script, and a pair cannot be expressed as one key so there is nowhere
// else for the precedence to live.
func (l *Loader) splitCoreFlag(flags *pflag.FlagSet) {
	value := strings.TrimSpace(l.coreFlag.Value.String())
	if value == "" {
		return
	}
	hostFlag := flags.Lookup("daemon-host")
	portFlag := flags.Lookup("daemon-port")
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		// A bare host, which is a legitimate thing to write: it leaves the port alone.
		if hostFlag != nil && !hostFlag.Changed {
			_ = flags.Set("daemon-host", value)
		}
		return
	}
	if trimmed := strings.TrimSpace(host); trimmed != "" && hostFlag != nil && !hostFlag.Changed {
		_ = flags.Set("daemon-host", trimmed)
	}
	if parsed, convErr := strconv.Atoi(port); convErr == nil && parsed > 0 &&
		portFlag != nil && !portFlag.Changed {
		_ = flags.Set("daemon-port", strconv.Itoa(parsed))
	}
}

// SearchPath returns where a configuration file is looked for, in order, so a caller can
// tell a user where to put one.
func SearchPath(workDir string) []string {
	candidates := make([]string, 0, 4)
	if dir := strings.TrimSpace(workDir); dir != "" {
		if found, ok := projectFile(dir); ok {
			candidates = append(candidates, found)
		} else {
			candidates = append(candidates, filepath.Join(dir, ProjectDir, FileName))
		}
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		candidates = append(candidates, filepath.Join(dir, "toolbox", FileName))
	}
	return candidates
}

// projectFile walks up from a directory looking for a per-project configuration file.
//
// Walking up is what makes a project's configuration work the same in a subdirectory as at
// its root, which is the convention every other tool in this ecosystem follows and the
// reason it surprises nobody. Stopping at the first one found is what makes a nested
// checkout its own deployment rather than a silent extension of its parent's.
func projectFile(dir string) (string, bool) {
	current, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(current, ProjectDir, FileName)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

// load reads the configuration file, once.
func (l *Loader) load(explicit string) error {
	if l.loaded {
		return nil
	}
	l.loaded = true

	// The path is resolved here rather than through viper's own search, because viper
	// searches one directory and this searches upward from the working directory with the
	// project's file ahead of the user's. That ordering is a decision, and a decision this
	// package makes explicitly.
	explicit = strings.TrimSpace(explicit)
	if explicit == "" {
		explicit = strings.TrimSpace(os.Getenv(EnvConfigFile))
	}
	path := explicit
	if path == "" {
		workDir := l.workDir
		if workDir == "" {
			workDir = "."
		}
		candidates := SearchPath(workDir)
		for _, candidate := range candidates {
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if explicit != "" {
			return fmt.Errorf("the configuration at %s: %w", path, err)
		}
		return nil
	}
	l.path = path
	l.viper.SetConfigFile(path)
	// The type is stated because an explicitly named file may have any extension, and
	// viper infers from the extension when it can. YAML is the default for a name that says
	// nothing.
	l.viper.SetConfigType(configTypeFor(path))
	if err := l.viper.ReadInConfig(); err != nil {
		return fmt.Errorf("the configuration at %s: %w", path, err)
	}
	return nil
}

// unknownFileKeys returns the dotted keys the file states that this package does not know.
//
// Read from a second, bare viper instance rather than from the resolving one, so the keys are
// the file's alone: the resolver also knows the defaults and the bound flags, and a check
// that could not tell those apart from the file's would accept anything.
func (l *Loader) unknownFileKeys() []string {
	if l.path == "" {
		return nil
	}
	bare := viper.New()
	bare.SetConfigFile(l.path)
	bare.SetConfigType(configTypeFor(l.path))
	if err := bare.ReadInConfig(); err != nil {
		// Already reported by the resolving read, which ran first and failed loudly.
		return nil
	}
	var unknown []string
	for _, key := range flattenKeys(bare.AllSettings(), "") {
		if !knownKeys[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// flattenKeys turns nested settings into dotted keys, the form the constants are written in.
func flattenKeys(settings map[string]any, prefix string) []string {
	var keys []string
	for name, value := range settings {
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		if nested, ok := value.(map[string]any); ok {
			keys = append(keys, flattenKeys(nested, key)...)
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

func sortedKeys() []string {
	keys := make([]string, 0, len(knownKeys))
	for key := range knownKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

func configTypeFor(path string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "json":
		return "json"
	case "toml":
		return "toml"
	default:
		return "yaml"
	}
}

// Resolve reads every layer and returns the configuration they agree on.
func (l *Loader) Resolve(explicit string) (Config, error) {
	if err := l.load(explicit); err != nil {
		return Config{}, err
	}

	// A key the file contains that this package does not understand is refused, by name.
	// A configuration file that silently ignores what it does not understand is a file that
	// cannot be trusted to state what it does, and a misspelled "prot" is otherwise
	// indistinguishable from a deliberate default.
	if unknown := l.unknownFileKeys(); len(unknown) > 0 {
		return Config{}, fmt.Errorf(
			"the configuration at %s has %s nobody understands: %s. The keys a deployment "+
				"may state are: %s",
			l.path, plural(len(unknown), "key", "keys"), strings.Join(unknown, ", "),
			strings.Join(sortedKeys(), ", "))
	}

	applyLegacyEnvironment(l.viper)

	resolved := Config{
		Daemon: Listen{
			Host: strings.TrimSpace(l.viper.GetString(KeyDaemonHost)),
			Port: l.viper.GetInt(KeyDaemonPort),
		},
		PolicyPath: strings.TrimSpace(l.viper.GetString(KeyPolicy)),
		Scope:      l.scope(),
		Path:       l.path,
		sources:    l.provenance(),
	}

	launch, err := ParseLaunch(l.viper.GetString(KeyDaemonLaunch))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", l.describe(KeyDaemonLaunch), err)
	}
	resolved.Launch = launch

	// The Model Context Protocol endpoint defaults to the daemon's address, so a deployment
	// that says nothing has one port. It is a computed default rather than a declared one,
	// because it depends on the daemon's address, which is itself resolved.
	resolved.MCP = Listen{
		Host: strings.TrimSpace(l.viper.GetString(KeyMCPHost)),
		Port: l.viper.GetInt(KeyMCPPort),
	}
	if resolved.MCP.Host == "" {
		resolved.MCP.Host = resolved.Daemon.Host
	}
	if resolved.MCP.Port == 0 {
		resolved.MCP.Port = resolved.Daemon.Port
	}

	if err := resolved.validate(); err != nil {
		return Config{}, err
	}
	return resolved, nil
}

// scope is the workspace selector, which a file must not carry.
//
// It is read from the environment alone: a flag is bound by the caller, and a file
// deliberately has no key for it, because the workspace differs per invocation and a file
// describes the installation.
func (l *Loader) scope() string {
	return strings.TrimSpace(os.Getenv(EnvPrefix + "_SCOPE"))
}

// applyLegacyEnvironment handles the convenience variables that do not map to one key.
//
// TOOLBOX_CORE carries "host:port", which is two facts and so cannot be a configuration
// key; it is split here and applied at the environment layer. It yields to the variables
// that do map to keys, so a deployment that has adopted TOOLBOX_DAEMON_HOST is not
// surprised by a stale TOOLBOX_CORE in a shell profile.
func applyLegacyEnvironment(v *viper.Viper) {
	if strings.TrimSpace(os.Getenv(EnvPrefix+"_"+envSuffix(KeyDaemonHost))) != "" ||
		strings.TrimSpace(os.Getenv(EnvPort)) != "" {
		return
	}
	value := strings.TrimSpace(os.Getenv(EnvCore))
	if value == "" {
		return
	}
	host, port, err := net.SplitHostPort(value)
	if err == nil {
		if trimmed := strings.TrimSpace(host); trimmed != "" {
			v.Set(KeyDaemonHost, trimmed)
		}
		if parsed, convErr := strconv.Atoi(port); convErr == nil && parsed > 0 {
			v.Set(KeyDaemonPort, parsed)
		}
		return
	}
	v.Set(KeyDaemonHost, value)
}

func envSuffix(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// Validate refuses a configuration that cannot be acted on.
//
// It is exported because a caller that adjusts a resolved configuration — a flag applied
// after the layers, say — has to be able to check the result, and a check it cannot reach is
// a check that does not happen.
func (c Config) Validate() error { return c.validate() }

// validate refuses a configuration that cannot be acted on.
func (c Config) validate() error {
	if strings.TrimSpace(c.Daemon.Host) == "" {
		return fmt.Errorf("the daemon needs a host; set daemon.host in the configuration or pass --daemon-host")
	}
	if err := validPort(c.Daemon.Port, "the daemon port"); err != nil {
		return err
	}
	if strings.TrimSpace(c.MCP.Host) == "" {
		return fmt.Errorf("the Model Context Protocol endpoint needs a host")
	}
	if err := validPort(c.MCP.Port, "the Model Context Protocol port"); err != nil {
		return err
	}
	// A deployment that has disabled the daemon and named no core has said nothing about
	// where anything is: there is no daemon to ask, and peers outside this process are
	// found through one. Reported here, as an incomplete configuration, rather than as a
	// resolution failure on some later call.
	if c.Launch == LaunchDisabled && !c.coreStated() {
		return fmt.Errorf(
			"daemon.launch is %q, so the configuration must state which core to connect to: "+
				"set daemon.host and daemon.port, or pass --daemon-host and --daemon-port", LaunchDisabled)
	}
	return nil
}

// coreStated reports whether the daemon's address came from somewhere other than the
// built-in default, which is what "the configuration names a core" has to mean for a
// deployment that disabled the daemon.
func (c Config) coreStated() bool {
	return c.SourceOf(KeyDaemonHost) != SourceDefault || c.SourceOf(KeyDaemonPort) != SourceDefault
}

func validPort(port int, what string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s is %d, which is not a port", what, port)
	}
	return nil
}

// provenance works out where each value came from.
//
// viper resolves the precedence but does not report which layer won, and "which of four
// places did this port come from" is otherwise a question only this process can answer. The
// layers are checked in the same order they are applied, so the answer is the first one
// that could have supplied the value.
func (l *Loader) provenance() map[string]Source {
	sources := make(map[string]Source, 6)
	for _, key := range []string{KeyDaemonHost, KeyDaemonPort, KeyDaemonLaunch, KeyMCPHost, KeyMCPPort, KeyPolicy} {
		switch {
		case l.flagChanged(key):
			sources[key] = SourceFlag
		case l.environmentSet(key):
			sources[key] = SourceEnvironment
		case l.path != "" && l.viper.InConfig(key):
			sources[key] = SourceFile
		default:
			sources[key] = SourceDefault
		}
	}
	return sources
}

func (l *Loader) flagChanged(key string) bool {
	// The pair flag counts as having supplied either key it was split into, so a caller
	// who wrote --core is not told the value came from a default they never chose.
	if l.coreFlag != nil && l.coreFlag.Changed &&
		(key == KeyDaemonHost || key == KeyDaemonPort) {
		return true
	}
	flag, ok := l.flags[key]
	return ok && flag.Changed
}

func (l *Loader) environmentSet(key string) bool {
	return strings.TrimSpace(os.Getenv(EnvPrefix+"_"+envSuffix(key))) != ""
}

// describe names a value's origin, so an error about one of them says which one.
func (l *Loader) describe(key string) string {
	switch l.sourceOf(key) {
	case SourceFlag:
		return "the --" + strings.ReplaceAll(key, ".", "-") + " flag"
	case SourceEnvironment:
		return EnvPrefix + "_" + envSuffix(key)
	case SourceFile:
		return l.path
	default:
		return "the built-in default"
	}
}

func (l *Loader) sourceOf(key string) Source {
	for _, source := range []Source{SourceFlag, SourceEnvironment, SourceFile, SourceDefault} {
		if source == SourceFlag && l.flagChanged(key) {
			return SourceFlag
		}
		if source == SourceEnvironment && l.environmentSet(key) {
			return SourceEnvironment
		}
		if source == SourceFile && l.path != "" && l.viper.InConfig(key) {
			return SourceFile
		}
	}
	return SourceDefault
}

// Describe names where a value came from, for a message that needs to say so.
func (c Config) describe(key string) string {
	switch c.SourceOf(key) {
	case SourceFlag:
		return "the --" + strings.ReplaceAll(key, ".", "-") + " flag"
	case SourceEnvironment:
		return EnvPrefix + "_" + envSuffix(key)
	case SourceFile:
		return c.Path
	default:
		return "the built-in default"
	}
}

// Report renders the resolved configuration and where each value came from, as the lines a
// command prints before it does anything.
//
// It reports rather than prints: a caller decides where the text goes, because the command
// line writes it to its error stream and a test reads it from a buffer.
func (c Config) Report() []string {
	lines := []string{
		"toolbox: daemon at " + c.Daemon.Addr() + " (" + c.sourcesPhrase() + ")",
		"toolbox: Model Context Protocol at " + c.MCP.Addr(),
		"toolbox: daemon launch is " + string(c.Launch) + " (" + string(c.SourceOf(KeyDaemonLaunch)) + ")",
	}
	if c.PolicyPath != "" {
		lines = append(lines, "toolbox: policy "+c.PolicyPath+" ("+string(c.SourceOf(KeyPolicy))+")")
	}
	if c.Scope != "" {
		lines = append(lines, "toolbox: workspace "+c.Scope)
	}
	// The file is named last, so it is the line a reader looks for once they have read what
	// the values were.
	if c.Path != "" {
		lines = append(lines, "toolbox: configuration from "+c.Path)
	}
	return lines
}

// sourcesPhrase names the layer the daemon's address came from, once for the pair rather
// than twice: a reader wants to know whether they configured this, not which half of an
// address came from where.
func (c Config) sourcesPhrase() string {
	host, port := c.SourceOf(KeyDaemonHost), c.SourceOf(KeyDaemonPort)
	if host == port {
		return string(host)
	}
	return string(host) + " host, " + string(port) + " port"
}
