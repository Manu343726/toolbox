package tool

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
)

//go:embed proto/tool.pb
var toolDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(toolDescriptorSet); err != nil {
		panic(err)
	}
}
