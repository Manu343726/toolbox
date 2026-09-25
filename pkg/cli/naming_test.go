package cli_test

import (
	"strings"
	"testing"

	"github.com/Manu343726/toolbox/pkg/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A command name has to identify one service, and a name several services claim is
// reached by whichever was added first — the other is not an error, it is just gone.
// These tests hold the qualification ladder, because the case is real rather than
// hypothetical: the protocol's own reflection services are two versions of one name.

// A name one service claims is not lengthened. A caller who learned "search" must keep
// being able to type it.
func TestAnUnambiguousNameIsNotLengthened(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	assert.Contains(t, commandNameList(root), "api-parser",
		"a service nothing else claims keeps its short name")
}

// Two services whose last segments match are told apart by the package segment that
// differs, which is the part that says which version is which.
func TestTwoServicesWithTheSameLastSegmentAreQualified(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox", IncludeReflection: true})

	names := commandNameList(root)
	assert.Contains(t, names, "v1.server-reflection")
	assert.Contains(t, names, "v1alpha.server-reflection")
	assert.NotContains(t, names, "server-reflection",
		"and neither keeps a name the other also claims")

	// Both are reachable, which is the whole point: before this, cobra resolved the bare
	// name to one of them and the other existed only in the tree.
	first := find(t, root, "v1.server-reflection")
	second := find(t, root, "v1alpha.server-reflection")
	assert.NotSame(t, first, second, "they are two commands, not one found twice")
}

// The qualified name is reachable, and so is its method, so a caller can reach every
// operation of every service.
func TestEveryQualifiedServiceIsFullyReachable(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox", IncludeReflection: true})

	for _, service := range []string{"v1.server-reflection", "v1alpha.server-reflection"} {
		command := find(t, root, service)
		assert.Contains(t, commandNameList(command), "server-reflection-info",
			"%s holds its own methods", service)
	}
}

// When the last package segment is not enough either, the whole path is used. Two
// services that share a last segment and a last package segment is a shape no contract
// in the framework has, and it is here so the last rung of the ladder is held: a name
// that reaches the wrong service is worse than an ugly one, so the fallback is the one
// thing guaranteed to be unique.
func TestAPackageSegmentThatAlsoCollidesFallsBackToTheWholePath(t *testing.T) {
	// a.b.v1.Same and a.c.v1.Same share the last name and the last package segment.
	root := generate(t, sourceWith(t, []string{
		"toolbox.a.b.v1.SameService",
		"toolbox.a.c.v1.SameService",
	}), cli.Options{CommandName: "toolbox"})

	names := commandNameList(root)
	assert.NotContains(t, names, "v1.same", "the package segment alone still collides")
	assert.NotContains(t, names, "same", "and so does the bare short name")
	assert.Len(t, names, 2, "there are two commands, not one found twice")
	assert.ElementsMatch(t, []string{"toolbox.a.b.v1.same", "toolbox.a.c.v1.same"}, names,
		"and each is reachable by its whole path")

	// Both hold their own method, so both are usable rather than merely present.
	for _, name := range names {
		assert.Contains(t, commandNameList(find(t, root, name)), "do",
			"%s holds its own methods", name)
	}
}

// The names a surface gets do not depend on the order the services were discovered in.
// A caller who typed a name yesterday must be able to type it today, and a reordering
// upstream must not be a breaking change.
func TestNamesDoNotDependOnTheOrderServicesWereFoundIn(t *testing.T) {
	forward := commandNameList(generate(t, sourceWith(t, []string{
		apiParserService, apiAdapterService, apiInvokerService,
	}), cli.Options{CommandName: "toolbox"}))
	backward := commandNameList(generate(t, sourceWith(t, []string{
		apiInvokerService, apiAdapterService, apiParserService,
	}), cli.Options{CommandName: "toolbox"}))

	assert.ElementsMatch(t, forward, backward,
		"the same services get the same commands whatever order they arrive in")
}

// A service with a single-segment name and nothing to disambiguate against keeps a
// usable name rather than producing an empty or dotted one.
func TestANameWithNoPackageIsNotMangled(t *testing.T) {
	root := generate(t, sourceWith(t, []string{"Same"}), cli.Options{CommandName: "toolbox"})

	for _, name := range commandNameList(root) {
		assert.NotContains(t, name, "..", "no empty segment is produced")
		assert.False(t, strings.HasPrefix(name, "."), "and no leading dot")
	}
}

// The command's own name is compared the way a caller reads it, ignoring the punctuation
// and case that separate a subsystem from its own service. "apitools" and "api-tools"
// are the same word to somebody typing a command.
func TestACommandNameIsComparedTheWayACallerReadsIt(t *testing.T) {
	source := newTestSource(t)
	// "api-parser" normalises to the same word as the service it hosts.
	root := generate(t, source, cli.Options{CommandName: "apiparser"})

	assert.Nil(t, findChild(root, "api-parser"),
		"a difference in punctuation and case is not a difference in name")
	assert.NotNil(t, methodCommandOrNil(t, root, "parse-api"),
		"so the service level is still dropped")
}

// The generator refuses to build a tree with no source, rather than an empty one that a
// caller would read as "this deployment has no services".
func TestTheGeneratorRefusesToBuildWithoutASource(t *testing.T) {
	_, err := cli.NewGenerator(nil, cli.Options{}).Generate(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source")
}
