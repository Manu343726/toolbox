package apiopenapi

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	"github.com/Manu343726/toolbox/pkg/openapi"
)

// This file is the RPC layer of the parser contract: it validates a request,
// converts it to a plain call, and converts the result back. The reading of a
// document is [openapi.Parse].

// ParserOptions configures the parser.
type ParserOptions struct {
	// Formats optionally narrows the format identifiers this parser claims. The
	// zero value claims the single format this subsystem owns.
	Formats []api.Format
}

// Parser serves the framework's parser contract for OpenAPI documents.
//
// It rejects a document written in a format it does not own, so a catalog that
// selects the wrong parser fails loudly instead of producing a description that
// was never in the document.
type Parser struct {
	formats map[string]bool
}

// NewParser creates the OpenAPI parser.
func NewParser(options ParserOptions) *Parser {
	claimed := options.Formats
	if len(claimed) == 0 {
		claimed = []api.Format{FormatOpenAPI}
	}
	parser := &Parser{formats: make(map[string]bool, len(claimed))}
	for _, format := range claimed {
		parser.formats[api.NormalizeIdentifier(string(format))] = true
	}
	return parser
}

// Formats returns the format identifiers this parser claims.
func (p *Parser) Formats() []api.Format {
	result := make([]api.Format, 0, len(p.formats))
	for format := range p.formats {
		result = append(result, api.Format(format))
	}
	return result
}

// ParseApi implements the framework's ApiParserService.
func (p *Parser) ParseApi(_ context.Context, request *connect.Request[apiv1.ParseApiRequest]) (*connect.Response[apiv1.ParseApiResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	format := strings.TrimSpace(request.Msg.GetFormat())
	hint := strings.TrimSpace(request.Msg.GetFormatHint())
	if format == "" {
		format = hint
	}
	if format == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("format is required"))
	}
	if !p.formats[api.NormalizeIdentifier(format)] {
		names := make([]string, 0, len(p.formats))
		for claimed := range p.formats {
			names = append(names, claimed)
		}
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("this parser handles %s, not %q", strings.Join(names, ", "), format),
		)
	}
	document := request.Msg.GetDocument()
	if len(document) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a document is required"))
	}
	described, warnings, err := openapi.Parse(document, openapi.ParseOptions{
		APIID:   request.Msg.GetApiId(),
		BaseURL: strings.TrimSpace(request.Msg.GetBaseUrl()),
		Source: api.Source{
			Kind:     request.Msg.GetSource().GetKind(),
			Location: request.Msg.GetSource().GetLocation(),
			Digest:   request.Msg.GetSource().GetDigest(),
		},
	})
	if err != nil {
		return nil, providerError(err)
	}
	return connect.NewResponse(&apiv1.ParseApiResponse{
		Api: described.ToProto(),
		// The parser reports the format descriptors it implements, so a catalog can
		// index them without a separate registration step.
		Formats:  []*apiv1.ApiFormatDescriptor{FormatDescriptor().ToProto()},
		Warnings: warnings,
	}), nil
}
