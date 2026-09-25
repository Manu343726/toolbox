package main

import (
	"github.com/Manu343726/toolsbox/pkg/cliapp"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	knowledge "github.com/Manu343726/toolsbox/subsystems/knowledge"
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
