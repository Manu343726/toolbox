package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	apiopenapi "github.com/Manu343726/toolbox/subsystems/apiopenapi"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        apiopenapi.Name,
		Description: "OpenAPI description parser and HTTP invocation provider subsystem",
		Factory: func() (*subsystem.Server, error) {
			return apiopenapi.New(apiopenapi.Options{})
		},
	})
}
