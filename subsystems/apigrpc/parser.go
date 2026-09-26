package apigrpc

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	"github.com/Manu343726/toolbox/pkg/protocontract"
)

// This file is the RPC layer of the parser contract: it validates a request,
// converts it to a plain call, and converts the result back. The contract
// translation itself is [protocontract].

// ParserOptions configures the parser.
type ParserOptions struct {
	// Formats optionally narrows the format identifiers this parser claims. The
	// zero value claims the single format this subsystem owns.
	Formats []api.Format
	// ExcludeServices drops services by full protobuf name, such as the reflection
	// services a server always exposes.
	ExcludeServices []string
}

// Parser serves the framework's parser contract for protobuf contracts.
type Parser struct {
	formats    map[string]bool
	descriptor *protocontract.Descriptor
}

// NewParser creates the gRPC contract parser.
func NewParser(options ParserOptions) *Parser {
	claimed := options.Formats
	if len(claimed) == 0 {
		claimed = []api.Format{FormatGRPC}
	}
	parser := &Parser{
		formats: make(map[string]bool, len(claimed)),
		descriptor: protocontract.NewDescriptor(protocontract.Descriptor{
			ExcludeServices: options.ExcludeServices,
		}),
	}
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
func (p *Parser) ParseApi(ctx context.Context, request *connect.Request[apiv1.ParseApiRequest]) (*connect.Response[apiv1.ParseApiResponse], error) {
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
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("this parser handles %s, not %q", strings.Join(formatNames(p.Formats()), ", "), format),
		)
	}
	// A live endpoint describes itself: the contract comes from the server's own
	// reflection, so a catalog never has to be handed a descriptor.
	described, err := p.descriptor.Describe(ctx, api.DescribeRequest{
		Document: request.Msg.GetDocument(),
		Format:   api.Format(format),
		BaseURL:  request.Msg.GetBaseUrl(),
		APIID:    request.Msg.GetApiId(),
		Source: api.Source{
			Kind:     request.Msg.GetSource().GetKind(),
			Location: request.Msg.GetSource().GetLocation(),
		},
	})
	if err != nil {
		return nil, providerError(err)
	}
	return connect.NewResponse(&apiv1.ParseApiResponse{
		Api: described.API.ToProto(),
		// The parser reports the format descriptors it implements, so a catalog can
		// index them without a separate registration step.
		Formats:  []*apiv1.ApiFormatDescriptor{FormatDescriptor().ToProto()},
		Warnings: described.Warnings,
	}), nil
}

func formatNames(formats []api.Format) []string {
	names := make([]string, 0, len(formats))
	for _, format := range formats {
		names = append(names, string(format))
	}
	return names
}

// providerError maps a classified error from the implementation package onto a
// ConnectRPC code, so the classification survives the transport instead of being
// re-derived from a message.
func providerError(err error) error {
	return api.ConnectError(err)
}
