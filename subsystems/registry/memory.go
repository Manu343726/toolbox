// Package registry provides the in-memory service registry used by the
// reference runtime. The storage API is independent from the ConnectRPC
// handler so a persistent registry can replace it without changing clients.
package registry

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	"google.golang.org/protobuf/proto"
)

var (
	// ErrNotFound indicates that a subsystem is not registered.
	ErrNotFound = errors.New("subsystem not found")
	// ErrInvalidRegistration indicates malformed registration data.
	ErrInvalidRegistration = errors.New("invalid service registration")
	// ErrLeaseExpired indicates that a registration is no longer active.
	ErrLeaseExpired = errors.New("service registration lease expired")
)

// EventType describes a registry mutation delivered to watchers.
type EventType string

const (
	// EventRegistered is emitted after a new registration.
	EventRegistered EventType = "registered"
	// EventUpdated is emitted when an existing registration is replaced.
	EventUpdated EventType = "updated"
	// EventHeartbeat is emitted when a lease is extended.
	EventHeartbeat EventType = "heartbeat"
	// EventDeregistered is emitted after a registration is removed.
	EventDeregistered EventType = "deregistered"
)

// Event is a registry change notification.
type Event struct {
	Type       EventType
	Descriptor *registryv1.ServiceDescriptor
}

// MemoryOptions configures a Memory registry.
type MemoryOptions struct {
	// DefaultLease is used when a registration does not specify a lease.
	DefaultLease time.Duration
	// Now is injectable for deterministic expiry tests.
	Now func() time.Time
}

// Memory is a concurrency-safe in-memory registry.
type Memory struct {
	mu           sync.RWMutex
	entries      map[string]*registryv1.ServiceDescriptor
	watchers     map[chan Event]struct{}
	defaultLease time.Duration
	now          func() time.Time
}

// NewMemory creates an in-memory registry.
func NewMemory(opts MemoryOptions) *Memory {
	if opts.DefaultLease <= 0 {
		opts.DefaultLease = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Memory{
		entries:      make(map[string]*registryv1.ServiceDescriptor),
		watchers:     make(map[chan Event]struct{}),
		defaultLease: opts.DefaultLease,
		now:          opts.Now,
	}
}

// Register adds or replaces a descriptor and returns the stored copy.
func (m *Memory) Register(descriptor *registryv1.ServiceDescriptor, lease time.Duration) (*registryv1.ServiceDescriptor, error) {
	if err := validateDescriptor(descriptor); err != nil {
		return nil, err
	}
	if lease < 0 {
		return nil, fmt.Errorf("%w: lease cannot be negative", ErrInvalidRegistration)
	}
	if lease == 0 {
		lease = m.defaultLease
	}

	now := m.now()
	stored := proto.Clone(descriptor).(*registryv1.ServiceDescriptor)
	stored.RegisteredAtUnixNano = now.UnixNano()
	stored.LeaseExpiresAtUnixNano = now.Add(lease).UnixNano()
	if stored.GetStatus() == registryv1.RegistryStatus_REGISTRY_STATUS_UNSPECIFIED {
		stored.Status = registryv1.RegistryStatus_REGISTRY_STATUS_SERVING
	}

	m.mu.Lock()
	_, existed := m.entries[stored.GetSubsystemName()]
	m.entries[stored.GetSubsystemName()] = stored
	m.mu.Unlock()

	eventType := EventRegistered
	if existed {
		eventType = EventUpdated
	}
	m.publish(Event{Type: eventType, Descriptor: cloneDescriptor(stored)})
	return cloneDescriptor(stored), nil
}

// Heartbeat extends an active registration lease.
func (m *Memory) Heartbeat(name string, lease time.Duration) (*registryv1.ServiceDescriptor, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%w: subsystem name is required", ErrInvalidRegistration)
	}
	if lease < 0 {
		return nil, fmt.Errorf("%w: lease cannot be negative", ErrInvalidRegistration)
	}
	if lease == 0 {
		lease = m.defaultLease
	}
	now := m.now()

	m.mu.Lock()
	entry, ok := m.entries[name]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if entry.GetLeaseExpiresAtUnixNano() != 0 && now.UnixNano() >= entry.GetLeaseExpiresAtUnixNano() {
		delete(m.entries, name)
		m.mu.Unlock()
		m.publish(Event{Type: EventDeregistered, Descriptor: cloneDescriptor(entry)})
		return nil, fmt.Errorf("%w: %s", ErrLeaseExpired, name)
	}
	updated := proto.Clone(entry).(*registryv1.ServiceDescriptor)
	updated.LeaseExpiresAtUnixNano = now.Add(lease).UnixNano()
	m.entries[name] = updated
	m.mu.Unlock()

	m.publish(Event{Type: EventHeartbeat, Descriptor: cloneDescriptor(updated)})
	return cloneDescriptor(updated), nil
}

// Deregister removes a registration. It is idempotent and reports whether a
// live entry was removed.
func (m *Memory) Deregister(name string) bool {
	m.mu.Lock()
	entry, ok := m.entries[name]
	if ok {
		delete(m.entries, name)
	}
	m.mu.Unlock()
	if ok {
		m.publish(Event{Type: EventDeregistered, Descriptor: cloneDescriptor(entry)})
	}
	return ok
}

// Get returns an active registration.
func (m *Memory) Get(name string) (*registryv1.ServiceDescriptor, error) {
	m.removeExpired()
	m.mu.RLock()
	entry, ok := m.entries[name]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return cloneDescriptor(entry), nil
}

// List returns active registrations matching the supplied filters.
func (m *Memory) List(name string, requiredCapabilities []string, includeExpired bool) []*registryv1.ServiceDescriptor {
	if !includeExpired {
		m.removeExpired()
	}
	m.mu.RLock()
	entries := make([]*registryv1.ServiceDescriptor, 0, len(m.entries))
	for _, entry := range m.entries {
		if name != "" && entry.GetSubsystemName() != name {
			continue
		}
		if !hasCapabilities(entry, requiredCapabilities) {
			continue
		}
		entries = append(entries, cloneDescriptor(entry))
	}
	m.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].GetSubsystemName() < entries[j].GetSubsystemName()
	})
	return entries
}

// Watch returns a buffered event channel. Call CloseWatch when finished.
//
// The buffer is how much change a watcher may fall behind by before it is
// disconnected. A watcher whose channel closes while it still expects events was
// disconnected for falling behind, and its caller should resynchronise — re-read
// the current set — rather than carry on with a partial picture.
func (m *Memory) Watch(buffer int) chan Event {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Event, buffer)
	m.mu.Lock()
	m.watchers[ch] = struct{}{}
	m.mu.Unlock()
	return ch
}

// CloseWatch removes a watcher created by Watch.
//
// Closing a watcher that was already disconnected for falling behind is not an
// error: the channel is closed exactly once, and this call is a no-op then.
func (m *Memory) CloseWatch(ch chan Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, watching := m.watchers[ch]; !watching {
		return
	}
	delete(m.watchers, ch)
	close(ch)
}

// removeExpired drops registrations whose lease lapsed, and reports each one.
//
// A lapsed lease is a removal, and a watcher that was not told would go on
// resolving a subsystem that is gone. That is the whole reason a watch exists, so
// expiry publishes rather than sweeping silently.
func (m *Memory) removeExpired() {
	now := m.now().UnixNano()
	expired := make([]*registryv1.ServiceDescriptor, 0)
	m.mu.Lock()
	for name, entry := range m.entries {
		if entry.GetLeaseExpiresAtUnixNano() != 0 && now >= entry.GetLeaseExpiresAtUnixNano() {
			delete(m.entries, name)
			expired = append(expired, entry)
		}
	}
	m.mu.Unlock()
	sort.Slice(expired, func(i, j int) bool {
		return expired[i].GetSubsystemName() < expired[j].GetSubsystemName()
	})
	for _, entry := range expired {
		m.publish(Event{Type: EventDeregistered, Descriptor: cloneDescriptor(entry)})
	}
}

// publish delivers an event to every watcher.
//
// A watcher that cannot keep up is disconnected rather than skipped. Dropping an
// event is the one outcome nobody should choose: a missed deregistration leaves a
// client resolving an endpoint that is gone, and the client has no way to notice it
// missed anything. Closing the channel tells it to resynchronise, which it can do
// correctly.
func (m *Memory) publish(event Event) {
	m.mu.Lock()
	lagging := make([]chan Event, 0)
	for watcher := range m.watchers {
		select {
		case watcher <- event:
		default:
			lagging = append(lagging, watcher)
		}
	}
	for _, watcher := range lagging {
		delete(m.watchers, watcher)
		close(watcher)
	}
	m.mu.Unlock()
}

func validateDescriptor(descriptor *registryv1.ServiceDescriptor) error {
	if descriptor == nil {
		return fmt.Errorf("%w: descriptor is required", ErrInvalidRegistration)
	}
	if strings.TrimSpace(descriptor.GetSubsystemName()) == "" {
		return fmt.Errorf("%w: subsystem_name is required", ErrInvalidRegistration)
	}
	if strings.TrimSpace(descriptor.GetEndpoint()) == "" {
		return fmt.Errorf("%w: endpoint is required", ErrInvalidRegistration)
	}
	parsed, err := url.Parse(descriptor.GetEndpoint())
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%w: endpoint must be an absolute http(s) URL", ErrInvalidRegistration)
	}
	return nil
}

func hasCapabilities(descriptor *registryv1.ServiceDescriptor, required []string) bool {
	if len(required) == 0 {
		return true
	}
	available := make(map[string]struct{}, len(descriptor.GetCapabilities()))
	for _, capability := range descriptor.GetCapabilities() {
		available[capability.GetName()] = struct{}{}
	}
	for _, name := range required {
		if _, ok := available[name]; !ok {
			return false
		}
	}
	return true
}

func cloneDescriptor(descriptor *registryv1.ServiceDescriptor) *registryv1.ServiceDescriptor {
	if descriptor == nil {
		return nil
	}
	return proto.Clone(descriptor).(*registryv1.ServiceDescriptor)
}
