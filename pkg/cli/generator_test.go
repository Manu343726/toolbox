package cli_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"context"

	"github.com/Manu343726/toolbox/pkg/cli"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The generator is the only thing that turns a contract into a command a person types,
// and every defect it had was invisible from the code: a nested message stayed a JSON
// blob until somebody needed a nested field, and a service named after its own command
// made its methods unreachable from help. So these tests drive it the way a caller does —
// through the command tree, with flags, and then by running the command — rather than
// through its internals.
//
// The schema comes from the descriptor set the framework embeds, so the fields and the
// comments are the real ones from a real contract rather than a fixture written to pass.
// The field kinds this contract does not have — narrow integers, enums, streaming — are
// covered against the contracts that do, in the testecho subsystem, which the framework
// documents as its vertical slice for generated CLI behaviour.

// Every method of every service is a command. A contract that changes without the
// commands changing is a contract whose tools a caller cannot reach, and the only way
// that is caught is by counting methods against commands.
func TestEveryMethodBecomesACommand(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	for _, serviceName := range []string{apiParserService, apiAdapterService, apiInvokerService} {
		schema, err := source.DescribeService(t.Context(), serviceName)
		require.NoError(t, err)
		require.NotEmpty(t, schema.Methods, "%s declares no methods, so this test proves nothing", serviceName)
		for _, method := range schema.Methods {
			t.Run(serviceName+"."+method.Name, func(t *testing.T) {
				// The command name is the method name in kebab case, which is the rule
				// the generated help states. Deriving it here rather than hard-coding it
				// keeps the test honest when a method is renamed.
				assert.NotNil(t, methodCommandOrNil(t, root, commandNameFor(method.Name)),
					"no command for %s.%s", serviceName, method.Name)
			})
		}
	}
}

// A service whose short name is the command's own name gets no level of its own. The
// contract is a package and the command is already that package's short name, so the
// level costs a word on every invocation and says nothing.
//
// This is the shape every standalone subsystem command has: `knowledge` hosting
// KnowledgeService, `registry` hosting RegistryService. Without the flattening, every
// method invocation in every subsystem command read "knowledge knowledge search".
func TestAServiceNamedAfterTheCommandAddsNoLevel(t *testing.T) {
	source := newTestSource(t)
	// "api-parser" is ApiParserService's short name, and the command is named after it.
	root := generate(t, source, cli.Options{CommandName: "api-parser"})

	assert.Nil(t, findChild(root, "api-parser"), "the service level is not a second name")
	assert.NotNil(t, findChild(root, "api-adapter"),
		"and a service with a different name keeps its level, so the rule is not blanket")
	assert.NotNil(t, methodCommandOrNil(t, root, "parse-api"), "the methods are on the command")
	assert.NotNil(t, methodCommandOrNil(t, root, "render-api"))
}

// A service that keeps its level holds only its own methods, so a caller who knows the
// service can predict the path.
func TestAServiceKeepsItsLevelWhenItAddsAName(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	adapter := find(t, root, "api-adapter")
	assert.Contains(t, commandNames(adapter), "render-api")
	assert.Contains(t, commandNames(adapter), "serve-api")
	assert.NotContains(t, commandNames(adapter), "parse-api",
		"another service's method is not a command of this one")
}

// A nested message is flattened into dotted flags, so a caller sets one field without
// hand-writing JSON. The JSON flag stays, so nothing becomes unreachable.
func TestANestedMessageBecomesDottedFlags(t *testing.T) {
	source := newTestSource(t)
	command := generateCommandFor(t, source, "parse-api")

	// ParseApiRequest.source is an ApiSource with documented fields.
	require.NotNil(t, command.Flags().Lookup("source"), "the message itself stays addressable")
	assert.Contains(t, flagUsage(command, "source"), "Where the document came from",
		"and its help is the contract's own comment")

	for _, nested := range []string{"source.location", "source.kind", "source.digest"} {
		assert.NotNil(t, command.Flags().Lookup(nested), "%s is a flag", nested)
	}
}

// A sub-flag's help is the contract's comment for that field. The documentation model
// already carries nested descriptions; using the field's type instead is how every
// sub-flag ends up documented as the word "string".
func TestASubFlagIsDocumentedFromTheContract(t *testing.T) {
	source := newTestSource(t)
	command := generateCommandFor(t, source, "parse-api")

	usage := flagUsage(command, "source.location")
	require.NotEmpty(t, usage)
	assert.NotEqual(t, "string", usage, "a field's type is not documentation")
	assert.Contains(t, usage, "File path, URL, or endpoint the description came from",
		"it is the field's own comment")
}

// A top-level flag is documented from the contract too, which is what makes generated
// help worth reading.
func TestAFlagIsDocumentedFromTheContract(t *testing.T) {
	source := newTestSource(t)
	command := generateCommandFor(t, source, "parse-api")

	assert.Contains(t, flagUsage(command, "format"), "Format identifier the document is written in")
	assert.Contains(t, flagUsage(command, "base_url"), "Base URL the document declares")
}

// A method's help states the fully-qualified method, because that is what a reader needs
// to find the contract the command came from.
func TestAMethodHelpNamesTheContract(t *testing.T) {
	source := newTestSource(t)
	command := generateCommandFor(t, source, "parse-api")

	help := command.Long
	assert.Contains(t, help, apiParserService+"/ParseApi", "the help says which method this is")
	assert.Contains(t, help, "interprets one description document",
		"and repeats the method's documentation, so the help stands on its own")
	assert.Contains(t, command.Short, "interprets one description document",
		"and the one-line form says it too, for the parent's command list")
}

// A repeated message is not flattened, because no flag addresses its first element. It
// stays JSON, and says so. Api.services is one, and the singular messages around it are
// flattened, so both halves of the rule are held by the same command.
func TestARepeatedMessageStaysJSON(t *testing.T) {
	source := newTestSource(t)
	command := generateCommandFor(t, source, "render-api")

	require.NotNil(t, command.Flags().Lookup("api"), "the whole Api is addressable")
	assert.NotNil(t, command.Flags().Lookup("api.id"),
		"a singular nested message is flattened")
	require.NotNil(t, command.Flags().Lookup("api.services"),
		"a repeated nested message is addressable")
	assert.Contains(t, flagUsage(command, "api.services"), "one JSON object per value",
		"and the help says which shape one value takes")
	assert.Nil(t, command.Flags().Lookup("api.services.operations"),
		"and is not flattened, because no flag addresses an element of a list")
}

// A message that contains itself does not make the flag walk recurse forever. ApiSchema
// holds repeated ApiSchema, so the contract in this very file is the case: a caller can
// hand-write the nesting, and the flags stop at a bound instead of hanging.
func TestASelfReferentialMessageStopsAtABound(t *testing.T) {
	source := newTestSource(t)
	command := generateCommandFor(t, source, "parse-api")

	// The walk reached into ApiSource and stopped; it did not run away.
	assert.NotNil(t, command.Flags().Lookup("source.digest"))
	for _, flag := range allFlags(command) {
		assert.Less(t, len(flag.Name), 64, "flag %q is not a runaway path", flag.Name)
	}
}

// A map is key=value, repeated, and says so in its help.
func TestAMapFlagTakesKeyValuePairs(t *testing.T) {
	source := newTestSource(t)
	command := generateCommandFor(t, source, "render-api")

	flag := namedFlag(t, command, "options")
	require.NotNil(t, flag, "RenderApiRequest.options is a map, so the command has one")
	assert.Contains(t, flag.Usage, "key=value", "the shape of one entry is stated")
	assert.Contains(t, flag.Usage, "repeatable", "and that more than one may be given")
}

// A map value that is not key=value is refused with the shape it wanted, not with a
// parse error from deep inside a protobuf library.
func TestAMapFlagRefusesAValueThatIsNotAKeyValuePair(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	flag := namedFlag(t, generateCommandFor(t, source, "render-api"), "options")
	require.NotNil(t, flag)
	_, err := executeMethod(t, root, "render-api", "--"+flag.Name, "novalue")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key=value")
}

// Every key=value pair reaches the request, and the last one for a key wins, because a
// repeated flag is how a caller overrides a value they set earlier.
func TestAMapFlagCarriesEveryPair(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	flag := namedFlag(t, generateCommandFor(t, source, "render-api"), "options")
	require.NotNil(t, flag)
	_, err := executeMethod(t, root, "render-api",
		"--"+flag.Name, "first=1", "--"+flag.Name, "second=2", "--"+flag.Name, "first=3")
	require.NoError(t, err)
	require.Len(t, source.invoked, 1)

	fields := source.invoked[0].request.Descriptor().Fields()
	mapField := fields.ByName("options")
	require.NotNil(t, mapField)
	values := source.invoked[0].request.Get(mapField).Map()
	assert.Equal(t, 2, values.Len(), "both keys are present")
	key := protoreflect.ValueOfString("first").MapKey()
	assert.Equal(t, "3", values.Get(key).String(), "the last value for a key wins")
	assert.Equal(t, "2", values.Get(protoreflect.ValueOfString("second").MapKey()).String())
}

// A bytes field has no textual form, so its flag takes base64 — the one encoding that
// survives a round trip through a shell argument unchanged.
func TestABytesFieldIsBase64Encoded(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	document := base64.StdEncoding.EncodeToString([]byte(`{"openapi":"3.1.0"}`))
	_, err := executeMethod(t, root, "parse-api", "--document", document, "--format", "openapi")
	require.NoError(t, err)
	require.Len(t, source.invoked, 1)

	fields := source.invoked[0].request.Descriptor().Fields()
	documentField := fields.ByName("document")
	require.NotNil(t, documentField)
	assert.Equal(t, []byte(`{"openapi":"3.1.0"}`), source.invoked[0].request.Get(documentField).Bytes())
}

// A bytes field given something that is not base64 is refused, and the message says so
// rather than reporting a generic encoding failure.
func TestABytesFieldRefusesWhatIsNotBase64(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	_, err := executeMethod(t, root, "parse-api", "--document", "not base64!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "document", "the error names the flag")
}

// A flag that was not given sends nothing. A default of "" or 0 that reached the wire
// would overwrite a value the server derives, and the difference between "unset" and
// "empty" is not something a CLI can see.
func TestAFlagThatWasNotGivenSendsNothing(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	_, err := executeMethod(t, root, "parse-api", "--format", "openapi")
	require.NoError(t, err)
	require.Len(t, source.invoked, 1)

	request := source.invoked[0].request
	fields := request.Descriptor().Fields()
	for _, name := range []protoreflect.Name{"format_hint", "base_url", "api_id", "server_id"} {
		field := fields.ByName(name)
		require.NotNil(t, field)
		assert.False(t, request.Has(field), "%s was not given, so it was not sent", name)
	}
	assert.True(t, request.Has(fields.ByName("format")), "and the one that was given was")
}

// A sub-flag overrides the JSON flag for the same field: the JSON flag is declared first,
// so a caller who spelled out both meant the specific one. The rest of the JSON survives,
// which is the point of having both.
func TestASubFlagOverridesTheJSONForTheSameField(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	_, err := executeMethod(t, root, "parse-api",
		"--format", "openapi",
		"--source", `{"kind":"from-json","location":"mem://one"}`,
		"--source.digest", "from-flag")
	require.NoError(t, err)
	require.Len(t, source.invoked, 1)

	request := source.invoked[0].request
	sourceField := request.Descriptor().Fields().ByName("source")
	require.NotNil(t, sourceField)
	nested := request.Get(sourceField).Message()
	digest := nested.Descriptor().Fields().ByName("digest")
	require.NotNil(t, digest)
	assert.Equal(t, "from-flag", nested.Get(digest).String(), "the specific flag wins")
	kind := nested.Descriptor().Fields().ByName("kind")
	require.NotNil(t, kind)
	assert.Equal(t, "from-json", nested.Get(kind).String(), "and the JSON still supplies the rest")
}

// The JSON flag alone still works, for a caller who has a document ready and would rather
// not spell out five flags.
func TestTheJSONFlagAloneSetsTheWholeMessage(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	_, err := executeMethod(t, root, "parse-api", "--source", `{"kind":"only-json"}`)
	require.NoError(t, err)
	require.Len(t, source.invoked, 1)

	request := source.invoked[0].request
	sourceField := request.Descriptor().Fields().ByName("source")
	nested := request.Get(sourceField).Message()
	kind := nested.Descriptor().Fields().ByName("kind")
	require.NotNil(t, kind)
	assert.Equal(t, "only-json", nested.Get(kind).String())
}

// Malformed JSON is refused with the position, because a caller pasting a document needs
// to know where the paste went wrong.
func TestMalformedJSONIsRefused(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	_, err := executeMethod(t, root, "parse-api", "--source", `{"api_id":`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source", "the error names the flag")
}

// The response is JSON with protobuf field names, so a caller can read it, and it is
// written to the command's own output rather than to the process's stdout directly — which
// is what lets a test read it at all.
func TestTheResponseIsJSONOnTheCommandOutput(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	out, err := executeMethod(t, root, "parse-api", "--format", "openapi")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(out), "{"), "the response is JSON: %s", out)
	assert.Contains(t, out, "\n  ", "and it is indented, because a person reads it too")
}

// A failure from the service is reported as it arrived. Silencing it would leave a caller
// unable to tell a rejected document from a broken tool.
func TestAServiceFailureReachesTheCaller(t *testing.T) {
	source := newTestSource(t)
	source.respond = func(protoreflect.Name, proto.Message) (proto.Message, error) {
		return nil, &serviceRefused{}
	}
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	_, err := executeMethod(t, root, "parse-api", "--format", "openapi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the parser refused the document")
}

type serviceRefused struct{}

func (*serviceRefused) Error() string { return "the parser refused the document" }

// A flag the contract does not have is refused, naming the flag. A typo that silently did
// nothing is how a caller concludes the tool cannot do what they asked.
func TestAnUnknownFlagIsRefused(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	_, err := executeMethod(t, root, "parse-api", "--not-a-field", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-field")
}

// A method is callable without any flag, and the field it does have is still there. A
// method whose fields are all optional is the common case, and a command that demanded
// one of them would be refusing a valid call.
func TestAMethodIsCallableWithoutAnyFlag(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox"})

	command := methodCommand(t, root, "stop-api")
	_, err := executeMethod(t, root, "stop-api")
	require.NoError(t, err)
	require.Len(t, source.invoked, 1)
	assert.Equal(t, "StopApi", source.invoked[0].method)

	fields := command.Flags()
	assert.NotNil(t, fields.Lookup("instance_id"), "its one field is still a flag")
}

// The reflection services are the protocol's own, and they are not commands by default: a
// caller who already knows a contract has no use for being told what it is. Asking for
// them adds them and nothing else changes, so the option cannot be quietly widening the
// surface past the two services it names.
func TestTheReflectionServicesAreNotCommandsUnlessAsked(t *testing.T) {
	source := newTestSource(t)
	without := generate(t, source, cli.Options{CommandName: "toolbox"})
	with := generate(t, source, cli.Options{CommandName: "toolbox", IncludeReflection: true})

	for _, name := range []string{"v1.server-reflection", "v1alpha.server-reflection"} {
		assert.NotContains(t, commandNameList(without), name, "reflection is not a command by default")
		assert.Contains(t, commandNameList(with), name, "and it is a command when asked for")
	}
	// The method is a child of its service, so the qualified service name is the path.
	reflection := find(t, with, "v1.server-reflection")
	assert.Contains(t, commandNameList(reflection), "server-reflection-info")

	// Asking for reflection adds reflection. It does not remove a feature, which is
	// what would happen if the option were implemented as a filter over everything.
	for _, name := range commandNameList(without) {
		assert.Contains(t, commandNameList(with), name, "%q is not lost", name)
	}
}

// A streaming method is refused by name, including one of the protocol's own, so a
// caller who typed it learns which method could not be called.
func TestAReflectionMethodIsRefusedRatherThanFaked(t *testing.T) {
	source := newTestSource(t)
	root := generate(t, source, cli.Options{CommandName: "toolbox", IncludeReflection: true})

	// The method sits under its qualified service, so the path is the qualified name and
	// then the method: two commands of the same name under different services are
	// reachable, which is what qualifying the services bought.
	_, err := executeMethod(t, root, "server-reflection-info")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "streaming method")
}

// A service that cannot be described is a failure of the whole tree, not a silently empty
// one. A caller who got a root command with fewer services would not know what was lost.
func TestAServiceThatCannotBeDescribedIsAnError(t *testing.T) {
	source := newTestSource(t)
	broken := &failingSource{testSource: source, failOn: apiParserService}
	_, err := cli.NewGenerator(broken, cli.Options{CommandName: "toolbox"}).Generate(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), apiParserService, "the error says which service")
}

type failingSource struct {
	*testSource
	failOn string
}

func (s *failingSource) DescribeService(ctx context.Context, name string) (*discovery.ServiceSchema, error) {
	if name == s.failOn {
		return nil, &describeFailed{name: name}
	}
	return s.testSource.DescribeService(ctx, name)
}

type describeFailed struct{ name string }

func (e *describeFailed) Error() string { return "cannot describe " + e.name }

// A generator with no source is refused rather than building an empty tree.
func TestAGeneratorWithNoSourceIsRefused(t *testing.T) {
	_, err := cli.NewGenerator(nil, cli.Options{}).Generate(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source")
}

// A service with no embedded documentation still gets commands, without help text. A
// missing comment is a gap in the contract, not a reason to hide the operation.
func TestAServiceWithoutDocumentationStillGetsCommands(t *testing.T) {
	source := newTestSource(t)
	undocumented := &undocumentedSource{testSource: source}
	root, err := cli.NewGenerator(undocumented, cli.Options{CommandName: "toolbox"}).Generate(t.Context())
	require.NoError(t, err)

	assert.NotNil(t, methodCommandOrNil(t, root, "parse-api"),
		"an undocumented service is still callable")
	command := methodCommand(t, root, "parse-api")
	assert.NotNil(t, command.Flags().Lookup("format"), "and its fields are still flags")
}

type undocumentedSource struct{ *testSource }

func (s *undocumentedSource) DescribeService(ctx context.Context, name string) (*discovery.ServiceSchema, error) {
	schema, err := s.testSource.DescribeService(ctx, name)
	if err != nil {
		return nil, err
	}
	schema.Documentation = nil
	return schema, nil
}

// commandNameFor is the rule the generator states in its help: a method name in kebab
// case. It is written out here rather than exported by the generator because a test that
// calls the generator's own naming cannot catch the generator renaming things.
func commandNameFor(method string) string {
	var out strings.Builder
	for i, r := range method {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out.WriteByte('-')
			}
			out.WriteRune(r + ('a' - 'A'))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func methodCommandOrNil(t *testing.T, root *cobra.Command, method string) *cobra.Command {
	t.Helper()
	if path := searchPath(root, method, nil); path != nil {
		return find(t, root, path...)
	}
	return nil
}

func namedFlag(t *testing.T, command *cobra.Command, name string) *pflag.Flag {
	t.Helper()
	return command.Flags().Lookup(name)
}

func allFlagsExceptHelp(command *cobra.Command) []*pflag.Flag {
	var flags []*pflag.Flag
	for _, flag := range allFlags(command) {
		if flag.Name != "help" {
			flags = append(flags, flag)
		}
	}
	return flags
}
