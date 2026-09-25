package workflow

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
)

//go:embed proto/workflow.pb
var workflowDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(workflowDescriptorSet); err != nil {
		panic(err)
	}
}
