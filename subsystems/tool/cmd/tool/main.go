package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	tool "github.com/Manu343726/toolbox/subsystems/tool"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        tool.Name,
		Description: "Declared tool subsystem",
		Factory: func() (*subsystem.Server, error) {
			return tool.New(tool.Options{})
		},
	})
}
