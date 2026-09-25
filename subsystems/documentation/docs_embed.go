package docs

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
)

//go:embed proto/documentation.pb
var documentationDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(documentationDescriptorSet); err != nil {
		panic(err)
	}
}
