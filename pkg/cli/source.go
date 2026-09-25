package cli

import (
	"context"
	"fmt"
	"sync"

	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/Manu343726/toolbox/pkg/docs"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// A deployment's service surface is spread over whatever serves it: the subsystems this
// process started, the subsystems a core elsewhere started, or both. A command generated
// against one endpoint would only reach that endpoint's operations, so a generated command
// resolves its service before it describes or calls it.
//
// Describing a service and invoking a method are implemented once each, against one
// endpoint, in pkg/discovery. A source here decides which endpoint they run against, and
// what it describes from when the endpoint is not the source of the contract.

// endpointCaller resolves a service to an endpoint and calls through it, caching one
// client per endpoint so a command describing five methods of one service does not open
// five connections.
//
// It is shared rather than written twice because the two sources differ only in where the
// schema comes from, and a caller reaching a method must not depend on which source built
// the command.
type endpointCaller struct {
	resolve core.Resolver
	mu      sync.Mutex
	clients map[string]*discovery.Client
}

func newEndpointCaller(resolve core.Resolver) *endpointCaller {
	return &endpointCaller{resolve: resolve, clients: make(map[string]*discovery.Client)}
}

// clientFor resolves a service and returns the client for the endpoint serving it.
func (c *endpointCaller) clientFor(ctx context.Context, name string) (*discovery.Client, error) {
	if c == nil || c.resolve == nil {
		return nil, fmt.Errorf("the CLI source has no resolver")
	}
	endpoint, err := c.resolve.Resolve(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", name, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients[endpoint.URL]; ok {
		return client, nil
	}
	client := discovery.New(endpoint.URL)
	c.clients[endpoint.URL] = client
	return client, nil
}

// Invoke resolves the service and calls the method at its endpoint.
func (c *endpointCaller) Invoke(ctx context.Context, service, method string, request proto.Message) (proto.Message, error) {
	client, err := c.clientFor(ctx, service)
	if err != nil {
		return nil, err
	}
	return client.Invoke(ctx, service, method, request)
}

// ResolverSource is a Source that describes services over reflection at the endpoint
// serving them.
//
// It suits a caller that already has a running endpoint — a standalone subsystem command,
// which starts its subsystem and then describes it. It implements DocumentationSource, so a
// service whose reflection carries no comments is still documented from the catalog of the
// subsystem serving it, which is the preference discovery.Client already applies.
type ResolverSource struct {
	*endpointCaller
	list func(context.Context) ([]string, error)
}

// ResolverSourceOptions configures a ResolverSource.
type ResolverSourceOptions struct {
	// Resolve maps a service name to the endpoint serving it.
	Resolve core.Resolver
	// Services enumerates the service names the deployment serves. Required: without it
	// the source could describe a service a caller named but could not offer a command
	// for one nobody named.
	Services func(context.Context) ([]string, error)
}

// NewResolverSource creates a Source that describes services over reflection.
func NewResolverSource(options ResolverSourceOptions) (*ResolverSource, error) {
	if options.Resolve == nil {
		return nil, fmt.Errorf("a resolver is required")
	}
	if options.Services == nil {
		return nil, fmt.Errorf("a service lister is required")
	}
	return &ResolverSource{endpointCaller: newEndpointCaller(options.Resolve), list: options.Services}, nil
}

// ListServices returns the service names the deployment serves, in the order the lister
// gave them.
func (s *ResolverSource) ListServices(ctx context.Context) ([]string, error) {
	if s == nil || s.list == nil {
		return nil, fmt.Errorf("the CLI source is not configured")
	}
	return s.list(ctx)
}

// DescribeService resolves the service and describes it at its endpoint.
func (s *ResolverSource) DescribeService(ctx context.Context, name string) (*discovery.ServiceSchema, error) {
	client, err := s.clientFor(ctx, name)
	if err != nil {
		return nil, err
	}
	return client.DescribeService(ctx, name)
}

// GetDocumentation asks the endpoint serving a service for its documentation.
func (s *ResolverSource) GetDocumentation(ctx context.Context, name string) (*docs.Service, error) {
	client, err := s.clientFor(ctx, name)
	if err != nil {
		return nil, err
	}
	return client.GetDocumentation(ctx, name)
}

// LinkedSource is a Source that describes services from the contracts linked into this
// binary, and calls them through a resolver.
//
// It exists because a command's flags have to exist before anything runs, and a host
// aggregates subsystems that are not started yet. Reflection describes a running endpoint;
// the linked descriptors describe what this binary was built with. Both are real, and they
// can disagree — so a test holds them to each other rather than leaving it to chance.
type LinkedSource struct {
	*endpointCaller
	// names is the surface this source offers, in the order it was given.
	names []string
}

// NewLinkedSource creates a Source over the contracts linked into this binary.
//
// The names are the surface to offer, not a filter: a name that is not linked cannot be
// described at all, and a caller told about an operation this binary cannot describe would
// be told about one it cannot call.
func NewLinkedSource(resolve core.Resolver, names []string) (*LinkedSource, error) {
	if resolve == nil {
		return nil, fmt.Errorf("a resolver is required")
	}
	return &LinkedSource{endpointCaller: newEndpointCaller(resolve), names: append([]string(nil), names...)}, nil
}

// ListServices returns the linked service names.
func (s *LinkedSource) ListServices(context.Context) ([]string, error) {
	if s == nil {
		return nil, fmt.Errorf("the CLI source is not configured")
	}
	return append([]string(nil), s.names...), nil
}

// DescribeService describes a service from the linked descriptors, documenting it from the
// embedded descriptor set when one is registered for it.
func (s *LinkedSource) DescribeService(_ context.Context, name string) (*discovery.ServiceSchema, error) {
	schema, err := DescribeLinkedService(name)
	if err != nil {
		return nil, err
	}
	if documentation, err := docs.DefaultCatalog().Get(name); err == nil {
		schema.Documentation = &documentation
	}
	return schema, nil
}

// GetDocumentation returns the embedded documentation for a service.
func (s *LinkedSource) GetDocumentation(_ context.Context, name string) (*docs.Service, error) {
	documentation, err := docs.DefaultCatalog().Get(name)
	if err != nil {
		return nil, err
	}
	return &documentation, nil
}

// DescribeLinkedService builds a schema for a service from the descriptors linked into this
// binary.
//
// It is the same shape discovery.Client produces from reflection, so the generator cannot
// tell which one built it. That is deliberate: a generator that behaved differently
// depending on its source would be two generators.
func DescribeLinkedService(name string) (*discovery.ServiceSchema, error) {
	found, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		return nil, fmt.Errorf("no linked contract for %s: %w", name, err)
	}
	service, ok := found.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s is not a service", name)
	}
	schema := &discovery.ServiceSchema{
		Name:          name,
		Descriptor:    service,
		Methods:       make([]discovery.MethodSchema, 0, service.Methods().Len()),
		Documentation: &docs.Service{Name: name},
	}
	for i := 0; i < service.Methods().Len(); i++ {
		method := service.Methods().Get(i)
		schema.Methods = append(schema.Methods, discovery.MethodSchema{
			Name:            string(method.Name()),
			Input:           method.Input(),
			Output:          method.Output(),
			ClientStreaming: method.IsStreamingClient(),
			ServerStreaming: method.IsStreamingServer(),
		})
	}
	return schema, nil
}
