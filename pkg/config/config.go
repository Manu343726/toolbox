// Package config resolves where the core is and what a client should do about it.
//
// One value, resolved identically by the daemon that binds it, the CLI acting as a
// client, and every subsystem command. They agree because they read the same
// places in the same order, not because they coordinate.
//
// There is no discovery protocol. A process that wants the core knows where it is:
// a flag, then the environment, then a configuration file, then a built-in default.
// A core that is not there is a connection failure, not a timeout waiting for
// something to appear — which is the behaviour a command wants, because "no daemon
// is running" is an answer rather than a hang.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultHost is the address a core binds and clients dial when nothing says
// otherwise. A loopback default is deliberate: a core reached over the network
// without being asked for is a surprise, and every deployment that wants one
// configures it.
const DefaultHost = "127.0.0.1"

// DefaultPort is the port a core binds and clients dial when nothing says
// otherwise. It is a fixed number rather than a discovered one, because that is
// what makes a subsystem able to find the core with no configuration at all.
const DefaultPort = 9180

// FileName is the configuration file's name, in whichever directory it is looked
// for.
const FileName = "config.yaml"

// Environment variables, in the order they are consulted.
const (
	// EnvCore is a "host:port" pair, or a bare host to use with EnvPort.
	EnvCore = "TOOLBOX_CORE"
	// EnvPort is a port number on its own.
	EnvPort = "TOOLBOX_PORT"
	// EnvConfigFile names a configuration file explicitly, bypassing the search.
	EnvConfigFile = "TOOLBOX_CONFIG"
)

// Core is a resolved core address.
type Core struct {
	// Host is the address to bind or dial. A hostname, an IP, or a DNS name.
	Host string
	// Port is the TCP port.
	Port int
}

// Config is a resolved configuration.
type Config struct {
	// Core is where the core is.
	Core Core
	// PolicyPath is the policy document the in-process core reads. A daemon-backed
	// client has no use for it and refuses one, because the daemon's policy is
	// authoritative for everything it serves.
	PolicyPath string
	// Path is the file the values came from, or empty for flag and environment.
	Path string
	// Scope is the workspace this client works in. Empty is the default scope.
	//
	// Scoping is per client and per project, so a single core can serve many
	// projects with different configuration, policy, and knowledge. It propagates
	// as core.Metadata.WorkspaceID on every call the client makes.
	Scope string
}

// Addr returns the address as "host:port".
func (c Core) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// URL returns the address as an http URL, which is what a ConnectRPC client and
// an MCP endpoint both take.
func (c Core) URL() string { return "http://" + c.Addr() }

// File is the on-disk shape. Every field is optional; the empty file is valid and
// means "all defaults".
type File struct {
	Core struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"core"`
	Policy string `yaml:"policy"`
	Scope  string `yaml:"scope"`
}

// Overrides are the values a caller supplies on the command line, which win over
// every file and every environment variable.
type Overrides struct {
	// Core is a "host:port" pair or a bare host.
	Core string
	// Port is a port number on its own. Zero leaves the port alone.
	Port int
	// Policy names a policy document.
	Policy string
	// Scope names the workspace this client works in.
	Scope string
	// ConfigFile names a configuration file explicitly.
	ConfigFile string
}

// Resolve reads every source in order and returns what they agree on.
//
// A file that does not exist is not an error: a deployment with no configuration
// is the common case and must not have to create an empty file to start. A file
// that exists and cannot be parsed *is* an error, because a mis-parsed policy or
// address that silently fell back to a default would be running on a
// configuration nobody chose.
func Resolve(overrides Overrides, workDir string) (Config, error) {
	resolved := Config{Core: Core{Host: DefaultHost, Port: DefaultPort}}

	path, err := findFile(overrides.ConfigFile, workDir)
	if err != nil {
		return Config{}, err
	}
	if path != "" {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return Config{}, fmt.Errorf("read the configuration at %s: %w", path, readErr)
		}
		var file File
		if err := yaml.Unmarshal(contents, &file); err != nil {
			return Config{}, fmt.Errorf("the configuration at %s: %w", path, err)
		}
		if host := strings.TrimSpace(file.Core.Host); host != "" {
			resolved.Core.Host = host
		}
		if file.Core.Port > 0 {
			resolved.Core.Port = file.Core.Port
		}
		resolved.PolicyPath = strings.TrimSpace(file.Policy)
		resolved.Scope = strings.TrimSpace(file.Scope)
		resolved.Path = path
	}

	applyEnvironment(&resolved)
	return applyOverrides(resolved, overrides)
}

func applyEnvironment(resolved *Config) {
	if value := strings.TrimSpace(os.Getenv(EnvCore)); value != "" {
		applyAddr(resolved, value)
	}
	if value := strings.TrimSpace(os.Getenv(EnvPort)); value != "" {
		if port, err := strconv.Atoi(value); err == nil && port > 0 {
			resolved.Core.Port = port
		}
	}
}

func applyOverrides(resolved Config, overrides Overrides) (Config, error) {
	if value := strings.TrimSpace(overrides.Core); value != "" {
		applyAddr(&resolved, value)
	}
	if overrides.Port > 0 {
		resolved.Core.Port = overrides.Port
	}
	if policy := strings.TrimSpace(overrides.Policy); policy != "" {
		resolved.PolicyPath = policy
	}
	if scope := strings.TrimSpace(overrides.Scope); scope != "" {
		resolved.Scope = scope
	}
	if resolved.Core.Port < 1 || resolved.Core.Port > 65535 {
		return Config{}, fmt.Errorf("the core port %d is not a port", resolved.Core.Port)
	}
	if strings.TrimSpace(resolved.Core.Host) == "" {
		return Config{}, fmt.Errorf("the core needs a host")
	}
	return resolved, nil
}

// applyAddr reads a "host:port" pair, or a bare host, and leaves the port alone
// when the value does not carry one.
func applyAddr(resolved *Config, value string) {
	if host, port, err := net.SplitHostPort(value); err == nil {
		if host = strings.TrimSpace(host); host != "" {
			resolved.Core.Host = host
		}
		if parsed, convErr := strconv.Atoi(port); convErr == nil && parsed > 0 {
			resolved.Core.Port = parsed
		}
		return
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		resolved.Core.Host = trimmed
	}
}

// findFile returns the configuration file to read, or an empty name when there is
// none.
//
// A project file wins over the user's, because a project that pins its own address
// and scope must not be silently overridden by a machine-wide default — and the
// reverse is the annoyance people actually hit, which is why an explicit
// TOOLBOX_CONFIG exists.
func findFile(explicit, workDir string) (string, error) {
	if name := strings.TrimSpace(explicit); name != "" {
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("the configuration at %s: %w", name, err)
		}
		return name, nil
	}
	if name := strings.TrimSpace(os.Getenv(EnvConfigFile)); name != "" {
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("the configuration named by %s, at %s: %w", EnvConfigFile, name, err)
		}
		return name, nil
	}
	for _, candidate := range searchPath(workDir) {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", nil
}

// searchPath is the project's file first, then the user's.
func searchPath(workDir string) []string {
	candidates := make([]string, 0, 2)
	if dir := strings.TrimSpace(workDir); dir != "" {
		candidates = append(candidates, filepath.Join(dir, ".toolbox", FileName))
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		candidates = append(candidates, filepath.Join(dir, "toolbox", FileName))
	}
	return candidates
}

// SearchPath returns where a configuration file is looked for, in order, so a
// caller can tell a user where to put one.
func SearchPath(workDir string) []string { return searchPath(workDir) }
