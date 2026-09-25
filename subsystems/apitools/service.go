package apitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	apitoolsv1 "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1"
)

// Service exposes a Memory catalog through the ApiToolsService contract. The
// service owns the repository and the exposure decisions; parsing and
// invocation are delegated to provider subsystems it discovers through the
// configured ProviderDirectory.
type Service struct {
	store     *Memory
	directory ProviderDirectory
}

// NewService creates the ConnectRPC handler for a catalog.
func NewService(store *Memory, directory ProviderDirectory) *Service {
	if store == nil {
		store = NewMemory(StoreOptions{})
	}
	return &Service{store: store, directory: directory}
}

// Store returns the underlying catalog for embedding and tests.
func (s *Service) Store() *Memory { return s.store }

// Directory returns the configured provider directory, or nil when none is set.
func (s *Service) Directory() ProviderDirectory { return s.directory }

// RegisterServer stores a server registration.
func (s *Service) RegisterServer(_ context.Context, req *connect.Request[apitoolsv1.RegisterServerRequest]) (*connect.Response[apitoolsv1.RegisterServerResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetServer() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server is required"))
	}
	server, err := api.ServerFromProto(req.Msg.GetServer())
	if err != nil {
		return nil, invalidError(err)
	}
	stored, replaced, err := s.store.RegisterServer(server, req.Msg.GetFailIfExists())
	if err != nil {
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.RegisterServerResponse{
		Server:   stored.ToProto(),
		Replaced: replaced,
		Revision: s.store.Revision(),
	}), nil
}

// GetServer returns one registered server and the APIs it hosts.
func (s *Service) GetServer(_ context.Context, req *connect.Request[apitoolsv1.GetServerRequest]) (*connect.Response[apitoolsv1.GetServerResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	server, err := s.store.GetServer(req.Msg.GetId())
	if err != nil {
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.GetServerResponse{
		Server: server.ToProto(),
		ApiIds: s.store.APIIDsForServer(server.ID),
	}), nil
}

// ListServers returns registered servers matching the filters.
func (s *Service) ListServers(_ context.Context, req *connect.Request[apitoolsv1.ListServersRequest]) (*connect.Response[apitoolsv1.ListServersResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("list request is required"))
	}
	servers := s.store.ListServers(ServerFilter{
		Query:  req.Msg.GetQuery(),
		Status: api.ServerStatusFromProto(req.Msg.GetStatus()),
		APIID:  req.Msg.GetApiId(),
		Format: req.Msg.GetFormat(),
	})
	response := &apitoolsv1.ListServersResponse{}
	for _, server := range servers {
		response.Servers = append(response.Servers, server.ToProto())
	}
	return connect.NewResponse(response), nil
}

// DeleteServer removes a server, unbinding its APIs when forced.
func (s *Service) DeleteServer(_ context.Context, req *connect.Request[apitoolsv1.DeleteServerRequest]) (*connect.Response[apitoolsv1.DeleteServerResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	removed, unbound, err := s.store.DeleteServer(req.Msg.GetId(), req.Msg.GetForce())
	if err != nil {
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.DeleteServerResponse{
		Removed:       removed,
		UnboundApiIds: unbound,
	}), nil
}

// RegisterApiFormat indexes a description format descriptor.
func (s *Service) RegisterApiFormat(_ context.Context, req *connect.Request[apitoolsv1.RegisterApiFormatRequest]) (*connect.Response[apitoolsv1.RegisterApiFormatResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetFormat() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("format descriptor is required"))
	}
	descriptor, err := api.FormatDescriptorFromProto(req.Msg.GetFormat())
	if err != nil {
		return nil, invalidError(err)
	}
	stored, replaced, err := s.store.RegisterFormat(descriptor, req.Msg.GetFailIfExists())
	if err != nil {
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.RegisterApiFormatResponse{
		Format:   stored.ToProto(),
		Replaced: replaced,
	}), nil
}

// ListApiFormats returns the format index: indexed descriptors merged with the
// formats advertised by the available parser providers, so a format nobody
// described is still discoverable when a provider can handle it.
func (s *Service) ListApiFormats(ctx context.Context, req *connect.Request[apitoolsv1.ListApiFormatsRequest]) (*connect.Response[apitoolsv1.ListApiFormatsResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("list request is required"))
	}
	parsers, err := s.providersForRole(ctx, api.ProviderParser)
	if err != nil {
		return nil, err
	}
	usage := s.store.FormatUsage()
	index := make(map[string]*apitoolsv1.ApiFormatEntry)
	for _, descriptor := range s.store.Formats() {
		index[descriptor.ID] = &apitoolsv1.ApiFormatEntry{
			Format:    descriptor.ToProto(),
			Id:        descriptor.ID,
			Described: true,
		}
	}
	for _, provider := range parsers {
		for _, format := range provider.Formats {
			entry, ok := index[format]
			if !ok {
				entry = &apitoolsv1.ApiFormatEntry{Id: format}
				index[format] = entry
			}
			entry.ParserAvailable = true
			entry.ParserIds = append(entry.ParserIds, provider.ID)
		}
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	response := &apitoolsv1.ListApiFormatsResponse{}
	for _, id := range ids {
		entry := index[id]
		entry.ParserIds = sortedCopy(entry.ParserIds)
		entry.RegisteredApis = int32(usage[id])
		if req.Msg.GetOnlyAvailable() && !entry.ParserAvailable {
			continue
		}
		response.Formats = append(response.Formats, entry)
	}
	return connect.NewResponse(response), nil
}

// RegisterApiTransport indexes a transport descriptor.
func (s *Service) RegisterApiTransport(_ context.Context, req *connect.Request[apitoolsv1.RegisterApiTransportRequest]) (*connect.Response[apitoolsv1.RegisterApiTransportResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetTransport() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("transport descriptor is required"))
	}
	descriptor, err := api.TransportDescriptorFromProto(req.Msg.GetTransport())
	if err != nil {
		return nil, invalidError(err)
	}
	stored, replaced, err := s.store.RegisterTransport(descriptor, req.Msg.GetFailIfExists())
	if err != nil {
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.RegisterApiTransportResponse{
		Transport: stored.ToProto(),
		Replaced:  replaced,
	}), nil
}

// ListApiTransports returns the transport index merged with the transports
// advertised by the available adapter providers.
func (s *Service) ListApiTransports(ctx context.Context, req *connect.Request[apitoolsv1.ListApiTransportsRequest]) (*connect.Response[apitoolsv1.ListApiTransportsResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("list request is required"))
	}
	adapters, err := s.providersForRole(ctx, api.ProviderAdapter)
	if err != nil {
		return nil, err
	}
	usage := s.store.TransportUsage()
	index := make(map[string]*apitoolsv1.ApiTransportEntry)
	for _, descriptor := range s.store.Transports() {
		index[descriptor.ID] = &apitoolsv1.ApiTransportEntry{
			Transport: descriptor.ToProto(),
			Id:        descriptor.ID,
			Described: true,
		}
	}
	for _, provider := range adapters {
		for _, transport := range provider.Transports {
			entry, ok := index[transport]
			if !ok {
				entry = &apitoolsv1.ApiTransportEntry{Id: transport}
				index[transport] = entry
			}
			entry.AdapterAvailable = true
			entry.AdapterIds = append(entry.AdapterIds, provider.ID)
		}
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	response := &apitoolsv1.ListApiTransportsResponse{}
	for _, id := range ids {
		entry := index[id]
		entry.AdapterIds = sortedCopy(entry.AdapterIds)
		entry.RegisteredServers = int32(usage[id])
		if req.Msg.GetOnlyAvailable() && !entry.AdapterAvailable {
			continue
		}
		response.Transports = append(response.Transports, entry)
	}
	return connect.NewResponse(response), nil
}

// ListProviders lists the extension points available in the deployment.
func (s *Service) ListProviders(ctx context.Context, req *connect.Request[apitoolsv1.ListProvidersRequest]) (*connect.Response[apitoolsv1.ListProvidersResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("list request is required"))
	}
	providers, err := s.allProviders(ctx)
	if err != nil {
		return nil, err
	}
	role := strings.TrimSpace(req.Msg.GetRole())
	format := strings.TrimSpace(req.Msg.GetFormat())
	transport := strings.TrimSpace(req.Msg.GetTransport())
	response := &apitoolsv1.ListProvidersResponse{DirectoryConfigured: s.directory != nil}
	for _, provider := range providers {
		if role != "" && provider.Role != role {
			continue
		}
		if format != "" && !provider.HandlesFormat(format) {
			continue
		}
		if transport != "" && !provider.HandlesTransport(transport) {
			continue
		}
		response.Providers = append(response.Providers, provider.ToProto())
	}
	return connect.NewResponse(response), nil
}

// RenderApi routes a description to an adapter provider that renders it into a
// target representation. The adapter sees only the standard description, so which
// format the description was parsed from is not its concern: this is how a gRPC
// contract becomes an OpenAPI document, and an OpenAPI document becomes a
// protobuf contract.
func (s *Service) RenderApi(ctx context.Context, req *connect.Request[apitoolsv1.RenderApiRequest]) (*connect.Response[apitoolsv1.RenderApiResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("render request is required"))
	}
	target := strings.TrimSpace(req.Msg.GetTarget())
	if target == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("target is required"))
	}
	source := req.Msg.GetApi()
	if source == nil {
		if strings.TrimSpace(req.Msg.GetApiId()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("api or api_id is required"))
		}
		stored, err := s.store.GetAPI(req.Msg.GetApiId())
		if err != nil {
			return nil, catalogError(err)
		}
		source = stored.ToProto()
	}
	described, err := api.APIFromProto(source)
	if err != nil {
		return nil, invalidError(err)
	}
	if s.directory == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no provider directory is configured"))
	}
	provider, err := s.selectRenderer(ctx, req.Msg.GetAdapterId(), target)
	if err != nil {
		return nil, err
	}
	client, err := s.directory.Adapter(ctx, provider.ID)
	if err != nil {
		return nil, catalogError(err)
	}
	response, err := client.RenderApi(ctx, connect.NewRequest(&apiv1.RenderApiRequest{
		Api:       described.ToProto(),
		Target:    target,
		MediaType: req.Msg.GetMediaType(),
		Options:   req.Msg.GetOptions(),
	}))
	if err != nil {
		return nil, providerError(err)
	}
	result := &apitoolsv1.RenderApiResponse{
		// The target's schema comes back as a file set, so a target that is
		// described by more than one file is passed through whole rather than
		// flattened into a single document.
		Files:         append([]*apiv1.ApiSchemaFile(nil), response.Msg.GetFiles()...),
		MediaType:     response.Msg.GetMediaType(),
		FileExtension: response.Msg.GetFileExtension(),
		Warnings:      append([]string(nil), response.Msg.GetWarnings()...),
		AdapterId:     provider.ID,
	}
	for _, descriptor := range response.Msg.GetTargets() {
		converted, err := api.FormatDescriptorFromProto(descriptor)
		if err != nil {
			return nil, invalidError(err)
		}
		// An adapter contributes the target descriptors it recognizes, so the
		// index learns what this deployment can produce.
		if _, _, err := s.store.RegisterFormat(converted, false); err != nil {
			return nil, catalogError(err)
		}
		result.Targets = append(result.Targets, converted.ToProto())
	}
	return connect.NewResponse(result), nil
}

// ParseApi routes a description to a parser provider and returns the description it
// produced, without storing it.
//
// The description may be a document the caller holds or a live endpoint that
// describes itself. A server that already serves its own contract is the common
// case, and a catalog that could only read documents would force every caller to
// obtain bytes the server had already published.
func (s *Service) ParseApi(ctx context.Context, req *connect.Request[apitoolsv1.ParseApiRequest]) (*connect.Response[apitoolsv1.ParseApiResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("parse request is required"))
	}
	document, err := descriptionBytes(req.Msg.GetDocument(), req.Msg.GetDocumentText(), req.Msg.GetBaseUrl())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	parsed, providerID, warnings, descriptors, err := s.parse(ctx, document, req.Msg.GetFormat(), req.Msg.GetFormatHint(), req.Msg.GetParserId(), req.Msg.GetSource(), req.Msg.GetBaseUrl(), req.Msg.GetApiId(), req.Msg.GetServerId())
	if err != nil {
		return nil, err
	}
	response := &apitoolsv1.ParseApiResponse{
		Api:      parsed.ToProto(),
		Warnings: warnings,
		ParserId: providerID,
	}
	for _, descriptor := range descriptors {
		response.Formats = append(response.Formats, descriptor.ToProto())
	}
	return connect.NewResponse(response), nil
}

// RegisterApi routes a description to a parser provider and stores the API it
// produced. The description may be a document the caller holds or a live endpoint
// that describes itself.
func (s *Service) RegisterApi(ctx context.Context, req *connect.Request[apitoolsv1.RegisterApiRequest]) (*connect.Response[apitoolsv1.RegisterApiResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("register request is required"))
	}
	document, err := descriptionBytes(req.Msg.GetDocument(), req.Msg.GetDocumentText(), req.Msg.GetBaseUrl())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	parsed, providerID, _, descriptors, err := s.parse(
		ctx, document, req.Msg.GetFormat(), req.Msg.GetFormatHint(), req.Msg.GetParserId(),
		nil, req.Msg.GetBaseUrl(), req.Msg.GetApiId(), req.Msg.GetServerId(),
	)
	if err != nil {
		return nil, err
	}
	stored, replaced, err := s.store.RegisterAPI(parsed, req.Msg.GetServerId(), req.Msg.GetFailIfExists())
	if err != nil {
		return nil, catalogError(err)
	}
	for _, descriptor := range descriptors {
		// A parser contributes the format descriptor it implements, so the
		// index learns about formats without a separate registration step.
		if _, _, indexErr := s.store.RegisterFormat(descriptor, false); indexErr != nil {
			return nil, catalogError(indexErr)
		}
	}
	if req.Msg.GetExposeAll() {
		for _, operation := range stored.Operations() {
			if _, err := s.store.SetExposed(stored.ID, operation.ID, true); err != nil {
				return nil, catalogError(err)
			}
		}
	}
	return connect.NewResponse(&apitoolsv1.RegisterApiResponse{
		Api:      stored.ToProto(),
		Replaced: replaced,
		Summary:  stored.Summarize().ToProto(),
		ParserId: providerID,
		Revision: s.store.Revision(),
	}), nil
}

// RegisterApiDescriptor stores an API description that was produced elsewhere.
func (s *Service) RegisterApiDescriptor(_ context.Context, req *connect.Request[apitoolsv1.RegisterApiDescriptorRequest]) (*connect.Response[apitoolsv1.RegisterApiDescriptorResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetApi() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("api descriptor is required"))
	}
	parsed, err := api.APIFromProto(req.Msg.GetApi())
	if err != nil {
		return nil, invalidError(err)
	}
	stored, replaced, err := s.store.RegisterAPI(parsed, "", req.Msg.GetFailIfExists())
	if err != nil {
		return nil, catalogError(err)
	}
	if req.Msg.GetExposeAll() {
		for _, operation := range stored.Operations() {
			if _, err := s.store.SetExposed(stored.ID, operation.ID, true); err != nil {
				return nil, catalogError(err)
			}
		}
	}
	return connect.NewResponse(&apitoolsv1.RegisterApiDescriptorResponse{
		Api:      stored.ToProto(),
		Replaced: replaced,
		Revision: s.store.Revision(),
	}), nil
}

// GetApi returns one registered API.
func (s *Service) GetApi(_ context.Context, req *connect.Request[apitoolsv1.GetApiRequest]) (*connect.Response[apitoolsv1.GetApiResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	stored, err := s.store.GetAPI(req.Msg.GetId())
	if err != nil {
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.GetApiResponse{
		Api:       stored.ToProto(),
		ServerIds: stored.ServerIDs,
	}), nil
}

// ListApis returns registered APIs matching the filters.
func (s *Service) ListApis(_ context.Context, req *connect.Request[apitoolsv1.ListApisRequest]) (*connect.Response[apitoolsv1.ListApisResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("list request is required"))
	}
	apis := s.store.ListAPIs(APIFilter{
		Query:       req.Msg.GetQuery(),
		Format:      req.Msg.GetFormat(),
		ServerID:    req.Msg.GetServerId(),
		SideEffects: req.Msg.GetSideEffects(),
	})
	response := &apitoolsv1.ListApisResponse{}
	for _, stored := range apis {
		response.Apis = append(response.Apis, stored.Summarize().ToProto())
	}
	return connect.NewResponse(response), nil
}

// DeleteApi removes a registered API.
func (s *Service) DeleteApi(_ context.Context, req *connect.Request[apitoolsv1.DeleteApiRequest]) (*connect.Response[apitoolsv1.DeleteApiResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	removed, err := s.store.DeleteAPI(req.Msg.GetId())
	if err != nil {
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.DeleteApiResponse{
		Removed:  removed,
		Revision: s.store.Revision(),
	}), nil
}

// DescribeOperation returns one operation's description and exposure state.
func (s *Service) DescribeOperation(_ context.Context, req *connect.Request[apitoolsv1.DescribeOperationRequest]) (*connect.Response[apitoolsv1.DescribeOperationResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetApiId()) == "" || strings.TrimSpace(req.Msg.GetOperationId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("api_id and operation_id are required"))
	}
	operation, allowed, err := s.store.Operation(req.Msg.GetApiId(), req.Msg.GetOperationId())
	if err != nil {
		return nil, catalogError(err)
	}
	stored, err := s.store.GetAPI(req.Msg.GetApiId())
	if err != nil {
		return nil, catalogError(err)
	}
	exposed := s.store.Exposed(operation.ID)
	return connect.NewResponse(&apitoolsv1.DescribeOperationResponse{
		Operation: operation.ToProto(),
		Allowed:   allowed,
		Exposed:   exposed,
		Invokable: !operation.Streaming.Streaming() && len(stored.ServerIDs) > 0,
	}), nil
}

// ExposeOperation exposes one operation.
func (s *Service) ExposeOperation(_ context.Context, req *connect.Request[apitoolsv1.ExposeOperationRequest]) (*connect.Response[apitoolsv1.ExposeOperationResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetApiId()) == "" || strings.TrimSpace(req.Msg.GetOperationId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("api_id and operation_id are required"))
	}
	changed, err := s.store.SetExposed(req.Msg.GetApiId(), req.Msg.GetOperationId(), true)
	if err != nil {
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.ExposeOperationResponse{
		OperationId: req.Msg.GetOperationId(),
		Exposed:     true,
		Changed:     changed,
	}), nil
}

// HideOperation hides one operation.
func (s *Service) HideOperation(_ context.Context, req *connect.Request[apitoolsv1.HideOperationRequest]) (*connect.Response[apitoolsv1.HideOperationResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetApiId()) == "" || strings.TrimSpace(req.Msg.GetOperationId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("api_id and operation_id are required"))
	}
	changed, err := s.store.SetExposed(req.Msg.GetApiId(), req.Msg.GetOperationId(), false)
	if err != nil {
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.HideOperationResponse{
		OperationId: req.Msg.GetOperationId(),
		Exposed:     false,
		Changed:     changed,
	}), nil
}

// OperationExposure reports the exposure footprint.
func (s *Service) OperationExposure(ctx context.Context, req *connect.Request[apitoolsv1.OperationExposureRequest]) (*connect.Response[apitoolsv1.OperationExposureResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exposure request is required"))
	}
	footprint := s.store.Exposure(ExposureFilter{
		APIID:   req.Msg.GetApiId(),
		Service: req.Msg.GetService(),
	})
	allowed, exposed, hidden, denied := 0, 0, 0, 0
	for _, entry := range footprint {
		if !entry.Allowed {
			denied++
			continue
		}
		allowed++
		if entry.Exposed {
			exposed++
		} else {
			hidden++
		}
	}
	invokersAvailable := false
	if s.directory != nil {
		invokers, err := s.providersForRole(ctx, api.ProviderInvoker)
		if err != nil {
			return nil, err
		}
		invokersAvailable = len(invokers) > 0
	}
	return connect.NewResponse(&apitoolsv1.OperationExposureResponse{
		Allowed:           int32(allowed),
		Exposed:           int32(exposed),
		Hidden:            int32(hidden),
		Denied:            int32(denied),
		InvokersAvailable: invokersAvailable,
	}), nil
}

// CallOperation routes one exposed operation to an adapter provider.
func (s *Service) CallOperation(ctx context.Context, req *connect.Request[apitoolsv1.CallOperationRequest]) (*connect.Response[apitoolsv1.CallOperationResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetApiId()) == "" || strings.TrimSpace(req.Msg.GetOperationId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("api_id and operation_id are required"))
	}
	arguments, err := callArguments(req.Msg.GetArgumentsJson(), req.Msg.GetArgumentsText())
	if err != nil {
		return nil, invalidError(err)
	}
	operation, allowed, err := s.store.Operation(req.Msg.GetApiId(), req.Msg.GetOperationId())
	if err != nil {
		return nil, catalogError(err)
	}
	if !allowed {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("operation %q is not authorized by the operation policy", operation.ID))
	}
	if !s.store.Exposed(operation.ID) {
		return nil, connect.NewError(
			connect.CodeFailedPrecondition,
			fmt.Errorf("operation %q is not exposed", operation.ID),
		)
	}
	stored, err := s.store.GetAPI(req.Msg.GetApiId())
	if err != nil {
		return nil, catalogError(err)
	}
	if len(stored.ServerIDs) == 0 {
		return nil, connect.NewError(
			connect.CodeFailedPrecondition,
			fmt.Errorf("api %q is not bound to a server", stored.ID),
		)
	}
	if s.directory == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no provider directory is configured"))
	}
	server, err := s.store.GetServer(stored.ServerIDs[0])
	if err != nil {
		return nil, catalogError(err)
	}
	provider, err := s.selectProvider(ctx, req.Msg.GetInvokerId(), api.ProviderInvoker, "adapter_id", server.Transport)
	if err != nil {
		return nil, err
	}
	client, err := s.directory.Invoker(ctx, provider.ID)
	if err != nil {
		return nil, catalogError(err)
	}
	request := &apiv1.InvokeApiRequest{
		ServerId:      server.ID,
		ApiId:         stored.ID,
		OperationId:   operation.ID,
		ArgumentsJson: arguments,
		RequestId:     req.Msg.GetRequestId(),
		TraceId:       req.Msg.GetTraceId(),
		Server:        server.ToProto(),
		Api:           stored.ToProto(),
		Operation:     operation.ToProto(),
	}
	header := connect.NewRequest(request)
	// Correlation identity travels as transport metadata, not as feature
	// fields, so a provider sees the same request identity the catalog received.
	if req != nil && req.Header() != nil {
		for _, name := range []string{"X-Toolbox-Request-Id", "X-Toolbox-Trace-Id", "X-Toolbox-Run-Id", "X-Toolbox-Policy-Id"} {
			if value := req.Header().Get(name); value != "" {
				header.Header().Set(name, value)
			}
		}
	}
	response, err := client.InvokeApi(ctx, header)
	if err != nil {
		return nil, providerError(err)
	}
	result := &apitoolsv1.CallOperationResponse{
		Status:      response.Msg.GetStatus(),
		ContentType: response.Msg.GetContentType(),
		Headers:     response.Msg.GetHeaders(),
		BodyJson:    response.Msg.GetBodyJson(),
		AdapterId:   provider.ID,
	}
	return connect.NewResponse(result), nil
}

func (s *Service) parse(
	ctx context.Context,
	document []byte,
	format string,
	formatHint string,
	parserID string,
	source *apiv1.ApiSource,
	baseURL string,
	apiID string,
	serverID string,
) (api.API, string, []string, []api.FormatDescriptor, error) {
	format = strings.TrimSpace(format)
	if format == "" {
		format = strings.TrimSpace(formatHint)
	}
	if format == "" {
		return api.API{}, "", nil, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("format or format_hint is required"))
	}
	if s.directory == nil {
		return api.API{}, "", nil, nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no provider directory is configured"))
	}
	provider, err := s.selectParser(ctx, parserID, format)
	if err != nil {
		return api.API{}, "", nil, nil, err
	}
	client, err := s.directory.Parser(ctx, provider.ID)
	if err != nil {
		return api.API{}, "", nil, nil, catalogError(err)
	}
	if len(document) == 0 && strings.TrimSpace(baseURL) == "" {
		return api.API{}, "", nil, nil, connect.NewError(
			connect.CodeInvalidArgument, fmt.Errorf("a description document or a base url is required"),
		)
	}
	request := &apiv1.ParseApiRequest{
		Document:   document,
		Format:     format,
		FormatHint: formatHint,
		Source:     source,
		BaseUrl:    baseURL,
		ApiId:      apiID,
		ServerId:   serverID,
	}
	response, err := client.ParseApi(ctx, connect.NewRequest(request))
	if err != nil {
		return api.API{}, "", nil, nil, providerError(err)
	}
	parsed, err := api.APIFromProto(response.Msg.GetApi())
	if err != nil {
		return api.API{}, "", nil, nil, invalidError(err)
	}
	descriptors := make([]api.FormatDescriptor, 0, len(response.Msg.GetFormats()))
	for _, descriptor := range response.Msg.GetFormats() {
		converted, err := api.FormatDescriptorFromProto(descriptor)
		if err != nil {
			return api.API{}, "", nil, nil, invalidError(err)
		}
		descriptors = append(descriptors, converted)
	}
	return parsed, provider.ID, append([]string(nil), response.Msg.GetWarnings()...), descriptors, nil
}

// selectProvider finds the one provider for a role that handles a claim, or the
// explicitly selected one. Several providers claiming the same thing is a real
// ambiguity rather than a reason to pick one, so the caller is told to choose.
func (s *Service) selectProvider(
	ctx context.Context,
	selectedID, role, selectionField, claim string,
) (api.Provider, error) {
	if s.directory == nil {
		return api.Provider{}, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no provider directory is configured"))
	}
	selectedID = strings.TrimSpace(selectedID)
	if selectedID != "" {
		return s.providerByID(ctx, selectedID, role)
	}
	candidates, err := s.providersForRole(ctx, role)
	if err != nil {
		return api.Provider{}, err
	}
	matching := make([]api.Provider, 0, len(candidates))
	for _, provider := range candidates {
		if handlesClaim(provider, role, claim) {
			matching = append(matching, provider)
		}
	}
	switch len(matching) {
	case 0:
		return api.Provider{}, connect.NewError(
			connect.CodeFailedPrecondition,
			fmt.Errorf("%w: no %s handles %q", ErrNoProvider, role, claim),
		)
	case 1:
		return matching[0], nil
	default:
		ids := make([]string, 0, len(matching))
		for _, provider := range matching {
			ids = append(ids, provider.ID)
		}
		return api.Provider{}, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf(
				"%w: several %ss handle %q (%s); select one with %s",
				ErrAmbiguous, role, claim, strings.Join(ids, ", "), selectionField,
			),
		)
	}
}

// handlesClaim reports whether a provider claims one identifier in the way its
// role implies: a format it parses, a target it renders into, or a transport it
// invokes over.
func handlesClaim(provider api.Provider, role, claim string) bool {
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return false
	}
	switch role {
	case api.ProviderParser:
		return provider.HandlesFormat(claim)
	case api.ProviderAdapter:
		return provider.HandlesTarget(claim)
	case api.ProviderInvoker:
		return provider.HandlesTransport(claim)
	default:
		return false
	}
}

func (s *Service) selectParser(ctx context.Context, parserID, format string) (api.Provider, error) {
	return s.selectProvider(ctx, parserID, api.ProviderParser, "parser_id", format)
}

func (s *Service) selectRenderer(ctx context.Context, adapterID, target string) (api.Provider, error) {
	return s.selectProvider(ctx, adapterID, api.ProviderAdapter, "adapter_id", target)
}

func (s *Service) providerByID(ctx context.Context, id, role string) (api.Provider, error) {
	providers, err := s.allProviders(ctx)
	if err != nil {
		return api.Provider{}, err
	}
	for _, provider := range providers {
		if provider.ID != strings.TrimSpace(id) {
			continue
		}
		if provider.Role != role {
			return api.Provider{}, connect.NewError(
				connect.CodeInvalidArgument,
				fmt.Errorf("provider %q is a %s, not a %s", provider.ID, provider.Role, role),
			)
		}
		return provider, nil
	}
	return api.Provider{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: %s provider %q", ErrNoProvider, role, id))
}

func (s *Service) allProviders(ctx context.Context) ([]api.Provider, error) {
	if s.directory == nil {
		return nil, nil
	}
	providers, err := s.directory.Providers(ctx)
	if err != nil {
		return nil, providerError(err)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return providers, nil
}

func (s *Service) providersForRole(ctx context.Context, role string) ([]api.Provider, error) {
	providers, err := s.allProviders(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]api.Provider, 0, len(providers))
	for _, provider := range providers {
		if provider.Role == role {
			result = append(result, provider)
		}
	}
	return result, nil
}

func sortedCopy(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return copied
}

func invalidError(err error) error {
	return connect.NewError(connect.CodeInvalidArgument, err)
}

// catalogError maps a catalog error to a canonical ConnectRPC status code.
func catalogError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrAlreadyExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, ErrNotAllowed):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, ErrNoProvider):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, ErrAmbiguous):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, api.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// providerError wraps a provider failure. A provider that is unavailable is a
// precondition failure of this call, not an internal failure of the catalog,
// and an error the provider itself rejected is reported with its own code.
func providerError(err error) error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		switch connectErr.Code() {
		case connect.CodeUnavailable:
			return connect.NewError(connect.CodeUnavailable, err)
		case connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodePermissionDenied, connect.CodeFailedPrecondition:
			return connect.NewError(connectErr.Code(), err)
		default:
			return connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewError(connect.CodeInternal, err)
}

// descriptionBytes resolves the description a request carries: a document as bytes
// or as text, or a live endpoint that describes itself. It refuses a request that
// supplies more than one form, and one that supplies none.
func descriptionBytes(binary []byte, text, baseURL string) ([]byte, error) {
	hasBytes := len(binary) > 0
	text = strings.TrimSpace(text)
	hasText := text != ""
	hasEndpoint := strings.TrimSpace(baseURL) != ""
	forms := 0
	for _, present := range []bool{hasBytes, hasText, hasEndpoint} {
		if present {
			forms++
		}
	}
	switch {
	case forms > 1:
		return nil, fmt.Errorf("supply a document or a base_url, not both")
	case hasBytes:
		return binary, nil
	case hasText:
		return []byte(text), nil
	case hasEndpoint:
		// A live endpoint is described through the parser that handles its
		// contract, which reads the contract the server itself publishes.
		return nil, nil
	default:
		return nil, fmt.Errorf("a description document or a base url is required")
	}
}

// callArguments resolves the caller values a request carries, whether they
// arrived as bytes or as text, and refuses a request that supplies both.
func callArguments(binary []byte, text string) (json.RawMessage, error) {
	hasBytes := len(binary) > 0
	hasText := strings.TrimSpace(text) != ""
	switch {
	case hasBytes && hasText:
		return nil, fmt.Errorf("supply either arguments_json or arguments_text, not both")
	case hasBytes:
		return ArgumentsObject(binary)
	case hasText:
		return ArgumentsObject(json.RawMessage(text))
	default:
		return ArgumentsObject(nil)
	}
}

// ArgumentsObject is a helper for callers that build operation arguments: it
// rejects a non-object argument document instead of forwarding it to a provider.
func ArgumentsObject(arguments json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(arguments))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`), nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &probe); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
	}
	return arguments, nil
}
