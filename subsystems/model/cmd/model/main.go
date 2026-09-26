package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	model "github.com/Manu343726/toolbox/subsystems/model"
	"github.com/Manu343726/toolbox/subsystems/registry"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        model.Name,
		Description: "Provider-neutral model subsystem",
		// A core is at one address, and that address is its registry's. This is how
		// a command finds the peer a core hosts, so pointing this command at a core
		// reaches the deployment rather than a private instance of this subsystem.
		ResolverFor: registry.CoreResolver,
		Factory: func() (*subsystem.Server, error) {
			return model.New(model.Options{})
		},
	})
}
