package registry

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

//go:embed proto/registry.pb
var registryDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(registryDescriptorSet); err != nil {
		panic(err)
	}
}
