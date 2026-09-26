module github.com/Manu343726/toolbox/subsystems/health

go 1.27

require (
	connectrpc.com/connect v1.20.0
	github.com/Manu343726/toolbox v0.0.0
	github.com/Manu343726/toolbox/subsystems/registry v0.0.0
	github.com/stretchr/testify v1.12.1
	google.golang.org/protobuf v1.36.11
)

require (
	connectrpc.com/grpcreflect v1.3.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/modelcontextprotocol/go-sdk v1.6.1 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/Manu343726/toolbox => ../..

replace github.com/Manu343726/toolbox/subsystems/registry => ../registry
