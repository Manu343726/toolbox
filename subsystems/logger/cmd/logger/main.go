package main

import (
	"fmt"
	"os"

	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/log"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	logger "github.com/Manu343726/toolbox/subsystems/logger"
	"github.com/Manu343726/toolbox/subsystems/registry"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        logger.Name,
		Description: "Log fanout subsystem: a deployment's routes, over ConnectRPC",
		// A core is at one address, and that address is its registry's. This is how
		// a command finds the peer a core hosts, so pointing this command at a core
		// reaches the deployment rather than a private instance of this subsystem.
		ResolverFor: registry.CoreResolver,
		Factory: func() (*subsystem.Server, error) {
			// The fanout is the deployment's configuration, so it is read from the
			// configuration file the same way a core reads it. This command is the case
			// where that matters: a logger started on its own has no in-process
			// handlers to share a router with, so it builds one from the file.
			resolved, err := config.New(config.Options{}).Resolve("")
			if err != nil {
				return nil, fmt.Errorf("read the deployment configuration: %w", err)
			}
			router, registry_, err := buildRouter(resolved)
			if err != nil {
				return nil, err
			}
			return logger.New(logger.Options{
				Router:    router,
				Providers: registry_.Providers(),
			})
		},
	})
}

// buildRouter turns the configuration file's logging section into a fanout.
//
// The base directory is the file's own directory, so a relative path in it means a path in
// the project that wrote it rather than wherever this command happened to start.
func buildRouter(resolved config.Config) (*log.Router, *log.Registry, error) {
	registry_ := log.NewRegistry()
	registry_.BaseDir = resolved.LoggingBaseDir
	if len(resolved.Logging) == 0 {
		// A deployment that configured no logging still needs somewhere to write: it gets a
		// file named after the subsystem, and a message saying so, because a logger that
		// silently records nothing is a logger nobody trusts.
		fanout, err := log.ParseConfig(map[string]any{
			"level": "info",
			"handlers": map[string]any{
				"file": map[string]any{
					"provider": log.ProviderText,
					"options":  map[string]any{"path": "logger.log"},
				},
			},
			"routes": []any{
				map[string]any{"handlers": []any{"file"}},
			},
		})
		if err != nil {
			return nil, nil, err
		}
		router, err := registry_.Build(fanout)
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintln(os.Stderr,
			"toolbox: no logging section in the configuration; writing to logger.log")
		return router, registry_, nil
	}
	fanout, err := log.ParseConfig(resolved.Logging)
	if err != nil {
		return nil, nil, fmt.Errorf("the logging section: %w", err)
	}
	router, err := registry_.Build(fanout)
	if err != nil {
		return nil, nil, err
	}
	return router, registry_, nil
}
