package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/cli"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/Manu343726/toolbox/pkg/docs"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// The generator is the only thing that turns a contract into a command a person types,
// and every defect it had was invisible from the code: an int32 field panicked only once
// a person passed one, and a nested message stayed a JSON blob until somebody needed a
// nested field. So these tests drive the generator the way a caller does — through the
// command tree, with flags, and then by running the command — rather than through its
// internals.
//
// The schemas come from the descriptor set the framework embeds, so the fields, kinds,
// and comments are the real ones from a real contract rather than a fixture written to
// pass. A fixture would have missed the int32 panic, because a fixture with only int64
// fields cannot have it.

// Importing the API package registers the descriptor set these tests document themselves
// from. Without it the catalog is empty and every assertion about documentation would
// pass for the wrong reason: there would be nothing to compare against, and a flag
// documented as "string" would look correct.
var _ api.Catalog

const (
	apiParserService  = "toolbox.api.v1.ApiParserService"
	apiAdapterService = "toolbox.api.v1.ApiAdapterService"
	apiInvokerService = "toolbox.api.v1.ApiInvokerService"

	// The protocol's own reflection services. They exist so a client can discover a
	// contract, and a caller that already knows the contract has no use for them as
	// commands, so the generator leaves them out unless asked.
	reflectionService      = "grpc.reflection.v1.ServerReflection"
	reflectionServiceAlpha = "grpc.reflection.v1alpha.ServerReflection"
	reflectionMethodName   = "ServerReflectionInfo"
)

// frameworkProtoPath is the framework's own contract, relative to this package.
//
// The generator is tested against the original .proto rather than a descriptor set, because
// that is where the documentation now comes from: the generated descriptor has the structure
// and none of the prose, and the prose is what every generated summary and flag description is
// taken from. A fixture built from a descriptor set would pass while testing a path the
// framework no longer takes.
func frameworkProtoPath() string {
	return filepath.Join("..", "api", "proto", "toolbox", "api", "v1", "api.proto")
}

// testSource is a discovery source over the framework's own contract. It records what it
// was asked to invoke, so a test can assert on the request that actually reached the
// wire rather than on the flags that were declared.
type testSource struct {
	files   *protoregistry.Files
	invoked []invocation
	// only, when set, is the exact set of services this source serves.
	only []string
	// respond replaces what a call returns, for the error paths.
	respond func(method protoreflect.Name, request proto.Message) (proto.Message, error)
}

type invocation struct {
	service string
	method  string
	request protoreflect.Message
}

func (s *testSource) ListServices(context.Context) ([]string, error) {
	if s.only != nil {
		return append([]string(nil), s.only...), nil
	}
	return []string{apiParserService, apiAdapterService, apiInvokerService, reflectionService, reflectionServiceAlpha}, nil
}

func (s *testSource) DescribeService(_ context.Context, name string) (*discovery.ServiceSchema, error) {
	if name == reflectionService || name == reflectionServiceAlpha {
		return s.reflectionSchema(name), nil
	}
	found, err := s.files.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		if s.only != nil {
			// A name this source was asked to serve but has no descriptor for. The
			// schema is minimal because naming reads the name and the method list, and
			// a fixture contract with real fields would test something else.
			return &discovery.ServiceSchema{
				Name:          name,
				Documentation: &docs.Service{Name: name, Description: name + " exists to be named."},
				Methods:       []discovery.MethodSchema{{Name: "Do"}},
			}, nil
		}
		return nil, err
	}
	service, ok := found.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, &notAServiceError{name: name}
	}
	// The documentation comes from the .proto, through the same call a subsystem server makes
	// when it mounts a service. Reading it from the process-wide catalog instead would test a
	// path that only worked while descriptor sets were embedded.
	documentation := docs.ExtractServiceDocumentation(service)
	if documentation == nil {
		// A service with no source is a real state, and the generator has to cope with it
		// rather than assume comments exist.
		documentation = &docs.Service{Name: name}
	}
	schema := &discovery.ServiceSchema{
		Name:          name,
		Descriptor:    service,
		Documentation: documentation,
		Methods:       make([]discovery.MethodSchema, 0, service.Methods().Len()),
	}
	for i := 0; i < service.Methods().Len(); i++ {
		method := service.Methods().Get(i)
		schema.Methods = append(schema.Methods, discovery.MethodSchema{
			Name:            string(method.Name()),
			Input:           method.Input(),
			Output:          method.Output(),
			ClientStreaming: method.IsStreamingClient(),
			ServerStreaming: method.IsStreamingServer(),
		})
	}
	return schema, nil
}

func (s *testSource) Invoke(_ context.Context, service, method string, request proto.Message) (proto.Message, error) {
	s.invoked = append(s.invoked, invocation{
		service: service,
		method:  method,
		request: request.ProtoReflect(),
	})
	if s.respond != nil {
		return s.respond(protoreflect.Name(method), request)
	}
	schema, err := s.DescribeService(context.Background(), service)
	if err != nil {
		return nil, err
	}
	for _, candidate := range schema.Methods {
		if candidate.Name == method {
			return dynamicpb.NewMessage(candidate.Output), nil
		}
	}
	return nil, &notAServiceError{name: service + "." + method}
}

// reflectionSchema describes a reflection service without a real descriptor, because
// the test only needs the generator to see a service by that name and decide whether to
// make commands for it.
func (s *testSource) reflectionSchema(name string) *discovery.ServiceSchema {
	return &discovery.ServiceSchema{
		Name: name,
		Documentation: &docs.Service{
			Name:        name,
			Description: "Reflection is a protocol feature.",
			Methods: []docs.Method{{
				Name:        reflectionMethodName,
				Description: "ServerReflectionInfo is a bidirectional streaming RPC.",
			}},
		},
		Methods: []discovery.MethodSchema{{
			Name:            reflectionMethodName,
			ClientStreaming: true,
			ServerStreaming: true,
		}},
	}
}

// sourceWith serves a named set of services, falling back to a minimal schema for a name
// the framework's own contracts do not define.
//
// The fallback is what lets the naming rules be tested against service names that are
// built to collide — "a.b.v1.SameService" and "a.c.v1.SameService" share a last segment
// and a last package segment — which is a shape no real contract has and exactly the
// shape the fallback exists for. Naming depends on nothing but the names.
func sourceWith(t *testing.T, names []string) *testSource {
	t.Helper()
	base := newTestSource(t)
	return &testSource{files: base.files, only: append([]string(nil), names...)}
}

type notAServiceError struct{ name string }

func (e *notAServiceError) Error() string { return e.name + " is not a service" }

// The fixture is registered and compiled once, because a path may only be registered once and
// every test in this package needs the same contract. A fresh registry is built per test, since
// that is per-test state and registering a file into a shared one would be a data race.
var (
	fixtureOnce sync.Once
	fixtureFile protoreflect.FileDescriptor
	fixtureErr  error
)

func newTestSource(t *testing.T) *testSource {
	t.Helper()
	fixtureOnce.Do(func() {
		source, err := os.ReadFile(frameworkProtoPath())
		if err != nil {
			fixtureErr = err
			return
		}
		if err := docs.RegisterProtoSource("cli-test/api.proto", source); err != nil {
			fixtureErr = err
			return
		}
		fixtureFile, fixtureErr = docs.CompileProtoSource("cli-test/api.proto")
	})
	require.NoError(t, fixtureErr,
		"the framework's contract is the fixture these CLI tests need, and it must compile")
	files := new(protoregistry.Files)
	require.NoError(t, files.RegisterFile(fixtureFile))
	return &testSource{files: files}
}

func generate(t *testing.T, source *testSource, options cli.Options) *cobra.Command {
	t.Helper()
	if options.CommandName == "" {
		options.CommandName = "toolbox"
	}
	root, err := cli.NewGenerator(source, options).Generate(t.Context())
	require.NoError(t, err)
	return root
}

// find walks a command path, failing the test if the path does not exist. A command that
// cannot be reached is the defect under test, so the failure belongs here rather than
// being a nil dereference three assertions later.
func find(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	current := root
	for _, name := range path {
		next := findChild(current, name)
		require.NotNil(t, next, "no command %q under %q; available: %s", name, current.Name(), commandNames(current))
		current = next
	}
	return current
}

func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

// commandNameList is every immediate subcommand name, in the order cobra lists them.
func commandNameList(command *cobra.Command) []string {
	names := make([]string, 0, len(command.Commands()))
	for _, child := range command.Commands() {
		names = append(names, child.Name())
	}
	return names
}

func commandNames(command *cobra.Command) string {
	names := make([]string, 0, len(command.Commands()))
	for _, child := range command.Commands() {
		names = append(names, child.Name())
	}
	return strings.Join(names, ", ")
}

// execute runs a command path with args and returns what it wrote.
func execute(t *testing.T, root *cobra.Command, args ...string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// pathOf finds where a method command lives: on the root, or under the service command
// when the service kept a level of its own. Tests name the method and let the harness
// work out the path, so a change in whether a service is flattened does not turn every
// test into a string edit — and a test that hard-codes the path would go on passing the
// command it names rather than the one a caller would type.
func pathOf(t *testing.T, root *cobra.Command, name string) []string {
	t.Helper()
	if path := searchPath(root, name, nil); path != nil {
		return path
	}
	require.FailNow(t, "no command "+name+" under "+root.Name()+"; available: "+commandNames(root))
	return nil
}

// searchPath finds a command at any depth, because how deep a method sits depends on
// whether its service kept a level of its own and whether that level needed a
// qualifier. Both are decisions the tests are checking, so the harness follows the tree
// rather than assuming a shape.
func searchPath(command *cobra.Command, name string, prefix []string) []string {
	if command.Name() == name && len(prefix) > 0 {
		return prefix
	}
	for _, child := range command.Commands() {
		if found := searchPath(child, name, append(append([]string{}, prefix...), child.Name())); found != nil {
			return found
		}
	}
	return nil
}

// executeMethod runs one method command with flags, wherever it lives.
func executeMethod(t *testing.T, root *cobra.Command, method string, args ...string) (string, error) {
	t.Helper()
	return execute(t, root, append(pathOf(t, root, method), args...)...)
}

// methodCommand returns one method command, wherever it lives.
func methodCommand(t *testing.T, root *cobra.Command, method string) *cobra.Command {
	t.Helper()
	return find(t, root, pathOf(t, root, method)...)
}

// generateCommandFor builds a tree and returns one method command from it, which is the
// shape most of these tests want: one command, inspected or run.
func generateCommandFor(t *testing.T, source *testSource, method string) *cobra.Command {
	t.Helper()
	return methodCommand(t, generate(t, source, cli.Options{CommandName: "toolbox"}), method)
}

// allFlags is every flag a command declares, in declaration order.
func allFlags(command *cobra.Command) []*pflag.Flag {
	var flags []*pflag.Flag
	command.Flags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag) })
	return flags
}

func flagUsage(command *cobra.Command, name string) string {
	flag := command.Flags().Lookup(name)
	if flag == nil {
		return ""
	}
	return flag.Usage
}
