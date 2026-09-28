package skillgit

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

// The original contract, embedded so its documentation can be read from the source.
//
// The generated descriptor carries the structure and none of the prose, because protoc-gen-go
// writes the prose into Go doc comments and the source info is a build artefact. The comments
// are also where the `@toolbox.side-effects` annotations live, so a subsystem that documented
// itself from its own descriptor would serve an operation that nobody classified — which is the
// one state a policy cannot grant by naming read or write.
//
// This file was missing, and the omission was invisible for a reason worth recording. Every
// method in this contract declares its side effects, the service works, the operations are
// listed, and all seven of them arrive in the policy layer as **unclassified**. The default
// policy grants reads and withholds writes, and it withholds an unclassified operation too — so
// a deployment withholding everything here looks exactly like a deployment whose policy is doing
// what it says. `ListCheckouts` declares `read_only`, changes nothing, and was still denied:
// a deployment that could not be asked which catalogs it has.
//
// The failure has no error message. An unclassified operation is refused, which is the safe
// direction, so nothing anywhere reports that the classification never arrived. The repository
// check that would have caught it is in `internal/repocheck`: a subsystem with a contract must
// embed its source, because the annotations are in the source and nowhere else.
//
//go:embed proto/skillgit.proto
var skillgitProtoSource []byte

func init() {
	// Panics on failure: a subsystem that cannot register its own contract is broken at build
	// time, and a process that started anyway would serve help with no descriptions and no
	// annotations — which is the failure this whole arrangement exists to remove.
	shareddocs.RegisterEmbeddedProtoSource("proto/skillgit.proto", skillgitProtoSource)
}
