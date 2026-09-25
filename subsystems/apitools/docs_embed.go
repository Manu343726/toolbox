package apitools

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

//go:embed proto/apitools.pb
var apiToolsDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(apiToolsDescriptorSet); err != nil {
		panic(err)
	}
}
