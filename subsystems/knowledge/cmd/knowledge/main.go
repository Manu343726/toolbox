package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	knowledge "github.com/Manu343726/toolbox/subsystems/knowledge"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        knowledge.Name,
		Description: "Knowledge source and retrieval subsystem",
		Factory: func() (*subsystem.Server, error) {
			return knowledge.New(knowledge.Options{})
		},
	})
}
