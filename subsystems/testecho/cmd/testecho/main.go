package main

import (
	"github.com/Manu343726/toolsbox/pkg/cliapp"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	testecho "github.com/Manu343726/toolsbox/subsystems/testecho"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        testecho.Name,
		Description: "Reference echo subsystem",
		Factory: func() (*subsystem.Server, error) {
			return testecho.New(testecho.Options{})
		},
	})
}
