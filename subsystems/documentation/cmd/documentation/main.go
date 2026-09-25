package main

import (
	"github.com/Manu343726/toolsbox/pkg/cliapp"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	documentation "github.com/Manu343726/toolsbox/subsystems/documentation"
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
