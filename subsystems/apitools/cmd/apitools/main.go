package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	apitools "github.com/Manu343726/toolbox/subsystems/apitools"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        apitools.Name,
		Description: "API catalog, format index, and on-demand API tool exposure subsystem",
		Factory: func() (*subsystem.Server, error) {
			return apitools.New(apitools.Options{})
		},
	})
}
