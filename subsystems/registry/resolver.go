package registry

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/core"
	registryv1 "github.com/Manu343726/toolsbox/subsystems/registry/registryv1"
	"github.com/Manu343726/toolsbox/subsystems/registry/registryv1/registryv1connect"
)

// Resolver adapts the standalone registry service to core.Resolver. It is
// intentionally an adapter in the registry subsystem, so other independent
// subsystems depend only on core.Resolver and never import registry code.
type Resolver struct {
	client registryv1connect.RegistryServiceClient
}

// NewResolver creates a resolver backed by a registry endpoint.
func NewResolver(endpoint string, httpClient *http.Client) *Resolver {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Resolver{client: registryv1connect.NewRegistryServiceClient(httpClient, endpoint)}
}

// Resolve accepts either a subsystem name or a fully-qualified service name.
func (r *Resolver) Resolve(ctx context.Context, serviceName string) (core.Endpoint, error) {
	if r == nil || r.client == nil {
		return core.Endpoint{}, fmt.Errorf("registry resolver is not configured")
	}
	name := strings.TrimSpace(serviceName)
	if name == "" {
		return core.Endpoint{}, fmt.Errorf("%w: empty service name", core.ErrNotFound)
	}

	// Fast path for a subsystem name.
	response, err := r.client.ListServices(ctx, connect.NewRequest(&registryv1.ListServicesRequest{SubsystemName: name}))
	if err == nil {
		for _, descriptor := range response.Msg.GetServices() {
			if descriptor.GetSubsystemName() == name {
				return endpointFromDescriptor(descriptor), nil
			}
		}
	}

	// Service names are indexed by the registry only as metadata, so scan the
	// list for a matching fully-qualified service contract.
	response, err = r.client.ListServices(ctx, connect.NewRequest(&registryv1.ListServicesRequest{}))
	if err != nil {
		return core.Endpoint{}, fmt.Errorf("list registry services: %w", err)
	}
	for _, descriptor := range response.Msg.GetServices() {
		for _, advertised := range descriptor.GetServiceNames() {
			if advertised == name {
				return endpointFromDescriptor(descriptor), nil
			}
		}
	}
	return core.Endpoint{}, fmt.Errorf("%w: %s", core.ErrNotFound, name)
}

func endpointFromDescriptor(descriptor *registryv1.ServiceDescriptor) core.Endpoint {
	capabilities := make([]string, 0, len(descriptor.GetCapabilities()))
	for _, capability := range descriptor.GetCapabilities() {
		capabilities = append(capabilities, capability.GetName())
	}
	return core.Endpoint{
		Name:         descriptor.GetSubsystemName(),
		URL:          descriptor.GetEndpoint(),
		Version:      descriptor.GetImplementationVersion(),
		ServiceNames: append([]string(nil), descriptor.GetServiceNames()...),
		Capabilities: capabilities,
	}
}
