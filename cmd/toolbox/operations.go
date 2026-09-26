package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/Manu343726/toolbox/pkg/cli"
	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/spf13/cobra"
)

// The main CLI offers every operation the built-in subsystems serve, as a command with that
// operation's own flags. It is the same generator the standalone subsystem commands use,
// over the same contracts, so `toolbox knowledge search` and `knowledge search` are one
// command with a different host around it.
//
// Two things are decided here rather than in the generator, because both are about this
// process rather than about the contract:
//
//   - the commands are built from the contracts linked into this binary, because a
//     command's flags must exist before anything runs and a host aggregates subsystems
//     that are not started yet;
//   - each command then resolves where to call, honouring the flags the caller passed. A
//     --component selection narrows what runs, and a configured core is preferred over
//     starting anything, so a command reaches the state a long-lived core holds rather
//     than writing to a store nobody else can see.
//
// Nothing here consults the policy. A policy states what an agent may call; an operator at
// a shell is the deployment's own operator, and a policy gate on the command line would stop
// the person who writes the policy from using it.

// streamingAnnotation marks a generated method command the unary generator cannot invoke, so
// the host can leave it alone rather than resolving for a call that will be refused.
const streamingAnnotation = "toolbox.streaming"

// lateResolver defers resolution to the moment of the call.
//
// The command tree is built before the caller has said which subsystems to run or which core
// to reach, because a command's flags have to exist before anything starts. So the tree is
// built from the contracts, and the resolver is filled in by the command that runs.
type lateResolver struct {
	mu       sync.Mutex
	resolver core.Resolver
}

func (l *lateResolver) set(resolver core.Resolver) {
	l.mu.Lock()
	l.resolver = resolver
	l.mu.Unlock()
}

func (l *lateResolver) Resolve(ctx context.Context, serviceName string) (core.Endpoint, error) {
	l.mu.Lock()
	resolver := l.resolver
	l.mu.Unlock()
	if resolver == nil {
		return core.Endpoint{}, fmt.Errorf(
			"nothing is serving %s: no core answered and no subsystem is running", serviceName)
	}
	return resolver.Resolve(ctx, serviceName)
}

// addOperationCommands generates a command per operation of the built-in subsystems and
// attaches them to a root.
//
// Building the tree costs no port, no listener and no log file: the subsystems are composed
// to learn what they serve and are never started, because a subsystem's factory builds its
// server and a server binds its port only when it starts. The composition is introspective
// for the same reason — this runs before any command has parsed its flags, so there is no
// deployment to configure a fanout from.
func addOperationCommands(root *cobra.Command) error {
	composed, _, err := buildHost(hostComposition{introspect: true})
	if err != nil {
		return err
	}
	names, err := composed.ServiceNames()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	resolver := &lateResolver{}
	source, err := cli.NewLinkedSource(resolver, names)
	if err != nil {
		return err
	}
	generated, err := cli.NewGenerator(source, cli.Options{
		CommandName: root.Name(),
		Description: root.Short,
	}).Generate(context.Background(), names...)
	if err != nil {
		return err
	}
	// The generated root carries the operations; the caller's root carries the deployment
	// commands, so only the operations are taken from it.
	//
	// Every command in the generated tree is retargeted, not just the top level. A service
	// command is a group whose RunE prints help, so retargeting only the top level would
	// leave every method calling with nothing running — and the failure would look like a
	// resolution problem rather than a wiring one.
	for _, command := range generated.Commands() {
		retargetOperationTree(command, resolver)
		root.AddCommand(command)
	}
	return nil
}

// retargetOperationTree retargets a command and everything under it.
func retargetOperationTree(command *cobra.Command, resolver *lateResolver) {
	children := command.Commands()
	if len(children) == 0 {
		retargetOperationCommand(command, resolver)
		return
	}
	for _, child := range children {
		retargetOperationTree(child, resolver)
	}
}

// retargetOperationCommand makes a generated operation command resolve its target from the
// caller's flags, call, and clean up.
//
// The generated command knows the operation and nothing about the deployment, which is the
// right division: the contract says what the call takes, and the flags say where it goes.
func retargetOperationCommand(command *cobra.Command, resolver *lateResolver) {
	inner := command.RunE
	if inner == nil {
		return
	}
	// A method command that cannot be invoked — a streaming one — keeps its refusal. Its
	// RunE never reaches a service, so there is nothing to resolve and starting subsystems
	// to answer it would be work for a message that says no.
	if _, ok := command.Annotations[streamingAnnotation]; ok {
		return
	}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		cleanup, err := resolveOperationTarget(cmd, resolver)
		if err != nil {
			return err
		}
		defer cleanup()
		return inner(cmd, args)
	}
	command.SilenceUsage = true
	command.SilenceErrors = true
}

// resolveOperationTarget decides where an operation call goes, and returns how to undo it.
//
// A core that answers is the target and nothing is started, because a core holds the state a
// deployment accumulates. When no core answers the command starts the subsystems the caller
// selected, so a deployment with no core still works from the command line.
//
// The two are not mixed. A call answered partly by a core and partly by a process would
// read to write to whichever answered, and a store the core does not hold is a write that
// reports success and is gone.
func resolveOperationTarget(cmd *cobra.Command, resolver *lateResolver) (func(), error) {
	resolved, err := resolveConfig(cmd)
	if err != nil {
		return nil, err
	}
	// Reported like every other command, because a caller who asked where a call went
	// should not have to infer it: this command may be answered by a core, by this
	// process, or by one after the other declined.
	reportConfig(cmd, resolved)

	// Whether a core answers, and what happens when one does not, is decided by how the
	// deployment manages the daemon's lifecycle. That is a statement about who owns the
	// state, so the three modes differ exactly here: "auto" starts one, "explicit" refuses
	// rather than hide a stopped service, and "disabled" never needs one.
	directory, err := reachCore(cmd.Context(), resolved, cmd.ErrOrStderr())
	if err != nil {
		return nil, err
	}
	if directory != nil {
		// A core is the target and nothing is started. Serving the call partly from a core
		// and partly from this process would read to a caller as a write that happened, when
		// it went to a store the core does not hold.
		resolver.set(directory)
		return directory.Close, nil
	}

	components, err := cmd.Flags().GetStringSlice("component")
	if err != nil {
		return nil, err
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return nil, err
	}
	if all && len(components) > 0 {
		return nil, fmt.Errorf("--all cannot be combined with --component")
	}
	if !all && len(components) == 0 {
		all = true
	}

	h, _, err := buildHost(hostComposition{config: resolved})
	if err != nil {
		return nil, err
	}
	if !all {
		if err := h.Select(components...); err != nil {
			return nil, err
		}
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := h.Start(ctx); err != nil {
		return nil, fmt.Errorf("start the subsystems this operation needs: %w", err)
	}
	stopHost := func() { _ = h.Shutdown(context.Background()) }

	// No core answered, so this process serves the call. One leg, because a local host
	// knows every service it runs and a leg that could not answer would only add a way to
	// be wrong.
	resolver.set(core.NewChain(core.Leg{Name: "this process", Resolver: hostResolver{host: h}}))
	return stopHost, nil
}
