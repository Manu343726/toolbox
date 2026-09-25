package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	skill "github.com/Manu343726/toolbox/subsystems/skill"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        skill.Name,
		Description: "Versioned skill subsystem",
		Factory: func() (*subsystem.Server, error) {
			return skill.New(skill.Options{})
		},
	})
}
