package docs_test

import (
	"testing"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A subsystem's docs_embed.go registers its contract from init, and that
// registration is the only thing that makes the framework's own contracts
// describable in a process that links them. A subsystem that registers nothing
// still builds, still serves, and answers every documentation request with
// nothing at all — so this asserts both halves of what one registration does.

const fixtureContract = `syntax = "proto3";

package fixture.registered.v1;

option go_package = "example.com/fixture/registeredv1;registeredv1";

// RegisteredService exists to be described.
service RegisteredService {
  // DoThing does the thing.
  rpc DoThing(DoThingRequest) returns (DoThingResponse);
}

// DoThingRequest asks for a thing.
message DoThingRequest {
  // Which thing.
  string id = 1;
}

// DoThingResponse carries the thing back.
message DoThingResponse {
  // The thing.
  string id = 1;
}
`

// Registering a source does two things, and they come from the same text: the
// contract becomes readable wherever documentation is extracted, and it reaches
// the default catalog a subsystem falls back to. Doing only the first leaves
// every subsystem that was handed no catalog describing nothing.
func TestRegisteringAProtoSourceDescribesItInTheDefaultCatalog(t *testing.T) {
	const path = "fixture/registered.proto"
	shareddocs.RegisterEmbeddedProtoSource(path, []byte(fixtureContract))

	// The tunnel: the source compiles to a descriptor that carries its comments.
	compiled, err := shareddocs.CompileProtoSource(path)
	require.NoError(t, err)
	require.Equal(t, 1, compiled.Services().Len())

	// The default catalog: the same contract is describable by name, with the prose
	// the source carried rather than the structure alone.
	documented, err := shareddocs.DefaultCatalog().Get("fixture.registered.v1.RegisteredService")
	require.NoError(t, err)
	assert.Equal(t, "RegisteredService exists to be described.", documented.Description)
	require.Len(t, documented.Methods, 1)
	assert.Equal(t, "DoThing does the thing.", documented.Methods[0].Description)
	assert.Equal(t, "fixture.registered.v1.DoThingRequest", documented.Methods[0].InputType)
	require.Len(t, documented.Methods[0].Parameters, 1)
	assert.Equal(t, "Which thing.", documented.Methods[0].Parameters[0].Description)

	// And it is in the listing, because a host decides what exists from the
	// listing rather than by asking for each name it hopes for.
	listed := false
	for _, service := range shareddocs.DefaultCatalog().List() {
		if service.Name == "fixture.registered.v1.RegisteredService" {
			listed = true
			break
		}
	}
	assert.True(t, listed, "a registered contract is in the default catalog's listing")
}

// Two packages registering the same path is a build-time mistake — one of them is
// not the contract it claims to be — and saying so is better than one silently
// winning and the other documenting the wrong file.
func TestRegisteringTheSameProtoSourceTwiceIsRefused(t *testing.T) {
	const path = "fixture/duplicate.proto"
	require.NotPanics(t, func() {
		shareddocs.RegisterEmbeddedProtoSource(path, []byte(fixtureContract))
	})
	assert.Panics(t, func() {
		shareddocs.RegisterEmbeddedProtoSource(path, []byte(fixtureContract))
	})
}

// A source that does not compile cannot be documented, and a subsystem whose
// contract does not compile is broken at build time rather than at first request.
func TestRegisteringAProtoSourceThatDoesNotCompilePanics(t *testing.T) {
	assert.Panics(t, func() {
		shareddocs.RegisterEmbeddedProtoSource("fixture/broken.proto", []byte("this is not a proto file"))
	})
}
