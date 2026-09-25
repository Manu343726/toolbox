package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	apimcp "github.com/Manu343726/toolbox/subsystems/apimcp"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        apimcp.Name,
		Description: "Model Context Protocol parser, renderer, and invocation provider subsystem",
		Factory: func() (*subsystem.Server, error) {
			return apimcp.New(apimcp.Options{})
		},
	})
}
