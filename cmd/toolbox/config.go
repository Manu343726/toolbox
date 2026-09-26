package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/spf13/cobra"
)

// Every command resolves its deployment configuration the same way, from the same places in
// the same order, so the daemon, this CLI acting as a client, and a subsystem command all
// agree without coordinating.
//
// The order is a file, then the environment, then a flag, and a built-in default underneath
// all of them — so a flag the caller gave wins, because the person at the terminal is the
// one who knows what they meant. See docs/decisions/0011-deployment-configuration.md.
//
// The resolution itself is viper's: it reads the file, consults the environment, and honours
// a bound flag only when the flag was changed. This file is the cobra half — declaring the
// flags, binding them to keys, and reporting what was resolved.

// addConfigFlags declares the flags the configuration layers share.
//
// They are persistent and on the root, because every subcommand resolves the same
// deployment: a local flag on the root is invisible to a subcommand, which is a bug a
// caller meets as "flag accessed but not defined" rather than as anything a test notices.
func addConfigFlags(root *cobra.Command) {
	flags := root.PersistentFlags()
	flags.String("daemon-host", "", "Host the daemon binds and clients dial; a flag beats the environment, which beats the configuration file")
	flags.Int("daemon-port", 0, "Port the daemon binds and clients dial")
	flags.String("mcp-host", "", "Host the Model Context Protocol endpoint binds; empty means the daemon's host")
	flags.Int("mcp-port", 0, "Port the Model Context Protocol endpoint binds; 0 means the daemon's port")
	flags.String("launch", "", "How the daemon's lifecycle is managed: auto, explicit, or disabled")
	flags.String("policy", "", "Path to a policy document deciding which operations may be exposed; the default grants every read and nothing that changes state")
	flags.String("config", "", "Path to a configuration file, bypassing the search")
	flags.String("scope", "", "Workspace this client works in; per invocation, and never read from a configuration file")
	flags.String("core", "", "Address of the core as host:port, the short way to point a command at one; a flag beats the environment")
	flags.Int("port", 0, "Deprecated: use --daemon-port")
	_ = flags.MarkDeprecated("port", "use --daemon-port; the port is the daemon's")
}

// resolveConfig reads the deployment configuration for one command.
func resolveConfig(cmd *cobra.Command) (config.Config, error) {
	loader := config.New(config.Options{Flags: cmd.Flags(), WorkDir: workDir()})
	explicit, err := cmd.Flags().GetString("config")
	if err != nil {
		return config.Config{}, err
	}
	resolved, err := loader.Resolve(explicit)
	if err != nil {
		return config.Config{}, err
	}
	// The workspace selector is per invocation, so it is a flag and never a file. It is
	// applied after the layers because there is no layer for it.
	if scope, scopeErr := cmd.Flags().GetString("scope"); scopeErr == nil {
		if trimmed := strings.TrimSpace(scope); trimmed != "" {
			resolved.Scope = trimmed
		}
	}
	return resolved, resolved.Validate()
}

// reportConfig writes where the values came from, so a command that behaves unexpectedly
// can be diagnosed from its own output rather than by guessing which layer set what.
func reportConfig(cmd *cobra.Command, resolved config.Config) {
	for _, line := range resolved.Report() {
		fmt.Fprintln(cmd.ErrOrStderr(), line)
	}
}

// workDir is the directory a project configuration file is searched from.
func workDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
