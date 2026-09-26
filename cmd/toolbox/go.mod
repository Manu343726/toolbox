module github.com/Manu343726/toolbox/cmd/toolbox

go 1.27

require (
	connectrpc.com/connect v1.20.0
	github.com/Manu343726/toolbox v0.0.0
	github.com/Manu343726/toolbox/subsystems/agent v0.0.0
	github.com/Manu343726/toolbox/subsystems/apigrpc v0.0.0
	github.com/Manu343726/toolbox/subsystems/apimcp v0.0.0
	github.com/Manu343726/toolbox/subsystems/apiopenapi v0.0.0
	github.com/Manu343726/toolbox/subsystems/apitools v0.0.0
	github.com/Manu343726/toolbox/subsystems/documentation v0.0.0
	github.com/Manu343726/toolbox/subsystems/health v0.0.0
	github.com/Manu343726/toolbox/subsystems/knowledge v0.0.0
	github.com/Manu343726/toolbox/subsystems/logfile v0.0.0
	github.com/Manu343726/toolbox/subsystems/logger v0.0.0
	github.com/Manu343726/toolbox/subsystems/model v0.0.0
	github.com/Manu343726/toolbox/subsystems/policy v0.0.0
	github.com/Manu343726/toolbox/subsystems/prompt v0.0.0
	github.com/Manu343726/toolbox/subsystems/registry v0.0.0
	github.com/Manu343726/toolbox/subsystems/skill v0.0.0
	github.com/Manu343726/toolbox/subsystems/tool v0.0.0
	github.com/Manu343726/toolbox/subsystems/workflow v0.0.0
	github.com/modelcontextprotocol/go-sdk v1.6.1
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	github.com/stretchr/testify v1.12.1
)

require (
	connectrpc.com/grpcreflect v1.3.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/sagikazarmark/locafero v0.11.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/sourcegraph/conc v0.3.1-0.20240121214520-5f936abd7ae8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/viper v1.21.0 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

replace github.com/Manu343726/toolbox => ../..

replace github.com/Manu343726/toolbox/subsystems/apimcp => ../../subsystems/apimcp

replace github.com/Manu343726/toolbox/subsystems/apiopenapi => ../../subsystems/apiopenapi

replace github.com/Manu343726/toolbox/subsystems/apigrpc => ../../subsystems/apigrpc

replace github.com/Manu343726/toolbox/subsystems/apitools => ../../subsystems/apitools

replace github.com/Manu343726/toolbox/subsystems/agent => ../../subsystems/agent

replace github.com/Manu343726/toolbox/subsystems/documentation => ../../subsystems/documentation

replace github.com/Manu343726/toolbox/subsystems/health => ../../subsystems/health

replace github.com/Manu343726/toolbox/subsystems/knowledge => ../../subsystems/knowledge

replace github.com/Manu343726/toolbox/subsystems/model => ../../subsystems/model

replace github.com/Manu343726/toolbox/subsystems/policy => ../../subsystems/policy

replace github.com/Manu343726/toolbox/subsystems/prompt => ../../subsystems/prompt

replace github.com/Manu343726/toolbox/subsystems/registry => ../../subsystems/registry

replace github.com/Manu343726/toolbox/subsystems/skill => ../../subsystems/skill

replace github.com/Manu343726/toolbox/subsystems/tool => ../../subsystems/tool

replace github.com/Manu343726/toolbox/subsystems/workflow => ../../subsystems/workflow

replace github.com/Manu343726/toolbox/subsystems/testecho => ../../subsystems/testecho

replace github.com/Manu343726/toolbox/subsystems/logger => ../../subsystems/logger

replace github.com/Manu343726/toolbox/subsystems/logfile => ../../subsystems/logfile
