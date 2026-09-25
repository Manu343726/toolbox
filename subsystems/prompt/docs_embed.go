package prompt

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

//go:embed proto/prompt.pb
var promptDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(promptDescriptorSet); err != nil {
		panic(err)
	}
}
