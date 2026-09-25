package mcp

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/discovery"
	"google.golang.org/protobuf/proto"
)

// Source supplies reflected service schemas and dynamic unary invocation to
// the MCP generator. discovery.Client implements Source for one endpoint;
// EndpointSource implements it for an aggregate of endpoints.
type Source interface {
	ListServices(context.Context) ([]string, error)
	DescribeService(context.Context, string) (*discovery.ServiceSchema, error)
	Invoke(context.Context, string, string, proto.Message) (proto.Message, error)
}

// ServiceMetadata is the explicit capability metadata for one service.
type ServiceMetadata struct {
	Name         string
	Capabilities []string
	// AllowedMethods optionally narrows the service to an explicit method
	// allow-list. An empty list means all unary methods are covered by the
	// service capability declaration.
	AllowedMethods []string
}

// ServiceEndpoint identifies a ConnectRPC endpoint and the services it
// advertises. Capabilities are attached to service names rather than inferred
// from reflected method names.
type ServiceEndpoint struct {
	Name     string
	URL      string
	Services []ServiceMetadata
}

// EndpointSource routes reflection and dynamic calls to one of several
// ConnectRPC endpoints. It is the multi-endpoint building block used by the
// aggregated host MCP.
type EndpointSource struct {
	mu        sync.RWMutex
	endpoints []ServiceEndpoint
	byService map[string]string
	// owners records every endpoint that serves a service, so a shared contract
	// keeps all of its owners rather than one of them.
	owners  map[string][]string
	clients map[string]*discovery.Client
}

// ServiceOwner returns the endpoint that serves a service, or an empty name when
// no endpoint does.
//
// A contract several subsystems serve has one owner on this path: reflection
// reaches a service by name, so it reaches the endpoint that registered it, and
// the first registration stands. That is the limit of what a name can address,
// and it is why the catalog is where providers are told apart — there each one is
// a separate API with its own identifier.
// SharedServices returns the services more than one endpoint serves, in a stable
// order.
//
// A contract several subsystems serve is how a format is contributed, so this is
// not a fault. It is reported because a service name reaches one endpoint: a
// deployment whose two subsystems claim the same contract by accident is looking
// at the same list, and the only sign it has is that list.
func (s *EndpointSource) SharedServices() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	shared := make([]string, 0, len(s.owners))
	for service, owners := range s.owners {
		if len(owners) > 1 {
			shared = append(shared, service)
		}
	}
	sort.Strings(shared)
	return shared
}

// NewEndpointSource creates a routing source for the supplied endpoints.
func NewEndpointSource(endpoints ...ServiceEndpoint) (*EndpointSource, error) {
	source := &EndpointSource{
		byService: make(map[string]string),
		owners:    make(map[string][]string),
		clients:   make(map[string]*discovery.Client),
	}
	for _, endpoint := range endpoints {
		if err := source.addEndpoint(endpoint); err != nil {
			return nil, err
		}
	}
	return source, nil
}

func (s *EndpointSource) addEndpoint(endpoint ServiceEndpoint) error {
	endpoint.Name = strings.TrimSpace(endpoint.Name)
	endpoint.URL = strings.TrimRight(strings.TrimSpace(endpoint.URL), "/")
	if endpoint.Name == "" {
		return fmt.Errorf("service endpoint name is required")
	}
	for _, existing := range s.endpoints {
		if existing.Name == endpoint.Name {
			return fmt.Errorf("service endpoint %q is configured more than once", endpoint.Name)
		}
	}
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("service endpoint %q has an invalid URL %q", endpoint.Name, endpoint.URL)
	}
	for _, service := range endpoint.Services {
		service.Name = strings.TrimSpace(service.Name)
		if service.Name == "" {
			return fmt.Errorf("service endpoint %q contains an unnamed service", endpoint.Name)
		}
		// Several subsystems serving one contract is expected — that is how a
		// format is added — so the name is recorded once, to the first endpoint that
		// advertised it, and every owner is kept. A caller that needs a specific
		// provider names it; a caller that reaches for the name alone is reaching
		// for a shared surface.
		if _, recorded := s.byService[service.Name]; !recorded {
			s.byService[service.Name] = endpoint.Name
		}
		s.owners[service.Name] = append(s.owners[service.Name], endpoint.Name)
	}
	s.endpoints = append(s.endpoints, endpoint)
	return nil
}

// ListServices returns the union of explicitly advertised service names. For
// endpoints without metadata it falls back to reflection discovery.
func (s *EndpointSource) ListServices(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	endpoints := append([]ServiceEndpoint(nil), s.endpoints...)
	s.mu.RUnlock()
	seen := make(map[string]bool)
	for _, endpoint := range endpoints {
		if len(endpoint.Services) > 0 {
			for _, service := range endpoint.Services {
				seen[service.Name] = true
			}
			continue
		}
		client, err := s.client(endpoint)
		if err != nil {
			return nil, err
		}
		names, err := client.ListServices(ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			seen[name] = true
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

// DescribeService returns the reflected schema for a service.
func (s *EndpointSource) DescribeService(ctx context.Context, serviceName string) (*discovery.ServiceSchema, error) {
	client, err := s.clientForService(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	return client.DescribeService(ctx, serviceName)
}

// Invoke calls a unary method on the endpoint advertising serviceName.
func (s *EndpointSource) Invoke(ctx context.Context, serviceName, methodName string, request proto.Message) (proto.Message, error) {
	client, err := s.clientForService(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	return client.Invoke(ctx, serviceName, methodName, request)
}

// ServiceOwner returns the endpoint that serves a service. A name addresses one
// endpoint, which is the limit of what reflection can reach.
func (s *EndpointSource) ServiceOwner(serviceName string) string { return s.ownerOf(serviceName) }

func (s *EndpointSource) ownerOf(serviceName string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byService[strings.TrimSpace(serviceName)]
}

func (s *EndpointSource) clientForService(ctx context.Context, serviceName string) (*discovery.Client, error) {
	s.mu.RLock()
	endpointName, known := s.byService[serviceName]
	var endpoint ServiceEndpoint
	found := false
	for _, candidate := range s.endpoints {
		if candidate.Name == endpointName {
			endpoint = candidate
			found = true
			break
		}
	}
	s.mu.RUnlock()
	if known && found {
		return s.client(endpoint)
	}
	// An endpoint without explicit service metadata is probed as a fallback.
	s.mu.RLock()
	endpoints := append([]ServiceEndpoint(nil), s.endpoints...)
	s.mu.RUnlock()
	var lastErr error
	for _, candidate := range endpoints {
		client, err := s.client(candidate)
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := client.DescribeService(ctx, serviceName); err == nil {
			return client, nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("discover service %q: %w", serviceName, lastErr)
	}
	return nil, fmt.Errorf("service %q is not present in any configured endpoint", serviceName)
}

func (s *EndpointSource) client(endpoint ServiceEndpoint) (*discovery.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if client, ok := s.clients[endpoint.Name]; ok {
		return client, nil
	}
	client := discovery.New(endpoint.URL)
	s.clients[endpoint.Name] = client
	return client, nil
}
