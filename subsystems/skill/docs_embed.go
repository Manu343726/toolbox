package skill

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
)

//go:embed proto/skill.pb
var skillDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(skillDescriptorSet); err != nil {
		panic(err)
	}
}
