package logger

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

//go:embed proto/logger.pb
var loggerDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(loggerDescriptorSet); err != nil {
		panic(err)
	}
}
