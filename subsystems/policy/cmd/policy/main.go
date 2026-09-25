package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	policy "github.com/Manu343726/toolbox/subsystems/policy"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        policy.Name,
		Description: "Domain policy evaluation subsystem",
		Factory: func() (*subsystem.Server, error) {
			return policy.New(policy.Options{})
		},
	})
}
