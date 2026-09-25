package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/Manu343726/toolsbox/pkg/core"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
)

// NewFromServiceEndpoints builds one MCP server over an arbitrary set of
// ConnectRPC endpoints. It is the public composition path for aggregating
// independently deployed services discovered by a registry or configuration.
func NewFromServiceEndpoints(ctx context.Context, endpoints []ServiceEndpoint, options Options) (*Server, error) {
	source, err := NewEndpointSource(endpoints...)
	if err != nil {
		return nil, err
	}
	return New(ctx, source, options)
}

// NewFromResolver builds an aggregated MCP from services resolved through a
// core.Resolver. The caller supplies the service names because a resolver is
// intentionally not required to expose a global list operation.
func NewFromResolver(ctx context.Context, resolver core.Resolver, serviceNames []string, options Options) (*Server, error) {
	if resolver == nil {
		return nil, fmt.Errorf("MCP resolver is required")
	}
	if len(serviceNames) == 0 {
		return nil, fmt.Errorf("at least one service name is required")
	}
	byEndpoint := make(map[string]*ServiceEndpoint)
	order := make([]string, 0)
	for _, serviceName := range serviceNames {
		serviceName = strings.TrimSpace(serviceName)
		if serviceName == "" {
			return nil, fmt.Errorf("service name cannot be empty")
		}
		endpoint, err := resolver.Resolve(ctx, serviceName)
		if err != nil {
			return nil, fmt.Errorf("resolve MCP service %q: %w", serviceName, err)
		}
		key := endpoint.Name + "\x00" + endpoint.URL
		metadata := ServiceMetadata{
			Name:         serviceName,
			Capabilities: append([]string(nil), endpoint.Capabilities...),
		}
		if existing, ok := byEndpoint[key]; ok {
			existing.Services = append(existing.Services, metadata)
			continue
		}
		byEndpoint[key] = &ServiceEndpoint{
			Name:     endpoint.Name,
			URL:      endpoint.URL,
			Services: []ServiceMetadata{metadata},
		}
		order = append(order, key)
	}
	endpoints := make([]ServiceEndpoint, 0, len(order))
	for _, key := range order {
		endpoints = append(endpoints, *byEndpoint[key])
	}
	return NewFromServiceEndpoints(ctx, endpoints, options)
}

// NewFromSubsystem builds an MCP server for all services on one already-started
// subsystem. Service capability metadata is taken from the subsystem's
// explicit service declarations, never from reflection alone.
func NewFromSubsystem(ctx context.Context, server *subsystem.Server, options Options) (*Server, error) {
	// A standalone subsystem MCP represents that subsystem's own mounted
	// services, including a health/documentation/registry service when that is
	// the subsystem being launched.
	options.IncludeInfrastructure = true
	return newFromSubsystemServices(ctx, server, nil, options)
}

// NewFromSubsystemService builds an MCP server for one service on an already
// started subsystem. It is the independent-MCP deployment path for a
// subsystem that mounts more than one contract.
func NewFromSubsystemService(ctx context.Context, server *subsystem.Server, serviceName string, options Options) (*Server, error) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return nil, fmt.Errorf("service name is required")
	}
	// An explicitly selected service is the user's chosen surface, even when
	// its name belongs to the infrastructure service family.
	options.IncludeInfrastructure = true
	return newFromSubsystemServices(ctx, server, []string{serviceName}, options)
}

func newFromSubsystemServices(ctx context.Context, server *subsystem.Server, requested []string, options Options) (*Server, error) {
	if server == nil {
		return nil, fmt.Errorf("subsystem server is required")
	}
	if strings.TrimSpace(server.Endpoint()) == "" {
		return nil, fmt.Errorf("subsystem %q must be started before creating an MCP server", server.Descriptor().SubsystemName)
	}
	wanted := make(map[string]bool, len(requested))
	for _, name := range requested {
		wanted[name] = true
	}
	metadata := make([]ServiceMetadata, 0, len(server.Services()))
	for _, service := range server.Services() {
		if len(wanted) > 0 && !wanted[service.Name] {
			continue
		}
		metadata = append(metadata, ServiceMetadata{
			Name:         service.Name,
			Capabilities: append([]string(nil), service.Capabilities...),
		})
	}
	if len(requested) > 0 && len(metadata) != len(requested) {
		return nil, fmt.Errorf("one or more requested services are not mounted by subsystem %q", server.Descriptor().SubsystemName)
	}
	if options.Policy == nil {
		options.Policy = PolicyFromServices(metadata)
	}
	endpoint := ServiceEndpoint{
		Name:     server.Descriptor().SubsystemName,
		URL:      server.Endpoint(),
		Services: metadata,
	}
	source, err := NewEndpointSource(endpoint)
	if err != nil {
		return nil, err
	}
	return New(ctx, source, options)
}

// NewFromDescriptors builds one aggregated MCP server for started subsystem
// descriptors. This is used by the combined host's global `mcp` command.
func NewFromDescriptors(ctx context.Context, descriptors []*subsystem.Descriptor, options Options) (*Server, error) {
	endpoints := make([]ServiceEndpoint, 0, len(descriptors))
	allMetadata := make([]ServiceMetadata, 0)
	for _, descriptor := range descriptors {
		if descriptor == nil {
			continue
		}
		if strings.TrimSpace(descriptor.Endpoint) == "" {
			return nil, fmt.Errorf("subsystem descriptor %q has no endpoint", descriptor.SubsystemName)
		}
		metadata := make([]ServiceMetadata, 0, len(descriptor.ServiceNames))
		for _, serviceName := range descriptor.ServiceNames {
			metadata = append(metadata, ServiceMetadata{
				Name:         serviceName,
				Capabilities: append([]string(nil), descriptor.Capabilities...),
			})
		}
		allMetadata = append(allMetadata, metadata...)
		endpoints = append(endpoints, ServiceEndpoint{
			Name:     descriptor.SubsystemName,
			URL:      descriptor.Endpoint,
			Services: metadata,
		})
	}
	if options.Policy == nil {
		options.Policy = PolicyFromServices(allMetadata)
	}
	source, err := NewEndpointSource(endpoints...)
	if err != nil {
		return nil, err
	}
	return New(ctx, source, options)
}
