package testecho

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
)

//go:embed proto/echo.pb
var echoDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(echoDescriptorSet); err != nil {
		panic(err)
	}
}
