package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	documentation "github.com/Manu343726/toolbox/subsystems/documentation"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        documentation.Name,
		Description: "Protobuf documentation subsystem",
		Factory: func() (*subsystem.Server, error) {
			return documentation.New(documentation.Options{})
		},
	})
}
