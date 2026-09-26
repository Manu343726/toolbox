package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/registry"
	testecho "github.com/Manu343726/toolbox/subsystems/testecho"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        testecho.Name,
		Description: "Reference echo subsystem",
		// A core is at one address, and that address is its registry's. This is how
		// a command finds the peer a core hosts, so pointing this command at a core
		// reaches the deployment rather than a private instance of this subsystem.
		ResolverFor: registry.CoreResolver,
		Factory: func() (*subsystem.Server, error) {
			return testecho.New(testecho.Options{})
		},
	})
}
