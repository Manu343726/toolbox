package knowledge

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
)

//go:embed proto/knowledge.pb
var knowledgeDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(knowledgeDescriptorSet); err != nil {
		panic(err)
	}
}
