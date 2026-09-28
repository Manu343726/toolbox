package contractcheck_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Manu343726/toolbox/internal/contractcheck"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repositoryRoot returns the tree the checks should run against: the module named by
// `REPOCHECK_ROOT`, or the repository root found by walking up to the workspace file so the
// checks do not depend on where in the tree they were invoked from.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	if named := os.Getenv(contractcheck.EnvRoot); named != "" {
		resolved := named
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(root, resolved)
		}
		info, err := os.Stat(resolved)
		require.NoError(t, err, "%s names a directory that does not exist", contractcheck.EnvRoot)
		require.True(t, info.IsDir(), "%s must name a directory", contractcheck.EnvRoot)
		return resolved
	}
	return root
}

// The three checks in `contracts.go` are each a claim about a repository, and this file is the
// claim about the claims: on this tree, all three hold. They are asserted against the whole
// repository rather than against a fixture, because the failures they exist for are not
// reachable from a fixture — a subsystem with a contract and no `docs_embed.go` compiles, serves,
// lists its operations and passes every other check in this package.
//
// They are also asserted per subsystem when the run is narrowed by `REPOCHECK_ROOT`, which is
// what the CI matrix job does, so a subsystem's own shape is checked in the job that asserts it
// stands alone. The documentation check is the exception: it is about a claim a document in the
// repository makes, and a document is not visible from inside one module, so it skips itself
// rather than passing for the wrong reason.

// A subsystem that declares a contract and does not embed its source serves operations that
// nobody classified. This is the check that would have caught `subsystems/skillgit`, whose every
// method is annotated and all seven of which reached the policy layer unclassified.
//
// `echo.proto` is named in the message because it is the smallest contract in the tree and the
// one a reader is most likely to check the rule against.
func TestEveryContractIsEmbeddedBySomethingThatReadsItsAnnotations(t *testing.T) {
	missing, err := contractcheck.UnembeddedContracts(repositoryRoot(t))
	require.NoError(t, err)
	for _, contract := range missing {
		t.Errorf("%s", contract.Error())
	}
}

// A method that declares no side effect the framework recognises is unclassified, and an
// unclassified operation is refused — so an operation a subsystem serves can be uncallable with
// nothing reporting it. This is the check that caught `OperationExposure` in `apitools`, which
// declared nothing and so withheld the one operation that reports what the policy permits.
func TestEveryMethodDeclaresASideEffectTheFrameworkRecognises(t *testing.T) {
	undeclared, err := contractcheck.UndeclaredEffects(repositoryRoot(t))
	require.NoError(t, err)
	for _, method := range undeclared {
		t.Errorf("%s", method.Error())
	}
}

// A row of the feature-document table that names a subsystem with no contract, or a document that
// is not there, exempts that subsystem from being described. It is the same failure as a CI
// matrix row naming no module, and it is checked for the same reason.
func TestEveryFeatureDocumentRowDescribesSomething(t *testing.T) {
	if os.Getenv(contractcheck.EnvRoot) != "" {
		t.Skip("a row of the feature-document table names a subsystem in the repository, which " +
			"is not visible from inside one module")
	}
	misplaced, err := contractcheck.MisplacedFeatureDocuments(repositoryRoot(t))
	require.NoError(t, err)
	for _, row := range misplaced {
		t.Errorf("%s", row.Error())
	}
}

// A document that describes a feature in prose has to let a reader arrive at the method. This is
// the check that found `docs/skills.md` describing six catalog operations as "register a
// catalog" and never naming `RegisterCatalog`.
func TestEveryFeatureDocumentNamesEveryOperationItDescribes(t *testing.T) {
	if os.Getenv(contractcheck.EnvRoot) != "" {
		t.Skip("a document describing a subsystem is not visible from inside one module, so " +
			"this check would pass for the wrong reason rather than run")
	}
	undocumented, err := contractcheck.UndocumentedOperations(repositoryRoot(t))
	require.NoError(t, err)
	for _, operation := range undocumented {
		t.Errorf("%s", operation.Error())
	}
}

// The checks above are only worth having if they fail on the thing they are for. Each case below
// is a fixture that reproduces one of the two defects this repository actually had, and each
// asserts that the check names the file or the method rather than merely reporting a count — a
// refusal a reader cannot act on is a refusal they route around.

// The first defect: a subsystem with a contract and no `docs_embed.go`. Every method in
// `subsystems/skillgit` reached the policy layer unclassified because of it.
func TestAContractWithNothingEmbeddingItIsReported(t *testing.T) {
	root := t.TempDir()
	writeProto(t, root, "subsystems/gone/proto/gone.proto", minimalContract)
	// A `docs_embed.go` that embeds a file this subsystem no longer has is the same failure
	// wearing a disguise: the file is present, so a check looking only for it would pass.
	writeFile(t, filepath.Join(root, "subsystems", "gone", "docs_embed.go"),
		"package gone\n\n//go:embed proto/renamed.proto\nvar source []byte\n")

	missing, err := contractcheck.UnembeddedContracts(root)
	require.NoError(t, err)
	require.Len(t, missing, 1)
	assert.Equal(t, "subsystems/gone", missing[0].Subsystem)
	assert.Contains(t, missing[0].Error(), "proto/gone.proto",
		"the message names the contract, so it is clear which file to embed")
	assert.Contains(t, missing[0].Error(), "docs_embed.go",
		"and the file to add")
}

// A contract that does embed its source passes, so the check is not satisfied by the presence of
// a `proto/` directory alone.
func TestAContractThatEmbedsItsSourceIsNotReported(t *testing.T) {
	root := t.TempDir()
	writeProto(t, root, "subsystems/kept/proto/kept.proto", minimalContract)
	writeFile(t, filepath.Join(root, "subsystems", "kept", "docs_embed.go"),
		"package kept\n\n//go:embed proto/kept.proto\nvar source []byte\n")

	missing, err := contractcheck.UnembeddedContracts(root)
	require.NoError(t, err)
	assert.Empty(t, missing)
}

// The second defect: a method that declares nothing. `OperationExposure` in `apitools` withheld
// the report of what the policy permits.
func TestAMethodThatDeclaresNoEffectIsReported(t *testing.T) {
	root := t.TempDir()
	writeProto(t, root, "subsystems/silent/proto/silent.proto", `syntax = "proto3";
package toolbox.silent.v1;
option go_package = "example.com/silent/v1;v1";
service SilentService {
  // Quiet says nothing about what it does.
  rpc Quiet(QuietRequest) returns (QuietResponse);
}
message QuietRequest {}
message QuietResponse {}
`)

	undeclared, err := contractcheck.UndeclaredEffects(root)
	require.NoError(t, err)
	require.Len(t, undeclared, 1)
	assert.Equal(t, "Quiet", undeclared[0].Method)
	assert.Contains(t, undeclared[0].Error(), "Unclassified is not a read",
		"the message says what the consequence is, because 'no annotation' on its own reads as "+
			"a style preference rather than as an operation nobody can call")
	assert.Contains(t, undeclared[0].Error(), "State what invoking it does",
		"and what to do about it")
}

// A misspelled effect is unclassified in exactly the same way as an absent one, and it is the
// case that is harder to see: an absent annotation looks like an oversight and a typo looks
// deliberate. The message has to quote what was written, or the reader is left searching.
func TestAMisspelledEffectIsReportedWithWhatWasWritten(t *testing.T) {
	root := t.TempDir()
	writeProto(t, root, "subsystems/typo/proto/typo.proto", `syntax = "proto3";
package toolbox.typo.v1;
option go_package = "example.com/typo/v1;v1";
service TypoService {
  // @toolbox.sideeffect read-only
  // Near does something.
  rpc Near(NearRequest) returns (NearResponse);
}
message NearRequest {}
message NearResponse {}
`)

	undeclared, err := contractcheck.UndeclaredEffects(root)
	require.NoError(t, err)
	require.Len(t, undeclared, 1)
	message := undeclared[0].Error()
	assert.Contains(t, message, "toolbox.sideeffect",
		"the message quotes the key as written, because the reader's first move is to find it")
	assert.Contains(t, message, "read_only",
		"and it lists what is accepted, so the fix does not require reading the parser")
}

// A row naming a document that is not there exempts its subsystem from being described by
// nothing. That is the CI-matrix-row failure, and it is checked for the same reason.
//
// The table is package-level, so the fixture is built from the real rows: every mapped subsystem
// gets a contract and no document gets written, so all four rows must report the missing
// document rather than the missing contract. Asserting the *reason* matters, because the two are
// different mistakes and a check that reported the wrong one would send a reader to fix a
// subsystem that is fine.
func TestAFeatureDocumentRowNamingNothingIsReported(t *testing.T) {
	root := t.TempDir()
	for _, subsystem := range []string{"skill", "skillgit", "policy", "logger"} {
		writeProto(t, root, "subsystems/"+subsystem+"/proto/"+subsystem+".proto", minimalContract)
	}

	misplaced, err := contractcheck.MisplacedFeatureDocuments(root)
	require.NoError(t, err)
	require.NotEmpty(t, misplaced, "and the check is not vacuous on a tree with no documents at all")
	for _, row := range misplaced {
		assert.Equal(t, "the document does not exist", row.Reason,
			"%s has a contract, so the missing thing is its document and not the subsystem",
			row.Subsystem)
	}
}

// minimalContract is a contract whose one method is fully annotated, so a fixture built from it
// fails the checks for the reason under test and for no other.
const minimalContract = `syntax = "proto3";
package toolbox.minimal.v1;
option go_package = "example.com/minimal/v1;v1";
service MinimalService {
  // @toolbox.side-effects read_only
  // Minimal does the least there is.
  rpc Minimal(MinimalRequest) returns (MinimalResponse);
}
message MinimalRequest {}
message MinimalResponse {}
`

// writeProto lays out a subsystem holding one contract.
func writeProto(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// writeFile lays out a file, creating the directories holding it.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
