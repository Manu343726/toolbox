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
	clients   map[string]*discovery.Client
}

// NewEndpointSource creates a routing source for the supplied endpoints.
func NewEndpointSource(endpoints ...ServiceEndpoint) (*EndpointSource, error) {
	source := &EndpointSource{
		byService: make(map[string]string),
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
		if previous, exists := s.byService[service.Name]; exists {
			return fmt.Errorf("service %q is advertised by both %q and %q", service.Name, previous, endpoint.Name)
		}
		s.byService[service.Name] = endpoint.Name
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

// ServiceMetadata returns the explicit metadata for a service, if known.
func (s *EndpointSource) ServiceMetadata(serviceName string) (ServiceMetadata, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	endpointName, ok := s.byService[serviceName]
	if !ok {
		return ServiceMetadata{}, false
	}
	for _, endpoint := range s.endpoints {
		if endpoint.Name != endpointName {
			continue
		}
		for _, service := range endpoint.Services {
			if service.Name == serviceName {
				return ServiceMetadata{
					Name:           service.Name,
					Capabilities:   append([]string(nil), service.Capabilities...),
					AllowedMethods: append([]string(nil), service.AllowedMethods...),
				}, true
			}
		}
	}
	return ServiceMetadata{}, false
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
