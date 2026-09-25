package registry

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/core"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
	"github.com/Manu343726/toolbox/subsystems/registry/registryv1/registryv1connect"
)

// A directory is a resolver that keeps a table of what a registry reports and keeps
// that table current from the registry's change stream.
//
// The obvious alternative is to ask the registry on every call. That puts a network
// round trip in front of every peer call, which a subsystem on a call path should
// not pay, and it means a resolver's answer is as stale as the last request rather
// than as stale as the last change. So the stream exists and this is its consumer:
// the table is the hot path, and the stream is what makes it true.
//
// Two states matter and are not the same. A directory that has synced knows what
// the registry has, so a name it does not hold is a name nobody serves. A directory
// that has never synced, or whose watch has dropped, knows nothing — and it says so,
// because "this process did not start that peer" and "I cannot hear the core" are
// different answers and a caller has to be able to act on each.

// DirectoryOptions configures a Directory.
type DirectoryOptions struct {
	// Endpoint is the registry's base URL.
	Endpoint string
	// HTTPClient performs the calls. Nil uses a bounded default.
	HTTPClient *http.Client
	// SyncTimeout bounds the first sync. Zero uses five seconds, because a caller
	// that cannot reach the core should be told quickly rather than waiting on it.
	SyncTimeout time.Duration
	// ReconnectDelay is how long a dropped watch waits before reconnecting. Zero
	// uses one second.
	ReconnectDelay time.Duration
	// Now is injectable for tests that need a deterministic clock.
	Now func() time.Time
}

// Directory resolves peers through a registry and follows its changes.
type Directory struct {
	client         registryv1connect.RegistryServiceClient
	syncTimeout    time.Duration
	reconnectDelay time.Duration
	now            func() time.Time

	mu        sync.RWMutex
	byName    map[string]core.Endpoint
	byService map[string]string
	synced    bool
	// lastError is why the directory is not currently synced, kept so a resolution
	// can report an outage rather than a missing service.
	lastError error
	// generation counts completed syncs, so a caller can tell a reconnection from a
	// first sync.
	generation int

	cancel context.CancelFunc
	done   chan struct{}
	closed bool
}

// NewDirectory creates a directory over a registry endpoint. It does not connect;
// call Start for that, so a caller decides when the first round trip happens.
func NewDirectory(options DirectoryOptions) (*Directory, error) {
	endpoint := strings.TrimSpace(options.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("a registry endpoint is required")
	}
	if options.SyncTimeout <= 0 {
		options.SyncTimeout = 5 * time.Second
	}
	if options.ReconnectDelay <= 0 {
		options.ReconnectDelay = time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	// The generated client does not guard a nil HTTP client, and a bounded one is
	// what a resolver wants anyway: an unbounded wait for a core that is down turns
	// a missing peer into a hang.
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Directory{
		client:         registryv1connect.NewRegistryServiceClient(options.HTTPClient, endpoint),
		syncTimeout:    options.SyncTimeout,
		reconnectDelay: options.ReconnectDelay,
		now:            options.Now,
		byName:         make(map[string]core.Endpoint),
		byService:      make(map[string]string),
		done:           make(chan struct{}),
	}, nil
}

// Start connects, performs the first sync, and then follows changes in the
// background.
//
// It returns once the first sync succeeds or fails, so a caller that proceeds knows
// which state the directory is in. A failure is returned rather than swallowed: a
// subsystem whose peer is unreachable should say so at start-up rather than on its
// first call.
func (d *Directory) Start(ctx context.Context) error {
	if d == nil || d.client == nil {
		return fmt.Errorf("registry directory is not configured")
	}
	if err := d.syncOnce(ctx); err != nil {
		return err
	}
	background, cancel := context.WithCancel(context.WithoutCancel(ctx))
	d.mu.Lock()
	d.cancel = cancel
	d.mu.Unlock()
	go d.follow(background)
	return nil
}

// follow keeps the table current, reconnecting when the stream ends.
//
// A watch that ends — cleanly, by falling behind, or by losing the connection — is
// re-established rather than tolerated. A directory that quietly stopped listening
// would go on answering from a table that stopped being true, which is worse than
// one that says it cannot reach the core.
func (d *Directory) follow(ctx context.Context) {
	defer close(d.done)
	for {
		if ctx.Err() != nil {
			return
		}
		if err := d.stream(ctx); err != nil && ctx.Err() == nil {
			d.setError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.reconnectDelay):
		}
	}
}

// stream consumes one change feed, updating the table as events arrive.
func (d *Directory) stream(ctx context.Context) error {
	stream, err := d.client.WatchServices(ctx, connect.NewRequest(&registryv1.WatchServicesRequest{}))
	if err != nil {
		return fmt.Errorf("%w: watch the registry: %w", core.ErrUnreachable, err)
	}
	for stream.Receive() {
		event := stream.Msg()
		switch event.GetType() {
		case registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_SYNC:
			d.replaceAll(event.GetServices())
			d.setError(nil)
		case registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_REGISTERED,
			registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_UPDATED:
			d.put(event.GetDescriptor_())
			d.setError(nil)
		case registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_DEREGISTERED:
			d.remove(event.GetDescriptor_().GetSubsystemName())
		case registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_HEARTBEAT:
			// A lease renewal changes reachability not at all, and re-adding the
			// endpoint would churn a table that is already correct.
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("%w: the registry watch ended: %w", core.ErrUnreachable, err)
	}
	return nil
}

func (d *Directory) syncOnce(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, d.syncTimeout)
	defer cancel()
	response, err := d.client.ListServices(bounded, connect.NewRequest(&registryv1.ListServicesRequest{}))
	if err != nil {
		wrapped := fmt.Errorf("%w: read the registry at start-up: %w", core.ErrUnreachable, err)
		d.setError(wrapped)
		return wrapped
	}
	d.replaceAll(response.Msg.GetServices())
	d.setError(nil)
	return nil
}

func (d *Directory) replaceAll(descriptors []*registryv1.ServiceDescriptor) {
	byName := make(map[string]core.Endpoint, len(descriptors))
	byService := make(map[string]string, len(descriptors))
	for _, descriptor := range descriptors {
		endpoint := endpointFromDescriptor(descriptor)
		byName[endpoint.Name] = endpoint
		for _, service := range endpoint.ServiceNames {
			byService[service] = endpoint.Name
		}
	}
	d.mu.Lock()
	d.byName = byName
	d.byService = byService
	d.synced = true
	d.generation++
	d.mu.Unlock()
}

func (d *Directory) put(descriptor *registryv1.ServiceDescriptor) {
	if descriptor == nil {
		return
	}
	endpoint := endpointFromDescriptor(descriptor)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.byName[endpoint.Name] = endpoint
	for _, service := range endpoint.ServiceNames {
		d.byService[service] = endpoint.Name
	}
	d.synced = true
}

func (d *Directory) remove(name string) {
	if name == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	endpoint, known := d.byName[name]
	if !known {
		return
	}
	delete(d.byName, name)
	for _, service := range endpoint.ServiceNames {
		if d.byService[service] == name {
			delete(d.byService, service)
		}
	}
}

func (d *Directory) setError(err error) {
	d.mu.Lock()
	d.lastError = err
	d.mu.Unlock()
}

// Resolve answers from the table, never from the network.
//
// A directory that has not synced reports that it is unreachable rather than that
// the service is missing. A caller that cannot tell those apart either refuses to
// start when a deployment legitimately runs without a peer, or retries forever
// against a core that is simply down.
func (d *Directory) Resolve(_ context.Context, serviceName string) (core.Endpoint, error) {
	if d == nil || d.client == nil {
		return core.Endpoint{}, fmt.Errorf("%w: registry directory is not configured", core.ErrUnreachable)
	}
	name := strings.TrimSpace(serviceName)
	if name == "" {
		return core.Endpoint{}, fmt.Errorf("%w: empty service name", core.ErrNotFound)
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if !d.synced {
		reason := d.lastError
		if reason == nil {
			reason = fmt.Errorf("the registry has not been read yet")
		}
		return core.Endpoint{}, fmt.Errorf("%w: %w", core.ErrUnreachable, reason)
	}
	if endpoint, ok := d.byName[name]; ok {
		return cloneEndpoint(endpoint), nil
	}
	if endpointName, ok := d.byService[name]; ok {
		if endpoint, found := d.byName[endpointName]; found {
			return cloneEndpoint(endpoint), nil
		}
	}
	return core.Endpoint{}, fmt.Errorf("%w: the registry does not serve %s", core.ErrNotFound, name)
}

// Endpoints returns every endpoint the table holds, in name order.
func (d *Directory) Endpoints() []core.Endpoint {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]core.Endpoint, 0, len(d.byName))
	for _, endpoint := range d.byName {
		result = append(result, cloneEndpoint(endpoint))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// Synced reports whether the directory currently holds a table it believes.
func (d *Directory) Synced() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.synced
}

// Generation counts completed syncs, so a caller can tell a reconnection from a
// first sync and a test can assert the table was refreshed.
func (d *Directory) Generation() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.generation
}

// Close stops following changes. It is safe to call more than once.
func (d *Directory) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
		<-d.done
	}
}

func cloneEndpoint(endpoint core.Endpoint) core.Endpoint {
	endpoint.ServiceNames = append([]string(nil), endpoint.ServiceNames...)
	return endpoint
}
