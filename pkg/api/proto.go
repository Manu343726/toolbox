package api

import (
	"encoding/json"
	"fmt"
	"time"

	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
)

// The conversions below map the standard model onto the framework's API
// introspection contract. Open identifiers — format, transport, parameter
// location, security type, side effect, source kind, provider role — cross the
// boundary as strings, so a user-defined value survives the round trip
// unchanged. Only the JSON value types and the server lifecycle status are
// enums, because the framework owns those vocabularies.

// SchemaTypeToProto converts a schema type to its contract enum.
func SchemaTypeToProto(schemaType SchemaType) apiv1.ApiSchemaType {
	switch schemaType {
	case TypeObject:
		return apiv1.ApiSchemaType_API_SCHEMA_TYPE_OBJECT
	case TypeArray:
		return apiv1.ApiSchemaType_API_SCHEMA_TYPE_ARRAY
	case TypeString:
		return apiv1.ApiSchemaType_API_SCHEMA_TYPE_STRING
	case TypeInteger:
		return apiv1.ApiSchemaType_API_SCHEMA_TYPE_INTEGER
	case TypeNumber:
		return apiv1.ApiSchemaType_API_SCHEMA_TYPE_NUMBER
	case TypeBoolean:
		return apiv1.ApiSchemaType_API_SCHEMA_TYPE_BOOLEAN
	case TypeNull:
		return apiv1.ApiSchemaType_API_SCHEMA_TYPE_NULL
	default:
		return apiv1.ApiSchemaType_API_SCHEMA_TYPE_UNSPECIFIED
	}
}

// SchemaTypeFromProto converts a contract schema-type enum to a schema type.
func SchemaTypeFromProto(schemaType apiv1.ApiSchemaType) SchemaType {
	switch schemaType {
	case apiv1.ApiSchemaType_API_SCHEMA_TYPE_OBJECT:
		return TypeObject
	case apiv1.ApiSchemaType_API_SCHEMA_TYPE_ARRAY:
		return TypeArray
	case apiv1.ApiSchemaType_API_SCHEMA_TYPE_STRING:
		return TypeString
	case apiv1.ApiSchemaType_API_SCHEMA_TYPE_INTEGER:
		return TypeInteger
	case apiv1.ApiSchemaType_API_SCHEMA_TYPE_NUMBER:
		return TypeNumber
	case apiv1.ApiSchemaType_API_SCHEMA_TYPE_BOOLEAN:
		return TypeBoolean
	case apiv1.ApiSchemaType_API_SCHEMA_TYPE_NULL:
		return TypeNull
	default:
		return TypeUnspecified
	}
}

// ServerStatusToProto converts a server status to its contract enum.
func ServerStatusToProto(status ServerStatus) apiv1.ApiServerStatus {
	switch status {
	case ServerStatusServing:
		return apiv1.ApiServerStatus_API_SERVER_STATUS_SERVING
	case ServerStatusNotServing:
		return apiv1.ApiServerStatus_API_SERVER_STATUS_NOT_SERVING
	case ServerStatusUnknown:
		return apiv1.ApiServerStatus_API_SERVER_STATUS_UNKNOWN
	default:
		return apiv1.ApiServerStatus_API_SERVER_STATUS_UNSPECIFIED
	}
}

// ServerStatusFromProto converts a contract status enum to a server status.
func ServerStatusFromProto(status apiv1.ApiServerStatus) ServerStatus {
	switch status {
	case apiv1.ApiServerStatus_API_SERVER_STATUS_SERVING:
		return ServerStatusServing
	case apiv1.ApiServerStatus_API_SERVER_STATUS_NOT_SERVING:
		return ServerStatusNotServing
	case apiv1.ApiServerStatus_API_SERVER_STATUS_UNKNOWN:
		return ServerStatusUnknown
	default:
		return ServerStatusUnspecified
	}
}

// ToProto converts the schema into its contract message.
func (s *Schema) ToProto() *apiv1.ApiSchema {
	if s == nil {
		return nil
	}
	message := &apiv1.ApiSchema{
		Ref:                         s.Ref,
		Type:                        SchemaTypeToProto(s.Type),
		Format:                      s.Format,
		Title:                       s.Title,
		Description:                 s.Description,
		Deprecated:                  s.Deprecated,
		ReadOnly:                    s.ReadOnly,
		Required:                    append([]string(nil), s.Required...),
		Items:                       s.Items.ToProto(),
		AdditionalProperties:        s.AdditionalProperties.ToProto(),
		AdditionalPropertiesAllowed: s.AdditionalPropertiesAllowed,
		EnumValues:                  append([]string(nil), s.Enum...),
		Minimum:                     s.Minimum,
		Maximum:                     s.Maximum,
		MinLength:                   s.MinLength,
		MaxLength:                   s.MaxLength,
		Pattern:                     s.Pattern,
	}
	for _, property := range s.Properties {
		message.Properties = append(message.Properties, &apiv1.ApiProperty{
			Name:     property.Name,
			Schema:   property.Schema.ToProto(),
			Required: property.Required,
		})
	}
	for _, alternative := range s.OneOf {
		message.OneOf = append(message.OneOf, alternative.ToProto())
	}
	for _, alternative := range s.AnyOf {
		message.AnyOf = append(message.AnyOf, alternative.ToProto())
	}
	if s.Default != nil {
		if encoded, err := json.Marshal(s.Default); err == nil {
			message.DefaultJson = encoded
		}
	}
	return message
}

// SchemaFromProto converts a contract schema message into the standard model.
func SchemaFromProto(message *apiv1.ApiSchema) (*Schema, error) {
	if message == nil {
		return nil, nil
	}
	schema := &Schema{
		Ref:                         message.GetRef(),
		Type:                        SchemaTypeFromProto(message.GetType()),
		Format:                      message.GetFormat(),
		Title:                       message.GetTitle(),
		Description:                 message.GetDescription(),
		Deprecated:                  message.GetDeprecated(),
		ReadOnly:                    message.GetReadOnly(),
		Required:                    append([]string(nil), message.GetRequired()...),
		AdditionalPropertiesAllowed: message.GetAdditionalPropertiesAllowed(),
		Enum:                        append([]string(nil), message.GetEnumValues()...),
		Pattern:                     message.GetPattern(),
	}
	items, err := SchemaFromProto(message.GetItems())
	if err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}
	schema.Items = items
	additional, err := SchemaFromProto(message.GetAdditionalProperties())
	if err != nil {
		return nil, fmt.Errorf("additionalProperties: %w", err)
	}
	schema.AdditionalProperties = additional
	for _, property := range message.GetProperties() {
		propertySchema, err := SchemaFromProto(property.GetSchema())
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", property.GetName(), err)
		}
		schema.Properties = append(schema.Properties, Property{
			Name:     property.GetName(),
			Schema:   propertySchema,
			Required: property.GetRequired(),
		})
	}
	for _, alternative := range message.GetOneOf() {
		converted, err := SchemaFromProto(alternative)
		if err != nil {
			return nil, fmt.Errorf("oneOf: %w", err)
		}
		schema.OneOf = append(schema.OneOf, converted)
	}
	for _, alternative := range message.GetAnyOf() {
		converted, err := SchemaFromProto(alternative)
		if err != nil {
			return nil, fmt.Errorf("anyOf: %w", err)
		}
		schema.AnyOf = append(schema.AnyOf, converted)
	}
	if len(message.GetDefaultJson()) > 0 {
		var value any
		if err := json.Unmarshal(message.GetDefaultJson(), &value); err != nil {
			return nil, fmt.Errorf("default: %w", err)
		}
		schema.Default = value
	}
	schema.Minimum = copyFloat(message.Minimum)
	schema.Maximum = copyFloat(message.Maximum)
	schema.MinLength = copyInt(message.MinLength)
	schema.MaxLength = copyInt(message.MaxLength)
	return schema, nil
}

func copyFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func copyInt(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// ToProto converts the operation into its contract message.
func (o Operation) ToProto() *apiv1.ApiOperation {
	message := &apiv1.ApiOperation{
		Id:           o.ID,
		ApiId:        o.APIID,
		Service:      o.Service,
		Name:         o.Name,
		Method:       o.Method,
		Path:         o.Path,
		Summary:      o.Summary,
		Description:  o.Description,
		Tags:         append([]string(nil), o.Tags...),
		Request:      o.Request.ToProto(),
		Response:     o.Response.ToProto(),
		Deprecated:   o.Deprecated,
		Streaming:    &apiv1.ApiStreaming{Client: o.Streaming.Client, Server: o.Streaming.Server},
		Capabilities: append([]string(nil), o.Capabilities...),
	}
	for _, parameter := range o.Parameters {
		message.Parameters = append(message.Parameters, &apiv1.ApiParameter{
			Name:        parameter.Name,
			In:          parameter.In,
			Description: parameter.Description,
			Required:    parameter.Required,
			Deprecated:  parameter.Deprecated,
			Schema:      parameter.Schema.ToProto(),
		})
	}
	for _, response := range o.Responses {
		message.Responses = append(message.Responses, &apiv1.ApiResponse{
			Status:      response.Status,
			Description: response.Description,
			Schema:      response.Schema.ToProto(),
		})
	}
	for _, requirement := range o.Security {
		message.Security = append(message.Security, &apiv1.ApiSecurityRequirement{
			Scheme: requirement.Scheme,
			Scopes: append([]string(nil), requirement.Scopes...),
		})
	}
	message.SideEffects = append([]string(nil), o.SideEffects...)
	return message
}

// OperationFromProto converts a contract operation message into the standard
// model.
func OperationFromProto(message *apiv1.ApiOperation) (Operation, error) {
	if message == nil {
		return Operation{}, fmt.Errorf("operation is required")
	}
	operation := Operation{
		ID:           message.GetId(),
		APIID:        message.GetApiId(),
		Service:      message.GetService(),
		Name:         message.GetName(),
		Method:       message.GetMethod(),
		Path:         message.GetPath(),
		Summary:      message.GetSummary(),
		Description:  message.GetDescription(),
		Tags:         append([]string(nil), message.GetTags()...),
		Deprecated:   message.GetDeprecated(),
		Capabilities: append([]string(nil), message.GetCapabilities()...),
		SideEffects:  append([]SideEffect(nil), message.GetSideEffects()...),
		Streaming: Streaming{
			Client: message.GetStreaming().GetClient(),
			Server: message.GetStreaming().GetServer(),
		},
	}
	request, err := SchemaFromProto(message.GetRequest())
	if err != nil {
		return Operation{}, fmt.Errorf("request: %w", err)
	}
	operation.Request = request
	response, err := SchemaFromProto(message.GetResponse())
	if err != nil {
		return Operation{}, fmt.Errorf("response: %w", err)
	}
	operation.Response = response
	for _, parameter := range message.GetParameters() {
		schema, err := SchemaFromProto(parameter.GetSchema())
		if err != nil {
			return Operation{}, fmt.Errorf("parameter %q: %w", parameter.GetName(), err)
		}
		operation.Parameters = append(operation.Parameters, Parameter{
			Name:        parameter.GetName(),
			In:          parameter.GetIn(),
			Description: parameter.GetDescription(),
			Required:    parameter.GetRequired(),
			Deprecated:  parameter.GetDeprecated(),
			Schema:      schema,
		})
	}
	for _, declared := range message.GetResponses() {
		schema, err := SchemaFromProto(declared.GetSchema())
		if err != nil {
			return Operation{}, fmt.Errorf("response %q: %w", declared.GetStatus(), err)
		}
		operation.Responses = append(operation.Responses, Response{
			Status:      declared.GetStatus(),
			Description: declared.GetDescription(),
			Schema:      schema,
		})
	}
	for _, requirement := range message.GetSecurity() {
		operation.Security = append(operation.Security, SecurityRequirement{
			Scheme: requirement.GetScheme(),
			Scopes: append([]string(nil), requirement.GetScopes()...),
		})
	}
	return operation, nil
}

// ToProto converts the service into its contract message.
func (s Service) ToProto() *apiv1.ApiService {
	message := &apiv1.ApiService{
		Id:           s.ID,
		Name:         s.Name,
		Title:        s.Title,
		Description:  s.Description,
		Capabilities: append([]string(nil), s.Capabilities...),
	}
	for _, operation := range s.Operations {
		message.Operations = append(message.Operations, operation.ToProto())
	}
	return message
}

// ServiceFromProto converts a contract service message into the standard
// model.
func ServiceFromProto(message *apiv1.ApiService) (Service, error) {
	if message == nil {
		return Service{}, fmt.Errorf("service is required")
	}
	service := Service{
		ID:           message.GetId(),
		Name:         message.GetName(),
		Title:        message.GetTitle(),
		Description:  message.GetDescription(),
		Capabilities: append([]string(nil), message.GetCapabilities()...),
	}
	for _, operation := range message.GetOperations() {
		converted, err := OperationFromProto(operation)
		if err != nil {
			return Service{}, fmt.Errorf("operation %q: %w", operation.GetName(), err)
		}
		service.Operations = append(service.Operations, converted)
	}
	return service, nil
}

// ToProto converts the API into its contract message.
func (a API) ToProto() *apiv1.Api {
	message := &apiv1.Api{
		Id:           a.ID,
		Name:         a.Name,
		Version:      a.Version,
		Title:        a.Title,
		Description:  a.Description,
		Format:       a.Format,
		ServerIds:    append([]string(nil), a.ServerIDs...),
		Capabilities: append([]string(nil), a.Capabilities...),
		Tags:         append([]string(nil), a.Tags...),
		Metadata:     copyMetadata(a.Metadata),
		Source: &apiv1.ApiSource{
			Kind:     a.Source.Kind,
			Location: a.Source.Location,
			Digest:   a.Source.Digest,
		},
	}
	for _, server := range a.DeclaredServers {
		message.DeclaredServers = append(message.DeclaredServers, &apiv1.ApiDeclaredServer{
			Url:         server.URL,
			Description: server.Description,
		})
	}
	for _, requirement := range a.Security {
		message.Security = append(message.Security, &apiv1.ApiSecurityRequirement{
			Scheme: requirement.Scheme,
			Scopes: append([]string(nil), requirement.Scopes...),
		})
	}
	for _, scheme := range a.SecuritySchemes {
		message.SecuritySchemes = append(message.SecuritySchemes, &apiv1.ApiSecurityScheme{
			Name:          scheme.Name,
			Type:          scheme.Type,
			In:            scheme.In,
			Scheme:        scheme.Scheme,
			ParameterName: scheme.ParameterName,
			Description:   scheme.Description,
			Scopes:        append([]string(nil), scheme.Scopes...),
		})
	}
	for _, service := range a.Services {
		message.Services = append(message.Services, service.ToProto())
	}
	return message
}

// APIFromProto converts a contract API message into the standard model. The
// result is validated, so a malformed description from a parser subsystem is
// rejected at the catalog boundary instead of being stored.
func APIFromProto(message *apiv1.Api) (API, error) {
	if message == nil {
		return API{}, fmt.Errorf("api is required")
	}
	target := API{
		ID:           message.GetId(),
		Name:         message.GetName(),
		Version:      message.GetVersion(),
		Title:        message.GetTitle(),
		Description:  message.GetDescription(),
		Format:       message.GetFormat(),
		ServerIDs:    append([]string(nil), message.GetServerIds()...),
		Capabilities: append([]string(nil), message.GetCapabilities()...),
		Tags:         append([]string(nil), message.GetTags()...),
		Metadata:     copyMetadata(message.GetMetadata()),
		Source: Source{
			Kind:     message.GetSource().GetKind(),
			Location: message.GetSource().GetLocation(),
			Digest:   message.GetSource().GetDigest(),
		},
	}
	for _, server := range message.GetDeclaredServers() {
		target.DeclaredServers = append(target.DeclaredServers, DeclaredServer{
			URL:         server.GetUrl(),
			Description: server.GetDescription(),
		})
	}
	for _, requirement := range message.GetSecurity() {
		target.Security = append(target.Security, SecurityRequirement{
			Scheme: requirement.GetScheme(),
			Scopes: append([]string(nil), requirement.GetScopes()...),
		})
	}
	for _, scheme := range message.GetSecuritySchemes() {
		target.SecuritySchemes = append(target.SecuritySchemes, SecurityScheme{
			Name:          scheme.GetName(),
			Type:          scheme.GetType(),
			In:            scheme.GetIn(),
			Scheme:        scheme.GetScheme(),
			ParameterName: scheme.GetParameterName(),
			Description:   scheme.GetDescription(),
			Scopes:        append([]string(nil), scheme.GetScopes()...),
		})
	}
	for _, service := range message.GetServices() {
		converted, err := ServiceFromProto(service)
		if err != nil {
			return API{}, fmt.Errorf("service %q: %w", service.GetName(), err)
		}
		target.Services = append(target.Services, converted)
	}
	return target.Normalize()
}

// ToProto converts the server into its contract message.
func (s Server) ToProto() *apiv1.ApiServer {
	message := &apiv1.ApiServer{
		Id:           s.ID,
		Name:         s.Name,
		BaseUrl:      s.BaseURL,
		Format:       s.Format,
		Transport:    s.Transport,
		Description:  s.Description,
		Capabilities: append([]string(nil), s.Capabilities...),
		Status:       ServerStatusToProto(s.Status),
		Metadata:     copyMetadata(s.Metadata),
	}
	if !s.RegisteredAt.IsZero() {
		message.RegisteredAtUnixNano = s.RegisteredAt.UnixNano()
	}
	return message
}

// ServerFromProto converts a contract server message into the standard model.
func ServerFromProto(message *apiv1.ApiServer) (Server, error) {
	if message == nil {
		return Server{}, fmt.Errorf("server is required")
	}
	server := Server{
		ID:           message.GetId(),
		Name:         message.GetName(),
		BaseURL:      message.GetBaseUrl(),
		Format:       message.GetFormat(),
		Transport:    message.GetTransport(),
		Description:  message.GetDescription(),
		Capabilities: append([]string(nil), message.GetCapabilities()...),
		Status:       ServerStatusFromProto(message.GetStatus()),
		Metadata:     copyMetadata(message.GetMetadata()),
	}
	if nanos := message.GetRegisteredAtUnixNano(); nanos != 0 {
		server.RegisteredAt = time.Unix(0, nanos).UTC()
	}
	if err := server.Validate(); err != nil {
		return Server{}, err
	}
	return server, nil
}

// ToProto converts the summary into its contract message.
func (s Summary) ToProto() *apiv1.ApiSummary {
	return &apiv1.ApiSummary{
		Id:         s.ID,
		Name:       s.Name,
		Version:    s.Version,
		Title:      s.Title,
		Format:     s.Format,
		ServerIds:  append([]string(nil), s.ServerIDs...),
		Services:   int32(s.Services),
		Operations: int32(s.Operations),
	}
}

// ToProto converts the format descriptor into its contract message.
func (d FormatDescriptor) ToProto() *apiv1.ApiFormatDescriptor {
	return &apiv1.ApiFormatDescriptor{
		Id:                   d.ID,
		Name:                 d.Name,
		Version:              d.Version,
		Description:          d.Description,
		MediaTypes:           append([]string(nil), d.MediaTypes...),
		FileExtensions:       append([]string(nil), d.FileExtensions...),
		SpecificationVersion: d.SpecificationVersion,
		Provider:             d.Provider,
		Metadata:             copyMetadata(d.Metadata),
	}
}

// FormatDescriptorFromProto converts a contract format descriptor into the
// standard model.
func FormatDescriptorFromProto(message *apiv1.ApiFormatDescriptor) (FormatDescriptor, error) {
	if message == nil {
		return FormatDescriptor{}, fmt.Errorf("format descriptor is required")
	}
	descriptor := FormatDescriptor{
		ID:                   message.GetId(),
		Name:                 message.GetName(),
		Version:              message.GetVersion(),
		Description:          message.GetDescription(),
		MediaTypes:           append([]string(nil), message.GetMediaTypes()...),
		FileExtensions:       append([]string(nil), message.GetFileExtensions()...),
		SpecificationVersion: message.GetSpecificationVersion(),
		Provider:             message.GetProvider(),
		Metadata:             copyMetadata(message.GetMetadata()),
	}
	if err := descriptor.Validate(); err != nil {
		return FormatDescriptor{}, err
	}
	return descriptor, nil
}

// ToProto converts the transport descriptor into its contract message.
func (d TransportDescriptor) ToProto() *apiv1.ApiTransportDescriptor {
	return &apiv1.ApiTransportDescriptor{
		Id:                d.ID,
		Name:              d.Name,
		Version:           d.Version,
		Description:       d.Description,
		Schemes:           append([]string(nil), d.Schemes...),
		SupportsStreaming: d.SupportsStreaming,
		Provider:          d.Provider,
		Metadata:          copyMetadata(d.Metadata),
	}
}

// TransportDescriptorFromProto converts a contract transport descriptor into the
// standard model.
func TransportDescriptorFromProto(message *apiv1.ApiTransportDescriptor) (TransportDescriptor, error) {
	if message == nil {
		return TransportDescriptor{}, fmt.Errorf("transport descriptor is required")
	}
	descriptor := TransportDescriptor{
		ID:                message.GetId(),
		Name:              message.GetName(),
		Version:           message.GetVersion(),
		Description:       message.GetDescription(),
		Schemes:           append([]string(nil), message.GetSchemes()...),
		SupportsStreaming: message.GetSupportsStreaming(),
		Provider:          message.GetProvider(),
		Metadata:          copyMetadata(message.GetMetadata()),
	}
	if err := descriptor.Validate(); err != nil {
		return TransportDescriptor{}, err
	}
	return descriptor, nil
}

// ToProto converts the provider into its contract message.
func (p Provider) ToProto() *apiv1.ApiProviderInfo {
	return &apiv1.ApiProviderInfo{
		Id:                    p.ID,
		Subsystem:             p.Subsystem,
		Role:                  p.Role,
		Formats:               append([]string(nil), p.Formats...),
		Transports:            append([]string(nil), p.Transports...),
		Targets:               append([]string(nil), p.Targets...),
		Endpoint:              p.Endpoint,
		ServiceNames:          append([]string(nil), p.ServiceNames...),
		Capabilities:          append([]string(nil), p.Capabilities...),
		Status:                ServerStatusToProto(p.Status),
		ImplementationVersion: p.ImplementationVersion,
	}
}
