package apiopenapi

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
)

// ParserOptions configures the parser.
type ParserOptions struct {
	// Formats optionally narrows the format identifiers this parser claims. The
	// zero value claims the single format this subsystem owns.
	Formats []api.Format
}

// Parser implements the framework's parser contract for OpenAPI documents.
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
		parser.formats[strings.TrimSpace(format)] = true
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
	if !p.formats[format] {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("this parser handles %s, not %q", strings.Join(p.Formats(), ", "), format),
		)
	}
	parsed, warnings, err := parseDocument(request.Msg.GetDocument(), parseRequest{
		APID: request.Msg.GetApiId(),
		Source: api.Source{
			Kind:     request.Msg.GetSource().GetKind(),
			Location: request.Msg.GetSource().GetLocation(),
			Digest:   request.Msg.GetSource().GetDigest(),
		},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// A document may declare its own servers; when the caller supplied a base URL
	// and the document declared none, the caller's location becomes the declared
	// one so the catalog can bind the API to it.
	if len(parsed.DeclaredServers) == 0 && strings.TrimSpace(request.Msg.GetBaseUrl()) != "" {
		parsed.DeclaredServers = []api.DeclaredServer{{URL: strings.TrimSpace(request.Msg.GetBaseUrl())}}
	}
	// The parser reports the format descriptors it implements, so a catalog can
	// index them without a separate registration step.
	return connect.NewResponse(&apiv1.ParseApiResponse{
		Api:      parsed.ToProto(),
		Warnings: warnings,
		Formats:  []*apiv1.ApiFormatDescriptor{FormatDescriptor().ToProto()},
	}), nil
}
