package core

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/discovery"
	"google.golang.org/protobuf/proto"
)

// Metadata is propagated on service-to-service calls. It is deliberately
// small and provider-neutral; authentication tokens belong in HTTP transport
// configuration, not in these fields.
type Metadata struct {
	RequestID   string
	TraceID     string
	ActorID     string
	RunID       string
	WorkspaceID string
	PolicyID    string
	// Headers contains additional protocol metadata. Keys are copied as-is.
	Headers map[string]string
}

// Header returns the metadata as HTTP headers.
func (m Metadata) Header() http.Header {
	header := make(http.Header, len(m.Headers)+6)
	for key, value := range m.Headers {
		header.Set(key, value)
	}
	setIfPresent(header, "X-Toolsbox-Request-Id", m.RequestID)
	setIfPresent(header, "X-Toolsbox-Trace-Id", m.TraceID)
	setIfPresent(header, "X-Toolsbox-Actor-Id", m.ActorID)
	setIfPresent(header, "X-Toolsbox-Run-Id", m.RunID)
	setIfPresent(header, "X-Toolsbox-Workspace-Id", m.WorkspaceID)
	setIfPresent(header, "X-Toolsbox-Policy-Id", m.PolicyID)
	return header
}

func setIfPresent(header http.Header, key, value string) {
	if strings.TrimSpace(value) != "" {
		header.Set(key, value)
	}
}

// ClientOptions configures a core service client.
type ClientOptions struct {
	// Resolver maps service names to endpoints.
	Resolver Resolver
	// HTTPClient is used for ConnectRPC calls. A timeout client is used when nil.
	HTTPClient *http.Client
	// ConnectOptions are applied to dynamically created Connect clients.
	ConnectOptions []connect.ClientOption
	// Metadata is added to every dynamic call.
	Metadata Metadata
}

// Client resolves, discovers, and calls independent subsystem services.
type Client struct {
	resolver       Resolver
	httpClient     *http.Client
	connectOptions []connect.ClientOption
	metadata       Metadata

	mu      sync.Mutex
	clients map[string]*discovery.Client
}

// NewClient creates a public service-to-service client.
func NewClient(options ClientOptions) *Client {
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		resolver:       options.Resolver,
		httpClient:     httpClient,
		connectOptions: append([]connect.ClientOption(nil), options.ConnectOptions...),
		metadata:       options.Metadata,
		clients:        make(map[string]*discovery.Client),
	}
}

// Resolver returns the configured endpoint resolver.
func (c *Client) Resolver() Resolver {
	return c.resolver
}

// Resolve returns the endpoint for a fully-qualified service name.
func (c *Client) Resolve(ctx context.Context, serviceName string) (Endpoint, error) {
	if c.resolver == nil {
		return Endpoint{}, fmt.Errorf("%w: no resolver configured", ErrNotFound)
	}
	return c.resolver.Resolve(ctx, serviceName)
}

// Discover returns reflection metadata for a service.
func (c *Client) Discover(ctx context.Context, serviceName string) (*discovery.ServiceSchema, error) {
	discoveryClient, err := c.discoveryFor(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	return discoveryClient.DescribeService(ctx, serviceName)
}

// Invoke performs a unary service-to-service call. The request may be a
// generated protobuf message or a dynamic message obtained from discovery.
func (c *Client) Invoke(ctx context.Context, serviceName, methodName string, request proto.Message) (proto.Message, error) {
	discoveryClient, err := c.discoveryFor(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	return discoveryClient.InvokeWithHeaders(ctx, serviceName, methodName, request, c.metadata.Header())
}

// InvokeJSON performs a unary call using protobuf JSON input and output.
func (c *Client) InvokeJSON(ctx context.Context, serviceName, methodName string, input []byte) ([]byte, error) {
	discoveryClient, err := c.discoveryFor(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	return discoveryClient.InvokeJSONWithHeaders(ctx, serviceName, methodName, input, c.metadata.Header())
}

// Service returns a convenience handle for one logical service.
func (c *Client) Service(serviceName string) *ServiceClient {
	return &ServiceClient{client: c, serviceName: serviceName}
}

func (c *Client) discoveryFor(ctx context.Context, serviceName string) (*discovery.Client, error) {
	endpoint, err := c.Resolve(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.clients[endpoint.URL]; ok {
		return existing, nil
	}
	client := discovery.New(endpoint.URL,
		discovery.WithHTTPClient(c.httpClient),
		discovery.WithConnectOptions(c.connectOptions...),
	)
	c.clients[endpoint.URL] = client
	return client, nil
}

// ServiceClient is a reusable handle for calls to one service.
type ServiceClient struct {
	client      *Client
	serviceName string
}

// ServiceName returns the fully-qualified service name.
func (s *ServiceClient) ServiceName() string {
	return s.serviceName
}

// Invoke performs a unary call to this service.
func (s *ServiceClient) Invoke(ctx context.Context, methodName string, request proto.Message) (proto.Message, error) {
	return s.client.Invoke(ctx, s.serviceName, methodName, request)
}

// InvokeJSON performs a protobuf JSON call to this service.
func (s *ServiceClient) InvokeJSON(ctx context.Context, methodName string, input []byte) ([]byte, error) {
	return s.client.InvokeJSON(ctx, s.serviceName, methodName, input)
}
