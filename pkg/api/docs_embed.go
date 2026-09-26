package api

import (
	_ "embed"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

// The framework's own API introspection contract, embedded as source so generated CLI help and
// documentation services can describe the extension point that parser and adapter subsystems
// implement.
//
// The source rather than the generated descriptor: the comments are also where the
// @toolbox.side-effects annotations live, and a contract documented from a generated descriptor
// serves operations that nobody classified.
//
// It lives in this package rather than its parent, because this is the package a caller imports
// to get the contract. Registered one level up, it would be loaded by any binary that imported
// pkg/api and by none that imported only apiv1 — so the documentation would be present or absent
// depending on something no reader could see.
//
//go:embed proto/toolbox/api/v1/api.proto
var apiProtoSource []byte

func init() {
	// Panics on failure: a package that cannot register its own contract is broken at build
	// time, and a process that started anyway would serve help with no descriptions and no
	// annotations.
	shareddocs.RegisterEmbeddedProtoSource("toolbox/api/v1/api.proto", apiProtoSource)
}
