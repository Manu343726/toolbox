package main

import (
	"github.com/Manu343726/toolsbox/pkg/cliapp"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	skill "github.com/Manu343726/toolsbox/subsystems/skill"
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
