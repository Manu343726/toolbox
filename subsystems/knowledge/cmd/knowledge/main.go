package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	knowledge "github.com/Manu343726/toolbox/subsystems/knowledge"
	"github.com/Manu343726/toolbox/subsystems/registry"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        knowledge.Name,
		Description: "Knowledge source and retrieval subsystem",
		// A core is at one address, and that address is its registry's. This is how
		// a command finds the peer a core hosts, so pointing this command at a core
		// reaches the deployment rather than a private instance of this subsystem.
		ResolverFor: registry.CoreResolver,
		Factory: func() (*subsystem.Server, error) {
			return knowledge.New(knowledge.Options{})
		},
	})
}
