package health

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
)

//go:embed proto/health.pb
var healthDescriptorSet []byte

func init() {
	if err := shareddocs.RegisterEmbeddedFile(healthDescriptorSet); err != nil {
		panic(err)
	}
}
