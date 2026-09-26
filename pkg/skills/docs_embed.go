package skills

import (
	_ "embed"

	"github.com/Manu343726/toolbox/pkg/api"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
)

// The catalog contract, embedded as source so generated CLI help and documentation services can
// describe the extension point a skill-catalog subsystem implements.
//
// The source rather than the generated descriptor: the comments are also where the
// @toolbox.side-effects annotations live, and a contract documented from a generated descriptor
// serves operations that nobody classified.
//
// It lives in this package rather than a subsystem's, because this is the package a catalog
// provider imports to get the contract. Registered one level up it would be loaded by any
// binary that imported this package and by none that imported only the generated one — so the
// documentation would be present or absent depending on something no reader could see.
//
//go:embed proto/toolbox/skills/v1/skill.proto
var skillProtoSource []byte

func init() {
	// Panics on failure: a package that cannot register its own contract is broken at build
	// time, and a process that started anyway would serve help with no descriptions and no
	// annotations.
	shareddocs.RegisterEmbeddedProtoSource("toolbox/skills/v1/skill.proto", skillProtoSource)
}

// ProviderRole is the role a subsystem plays when it serves skills from a source of them.
//
// It is a provider role in the same sense as `parser`, `adapter` and `invoker`: a role has a
// contract, a deployment registers implementations of it, and the aggregator resolves to one at
// the point of use. What a catalog must do is read and hold skills; what a parser must do is
// read a description document; and a deployment that has integrated a git-backed catalog and a
// public one has integrated two of the same, which is why resolution is by identifier and never
// by contract name.
const ProviderRole api.ProviderRole = "skillcatalog"

// CatalogService is the contract a catalog provider implements.
const CatalogService = "toolbox.skills.v1.SkillCatalogService"
