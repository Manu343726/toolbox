// Package discovery provides reflection-based service discovery and dynamic
// ConnectRPC invocation for services whose protobuf schemas are not linked into
// the caller at compile time.
package discovery

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/Manu343726/toolbox/pkg/docs"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// MethodSchema describes one reflected RPC method.
type MethodSchema struct {
	Name            string
	Input           protoreflect.MessageDescriptor
	Output          protoreflect.MessageDescriptor
	ClientStreaming bool
	ServerStreaming bool
}

// ServiceSchema is the complete reflection result for one service.
type ServiceSchema struct {
	Name               string
	Descriptor         protoreflect.ServiceDescriptor
	Methods            []MethodSchema
	RawFileDescriptors []*descriptorpb.FileDescriptorProto
	Documentation      *docs.Service
}

// Option configures a discovery Client.
type Option func(*Client)

// WithHTTPClient supplies the HTTP client used for ConnectRPC calls and
// reflection. It is primarily useful for tests and custom transports.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithReflectionClient supplies a separate HTTP/2-capable client for gRPC
// reflection. Connect calls continue to use WithHTTPClient's client.
func WithReflectionClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.reflectionClient = client
		}
	}
}

// WithConnectOptions adds options to dynamically created Connect clients.
func WithConnectOptions(options ...connect.ClientOption) Option {
	return func(c *Client) {
		c.connectOptions = append(c.connectOptions, options...)
	}
}

// Client discovers and invokes services at one ConnectRPC endpoint.
type Client struct {
	endpoint         string
	httpClient       *http.Client
	reflectionClient *http.Client
	connectOptions   []connect.ClientOption

	mu      sync.RWMutex
	schemas map[string]*ServiceSchema
}

// New creates a discovery client for a base URL such as
// http://127.0.0.1:9000.
func New(endpoint string, options ...Option) *Client {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	httpClient := &http.Client{Timeout: 15 * time.Second}
	reflectionClient := defaultReflectionClient(endpoint)
	client := &Client{
		endpoint:         endpoint,
		httpClient:       httpClient,
		reflectionClient: reflectionClient,
		schemas:          make(map[string]*ServiceSchema),
	}
	for _, option := range options {
		option(client)
	}
	return client
}

// Endpoint returns the address the client was given, trimmed of surrounding space and of a
// trailing slash.
//
// It is not otherwise rewritten: a scheme is not added, because the address came from whoever
// resolved it and inventing one here would be a second place for that decision to be wrong.
func (c *Client) Endpoint() string {
	return c.endpoint
}

// ClearCache removes cached reflection descriptors.
func (c *Client) ClearCache() {
	c.mu.Lock()
	clear(c.schemas)
	c.mu.Unlock()
}

// ListServices lists all services exposed through gRPC reflection, including
// reflection and platform services.
func (c *Client) ListServices(ctx context.Context) ([]string, error) {
	stream := grpcreflect.NewClient(c.reflectionClient, c.endpoint).NewStream(ctx)
	defer func() { _, _ = stream.Close() }()
	names, err := stream.ListServices()
	if err != nil {
		return nil, fmt.Errorf("list services at %s: %w", c.endpoint, err)
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, string(name))
	}
	sort.Strings(result)
	return result, nil
}

// Discover lists and describes every service at the endpoint.
func (c *Client) Discover(ctx context.Context) ([]*ServiceSchema, error) {
	names, err := c.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*ServiceSchema, 0, len(names))
	for _, name := range names {
		schema, describeErr := c.DescribeService(ctx, name)
		if describeErr != nil {
			return nil, fmt.Errorf("describe %s: %w", name, describeErr)
		}
		result = append(result, schema)
	}
	return result, nil
}

// DescribeService retrieves and caches a service descriptor and documentation.
func (c *Client) DescribeService(ctx context.Context, name string) (*ServiceSchema, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("service name is required")
	}
	c.mu.RLock()
	cached := c.schemas[name]
	c.mu.RUnlock()
	if cached != nil {
		return cloneSchema(cached), nil
	}

	stream := grpcreflect.NewClient(c.reflectionClient, c.endpoint).NewStream(ctx)
	defer func() { _, _ = stream.Close() }()
	files, err := stream.FileContainingSymbol(protoreflect.FullName(name))
	if err != nil {
		return nil, fmt.Errorf("find descriptor for %s: %w", name, err)
	}
	descriptor, err := serviceDescriptor(files, name)
	if err != nil {
		return nil, err
	}
	documentation := docs.ExtractServiceDocumentation(descriptor)
	if catalogDocumentation, catalogErr := docs.DefaultCatalog().Get(name); catalogErr == nil {
		documentation = &catalogDocumentation
	}
	schema := &ServiceSchema{
		Name:               name,
		Descriptor:         descriptor,
		Methods:            make([]MethodSchema, 0, descriptor.Methods().Len()),
		RawFileDescriptors: files,
		Documentation:      documentation,
	}
	for i := 0; i < descriptor.Methods().Len(); i++ {
		method := descriptor.Methods().Get(i)
		schema.Methods = append(schema.Methods, MethodSchema{
			Name:            string(method.Name()),
			Input:           method.Input(),
			Output:          method.Output(),
			ClientStreaming: method.IsStreamingClient(),
			ServerStreaming: method.IsStreamingServer(),
		})
	}
	c.mu.Lock()
	c.schemas[name] = schema
	c.mu.Unlock()
	return cloneSchema(schema), nil
}

// Invoke performs a unary RPC using a typed or dynamic protobuf request. A nil
// request creates an empty message of the reflected input type.
func (c *Client) Invoke(ctx context.Context, serviceName, methodName string, request proto.Message) (proto.Message, error) {
	return c.InvokeWithHeaders(ctx, serviceName, methodName, request, nil)
}

// InvokeWithHeaders is Invoke with additional protocol metadata headers.
func (c *Client) InvokeWithHeaders(ctx context.Context, serviceName, methodName string, request proto.Message, headers http.Header) (proto.Message, error) {
	schema, err := c.DescribeService(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	method := findMethod(schema, methodName)
	if method == nil {
		return nil, fmt.Errorf("method %s.%s not found", serviceName, methodName)
	}
	if method.ClientStreaming || method.ServerStreaming {
		return nil, fmt.Errorf("method %s.%s is streaming; streaming invocation is not implemented", serviceName, methodName)
	}

	dynamicRequest := dynamicpb.NewMessage(method.Input)
	if request != nil {
		if got, want := request.ProtoReflect().Descriptor().FullName(), method.Input.FullName(); got != want {
			return nil, fmt.Errorf("request type %s does not match %s", got, want)
		}
		encoded, err := proto.Marshal(request)
		if err != nil {
			return nil, fmt.Errorf("encode request %s: %w", method.Input.FullName(), err)
		}
		if err := proto.Unmarshal(encoded, dynamicRequest); err != nil {
			return nil, fmt.Errorf("decode request %s: %w", method.Input.FullName(), err)
		}
	}
	clientOptions := append([]connect.ClientOption(nil), c.connectOptions...)
	clientOptions = append(clientOptions, connect.WithResponseInitializer(func(_ connect.Spec, value any) error {
		target, ok := value.(*dynamicpb.Message)
		if !ok {
			return fmt.Errorf("unexpected dynamic response type %T", value)
		}
		*target = *dynamicpb.NewMessage(method.Output)
		return nil
	}))
	client := connect.NewClient[dynamicpb.Message, dynamicpb.Message](
		c.httpClient,
		c.endpoint+"/"+serviceName+"/"+methodName,
		clientOptions...,
	)
	requestMessage := connect.NewRequest(dynamicRequest)
	for key, values := range headers {
		for _, value := range values {
			requestMessage.Header().Add(key, value)
		}
	}
	response, err := client.CallUnary(ctx, requestMessage)
	if err != nil {
		return nil, fmt.Errorf("invoke %s.%s: %w", serviceName, methodName, err)
	}
	return response.Msg, nil
}

// InvokeJSON is a convenience wrapper for callers that do not have generated
// request types. It uses protobuf JSON semantics for input and output.
func (c *Client) InvokeJSON(ctx context.Context, serviceName, methodName string, input []byte) ([]byte, error) {
	return c.InvokeJSONWithHeaders(ctx, serviceName, methodName, input, nil)
}

// InvokeJSONWithHeaders is InvokeJSON with additional protocol metadata headers.
func (c *Client) InvokeJSONWithHeaders(ctx context.Context, serviceName, methodName string, input []byte, headers http.Header) ([]byte, error) {
	schema, err := c.DescribeService(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	method := findMethod(schema, methodName)
	if method == nil {
		return nil, fmt.Errorf("method %s.%s not found", serviceName, methodName)
	}
	request := dynamicpb.NewMessage(method.Input)
	if len(input) > 0 {
		if err := (protojson.UnmarshalOptions{}).Unmarshal(input, request); err != nil {
			return nil, fmt.Errorf("decode request JSON: %w", err)
		}
	}
	response, err := c.InvokeWithHeaders(ctx, serviceName, methodName, request, headers)
	if err != nil {
		return nil, err
	}
	return (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(response)
}

func findMethod(schema *ServiceSchema, name string) *MethodSchema {
	for i := range schema.Methods {
		if schema.Methods[i].Name == name {
			return &schema.Methods[i]
		}
	}
	return nil
}

func serviceDescriptor(files []*descriptorpb.FileDescriptorProto, name string) (protoreflect.ServiceDescriptor, error) {
	fileSet, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: files})
	if err != nil {
		return nil, fmt.Errorf("build descriptors for %s: %w", name, err)
	}
	descriptor, err := fileSet.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		return nil, fmt.Errorf("find service %s: %w", name, err)
	}
	service, ok := descriptor.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("symbol %s is not a service", name)
	}
	return service, nil
}

func cloneSchema(schema *ServiceSchema) *ServiceSchema {
	if schema == nil {
		return nil
	}
	clone := *schema
	clone.Methods = append([]MethodSchema(nil), schema.Methods...)
	clone.RawFileDescriptors = append([]*descriptorpb.FileDescriptorProto(nil), schema.RawFileDescriptors...)
	if schema.Documentation != nil {
		documentation := *schema.Documentation
		clone.Documentation = &documentation
	}
	return &clone
}

func defaultReflectionClient(endpoint string) *http.Client {
	parsed, err := url.Parse(endpoint)
	if err == nil && parsed.Scheme == "http" {
		return &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http2.Transport{
				AllowHTTP: true,
				DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
					var dialer net.Dialer
					return dialer.DialContext(ctx, network, address)
				},
			},
		}
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// IsReflectionService reports whether a service name belongs to gRPC
// reflection and should normally be hidden from generated user commands.
func IsReflectionService(name string) bool {
	return name == "grpc.reflection.v1.ServerReflection" || name == "grpc.reflection.v1alpha.ServerReflection"
}
