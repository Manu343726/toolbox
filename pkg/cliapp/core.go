package cliapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/cli"
	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/spf13/cobra"
)

// A standalone subsystem command has two honest modes, and which one it is in decides what a
// write through it means.
//
// Started privately, it is a complete deployment of one subsystem: every operation is
// reachable, and the state lives exactly as long as the process. That is the right shape
// for a subsystem under development, and for a deployment of one thing that keeps no state
// worth sharing.
//
// Pointed at a core, it calls that deployment: the same operations, against the state the
// deployment holds. That is the right shape for everything else, because a write to a
// private instance reports success and is gone by the next invocation.
//
// The mode is decided by whether a core address is configured, and the address comes from a
// flag, the environment, or a configuration file — in that order, as everywhere else in the
// framework. It is not a registry lookup: this command knows which contract it serves, and a
// core's address is where contracts are served. That is what keeps a standalone command from
// depending on the registry subsystem merely to find its own way home.

// target decides where an operation call goes, and starts a private instance if that is the
// answer.
//
// It is resolved when a call is made rather than when the command tree is built, because a
// command's flags have to exist before anything runs and the flag that chooses the mode is
// one of them. So the tree is built from the contracts this binary links, and the endpoint
// is looked up once there is a call to send.
type target struct {
	options Options
	// flag reads the --core value. It is a pointer because cobra fills it during parsing,
	// after this is built and before the first call.
	flag func() string
	// names are the contracts this subsystem serves, from composing it without starting.
	names []string

	mu     sync.Mutex
	server *subsystem.Server
	// resolver reaches the configured core, built on first use.
	resolver core.Resolver
	// release closes it, once the command is done.
	release func()
	// started records that the private instance was created, so a second call does not
	// compose a second one.
	started bool
}

// newTarget composes the subsystem to learn what it serves, without starting it: a factory
// builds a server, and a server binds its port only when it starts.
func newTarget(options Options, flag func() string) (*target, []string, error) {
	composed, err := options.Factory()
	if err != nil {
		return nil, nil, err
	}
	var names []string
	for _, service := range composed.Services() {
		names = append(names, service.Name)
	}
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("the %s subsystem serves no contract", options.Name)
	}
	return &target{options: options, flag: flag, names: names}, names, nil
}

// Resolve returns the endpoint serving a service.
func (t *target) Resolve(ctx context.Context, serviceName string) (core.Endpoint, error) {
	if !servesAny(t.names, serviceName) {
		return core.Endpoint{}, fmt.Errorf(
			"%w: %s serves %s, not %s",
			core.ErrNotFound, t.options.Name, strings.Join(t.names, ", "), serviceName)
	}
	if address := t.coreAddress(); address != "" {
		resolver, err := t.coreResolver()
		if err != nil {
			return core.Endpoint{}, err
		}
		endpoint, err := resolver.Resolve(ctx, serviceName)
		if err != nil {
			// Not wrapped again here. A failure that reaches the caller is wrapped once,
			// where the core's address is known, and a message that says the same thing
			// twice is a message nobody reads to the end.
			return core.Endpoint{}, err
		}
		t.report("calling the core at %s", address)
		return endpoint, nil
	}
	server, err := t.privateInstance(ctx)
	if err != nil {
		return core.Endpoint{}, err
	}
	return core.Endpoint{
		Name:         t.options.Name,
		URL:          server.Endpoint(),
		ServiceNames: t.names,
	}, nil
}

// coreAddress returns the configured core address, or empty for a private instance.
func (t *target) coreAddress() string {
	if flag := t.flag; flag != nil {
		if address := normaliseCoreAddress(flag()); address != "" {
			return address
		}
	}
	if address := normaliseCoreAddress(t.options.Core); address != "" {
		return address
	}
	return normaliseCoreAddress(configuredCoreAddress(t.options))
}

// configuredCoreAddress resolves a core address from the environment or a configuration
// file, and reports whether one was asked for at all.
func configuredCoreAddress(options Options) string {
	if os.Getenv(config.EnvCore) != "" || os.Getenv(config.EnvPort) != "" {
		if resolved, err := resolveFromConfig(); err == nil {
			return resolved
		}
	}
	if resolved, err := resolveFromConfig(); err == nil && resolved != "" {
		return resolved
	}
	return ""
}

// resolveFromConfig reads the configuration chain, reporting whether a core was named.
//
// Only an address somebody asked for counts. Every client has a default address, and treating
// the default as configured would point every subsystem command at a core that is not
// running and report an unreachable deployment where there is none.
func resolveFromConfig() (string, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return "", nil
	}
	resolved, err := config.Resolve(config.Overrides{
		ConfigFile: os.Getenv(config.EnvConfigFile),
	}, workDir)
	if err != nil {
		return "", err
	}
	asked := os.Getenv(config.EnvCore) != "" || os.Getenv(config.EnvPort) != "" || resolved.Path != ""
	if !asked {
		return "", nil
	}
	return resolved.Core.Addr(), nil
}

func normaliseCoreAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if !strings.Contains(address, "://") {
		return "http://" + address
	}
	return address
}

// coreResolver builds the resolver for the configured core, once.
//
// A core's address is the registry's, and a peer is found by asking the core what it holds,
// so the resolver is built from the registry's contract. It is built once and kept, because a
// directory holds a change stream and a second one per call would be a second subscription to
// the same thing.
func (t *target) coreResolver() (core.Resolver, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resolver != nil {
		return t.resolver, nil
	}
	build := t.options.ResolverFor
	if build == nil {
		return nil, fmt.Errorf(
			"this command cannot resolve a peer in the core at %s: a core's address is its "+
				"registry's, and finding a service needs the registry's contract. A host "+
				"command wires that; a standalone subsystem command does not",
			t.coreAddress())
	}
	resolver, release, err := build(t.coreAddress())
	if err != nil {
		return nil, err
	}
	if release != nil {
		t.release = release
	}
	t.resolver = resolver
	return resolver, nil
}

// privateInstance starts the subsystem on first use and returns it.
func (t *target) privateInstance(ctx context.Context) (*subsystem.Server, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started {
		if t.server == nil {
			return nil, fmt.Errorf("the %s subsystem could not be started", t.options.Name)
		}
		return t.server, nil
	}
	server, err := t.options.Factory()
	if err != nil {
		return nil, err
	}
	if err := server.Start(ctx); err != nil {
		return nil, fmt.Errorf("start %s: %w", t.options.Name, err)
	}
	t.server = server
	t.started = true
	t.report("running a private instance of %s", t.options.Name)
	return server, nil
}

// serve starts the private instance and blocks, which is what the root command does when no
// operation was named. A command pointed at a core has nothing to serve — the core is
// already serving — so it prints its help and returns, because a command with nothing to do
// that hangs is indistinguishable from a hung deployment.
func (t *target) serve(ctx context.Context, cmd *cobra.Command) error {
	if address := t.coreAddress(); address != "" {
		t.report("calling the core at %s; there is nothing to serve here", address)
		return cmd.Help()
	}
	server, err := t.privateInstance(ctx)
	if err != nil {
		return err
	}
	return server.Wait(ctx)
}

// localOnly refuses a command that only makes sense with a subsystem in this process.
//
// It is a refusal rather than a hidden command. A command that is hidden when a core is
// configured is a command whose absence a caller has to guess at, and the question "can this
// serve anything?" is worth answering — it is just answered with the reason rather than by
// silence. The check is made when the command runs because a command's flags are only
// parsed then, and the flag is what decides the mode.
func (t *target) localOnly(what string) error {
	address := t.coreAddress()
	if address == "" {
		return nil
	}
	return fmt.Errorf(
		"%s needs a subsystem in this process, and this command is pointed at the core at %s: "+
			"the core is already serving. Run it without --core to serve %s here",
		what, address, t.options.Name)
}

// shutdown stops the private instance, if one was started.
func (t *target) shutdown() {
	t.mu.Lock()
	server := t.server
	t.server = nil
	t.started = false
	release := t.release
	t.resolver = nil
	t.release = nil
	t.mu.Unlock()
	if server != nil {
		_ = server.Shutdown(context.Background())
	}
	if release != nil {
		release()
	}
}

func (t *target) report(format string, args ...any) {
	out := t.options.Report
	if out == nil {
		out = os.Stderr
	}
	if out == io.Discard {
		return
	}
	fmt.Fprintf(out, t.options.Name+": "+format+"\n", args...)
}

func servesAny(serviceNames []string, serviceName string) bool {
	for _, candidate := range serviceNames {
		if candidate == serviceName {
			return true
		}
	}
	return false
}

// source builds the generator source for the command: the contracts this binary links, and
// the target that decides where a call goes.
func (t *target) source() (cli.Source, error) {
	return cli.NewLinkedSource(core.ResolverFunc(t.Resolve), t.names)
}

// coreUnreachable names the core a failed call was aimed at, when the failure was the core
// not answering rather than the operation refusing.
//
// A refusal from the service is left exactly as it arrived: the service said no, and adding
// "the core did not answer" to a refusal would send a reader to the wrong place — they would
// go and start a core that is already running.
func coreUnreachable(err error, address string) error {
	if err == nil || strings.TrimSpace(address) == "" {
		return err
	}
	// A resolver that says it could not be consulted has already named the core, because
	// only it knows the address. Wrapping again would repeat it.
	if errors.Is(err, core.ErrUnreachable) {
		return err
	}
	var connectErr *net.OpError
	if errors.As(err, &connectErr) ||
		strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "no such host") {
		return fmt.Errorf("the core at %s did not answer: %w", address, err)
	}
	return err
}

// reportFailedCalls makes a failed call name the core it was aimed at.
//
// Every operation command is wrapped, so a caller who pointed a command at a core and got a
// transport failure learns that the core did not answer rather than inferring it from a dial
// error. A refusal from the service is left exactly as it arrived: the service said no, and
// adding "the core did not answer" to a refusal would send the reader to the wrong place.
func reportFailedCalls(root *cobra.Command, address func() string) {
	if root == nil {
		return
	}
	for _, command := range root.Commands() {
		wrapFailedCall(command, address)
	}
}

func wrapFailedCall(command *cobra.Command, address func() string) {
	if command == nil {
		return
	}
	if inner := command.RunE; inner != nil && !command.HasSubCommands() {
		command.RunE = func(cmd *cobra.Command, args []string) error {
			err := inner(cmd, args)
			if target := address(); target != "" {
				return coreUnreachable(err, target)
			}
			return err
		}
	}
	for _, child := range command.Commands() {
		wrapFailedCall(child, address)
	}
}
