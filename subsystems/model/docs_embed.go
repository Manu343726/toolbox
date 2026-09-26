package model

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

// The original contract, embedded so its documentation can be read from the source.
//
// The generated descriptor carries the structure and none of the prose, because protoc-gen-go
// writes the prose into Go doc comments and the source info is a build artefact. The comments
// are also where the @toolbox.side-effects annotations live, so a subsystem that documented
// itself from its own descriptor would serve an operation that nobody classified — which is the
// one state a policy cannot grant by naming read or write.
//
//go:embed proto/model.proto
var modelProtoSource []byte

func init() {
	// Panics on failure: a subsystem that cannot register its own contract is broken at build
	// time, and a process that started anyway would serve help with no descriptions and no
	// annotations — which is the failure this whole arrangement exists to remove.
	shareddocs.RegisterEmbeddedProtoSource("proto/model.proto", modelProtoSource)
}
