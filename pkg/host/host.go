// Package host composes independently-built subsystem factories into one
// process. It does not import any feature implementation; callers register
// factories explicitly, which keeps subsystem modules independent.
package host

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Manu343726/toolbox/pkg/subsystem"
)

// Registration is a public callback invoked after a subsystem is ready.
type Registration func(context.Context, *subsystem.Descriptor) error

// Host owns selected subsystem servers.
type Host struct {
	mu         sync.Mutex
	factories  map[string]subsystem.Factory
	servers    map[string]*subsystem.Server
	selected   []string
	startOrder []string
	onStarted  Registration
	started    bool
}

// New creates an empty host.
func New() *Host {
	return &Host{
		factories: make(map[string]subsystem.Factory),
		servers:   make(map[string]*subsystem.Server),
	}
}

// Register adds a subsystem factory under a stable host name.
func (h *Host) Register(name string, factory subsystem.Factory) error {
	if name == "" || factory == nil {
		return fmt.Errorf("subsystem name and factory are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return fmt.Errorf("cannot register %q after host start", name)
	}
	if _, exists := h.factories[name]; exists {
		return fmt.Errorf("subsystem %q is already registered", name)
	}
	h.factories[name] = factory
	return nil
}

// OnStarted sets a callback invoked after each selected server starts.
func (h *Host) OnStarted(callback Registration) {
	h.mu.Lock()
	h.onStarted = callback
	h.mu.Unlock()
}

// Select chooses subsystem names. An empty selection means all registered
// subsystems, sorted by name.
func (h *Host) Select(names ...string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return fmt.Errorf("cannot select subsystems after host start")
	}
	if len(names) == 0 {
		for name := range h.factories {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := h.factories[name]; !exists {
			return fmt.Errorf("subsystem %q is not registered", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("subsystem %q selected more than once", name)
		}
		seen[name] = struct{}{}
	}
	h.selected = append([]string(nil), names...)
	return nil
}

// Start constructs and starts selected subsystems. If one fails, previously
// started subsystems are stopped before returning.
func (h *Host) Start(ctx context.Context) error {
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return fmt.Errorf("host already started")
	}
	names := append([]string(nil), h.selected...)
	if len(names) == 0 {
		for name := range h.factories {
			names = append(names, name)
		}
		sort.Strings(names)
		h.selected = append([]string(nil), names...)
	}
	factories := make(map[string]subsystem.Factory, len(names))
	for _, name := range names {
		factories[name] = h.factories[name]
	}
	callback := h.onStarted
	h.started = true
	h.startOrder = nil
	h.mu.Unlock()

	for _, name := range names {
		factory := factories[name]
		server, err := factory()
		if err != nil {
			_ = h.Shutdown(context.Background())
			return fmt.Errorf("construct subsystem %q: %w", name, err)
		}
		if err := server.Start(ctx); err != nil {
			_ = h.Shutdown(context.Background())
			return fmt.Errorf("start subsystem %q: %w", name, err)
		}
		h.mu.Lock()
		h.servers[name] = server
		h.startOrder = append(h.startOrder, name)
		h.mu.Unlock()
		if callback != nil {
			if err := callback(ctx, server.Descriptor()); err != nil {
				_ = h.Shutdown(context.Background())
				return fmt.Errorf("register subsystem %q: %w", name, err)
			}
		}
	}
	return nil
}

// Shutdown stops all started subsystems in reverse start order.
func (h *Host) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	entries := make([]struct {
		name   string
		server *subsystem.Server
	}, 0, len(h.servers))
	for name, server := range h.servers {
		entries = append(entries, struct {
			name   string
			server *subsystem.Server
		}{name: name, server: server})
	}
	order := make(map[string]int, len(h.startOrder))
	for i, name := range h.startOrder {
		order[name] = i
	}
	sort.Slice(entries, func(i, j int) bool { return order[entries[i].name] > order[entries[j].name] })
	h.servers = make(map[string]*subsystem.Server)
	h.startOrder = nil
	h.started = false
	h.mu.Unlock()

	var firstErr error
	for _, entry := range entries {
		if err := entry.server.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("shutdown subsystem %q: %w", entry.name, err)
		}
	}
	return firstErr
}

// Servers returns a snapshot of currently started servers by host name.
func (h *Host) Servers() map[string]*subsystem.Server {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make(map[string]*subsystem.Server, len(h.servers))
	for name, server := range h.servers {
		result[name] = server
	}
	return result
}

// Started reports whether the host has been started. A caller that composes a host and
// then runs a command against it needs to know whether starting it again is a mistake or
// what it expected.
func (h *Host) Started() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.started
}

// ServiceNames returns the fully-qualified protobuf services the selected subsystems
// declare, sorted and deduplicated.
//
// It composes each selected subsystem without starting it, because a factory builds a
// server and a server binds its port only when it starts. So a caller can learn what a
// deployment would serve — to build a command tree, say — without a port being held and
// without a listener that has to be released.
//
// A factory that fails is reported rather than skipped: a subsystem this process cannot
// compose is a subsystem whose operations cannot be offered, and a caller told about an
// operation that will not run is worse off than one told the deployment is incomplete.
func (h *Host) ServiceNames() ([]string, error) {
	h.mu.Lock()
	selected := append([]string(nil), h.selected...)
	started := h.started
	h.mu.Unlock()

	if len(selected) == 0 {
		if started {
			// Already running: the started servers are the truth, and re-composing a
			// factory to read a name from it would be redundant.
			return startedServiceNames(h.Servers()), nil
		}
		h.mu.Lock()
		for name := range h.factories {
			selected = append(selected, name)
		}
		h.mu.Unlock()
		sort.Strings(selected)
	}

	seen := map[string]bool{}
	var names []string
	for _, name := range selected {
		h.mu.Lock()
		factory, ok := h.factories[name]
		h.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("subsystem %q is not registered", name)
		}
		server, err := factory()
		if err != nil {
			return nil, fmt.Errorf("compose subsystem %q: %w", name, err)
		}
		for _, service := range server.Services() {
			if service.Name == "" || seen[service.Name] {
				continue
			}
			seen[service.Name] = true
			names = append(names, service.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// startedServiceNames collects the services of subsystems that are running.
func startedServiceNames(servers map[string]*subsystem.Server) []string {
	seen := map[string]bool{}
	var names []string
	for _, server := range servers {
		for _, service := range server.Services() {
			if service.Name == "" || seen[service.Name] {
				continue
			}
			seen[service.Name] = true
			names = append(names, service.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Descriptors returns registration metadata for started subsystems.
func (h *Host) Descriptors() []*subsystem.Descriptor {
	servers := h.Servers()
	result := make([]*subsystem.Descriptor, 0, len(servers))
	for _, server := range servers {
		result = append(result, server.Descriptor())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SubsystemName < result[j].SubsystemName })
	return result
}
