package main

import (
	"fmt"
	"os"

	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/spf13/cobra"
)

// Every command resolves the core address the same way, from the same places in
// the same order, so the daemon, this CLI acting as a client, and a subsystem
// command all agree without coordinating. The order is: flag, then environment,
// then the configuration file, then the built-in default. See
// docs/decisions/0010-core-and-daemon.md.

// resolveConfig reads the core address, the policy, and the scope for one command.
//
// The working directory is part of the search path, so a project file is found
// when the command runs inside a project and the user's file is the fallback. A
// command run from anywhere else gets the user's.
func resolveConfig(cmd *cobra.Command) (config.Config, error) {
	core, err := cmd.Flags().GetString("core")
	if err != nil {
		return config.Config{}, err
	}
	port, err := cmd.Flags().GetInt("port")
	if err != nil {
		return config.Config{}, err
	}
	policyPath, err := cmd.Flags().GetString("policy")
	if err != nil {
		return config.Config{}, err
	}
	scope, err := cmd.Flags().GetString("scope")
	if err != nil {
		return config.Config{}, err
	}
	workDir, err := os.Getwd()
	if err != nil {
		return config.Config{}, err
	}
	return config.Resolve(config.Overrides{
		Core:       core,
		Port:       port,
		Policy:     policyPath,
		Scope:      scope,
		ConfigFile: os.Getenv(config.EnvConfigFile),
	}, workDir)
}

// reportConfig writes where the values came from, so a command that behaves
// unexpectedly can be diagnosed from its own output rather than by guessing which
// layer set what.
func reportConfig(cmd *cobra.Command, resolved config.Config) {
	out := cmd.ErrOrStderr()
	if resolved.Path != "" {
		fmt.Fprintf(out, "toolbox: configuration from %s, core at %s\n", resolved.Path, resolved.Core.Addr())
		return
	}
	fmt.Fprintf(out, "toolbox: core at %s\n", resolved.Core.Addr())
}
