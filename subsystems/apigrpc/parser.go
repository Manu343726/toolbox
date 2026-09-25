package apigrpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// ParserOptions configures the parser.
type ParserOptions struct {
	// Formats optionally narrows the format identifiers this parser claims.
	Formats []api.Format
	// ExcludeServices drops services matching these names, such as the reflection
	// services, from a parsed contract.
	ExcludeServices []string
}

// Parser implements the framework's parser contract for protobuf service
// contracts. It reads a FileDescriptorSet, or a live endpoint's reflection when
// the caller supplies a base URL instead of a document.
type Parser struct {
	formats  map[string]bool
	excluded map[string]bool
}

// NewParser creates the gRPC contract parser.
func NewParser(options ParserOptions) *Parser {
	claimed := options.Formats
	if len(claimed) == 0 {
		claimed = []api.Format{FormatGRPC}
	}
	parser := &Parser{formats: make(map[string]bool, len(claimed)), excluded: make(map[string]bool)}
	for _, format := range claimed {
		parser.formats[strings.TrimSpace(format)] = true
	}
	for _, name := range options.ExcludeServices {
		parser.excluded[strings.TrimSpace(name)] = true
	}
	return parser
}

// Formats returns the format identifiers this parser claims.
func (p *Parser) Formats() []api.Format {
	result := make([]api.Format, 0, len(p.formats))
	for format := range p.formats {
		result = append(result, format)
	}
	return result
}

// ParseApi implements the framework's ApiParserService.
func (p *Parser) ParseApi(ctx context.Context, request *connect.Request[apiv1.ParseApiRequest]) (*connect.Response[apiv1.ParseApiResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	format := strings.TrimSpace(request.Msg.GetFormat())
	if format == "" {
		format = strings.TrimSpace(request.Msg.GetFormatHint())
	}
	if format == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("format is required"))
	}
	if !p.formats[format] {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("this parser handles %s, not %q", strings.Join(p.Formats(), ", "), format),
		)
	}
	var (
		parsed   api.API
		warnings []string
		err      error
	)
	switch {
	case len(request.Msg.GetDocument()) > 0:
		parsed, warnings, err = p.parseDescriptorSet(request.Msg)
	case strings.TrimSpace(request.Msg.GetBaseUrl()) != "":
		// A live endpoint describes itself: the contract comes from the server's
		// own reflection, so the catalog never has to be handed a descriptor.
		parsed, warnings, err = p.parseReflection(ctx, request.Msg)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("document or base_url is required"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&apiv1.ParseApiResponse{
		Api:      parsed.ToProto(),
		Warnings: warnings,
		Formats:  []*apiv1.ApiFormatDescriptor{FormatDescriptor().ToProto()},
	}), nil
}

func (p *Parser) parseDescriptorSet(request *apiv1.ParseApiRequest) (api.API, []string, error) {
	document := request.GetDocument()
	digest := sha256.Sum256(document)
	fileSet := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(document, fileSet); err != nil {
		return api.API{}, nil, fmt.Errorf("decode FileDescriptorSet: %w", err)
	}
	files, err := protodesc.NewFiles(fileSet)
	if err != nil {
		return api.API{}, nil, fmt.Errorf("build descriptors: %w", err)
	}
	warnings := make([]string, 0)
	services := make([]protoreflect.ServiceDescriptor, 0)
	files.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		declared := file.Services()
		for i := 0; i < declared.Len(); i++ {
			service := declared.Get(i)
			if p.excluded[string(service.FullName())] {
				continue
			}
			services = append(services, service)
		}
		return true
	})
	sort.Slice(services, func(i, j int) bool {
		return services[i].FullName() < services[j].FullName()
	})
	built, err := p.build(request, services, fileSet, api.Source{
		Kind:     sourceKind(request.GetSource().GetKind()),
		Location: request.GetSource().GetLocation(),
		Digest:   hex.EncodeToString(digest[:]),
	})
	if err != nil {
		return api.API{}, nil, err
	}
	return built, warnings, nil
}

func (p *Parser) parseReflection(ctx context.Context, request *apiv1.ParseApiRequest) (api.API, []string, error) {
	client := newDiscoveryClient(request.GetBaseUrl(), nil)
	names, err := client.ListServices(ctx)
	if err != nil {
		return api.API{}, nil, fmt.Errorf("list services via reflection: %w", err)
	}
	services := make([]protoreflect.ServiceDescriptor, 0, len(names))
	warnings := make([]string, 0)
	for _, name := range names {
		if p.excluded[name] || isReflectionService(name) {
			continue
		}
		schema, err := client.DescribeService(ctx, name)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("service %q could not be described: %v", name, err))
			continue
		}
		if schema.Descriptor != nil {
			services = append(services, schema.Descriptor)
		}
	}
	if len(services) == 0 {
		return api.API{}, nil, fmt.Errorf("endpoint %q exposes no describable services", request.GetBaseUrl())
	}
	built, err := p.build(request, services, nil, api.Source{
		Kind:     "reflection",
		Location: request.GetBaseUrl(),
	})
	if err != nil {
		return api.API{}, nil, err
	}
	return built, warnings, nil
}

func (p *Parser) build(
	request *apiv1.ParseApiRequest,
	services []protoreflect.ServiceDescriptor,
	fileSet *descriptorpb.FileDescriptorSet,
	source api.Source,
) (api.API, error) {
	if len(services) == 0 {
		return api.API{}, fmt.Errorf("contract declares no services")
	}
	identifier := strings.TrimSpace(request.GetApiId())
	if identifier == "" {
		identifier = apiSlug(services[0].FullName())
	}
	target := api.API{
		ID:     identifier,
		Name:   identifier,
		Format: FormatGRPC,
		Source: source,
		Title:  string(services[0].FullName()),
	}
	if target.Source.Kind == "" {
		target.Source.Kind = "descriptor_set"
	}
	if base := strings.TrimSpace(request.GetBaseUrl()); base != "" {
		target.DeclaredServers = []api.DeclaredServer{{URL: base}}
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
		return api.API{}, err
	}
	return normalized, nil
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
		method := methods.Get(i)
		byName[string(method.Name())] = method
	}
	for _, name := range names {
		operation, err := buildOperation(service, byName[name], documentation, fileSet)
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
	_ *descriptorpb.FileDescriptorSet,
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
	operation := api.Operation{
		Name:     string(method.Name()),
		Method:   string(method.Name()),
		Summary:  summary,
		Response: api.SchemaForMessage(method.Output()),
		Request:  api.SchemaForMessage(method.Input()),
		Streaming: api.Streaming{
			Client: method.IsStreamingClient(),
			Server: method.IsStreamingServer(),
		},
	}
	// A gRPC contract carries no capabilities, side effects, or security
	// declarations. Leaving them empty is deliberate: the catalog's policy then
	// refuses the operation until a deployment declares what it authorizes,
	// rather than assuming a method is safe because it is a function.
	//
	// Validation happens once the whole API is assembled, because an operation's
	// identifiers are derived from the API and service it belongs to.
	return operation, nil
}

func isReflectionService(name string) bool {
	return strings.Contains(name, "grpc.reflection")
}

func sourceKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "descriptor_set"
	}
	return strings.TrimSpace(kind)
}

func apiSlug(name protoreflect.FullName) string {
	parts := strings.Split(string(name), ".")
	if len(parts) == 0 {
		return "api"
	}
	return apiSlugFromString(parts[len(parts)-1])
}

func apiSlugFromString(value string) string {
	var builder strings.Builder
	previousDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			previousDash = false
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r + ('a' - 'A'))
			previousDash = false
		default:
			if !previousDash && builder.Len() > 0 {
				builder.WriteRune('-')
				previousDash = true
			}
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "api"
	}
	return strings.TrimSuffix(slug, "-service")
}
