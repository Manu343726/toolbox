package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	registry "github.com/Manu343726/toolbox/subsystems/registry"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        registry.Name,
		Description: "Service registration and discovery subsystem",
		Factory: func() (*subsystem.Server, error) {
			return registry.New(registry.Options{})
		},
	})
}
