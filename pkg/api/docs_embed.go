package api

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

// apiDescriptorSet is the framework's own API introspection contract, embedded
// so generated CLI help and documentation services can describe the extension
// point that parser and adapter subsystems implement.
//
//go:embed proto/toolbox/api/v1/api.pb
var apiDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(apiDescriptorSet); err != nil {
		panic(err)
	}
}
