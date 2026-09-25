package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/subsystem"
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
		metadata := ServiceMetadata{Name: serviceName}
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
	// services, so nothing is exempt from it: if the subsystem serves a health or
	// registry service, that is part of what this command offers.
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
	// An explicitly selected service is the user's chosen surface.
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
	names := make([]string, 0, len(server.Services()))
	for _, service := range server.Services() {
		if len(wanted) > 0 && !wanted[service.Name] {
			continue
		}
		names = append(names, service.Name)
	}
	if len(requested) > 0 && len(names) != len(requested) {
		return nil, fmt.Errorf("one or more requested services are not mounted by subsystem %q", server.Descriptor().SubsystemName)
	}
	metadata := make([]ServiceMetadata, 0, len(names))
	for _, name := range names {
		metadata = append(metadata, ServiceMetadata{Name: name})
	}
	// A standalone subsystem's MCP is that subsystem's own surface, and the
	// composition that launched it is the deployment — so it states the policy
	// rather than leaving the gateway to guess one. A gateway with no policy
	// exposes nothing, which is right for a host serving many subsystems and wrong
	// for a command serving exactly the one the user asked for.
	if options.Policy.Len() == 0 && options.InitialExposure == ExposeAllowedFeatures {
		options.Policy = APIPolicy()
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
	for _, descriptor := range descriptors {
		if descriptor == nil {
			continue
		}
		if strings.TrimSpace(descriptor.Endpoint) == "" {
			return nil, fmt.Errorf("subsystem descriptor %q has no endpoint", descriptor.SubsystemName)
		}
		metadata := make([]ServiceMetadata, 0, len(descriptor.ServiceNames))
		for _, serviceName := range descriptor.ServiceNames {
			metadata = append(metadata, ServiceMetadata{Name: serviceName})
		}
		endpoints = append(endpoints, ServiceEndpoint{
			Name:     descriptor.SubsystemName,
			URL:      descriptor.Endpoint,
			Services: metadata,
		})
	}
	// Nothing is derived from the descriptors here. A host serves many subsystems
	// at once, so which of their operations an agent may call is the deployment's
	// decision and the composition supplies it; a gateway with no policy exposes
	// introspection only.
	source, err := NewEndpointSource(endpoints...)
	if err != nil {
		return nil, err
	}
	return New(ctx, source, options)
}
