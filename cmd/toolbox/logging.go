package main

import (
	"fmt"
	"os"

	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/log"
	"github.com/Manu343726/toolbox/subsystems/logfile"
)

// fanout is a deployment's logging: the router every handler and the logger subsystem use,
// and the registry that built it, which is what a project's own configuration is built from.
type fanout struct {
	Router   *log.Router
	Registry *log.Registry
}

// silentFanout is the fanout of a host composed only to be described.
//
// It discards everything and opens nothing. Describing a deployment must not create its log
// files: the command tree is built before any command has parsed its flags, so there is no
// deployment to configure yet, and asking what a command can do should not leave a log
// behind. It is a real router rather than a nil one, because describing a host composes every
// subsystem's factory and a factory that needs a fanout should not be describing one that
// does not exist.
func silentFanout() (fanout, error) {
	registry := log.NewRegistry()
	if err := registry.Register(logfile.LogProvider()); err != nil {
		return fanout{}, err
	}
	settings, err := log.ParseConfig(map[string]any{
		"level":    "info",
		"handlers": map[string]any{"discarded": map[string]any{"provider": log.ProviderNull}},
		"routes":   []any{map[string]any{"handlers": []any{"discarded"}}},
	})
	if err != nil {
		return fanout{}, err
	}
	router, err := registry.Build(settings)
	if err != nil {
		return fanout{}, err
	}
	return fanout{Router: router, Registry: registry}, nil
}

// buildFanout turns the configuration file's logging section into a router.
//
// It is built before any subsystem starts, and it is built from the file rather than from
// flags, because a deployment's routes are the deployment's. A deployment that said nothing
// still gets a file named after the command, and a message saying so: a tool that silently
// records nothing is a tool nobody can debug.
func buildFanout(resolved config.Config) (fanout, error) {
	registry := log.NewRegistry()
	// The backends a subsystem provides are registered here, before the subsystems start,
	// because the fanout is built before they do. A deployment's configuration can therefore
	// name a backend that this command ships, which is what makes the backend list the
	// subsystem's role rather than a special case in the host.
	if err := registry.Register(logfile.LogProvider()); err != nil {
		return fanout{}, err
	}
	registry.BaseDir = resolved.LoggingBaseDir

	if len(resolved.Logging) == 0 {
		settings, err := log.ParseConfig(map[string]any{
			"level": "info",
			"handlers": map[string]any{
				"file": map[string]any{
					"provider": log.ProviderText,
					"options":  map[string]any{"path": "toolbox.log"},
				},
			},
			"routes": []any{map[string]any{"handlers": []any{"file"}}},
		})
		if err != nil {
			return fanout{}, err
		}
		router, err := registry.Build(settings)
		if err != nil {
			return fanout{}, err
		}
		fmt.Fprintln(os.Stderr, "toolbox: no logging section in the configuration; writing to toolbox.log")
		return fanout{Router: router, Registry: registry}, nil
	}

	settings, err := log.ParseConfig(resolved.Logging)
	if err != nil {
		return fanout{}, fmt.Errorf("the logging section: %w", err)
	}
	router, err := registry.Build(settings)
	if err != nil {
		return fanout{}, err
	}
	return fanout{Router: router, Registry: registry}, nil
}
