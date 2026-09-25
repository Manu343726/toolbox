package agent

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

//go:embed proto/agent.pb
var agentDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(agentDescriptorSet); err != nil {
		panic(err)
	}
}
