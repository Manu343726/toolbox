package main

import (
	"github.com/Manu343726/toolsbox/pkg/cliapp"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	workflow "github.com/Manu343726/toolsbox/subsystems/workflow"
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
