package main

import (
	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	apigrpc "github.com/Manu343726/toolbox/subsystems/apigrpc"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        apigrpc.Name,
		Description: "OpenAPI description parser and HTTP invocation provider subsystem",
		Factory: func() (*subsystem.Server, error) {
			return apigrpc.New(apigrpc.Options{})
		},
	})
}
