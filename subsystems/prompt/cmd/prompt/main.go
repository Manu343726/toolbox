package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	prompt "github.com/Manu343726/toolbox/subsystems/prompt"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        prompt.Name,
		Description: "Provider-neutral prompt subsystem",
		Factory: func() (*subsystem.Server, error) {
			return prompt.New(prompt.Options{})
		},
	})
}
