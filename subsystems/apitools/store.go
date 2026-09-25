package apitools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
)

// Store errors. Callers map them to ConnectRPC status codes, and tests match
// them with errors.Is.
var (
	// ErrNotFound is returned when a requested server, API, or operation is not
	// in the catalog.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists is returned when a registration would replace an
	// existing resource and the caller required a create.
	ErrAlreadyExists = errors.New("already exists")
	// ErrNotAllowed is returned when policy denies an exposure request.
	ErrNotAllowed = errors.New("not allowed")
	// ErrInvalid is returned when a registration violates the catalog contract.
	ErrInvalid = errors.New("invalid")
	// ErrNoProvider is returned when no parser or adapter provider is available
	// for the requested format or transport.
	ErrNoProvider = errors.New("no provider")
	// ErrAmbiguous is returned when several providers match and the caller did
	// not select one.
	ErrAmbiguous = errors.New("ambiguous provider")
)

// StoreOptions configures the in-memory catalog.
type StoreOptions struct {
	// Policy decides which operations may be exposed. The zero value permits
	// nothing: a catalog with no stated policy describes and documents every
	// registered API while none of it is callable, because "no policy" and "every
	// policy" must never be the same value.
	Policy api.Policy
	// Now supplies the clock used for registration timestamps. Tests inject a
	// fixed clock; the zero value uses time.Now.
	Now func() time.Time
}

// Memory is the in-process repository of servers, APIs, indexed format and
// transport descriptors, and operation exposure. It is safe for concurrent use
// and returns deterministic, sorted results.
type Memory struct {
	mu         sync.RWMutex
	servers    map[string]api.Server
	apis       map[string]api.API
	formats    map[string]api.FormatDescriptor
	transports map[string]api.TransportDescriptor
	exposure   map[string]bool
	revision   int64
	policy     api.Policy
	now        func() time.Time
}

// NewMemory creates an empty catalog.
func NewMemory(options StoreOptions) *Memory {
	// The zero policy is the deny-all policy, so nothing here invents a default
	// that would authorize a deployment's whole surface.
	policy := options.Policy
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Memory{
		servers:    make(map[string]api.Server),
		apis:       make(map[string]api.API),
		formats:    make(map[string]api.FormatDescriptor),
		transports: make(map[string]api.TransportDescriptor),
		exposure:   make(map[string]bool),
		policy:     policy,
		now:        now,
	}
}

// Policy returns the policy the catalog authorizes exposure with.
func (m *Memory) Policy() api.Policy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.policy
}

// Revision returns the current catalog revision. It increases on every change,
// so a client can detect that its view is stale.
func (m *Memory) Revision() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.revision
}

// RegisterServer stores a server registration.
func (m *Memory) RegisterServer(server api.Server, failIfExists bool) (api.Server, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	normalized := server.Clone()
	if err := normalized.Normalize(m.now()); err != nil {
		return api.Server{}, false, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	previous, existed := m.servers[normalized.ID]
	if existed && failIfExists {
		return api.Server{}, false, fmt.Errorf("%w: server %q", ErrAlreadyExists, normalized.ID)
	}
	// A replacement keeps the original registration time, so the catalog records
	// when the server first appeared rather than when it was last edited.
	if existed && !previous.RegisteredAt.IsZero() {
		normalized.RegisteredAt = previous.RegisteredAt
	}
	m.servers[normalized.ID] = normalized
	m.revision++
	return normalized.Clone(), existed, nil
}

// GetServer returns one registered server.
func (m *Memory) GetServer(id string) (api.Server, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	server, ok := m.servers[strings.TrimSpace(id)]
	if !ok {
		return api.Server{}, fmt.Errorf("%w: server %q", ErrNotFound, id)
	}
	return server.Clone(), nil
}

// ServerFilter narrows a server listing.
type ServerFilter struct {
	// Query matches the server name, identifier, or base URL.
	Query string
	// Status, when set, requires the server to have that status.
	Status api.ServerStatus
	// APIID requires the server to host that API.
	APIID string
	// Format requires the server to serve that description format.
	Format api.Format
}

// ListServers returns matching servers ordered by identifier.
func (m *Memory) ListServers(filter ServerFilter) []api.Server {
	m.mu.RLock()
	defer m.mu.RUnlock()
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	apiID := strings.TrimSpace(filter.APIID)
	format := strings.TrimSpace(filter.Format)
	result := make([]api.Server, 0, len(m.servers))
	for _, server := range m.servers {
		if filter.Status != api.ServerStatusUnspecified && server.Status != filter.Status {
			continue
		}
		if format != "" && server.Format != format {
			continue
		}
		if apiID != "" && !servesAPI(server, m.apis[apiID]) {
			continue
		}
		if query != "" && !serverMatches(server, query) {
			continue
		}
		result = append(result, server.Clone())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func serverMatches(server api.Server, query string) bool {
	return strings.Contains(strings.ToLower(server.ID), query) ||
		strings.Contains(strings.ToLower(server.Name), query) ||
		strings.Contains(strings.ToLower(server.BaseURL), query)
}

func servesAPI(server api.Server, target api.API) bool {
	if server.ID == "" {
		return false
	}
	for _, id := range target.ServerIDs {
		if id == server.ID {
			return true
		}
	}
	return false
}

// APIIDsForServer returns the identifiers of the APIs a server hosts.
func (m *Memory) APIIDsForServer(serverID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	serverID = strings.TrimSpace(serverID)
	result := make([]string, 0)
	for id, target := range m.apis {
		if servesAPI(m.servers[serverID], target) {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

// DeleteServer removes a server. When APIs are still hosted by it the removal is
// rejected unless force is set, in which case those APIs are unbound rather
// than deleted: their descriptions stay valid, they simply have no location.
func (m *Memory) DeleteServer(id string, force bool) (bool, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id = strings.TrimSpace(id)
	server, ok := m.servers[id]
	if !ok {
		return false, nil, fmt.Errorf("%w: server %q", ErrNotFound, id)
	}
	hosted := make([]string, 0)
	for apiID, target := range m.apis {
		if servesAPI(server, target) {
			hosted = append(hosted, apiID)
		}
	}
	sort.Strings(hosted)
	if len(hosted) > 0 && !force {
		return false, nil, fmt.Errorf(
			"%w: server %q still hosts %d api(s) (%s); retry with force to unbind them",
			ErrInvalid, id, len(hosted), strings.Join(hosted, ", "),
		)
	}
	for _, apiID := range hosted {
		target := m.apis[apiID]
		remaining := make([]string, 0, len(target.ServerIDs))
		for _, bound := range target.ServerIDs {
			if bound != id {
				remaining = append(remaining, bound)
			}
		}
		target.ServerIDs = remaining
		m.apis[apiID] = target
	}
	delete(m.servers, id)
	m.revision++
	return true, hosted, nil
}

// RegisterFormat indexes a description format descriptor. Registering a format
// this framework has never seen is normal: the descriptor is what makes a
// user-defined format discoverable.
func (m *Memory) RegisterFormat(descriptor api.FormatDescriptor, failIfExists bool) (api.FormatDescriptor, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	normalized := descriptor.Clone()
	normalized.ID = strings.TrimSpace(normalized.ID)
	normalized.Name = strings.TrimSpace(normalized.Name)
	if err := normalized.Validate(); err != nil {
		return api.FormatDescriptor{}, false, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	_, existed := m.formats[normalized.ID]
	if existed && failIfExists {
		return api.FormatDescriptor{}, false, fmt.Errorf("%w: format %q", ErrAlreadyExists, normalized.ID)
	}
	m.formats[normalized.ID] = normalized
	m.revision++
	return normalized.Clone(), existed, nil
}

// Format returns one indexed format descriptor.
func (m *Memory) Format(id string) (api.FormatDescriptor, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	descriptor, ok := m.formats[strings.TrimSpace(id)]
	if !ok {
		return api.FormatDescriptor{}, false
	}
	return descriptor.Clone(), true
}

// Formats returns the indexed format descriptors ordered by identifier.
func (m *Memory) Formats() []api.FormatDescriptor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return sortedFormats(m.formats)
}

// RegisterTransport indexes a transport descriptor.
func (m *Memory) RegisterTransport(descriptor api.TransportDescriptor, failIfExists bool) (api.TransportDescriptor, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	normalized := descriptor.Clone()
	normalized.ID = strings.TrimSpace(normalized.ID)
	normalized.Name = strings.TrimSpace(normalized.Name)
	if err := normalized.Validate(); err != nil {
		return api.TransportDescriptor{}, false, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	_, existed := m.transports[normalized.ID]
	if existed && failIfExists {
		return api.TransportDescriptor{}, false, fmt.Errorf("%w: transport %q", ErrAlreadyExists, normalized.ID)
	}
	m.transports[normalized.ID] = normalized
	m.revision++
	return normalized.Clone(), existed, nil
}

// Transport returns one indexed transport descriptor.
func (m *Memory) Transport(id string) (api.TransportDescriptor, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	descriptor, ok := m.transports[strings.TrimSpace(id)]
	if !ok {
		return api.TransportDescriptor{}, false
	}
	return descriptor.Clone(), true
}

// Transports returns the indexed transport descriptors ordered by identifier.
func (m *Memory) Transports() []api.TransportDescriptor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return sortedTransports(m.transports)
}

func sortedFormats(source map[string]api.FormatDescriptor) []api.FormatDescriptor {
	ids := make([]string, 0, len(source))
	for id := range source {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]api.FormatDescriptor, 0, len(ids))
	for _, id := range ids {
		result = append(result, source[id].Clone())
	}
	return result
}

func sortedTransports(source map[string]api.TransportDescriptor) []api.TransportDescriptor {
	ids := make([]string, 0, len(source))
	for id := range source {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]api.TransportDescriptor, 0, len(ids))
	for _, id := range ids {
		result = append(result, source[id].Clone())
	}
	return result
}

// RegisterAPI stores an API description, binding it to a registered server when
// one is named. The description is validated and normalized first, so a parser
// provider cannot introduce an operation identifier that does not match its own
// service and name.
func (m *Memory) RegisterAPI(target api.API, serverID string, failIfExists bool) (api.API, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	normalized, err := target.Normalize()
	if err != nil {
		return api.API{}, false, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	serverID = strings.TrimSpace(serverID)
	if serverID != "" {
		server, ok := m.servers[serverID]
		if !ok {
			return api.API{}, false, fmt.Errorf("%w: server %q", ErrNotFound, serverID)
		}
		// A format mismatch is a real error rather than a silent override: the
		// adapter that would invoke this API speaks a different format than the
		// parser that described it.
		if normalized.Format != server.Format {
			return api.API{}, false, fmt.Errorf(
				"%w: api %q is %q but server %q serves %q",
				ErrInvalid, normalized.ID, normalized.Format, serverID, server.Format,
			)
		}
		normalized.ServerIDs = []string{serverID}
	}
	_, existed := m.apis[normalized.ID]
	if existed && failIfExists {
		return api.API{}, false, fmt.Errorf("%w: api %q", ErrAlreadyExists, normalized.ID)
	}
	// Replacing an API drops exposure state for operations that no longer
	// exist, and keeps it for operations that do.
	m.pruneExposureLocked(normalized)
	m.apis[normalized.ID] = normalized
	m.revision++
	return normalized.Clone(), existed, nil
}

// pruneExposureLocked removes exposure entries for operations the incoming API
// no longer declares. It is called with the write lock held. The comparison is
// against the incoming description rather than the stored one, so removing an
// operation really does remove its exposure instead of keeping it.
func (m *Memory) pruneExposureLocked(incoming api.API) {
	live := make(map[string]bool)
	for _, operation := range incoming.Operations() {
		live[operation.ID] = true
	}
	prefix := incoming.ID + "/"
	for id := range m.exposure {
		if strings.HasPrefix(id, prefix) && !live[id] {
			delete(m.exposure, id)
		}
	}
}

// GetAPI returns one registered API.
func (m *Memory) GetAPI(id string) (api.API, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	target, ok := m.apis[strings.TrimSpace(id)]
	if !ok {
		return api.API{}, fmt.Errorf("%w: api %q", ErrNotFound, id)
	}
	return target.Clone(), nil
}

// APIFilter narrows an API listing.
type APIFilter struct {
	// Query matches the API name, title, or identifier.
	Query string
	// Format requires the API to have that description format.
	Format api.Format
	// ServerID requires the API to be hosted by that server.
	ServerID string
	// SideEffects requires the API to have an operation declaring one of them.
	SideEffects []api.SideEffect
}

// ListAPIs returns matching APIs ordered by identifier.
func (m *Memory) ListAPIs(filter APIFilter) []api.API {
	m.mu.RLock()
	defer m.mu.RUnlock()
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	serverID := strings.TrimSpace(filter.ServerID)
	effects := make([]api.SideEffect, 0, len(filter.SideEffects))
	for _, effect := range filter.SideEffects {
		if trimmed := api.SideEffect(strings.TrimSpace(effect)); trimmed != "" {
			effects = append(effects, trimmed)
		}
	}
	format := strings.TrimSpace(filter.Format)
	result := make([]api.API, 0, len(m.apis))
	for _, target := range m.apis {
		if format != "" && target.Format != format {
			continue
		}
		if serverID != "" && !containsString(target.ServerIDs, serverID) {
			continue
		}
		if len(effects) > 0 && !hasSideEffect(target, effects) {
			continue
		}
		if query != "" && !apiMatches(target, query) {
			continue
		}
		result = append(result, target.Clone())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func apiMatches(target api.API, query string) bool {
	return strings.Contains(strings.ToLower(target.ID), query) ||
		strings.Contains(strings.ToLower(target.Name), query) ||
		strings.Contains(strings.ToLower(target.Title), query)
}

// hasSideEffect reports whether any operation of an API declares one of the
// effects. It answers "which APIs offer something that does this", which is a
// question about the operations rather than about who may call them.
func hasSideEffect(target api.API, effects []api.SideEffect) bool {
	for _, operation := range target.Operations() {
		for _, declared := range operation.SideEffects {
			for _, wanted := range effects {
				if declared == wanted {
					return true
				}
			}
		}
	}
	return false
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// DeleteAPI removes an API and its exposure state.
func (m *Memory) DeleteAPI(id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id = strings.TrimSpace(id)
	if _, ok := m.apis[id]; !ok {
		return false, fmt.Errorf("%w: api %q", ErrNotFound, id)
	}
	delete(m.apis, id)
	prefix := id + "/"
	for operationID := range m.exposure {
		if strings.HasPrefix(operationID, prefix) {
			delete(m.exposure, operationID)
		}
	}
	m.revision++
	return true, nil
}

// Operation finds one operation of one API and reports whether the catalog
// policy allows exposing it.
func (m *Memory) Operation(apiID, operationID string) (api.Operation, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	target, ok := m.apis[strings.TrimSpace(apiID)]
	if !ok {
		return api.Operation{}, false, fmt.Errorf("%w: api %q", ErrNotFound, apiID)
	}
	operationID = strings.TrimSpace(operationID)
	for _, service := range target.Services {
		for _, operation := range service.Operations {
			if operation.ID == operationID {
				return operation.Clone(), m.allows(target, operation), nil
			}
		}
	}
	return api.Operation{}, false, fmt.Errorf("%w: operation %q of api %q", ErrNotFound, operationID, apiID)
}

// SetExposed records an exposure decision. Hiding is always permitted;
// exposing requires that policy allows the operation, that the operation is not
// streaming, and that the API is bound to a server.
func (m *Memory) SetExposed(apiID, operationID string, exposed bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	apiID = strings.TrimSpace(apiID)
	operationID = strings.TrimSpace(operationID)
	target, ok := m.apis[apiID]
	if !ok {
		return false, fmt.Errorf("%w: api %q", ErrNotFound, apiID)
	}
	operation, found := findOperation(target, operationID)
	if !found {
		return false, fmt.Errorf("%w: operation %q of api %q", ErrNotFound, operationID, apiID)
	}
	if exposed {
		if !m.allows(target, operation) {
			return false, fmt.Errorf(
				"%w: operation %q is not authorized by the deployment's policy", ErrNotAllowed, operationID,
			)
		}
		if operation.Streaming.Streaming() {
			return false, fmt.Errorf(
				"%w: operation %q is streaming; the catalog invokes unary operations only",
				ErrInvalid, operationID,
			)
		}
		if len(target.ServerIDs) == 0 {
			return false, fmt.Errorf(
				"%w: api %q is not bound to a server; register a server before exposing operations",
				ErrInvalid, apiID,
			)
		}
	}
	if m.exposure[operationID] == exposed {
		return false, nil
	}
	m.exposure[operationID] = exposed
	m.revision++
	return true, nil
}

// allows reports whether the catalog's policy permits exposing an operation.
//
// The identifier and the declared side effects are the whole input: what the
// operation is called, and what its contract says invoking it does. Both are facts
// the description already carries, so the catalog does not have to be told a
// second vocabulary to answer this.
func (m *Memory) allows(target api.API, operation api.Operation) bool {
	return m.policy.Allows(api.OperationFacts{
		ID:          operation.ID,
		SideEffects: operation.SideEffects,
	})
}

func findOperation(target api.API, operationID string) (api.Operation, bool) {
	for _, service := range target.Services {
		for _, operation := range service.Operations {
			if operation.ID == operationID {
				return operation, true
			}
		}
	}
	return api.Operation{}, false
}

// Exposed reports whether an operation is currently exposed.
func (m *Memory) Exposed(operationID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.exposure[strings.TrimSpace(operationID)]
}

// Exposure is the footprint of one operation.
type Exposure struct {
	// Operation is the operation the footprint describes.
	Operation api.Operation
	// Allowed reports whether policy permits exposing the operation.
	Allowed bool
	// Exposed reports whether the operation is currently exposed.
	Exposed bool
	// Invokable reports whether the operation can be invoked: it is unary and
	// its API is bound to a server.
	Invokable bool
}

// ExposureFilter narrows an exposure report.
type ExposureFilter struct {
	// APIID limits the report to one API.
	APIID string
	// Service limits the report to one service name.
	Service string
}

// Exposure returns the footprint of matching operations, ordered by API
// identifier and then by declared operation order.
func (m *Memory) Exposure(filter ExposureFilter) []Exposure {
	m.mu.RLock()
	defer m.mu.RUnlock()
	apiID := strings.TrimSpace(filter.APIID)
	service := strings.TrimSpace(filter.Service)
	ids := make([]string, 0, len(m.apis))
	for id := range m.apis {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]Exposure, 0)
	for _, id := range ids {
		if apiID != "" && id != apiID {
			continue
		}
		target := m.apis[id]
		for _, candidate := range target.Services {
			if service != "" && candidate.Name != service {
				continue
			}
			for _, operation := range candidate.Operations {
				result = append(result, Exposure{
					Operation: operation.Clone(),
					Allowed:   m.allows(target, operation),
					Exposed:   m.exposure[operation.ID],
					Invokable: !operation.Streaming.Streaming() && len(target.ServerIDs) > 0,
				})
			}
		}
	}
	return result
}

// FormatUsage counts the registered APIs per format, so a format index can
// report how much of the catalog depends on each format.
func (m *Memory) FormatUsage() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	usage := make(map[string]int, len(m.apis))
	for _, target := range m.apis {
		usage[target.Format]++
	}
	return usage
}

// TransportUsage counts the registered servers per transport.
func (m *Memory) TransportUsage() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	usage := make(map[string]int, len(m.servers))
	for _, server := range m.servers {
		usage[server.Transport]++
	}
	return usage
}

// Catalog returns a read-only api.Catalog view of the store, so an MCP gateway
// or another adapter can consume the repository without importing this
// subsystem's service contract.
func (m *Memory) Catalog() api.Catalog { return catalogView{store: m} }

type catalogView struct {
	store *Memory
}

// Servers implements api.Catalog.
func (c catalogView) Servers(context.Context) ([]api.Server, error) {
	return c.store.ListServers(ServerFilter{}), nil
}

// APIs implements api.Catalog.
func (c catalogView) APIs(context.Context) ([]api.API, error) {
	return c.store.ListAPIs(APIFilter{}), nil
}
