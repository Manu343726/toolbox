// Package protocontract turns a protobuf service contract into the framework's
// standard API description, and calls the services that contract describes.
//
// It is a plain Go package: it depends on the standard model, on protobuf
// descriptors, and on the reflection client, and on no transport. A deployment
// uses it directly to describe its own subsystems in process; the gRPC provider
// subsystem wraps it when a parser has to be addressable over ConnectRPC.
//
// Two ways in, because a contract can be held or can be served:
//
//   - FromDescriptorSet reads a FileDescriptorSet, for a contract that exists as
//     a file or as bytes a build produced.
//   - FromEndpoint reads a live endpoint's own reflection, for a contract the
//     server already knows and nobody has to ship twice.
package protocontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/discovery"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Format is the format identifier a protobuf service contract is described as.
// It is the identifier a parser claims, not a type the framework defines.
const Format api.Format = "grpc"

// SourceKindDescriptorSet names the origin of a description read from bytes.
const SourceKindDescriptorSet = "descriptor_set"

// SourceKindReflection names the origin of a description read from a live
// endpoint that described itself.
const SourceKindReflection = "reflection"

// Descriptor reads protobuf service contracts into the standard model.
type Descriptor struct {
	// APIID overrides the identifier derived from the contract. Empty derives one
	// from the first service's name.
	APIID string
	// ExcludeServices drops services by full protobuf name, such as the
	// reflection services a server always exposes.
	ExcludeServices []string
	// HTTPClient performs reflection reads. The zero value uses a bounded client.
	HTTPClient *http.Client
	// Clients supplies pre-built reflection clients, keyed by endpoint, for a
	// deployment that shares one client per endpoint.
	Clients map[string]*discovery.Client
}

// NewDescriptor creates a contract reader.
func NewDescriptor(options Descriptor) *Descriptor {
	reader := &Descriptor{
		APIID:           strings.TrimSpace(options.APIID),
		ExcludeServices: append([]string(nil), options.ExcludeServices...),
		HTTPClient:      options.HTTPClient,
		Clients:         make(map[string]*discovery.Client, len(options.Clients)),
	}
	for endpoint, client := range options.Clients {
		reader.Clients[endpoint] = client
	}
	return reader
}

// Formats returns the format identifiers this reader claims.
func (d *Descriptor) Formats() []api.Format { return []api.Format{Format} }

// Format returns the format identifier this reader claims.
func (d *Descriptor) Format() api.Format { return Format }

// FormatDescriptor describes the format for a catalog's index.
func FormatDescriptor() api.FormatDescriptor {
	return api.FormatDescriptor{
		ID:                   string(Format),
		Name:                 "gRPC",
		Description:          "Protobuf service contracts, from a FileDescriptorSet or from a live endpoint's own reflection.",
		SpecificationVersion: "3",
		FileExtensions:       []string{"proto", "protoset"},
		MediaTypes:           []string{"application/proto", "application/x-protobuf"},
	}
}

// TransportDescriptors describes the transports this package can call over.
func TransportDescriptors() []api.TransportDescriptor {
	return []api.TransportDescriptor{
		{ID: "connectrpc", Name: "ConnectRPC", Description: "The Connect protocol, over HTTP/1.1, HTTP/2, or h2c."},
		{ID: "grpc", Name: "gRPC", Description: "The gRPC protocol, over HTTP/2."},
		{ID: "grpc-web", Name: "gRPC-Web", Description: "The gRPC-Web protocol, for browsers and proxies that do not speak HTTP/2."},
	}
}

// Describe reads a contract from a document, or from a live endpoint that describes
// itself, honouring the identifier and source the caller supplied.
//
// It is the entry point a catalog or a host uses, because it is the shape the
// framework's describe request already has: a document, a format, a base URL, an
// identifier, and where the description came from. The reader's own options supply
// the defaults a request leaves empty, and the result is the framework's describe
// result, so a composition does not translate between shapes.
func (d *Descriptor) Describe(ctx context.Context, request api.DescribeRequest) (api.DescribeResult, error) {
	if len(request.Document) == 0 && strings.TrimSpace(request.BaseURL) == "" {
		return api.DescribeResult{}, api.Errorf(api.KindInvalid, "a document or a base url is required")
	}
	result := api.DescribeResult{
		API:        api.API{},
		Formats:    []api.FormatDescriptor{d.FormatDescriptor()},
		ProviderID: "",
	}
	if len(request.Document) == 0 {
		described, warnings, err := d.FromEndpoint(ctx, request.BaseURL)
		if err != nil {
			return result, err
		}
		result.Warnings = warnings
		described, err = d.rename(described, request)
		if err != nil {
			return result, err
		}
		result.API = described
		return result, nil
	}
	described, warnings, err := d.FromDescriptorSet(request.Document)
	if err != nil {
		return result, err
	}
	result.Warnings = warnings
	described, err = d.rewrite(described, request)
	if err != nil {
		return result, err
	}
	result.API = described
	return result, nil
}

// rename applies a requested identifier to a described API.
func (d *Descriptor) rename(described api.API, request api.DescribeRequest) (api.API, error) {
	identifier := strings.TrimSpace(request.APIID)
	if identifier == "" {
		identifier = strings.TrimSpace(d.APIID)
	}
	if identifier == "" || identifier == described.ID {
		return described, nil
	}
	described.ID = identifier
	described.Name = identifier
	return described.Normalize()
}

// rewrite applies the caller's source to a described API, then its identifier. A
// caller that knows where a document came from says so, and the description
// records it.
func (d *Descriptor) rewrite(described api.API, request api.DescribeRequest) (api.API, error) {
	if kind := strings.TrimSpace(request.Source.Kind); kind != "" {
		described.Source.Kind = kind
	}
	if location := strings.TrimSpace(request.Source.Location); location != "" {
		described.Source.Location = location
	}
	if described.Source.Kind == "" {
		described.Source.Kind = SourceKindDescriptorSet
	}
	return d.rename(described, request)
}

// FormatDescriptor describes the format this reader claims, for a catalog's index.
func (d *Descriptor) FormatDescriptor() api.FormatDescriptor {
	descriptor := FormatDescriptor()
	if d != nil {
		if format := d.Format(); format != "" {
			descriptor.ID = string(format)
		}
	}
	return descriptor
}

// FromDescriptorSet reads a contract from a FileDescriptorSet.
//
// The returned API records the document's digest, so a consumer can tell that a
// registered description changed under it. Warnings describe anything in the
// contract this reader deliberately left out.
func (d *Descriptor) FromDescriptorSet(document []byte) (api.API, []string, error) {
	if len(document) == 0 {
		return api.API{}, nil, api.Errorf(api.KindInvalid, "a FileDescriptorSet is required")
	}
	digest := sha256.Sum256(document)
	fileSet := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(document, fileSet); err != nil {
		return api.API{}, nil, api.WrapError(api.KindInvalid, err, "decode FileDescriptorSet")
	}
	files, err := protodesc.NewFiles(fileSet)
	if err != nil {
		return api.API{}, nil, api.WrapError(api.KindInvalid, err, "build descriptors")
	}
	services := d.servicesOf(func(yield func(protoreflect.ServiceDescriptor) bool) {
		files.RangeFiles(func(file protoreflect.FileDescriptor) bool {
			declared := file.Services()
			for i := 0; i < declared.Len(); i++ {
				if !yield(declared.Get(i)) {
					return false
				}
			}
			return true
		})
	})
	described, err := d.build(services, fileSet, api.Source{
		Kind:     SourceKindDescriptorSet,
		Digest:   hex.EncodeToString(digest[:]),
		Location: SourceKindDescriptorSet,
	})
	if err != nil {
		return api.API{}, nil, err
	}
	return described, nil, nil
}

// FromEndpoint reads the contract a live endpoint serves, from its own
// reflection.
//
// A service that cannot be described becomes a warning rather than a failure, so
// one broken service does not make an endpoint undescribable; an endpoint with no
// describable service at all is an error, because there is nothing to register.
func (d *Descriptor) FromEndpoint(ctx context.Context, endpoint string) (api.API, []string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return api.API{}, nil, api.Errorf(api.KindInvalid, "an endpoint is required")
	}
	client := d.clientFor(endpoint)
	names, err := client.ListServices(ctx)
	if err != nil {
		return api.API{}, nil, api.WrapError(api.KindUnavailable, err, "list services of %q", endpoint)
	}
	services := make([]protoreflect.ServiceDescriptor, 0, len(names))
	warnings := make([]string, 0)
	for _, name := range names {
		if d.excluded(name) {
			continue
		}
		schema, describeErr := client.DescribeService(ctx, name)
		if describeErr != nil {
			warnings = append(warnings, "service "+name+" could not be described: "+describeErr.Error())
			continue
		}
		if schema.Descriptor != nil {
			services = append(services, schema.Descriptor)
		}
	}
	if len(services) == 0 {
		return api.API{}, warnings, api.Errorf(api.KindInvalid, "endpoint %q exposes no describable service", endpoint)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].FullName() < services[j].FullName() })
	described, err := d.build(services, nil, api.Source{Kind: SourceKindReflection, Location: endpoint})
	if err != nil {
		return api.API{}, warnings, err
	}
	described.DeclaredServers = []api.DeclaredServer{{URL: endpoint}}
	normalized, err := described.Normalize()
	if err != nil {
		return api.API{}, warnings, api.WrapError(api.KindInvalid, err, "normalize the described API")
	}
	return normalized, warnings, nil
}

// servicesOf collects the services a descriptor set declares, in a deterministic
// order, dropping the ones the caller excluded.
func (d *Descriptor) servicesOf(each func(func(protoreflect.ServiceDescriptor) bool)) []protoreflect.ServiceDescriptor {
	services := make([]protoreflect.ServiceDescriptor, 0)
	each(func(service protoreflect.ServiceDescriptor) bool {
		if d.excluded(string(service.FullName())) {
			return true
		}
		services = append(services, service)
		return true
	})
	sort.Slice(services, func(i, j int) bool { return services[i].FullName() < services[j].FullName() })
	return services
}

func (d *Descriptor) excluded(name string) bool {
	if strings.Contains(name, "grpc.reflection") || discovery.IsReflectionService(name) {
		return true
	}
	for _, candidate := range d.ExcludeServices {
		if strings.TrimSpace(candidate) == name {
			return true
		}
	}
	return false
}

func (d *Descriptor) build(
	services []protoreflect.ServiceDescriptor,
	fileSet *descriptorpb.FileDescriptorSet,
	source api.Source,
) (api.API, error) {
	if len(services) == 0 {
		return api.API{}, api.Errorf(api.KindInvalid, "the contract declares no service")
	}
	identifier := strings.TrimSpace(d.APIID)
	if identifier == "" {
		identifier = Slug(services[0].FullName())
	}
	target := api.API{
		ID:     identifier,
		Name:   identifier,
		Format: Format,
		Source: source,
		Title:  string(services[0].FullName()),
	}
	if target.Source.Kind == "" {
		target.Source.Kind = SourceKindDescriptorSet
	}
	for _, service := range services {
		built, err := buildService(service, fileSet)
		if err != nil {
			return api.API{}, err
		}
		target.Services = append(target.Services, built)
	}
	normalized, err := target.Normalize()
	if err != nil {
		return api.API{}, api.WrapError(api.KindInvalid, err, "normalize the described API")
	}
	return normalized, nil
}

func (d *Descriptor) clientFor(endpoint string) *discovery.Client {
	if existing, ok := d.Clients[endpoint]; ok {
		return existing
	}
	client := discovery.New(endpoint, DiscoveryOptions(d.HTTPClient)...)
	d.Clients[endpoint] = client
	return client
}

// DiscoveryOptions returns the discovery options for an HTTP client.
//
// Only the call transport is supplied. The reflection client is left to the
// discovery package, which builds one that can speak the protocol reflection needs
// — h2c, in this framework's case — instead of borrowing a client configured for
// ordinary calls and discovering the difference as a failed read.
func DiscoveryOptions(client *http.Client) []discovery.Option {
	if client == nil {
		return nil
	}
	return []discovery.Option{discovery.WithHTTPClient(client)}
}

// BoundedClient returns an HTTP client with a bounded timeout, for a caller that
// has none.
func BoundedClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

func buildService(service protoreflect.ServiceDescriptor, fileSet *descriptorpb.FileDescriptorSet) (api.Service, error) {
	documentation := shareddocs.ExtractServiceDocumentation(service)
	built := api.Service{
		Name:        string(service.FullName()),
		Title:       string(service.Name()),
		Description: documentation.Description,
	}
	methods := service.Methods()
	names := make([]string, 0, methods.Len())
	for i := 0; i < methods.Len(); i++ {
		names = append(names, string(methods.Get(i).Name()))
	}
	sort.Strings(names)
	byName := make(map[string]protoreflect.MethodDescriptor, methods.Len())
	for i := 0; i < methods.Len(); i++ {
		byName[string(methods.Get(i).Name())] = methods.Get(i)
	}
	for _, name := range names {
		operation, err := buildOperation(service, byName[name], documentation)
		if err != nil {
			return api.Service{}, err
		}
		built.Operations = append(built.Operations, operation)
	}
	return built, nil
}

func buildOperation(
	service protoreflect.ServiceDescriptor,
	method protoreflect.MethodDescriptor,
	documentation *shareddocs.Service,
) (api.Operation, error) {
	summary := ""
	if documentation != nil {
		for i := range documentation.Methods {
			if documentation.Methods[i].Name == string(method.Name()) {
				summary = documentation.Methods[i].Description
				break
			}
		}
	}
	// A protobuf contract carries no capabilities, side effects, or security
	// declarations. Leaving them empty is deliberate: a policy then refuses the
	// operation until a deployment declares what it authorizes, rather than
	// treating a method as safe because it is a function. A deployment that
	// describes its own subsystems attaches the capabilities it declared when it
	// registers the description.
	operation := api.Operation{
		Name:     string(method.Name()),
		Method:   string(method.Name()),
		Summary:  summary,
		Request:  api.SchemaForMessage(method.Input()),
		Response: api.SchemaForMessage(method.Output()),
		Streaming: api.Streaming{
			Client: method.IsStreamingClient(),
			Server: method.IsStreamingServer(),
		},
	}
	return operation, nil
}

// Slug derives an API identifier from a protobuf service name.
func Slug(name protoreflect.FullName) string {
	parts := strings.Split(string(name), ".")
	if len(parts) == 0 {
		return "api"
	}
	return slug(parts[len(parts)-1])
}

// slug turns a service name into an identifier a human can read. Word boundaries
// come from the name's own case, because protobuf names are camel-cased by
// convention: EchoService reads as echo-service, not as echoservice.
func slug(value string) string {
	var builder strings.Builder
	previousDash := false
	var previous rune
	for index, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			previousDash = false
		case r >= 'A' && r <= 'Z':
			// A capital that follows a lower-case letter or a digit starts a word.
			if index > 0 && !previousDash && (previous >= 'a' && previous <= 'z' || previous >= '0' && previous <= '9') {
				builder.WriteRune('-')
			}
			builder.WriteRune(r + ('a' - 'A'))
			previousDash = false
		default:
			if !previousDash && builder.Len() > 0 {
				builder.WriteRune('-')
				previousDash = true
			}
		}
		previous = r
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "api"
	}
	return strings.TrimSuffix(result, "-service")
}
