// Package core contains the public runtime primitives shared by independent
// Toolsbox subsystems: endpoint resolution, service discovery, and dynamic
// service-to-service ConnectRPC calls.
package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	// ErrNotFound indicates that a resolver has no endpoint for a service.
	ErrNotFound = errors.New("service endpoint not found")
	// ErrInvalidEndpoint indicates malformed endpoint metadata.
	ErrInvalidEndpoint = errors.New("invalid service endpoint")
)

// Endpoint is the provider-neutral information needed to call a service.
type Endpoint struct {
	// Name is the subsystem or logical service name used for registration.
	Name string
	// URL is the base HTTP endpoint for ConnectRPC calls.
	URL string
	// Version is the implementation version.
	Version string
	// ServiceNames lists fully-qualified protobuf services at the endpoint.
	ServiceNames []string
	// Capabilities lists semantic capabilities exposed by the endpoint.
	Capabilities []string
}

// Resolver maps a fully-qualified protobuf service name to an endpoint.
// Implementations may use a registry, static configuration, DNS, or another
// discovery mechanism. Core does not depend on a particular registry
// implementation.
type Resolver interface {
	Resolve(context.Context, string) (Endpoint, error)
}

// ResolverFunc adapts a function to Resolver.
type ResolverFunc func(context.Context, string) (Endpoint, error)

// Resolve implements Resolver.
func (f ResolverFunc) Resolve(ctx context.Context, serviceName string) (Endpoint, error) {
	return f(ctx, serviceName)
}

// StaticResolver is a concurrency-safe in-memory resolver useful for tests,
// single-process deployments, and bootstrap configuration.
type StaticResolver struct {
	mu        sync.RWMutex
	byName    map[string]Endpoint
	byService map[string]string
}

// NewStaticResolver creates a resolver from endpoint definitions.
func NewStaticResolver(endpoints ...Endpoint) *StaticResolver {
	r := &StaticResolver{byName: make(map[string]Endpoint), byService: make(map[string]string)}
	for _, endpoint := range endpoints {
		_ = r.Register(endpoint)
	}
	return r
}

// Register adds or replaces an endpoint and indexes all advertised service
// names. Invalid endpoints are ignored; use ValidateEndpoint when an error is
// required.
func (r *StaticResolver) Register(endpoint Endpoint) error {
	if err := ValidateEndpoint(endpoint); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[endpoint.Name] = cloneEndpoint(endpoint)
	for _, serviceName := range endpoint.ServiceNames {
		r.byService[serviceName] = endpoint.Name
	}
	return nil
}

// Deregister removes an endpoint and its service indexes.
func (r *StaticResolver) Deregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	endpoint, ok := r.byName[name]
	if !ok {
		return
	}
	delete(r.byName, name)
	for serviceName, endpointName := range r.byService {
		if endpointName == name {
			delete(r.byService, serviceName)
		}
	}
	_ = endpoint
}

// Resolve finds an endpoint by subsystem name or fully-qualified service name.
func (r *StaticResolver) Resolve(_ context.Context, serviceName string) (Endpoint, error) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return Endpoint{}, fmt.Errorf("%w: empty service name", ErrNotFound)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if endpoint, ok := r.byName[serviceName]; ok {
		return cloneEndpoint(endpoint), nil
	}
	if endpointName, ok := r.byService[serviceName]; ok {
		if endpoint, ok := r.byName[endpointName]; ok {
			return cloneEndpoint(endpoint), nil
		}
	}
	return Endpoint{}, fmt.Errorf("%w: %s", ErrNotFound, serviceName)
}

// ValidateEndpoint checks the minimum endpoint metadata.
func ValidateEndpoint(endpoint Endpoint) error {
	if strings.TrimSpace(endpoint.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidEndpoint)
	}
	if strings.TrimSpace(endpoint.URL) == "" {
		return fmt.Errorf("%w: URL is required for %s", ErrInvalidEndpoint, endpoint.Name)
	}
	return nil
}

func cloneEndpoint(endpoint Endpoint) Endpoint {
	endpoint.ServiceNames = append([]string(nil), endpoint.ServiceNames...)
	endpoint.Capabilities = append([]string(nil), endpoint.Capabilities...)
	return endpoint
}

// SortedServiceNames returns a deterministic copy of service names.
func (e Endpoint) SortedServiceNames() []string {
	result := append([]string(nil), e.ServiceNames...)
	sort.Strings(result)
	return result
}
