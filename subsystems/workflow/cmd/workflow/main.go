package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	workflow "github.com/Manu343726/toolbox/subsystems/workflow"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        workflow.Name,
		Description: "Provider-neutral workflow subsystem",
		Factory: func() (*subsystem.Server, error) {
			return workflow.New(workflow.Options{})
		},
	})
}
