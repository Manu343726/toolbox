package main

import (
	"github.com/Manu343726/toolsbox/pkg/cliapp"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	tool "github.com/Manu343726/toolsbox/subsystems/tool"
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
