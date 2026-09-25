package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	health "github.com/Manu343726/toolbox/subsystems/health"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        health.Name,
		Description: "Health and readiness subsystem",
		Factory: func() (*subsystem.Server, error) {
			return health.New(health.Options{})
		},
	})
}
