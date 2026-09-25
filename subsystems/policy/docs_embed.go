package policy

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

//go:embed proto/policy.pb
var policyDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(policyDescriptorSet); err != nil {
		panic(err)
	}
}
