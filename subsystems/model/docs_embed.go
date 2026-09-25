package model

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

//go:embed proto/model.pb
var modelDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(modelDescriptorSet); err != nil {
		panic(err)
	}
}
