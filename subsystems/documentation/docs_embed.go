package docs

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

//go:embed proto/documentation.pb
var documentationDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(documentationDescriptorSet); err != nil {
		panic(err)
	}
}
