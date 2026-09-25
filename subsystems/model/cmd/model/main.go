package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	model "github.com/Manu343726/toolbox/subsystems/model"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        model.Name,
		Description: "Provider-neutral model subsystem",
		Factory: func() (*subsystem.Server, error) {
			return model.New(model.Options{})
		},
	})
}
