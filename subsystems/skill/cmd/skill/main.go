package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/registry"
	skill "github.com/Manu343726/toolbox/subsystems/skill"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        skill.Name,
		Description: "Versioned skill subsystem",
		// A core is at one address, and that address is its registry's. This is how
		// a command finds the peer a core hosts, so pointing this command at a core
		// reaches the deployment rather than a private instance of this subsystem.
		ResolverFor: registry.CoreResolver,
		Factory: func() (*subsystem.Server, error) {
			return skill.New(skill.Options{})
		},
	})
}
