// Package documentation implements the standalone documentation subservice.
// It adapts the shared, protobuf-derived docs model to this subservice's
// independently-owned protocol.
package docs

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	documentationv1 "github.com/Manu343726/toolbox/subsystems/documentation/documentationv1"
	"github.com/Manu343726/toolbox/subsystems/documentation/documentationv1/documentationv1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "documentation"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the documentation subsystem.
type Options struct {
	// Catalog contains extracted documentation. A new catalog is used when nil.
	Catalog *shareddocs.Catalog
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Background is optional subsystem work.
	Background func(context.Context) error
}

// Handler is the ConnectRPC implementation of DocumentationService.
type Handler struct {
	catalog *shareddocs.Catalog
}

// NewHandler creates a documentation handler. It is useful when embedding the
// documentation service in another independently-launched process.
func NewHandler(catalog *shareddocs.Catalog) *Handler {
	if catalog == nil {
		catalog = shareddocs.DefaultCatalog()
	}
	return &Handler{catalog: catalog}
}

// New is the programmatic in-process entrypoint for the documentation service.
func New(options Options) (*subsystem.Server, error) {
	catalog := options.Catalog
	if catalog == nil {
		catalog = shareddocs.DefaultCatalog()
	}
	path, handler := documentationv1connect.NewDocumentationServiceHandler(NewHandler(catalog))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       Version,
		Description:   "Documentation derived from protobuf descriptors.",
		ListenAddress: options.ListenAddress,
		Documentation: catalog,
		Background:    options.Background,
		Services: []subsystem.Service{{
			Name:         documentationv1connect.DocumentationServiceName,
			Path:         path,
			Handler:      handler,
			Capabilities: []string{"documentation.read"},
		}},
	})
}

// GetDocumentation returns documentation for one service.
func (h *Handler) GetDocumentation(_ context.Context, req *connect.Request[documentationv1.GetDocumentationRequest]) (*connect.Response[documentationv1.GetDocumentationResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetServiceName()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("service_name is required"))
	}
	doc, err := h.catalog.Get(req.Msg.GetServiceName())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&documentationv1.GetDocumentationResponse{Documentation: toProto(doc)}), nil
}

// ListDocumentation returns all matching documentation.
func (h *Handler) ListDocumentation(_ context.Context, req *connect.Request[documentationv1.ListDocumentationRequest]) (*connect.Response[documentationv1.ListDocumentationResponse], error) {
	docs := h.catalog.List()
	if req != nil && req.Msg != nil && req.Msg.GetServiceName() != "" {
		doc, err := h.catalog.Get(req.Msg.GetServiceName())
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		docs = []shareddocs.Service{doc}
	}
	result := make([]*documentationv1.ServiceDocumentation, 0, len(docs))
	for _, doc := range docs {
		result = append(result, toProto(doc))
	}
	return connect.NewResponse(&documentationv1.ListDocumentationResponse{Services: result}), nil
}

func toProto(doc shareddocs.Service) *documentationv1.ServiceDocumentation {
	result := &documentationv1.ServiceDocumentation{
		Name:        doc.Name,
		Description: doc.Description,
		Methods:     make([]*documentationv1.MethodDocumentation, 0, len(doc.Methods)),
	}
	for _, method := range doc.Methods {
		result.Methods = append(result.Methods, &documentationv1.MethodDocumentation{
			Name:            method.Name,
			Description:     method.Description,
			InputType:       method.InputType,
			OutputType:      method.OutputType,
			ClientStreaming: method.ClientStreaming,
			ServerStreaming: method.ServerStreaming,
			Parameters:      parametersToProto(method.Parameters),
		})
	}
	return result
}

func parametersToProto(parameters []shareddocs.Parameter) []*documentationv1.ParameterDocumentation {
	result := make([]*documentationv1.ParameterDocumentation, 0, len(parameters))
	for _, parameter := range parameters {
		result = append(result, &documentationv1.ParameterDocumentation{
			Name:         parameter.Name,
			Description:  parameter.Description,
			TypeName:     parameter.TypeName,
			Repeated:     parameter.Repeated,
			Required:     parameter.Required,
			DefaultValue: parameter.DefaultValue,
			Fields:       parametersToProto(parameter.Fields),
		})
	}
	return result
}
