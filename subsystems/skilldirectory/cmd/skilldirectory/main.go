package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/registry"
	"github.com/Manu343726/toolbox/subsystems/skilldirectory"
	"github.com/Manu343726/toolbox/subsystems/skillgit"
)

// main runs the contributor on its own.
//
// A contributor has nothing to serve, so this command does not exist to be called: it exists
// so the subsystem can be developed, built and tested as a subsystem like any other, and so a
// person can run it alone to see what it would contribute. Run as part of a host, it
// contributes and is finished.
func main() {
	cliapp.Main(cliapp.Options{
		Name:        skilldirectory.Name,
		Description: skilldirectory.Description,
		ResolverFor: registry.CoreResolver,
		Factory: func() (*subsystem.Server, error) {
			return skilldirectory.New(skilldirectory.Options{
				DataDir: skillgit.DefaultDataDir(),
			})
		},
	})
}
