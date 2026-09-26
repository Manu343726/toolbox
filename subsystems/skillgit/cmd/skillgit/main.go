package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/registry"
	"github.com/Manu343726/toolbox/subsystems/skillgit"
)

// main runs one git-backed skill catalog provider.
//
// It is a standalone command as well as something a host composes, because a deployment's
// catalogs outlive any process: one that keeps its checkouts somewhere a person can see and a
// person can delete from is a deployment whose skills do not disappear with a restart.
func main() {
	cliapp.Main(cliapp.Options{
		Name:        skillgit.Name,
		Description: skillgit.Description,
		// A core is at one address, and that address is its registry's. This is how a
		// command finds the peer a core hosts, so pointing this command at a core reaches the
		// deployment rather than a private instance of this provider.
		ResolverFor: registry.CoreResolver,
		Factory: func() (*subsystem.Server, error) {
			return skillgit.New(skillgit.Options{
				// The data directory is where this provider keeps its checkouts and its record
				// of them. It defaults to the deployment's own data directory rather than to a
				// project's, because a catalog outlives the project that first named one of
				// them.
				DataDir: skillgit.DefaultDataDir(),
			})
		},
	})
}
