package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	agent "github.com/Manu343726/toolbox/subsystems/agent"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        agent.Name,
		Description: "Agent profile subsystem",
		Factory: func() (*subsystem.Server, error) {
			return agent.New(agent.Options{})
		},
	})
}
