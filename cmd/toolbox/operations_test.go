package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Manu343726/toolbox/pkg/cli"
	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The main CLI and the standalone subsystem commands are the same generator over the same
// contracts, so what a caller can do through one they can do through the other. These tests
// hold that, and they hold the parts that only exist once there is a deployment to talk
// to: which operations are offered, where a call goes, and what happens when nothing is
// running.

// EveryOperationInTheContractIsACommand is the promise the main CLI makes. A subsystem's
// operations are its product, and a command tree missing one is a tool a caller cannot
// reach.
func TestEveryOperationInTheContractIsACommand(t *testing.T) {
	for _, subsystem := range []string{"knowledge", "registry", "workflow", "health", "documentation", "prompt"} {
		t.Run(subsystem, func(t *testing.T) {
			names, err := serviceNamesFor(t, subsystem)
			require.NoError(t, err)
			require.NotEmpty(t, names, "%s serves no contract, so this test proves nothing", subsystem)

			root := rootFor(t)
			offered := 0
			for _, service := range names {
				schema, err := cli.DescribeLinkedService(service)
				require.NoError(t, err)
				require.NotEmpty(t, schema.Methods)
				for _, method := range schema.Methods {
					offered++
					assert.NotNil(t, findCommand(root, method.Name),
						"%s: %s has no command", subsystem, method.Name)
				}
			}
			assert.NotZero(t, offered)
		})
	}
}

// The generated operations and the deployment commands live on one root, and neither set
// hides the other. A caller who cannot find `daemon` has lost the deployment; a caller who
// cannot find `search` has lost the product.
//
// The service keeps its level here, because the command is `toolbox` and the service is
// `knowledge`: a namespace that costs a word and names nothing the caller did not already
// name is dropped, and this one is not that. The operations are therefore one level down,
// and the test looks for them where they are rather than where a flat list would be easier
// to assert on.
func TestTheDeploymentCommandsAndTheOperationsCoexist(t *testing.T) {
	root := rootFor(t)
	names := commandNames(root)
	for _, wanted := range []string{"daemon", "mcp", "knowledge", "registry", "workflow"} {
		assert.Contains(t, names, wanted)
	}
	for _, wanted := range []string{"Search", "PutSource", "ListServices", "WatchServices"} {
		assert.NotNil(t, findCommand(root, wanted), "the operation %s is reachable", wanted)
	}
	// And the operations sit under their service, so a caller who knows the contract knows
	// the path without being told a different one.
	knowledge := findChild(root, "knowledge")
	require.NotNil(t, knowledge)
	assert.Contains(t, commandNames(knowledge), "search")
}

// Every operation command carries the flags its contract declares, typed as the contract
// declares them. A command that lost a field cannot make the call its contract describes.
func TestEveryOperationCommandCarriesItsContractFlags(t *testing.T) {
	search := findCommand(rootFor(t), "Search")
	require.NotNil(t, search)

	// SearchRequest declares query, limit and tags.
	require.NotNil(t, search.Flags().Lookup("query"))
	require.NotNil(t, search.Flags().Lookup("limit"))
	require.NotNil(t, search.Flags().Lookup("tags"))
	assert.Equal(t, "int64", search.Flags().Lookup("limit").Value.Type(),
		"a 32-bit field is read as a 64-bit flag and sent at the field's own width")
	assert.Equal(t, "stringSlice", search.Flags().Lookup("tags").Value.Type())
}

// A nested message is flattened, in the main CLI as in a subsystem command. The rule is one
// rule; a second implementation here would be a second rule, free to disagree.
func TestANestedMessageIsFlattenedInTheMainCLI(t *testing.T) {
	put := findCommand(rootFor(t), "PutSource")
	require.NotNil(t, put)

	require.NotNil(t, put.Flags().Lookup("source"), "the message is addressable")
	require.NotNil(t, put.Flags().Lookup("source.id"), "and so is one of its fields")
	assert.Contains(t, put.Flags().Lookup("source.id").Usage, "Stable source identifier",
		"documented from the contract, not from the field's type")
}

// A streaming method is offered and refused. It is not hidden: a caller who wants to know
// the method exists can, and a caller who tries is told why it cannot be called — which is a
// different answer from a connection failure, and points at a different fix.
func TestAStreamingMethodIsOfferedAndRefused(t *testing.T) {
	require.NotNil(t, findCommand(rootFor(t), "WatchServices"),
		"the method is a command")

	_, _, err := runRoot(t, "registry", "watch-services")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "streaming method")
}

// A call is answered, and the answer says which core it was resolved against. A caller whose
// write succeeded needs to know whether the next process will see it, and that is not
// something the output of a successful call can be left to imply.
//
// The mode is pinned because the default is to start a core, and a test about reporting
// should not also be a test about spawning one.
func TestACallReportsTheCoreItResolvedAgainst(t *testing.T) {
	_, stderr, err := runRoot(t, "health", "check",
		"--launch", "disabled", "--daemon-host", "127.0.0.1", "--daemon-port", "1")
	require.NoError(t, err)
	assert.Contains(t, stderr, "daemon at", "the resolved address is reported, like every other command")
	assert.Contains(t, stderr, "launch is disabled", "and so is the mode that decided what to do about it")
}

// A command that does not exist is refused by name. A caller who mistyped an operation needs
// to be told which word was not understood, not shown an unrelated failure.
func TestAnUnknownCommandIsRefusedByName(t *testing.T) {
	_, _, err := runRoot(t, "no-such-command")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-command")
}

// The command line is not gated by the policy. A policy states what an agent may call; an
// operator at a shell is the deployment's own operator, and a policy gate here would stop
// the person who writes the policy from using it. This holds that with the default
// read-only policy in force, a write still happens — and says so, because a reader of the
// test should not have to infer which behaviour was intended.
func TestTheCommandLineIsNotGatedByThePolicy(t *testing.T) {
	_, stderr, err := runRoot(t, "knowledge", "put-source",
		"--launch", "disabled", "--daemon-host", "127.0.0.1", "--daemon-port", "1",
		"--source.id", "policy-check",
		"--source.name", "Policy check",
		"--source.type", "note",
		"--source.location", "mem://policy-check")
	require.NoError(t, err, "an operator may write with a read-only policy in force")
	assert.NotContains(t, stderr, "policy document",
		"and nothing consulted one, so nothing reported one")
}

// The main CLI and a standalone subsystem command offer the same flags for the same
// contract. They describe the same contract by two routes — linked descriptors and
// reflection — and the routes can disagree, so they are compared rather than assumed equal.
// A caller who learned one has learned the other.
func TestTheMainCLIAndTheSubsystemCommandOfferTheSameFlags(t *testing.T) {
	standalone := subsystemFlagsFor(t, "knowledge", "Search")
	require.NotEmpty(t, standalone, "the subsystem command declares flags, or this proves nothing")

	main := findCommand(rootFor(t), "Search")
	require.NotNil(t, main)
	for name, kind := range standalone {
		flag := main.Flags().Lookup(name)
		require.NotNil(t, flag, "the main CLI is missing --%s, which the subsystem command has", name)
		assert.Equal(t, kind, flag.Value.Type(), "--%s is typed the same in both", name)
	}
	for _, name := range []string{"query", "limit", "tags"} {
		assert.Contains(t, standalone, name)
	}
}

// The help of an operation command names the contract it came from, so a reader who found a
// command by guessing can find the contract that defines it.
func TestAnOperationCommandNamesItsContract(t *testing.T) {
	search := findCommand(rootFor(t), "Search")
	require.NotNil(t, search)
	assert.Contains(t, search.Long, "toolbox.knowledge.v1.KnowledgeService/Search")
	assert.Contains(t, search.Short, "relevant passages",
		"and the one-line form says what it does, for the command list")
}

// serviceNamesFor returns the contracts one subsystem serves, by composing it without
// starting it.
func serviceNamesFor(t *testing.T, subsystem string) ([]string, error) {
	t.Helper()
	composed, _, err := buildHost(config.Config{LoggingBaseDir: t.TempDir()}, "")
	if err != nil {
		return nil, err
	}
	if err := composed.Select(subsystem); err != nil {
		return nil, err
	}
	return composed.ServiceNames()
}

// subsystemFlagsFor returns the flags one method has in its standalone subsystem command,
// described over reflection from the running subsystem — the other route to the same
// contract.
func subsystemFlagsFor(t *testing.T, subsystem, method string) map[string]string {
	t.Helper()
	composed, _, err := buildHost(config.Config{LoggingBaseDir: t.TempDir()}, "")
	require.NoError(t, err)
	require.NoError(t, composed.Select(subsystem))
	require.NoError(t, composed.Start(t.Context()))
	t.Cleanup(func() { _ = composed.Shutdown(context.Background()) })

	server, ok := composed.Servers()[subsystem]
	require.True(t, ok, "the %s subsystem did not start", subsystem)
	names := make([]string, 0, len(server.Services()))
	for _, service := range server.Services() {
		names = append(names, service.Name)
	}
	root, err := cli.NewGenerator(discovery.New(server.Endpoint()), cli.Options{
		CommandName: subsystem,
	}).Generate(t.Context(), names...)
	require.NoError(t, err)

	command := findCommand(root, method)
	require.NotNil(t, command, "the subsystem command has no %s", method)
	flags := map[string]string{}
	command.Flags().VisitAll(func(flag *pflag.Flag) {
		if flag.Name != "help" {
			flags[flag.Name] = flag.Value.Type()
		}
	})
	return flags
}

// findCommand locates a method command anywhere in the tree, at any depth, because how deep
// a method sits depends on whether its service kept a level of its own.
func findCommand(root *cobra.Command, method string) *cobra.Command {
	return searchCommand(root, method)
}

func searchCommand(command *cobra.Command, name string) *cobra.Command {
	if command.Name() == name || command.Name() == kebab(name) {
		return command
	}
	for _, child := range command.Commands() {
		if found := searchCommand(child, name); found != nil {
			return found
		}
	}
	return nil
}

// kebab converts a protobuf method name to the command name the generator derives. It is
// written out here rather than taken from the generator, because a test that calls the
// generator's own naming cannot catch the generator renaming things.
func kebab(value string) string {
	var out strings.Builder
	for i, r := range value {
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

func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func commandNames(command *cobra.Command) []string {
	names := make([]string, 0, len(command.Commands()))
	for _, child := range command.Commands() {
		names = append(names, child.Name())
	}
	return names
}

// runRoot executes the real command, returning stdout, stderr and the error. The real
// command rather than a composed one, because the wiring between the flag, the resolver and
// the generated call is the part with the defects.
func runRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runRootIn(t, args...)
}

// runRootIn is runRoot with a context the test chooses, for a command that blocks.
func runRootIn(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runRootInContext(t, t.Context(), args...)
}

// runRootInContext executes the real command under a context the caller cancels, which is
// how a test runs something that serves until interrupted.
func runRootInContext(t *testing.T, ctx context.Context, args ...string) (string, string, error) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := rootFor(t)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

// nothingListening reports whether anything is bound to an address, which is how a test sees
// that a command refused before it bound anything.
func nothingListening(t *testing.T, address string) bool {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err != nil {
		return true
	}
	_ = connection.Close()
	return false
}
