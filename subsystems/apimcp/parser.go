package apimcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
)

// This file is the RPC layer of the parser contract: it validates a request,
// converts it to a plain call, and converts the result back. Reading a server — or a
// published manifest — is [toolboxmcp.Describe] and [toolboxmcp.DescribeDocument].

// ParserOptions configures the parser.
type ParserOptions struct {
	// Describe are the options a live server is read with. A caller that supplies
	// none gets a bounded client.
	Describe toolboxmcp.DescribeOptions
	// Formats optionally narrows the format identifiers this parser claims. The
	// zero value claims the single format this subsystem owns.
	Formats []api.Format
}

// Parser serves the framework's parser contract for Model Context Protocol servers
// and for manifests published from them.
//
// It rejects a document or endpoint in a format it does not own, so a catalog that
// selects the wrong parser fails loudly instead of producing a description that was
// never in the source.
type Parser struct {
	formats  map[string]bool
	describe toolboxmcp.DescribeOptions
}

// NewParser creates the Model Context Protocol parser.
func NewParser(options ParserOptions) *Parser {
	claimed := options.Formats
	if len(claimed) == 0 {
		claimed = []api.Format{FormatMCP}
	}
	parser := &Parser{
		formats:  make(map[string]bool, len(claimed)),
		describe: options.Describe,
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
//
// A request may name a live endpoint, which is described through the protocol's own
// tool list, or supply a document, which is a manifest a previous description was
// published as. Both are the same format: one is read from a server, the other
// from a file.
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
	options := p.describe
	options.APIID = request.Msg.GetApiId()
	var (
		described api.API
		warnings  []string
		err       error
	)
	switch {
	case len(request.Msg.GetDocument()) > 0:
		described, warnings, err = toolboxmcp.DescribeDocument(request.Msg.GetDocument(), options)
	case strings.TrimSpace(request.Msg.GetBaseUrl()) != "":
		// A live server describes itself: the description comes from the tools it
		// lists, so a catalog never has to be handed a document it would otherwise
		// have to keep in step.
		described, warnings, err = toolboxmcp.Describe(ctx, request.Msg.GetBaseUrl(), options)
	default:
		return nil, connect.NewError(
			connect.CodeInvalidArgument, fmt.Errorf("document or base_url is required"),
		)
	}
	if err != nil {
		return nil, providerError(err)
	}
	if base := strings.TrimSpace(request.Msg.GetBaseUrl()); base != "" && len(described.DeclaredServers) == 0 {
		described.DeclaredServers = []api.DeclaredServer{{URL: base}}
	}
	return connect.NewResponse(&apiv1.ParseApiResponse{
		Api: described.ToProto(),
		// The parser reports the format descriptors it implements, so a catalog can
		// index them without a separate registration step.
		Formats:  []*apiv1.ApiFormatDescriptor{FormatDescriptor().ToProto()},
		Warnings: warnings,
	}), nil
}

// boundedClient returns a client with a bounded timeout, for a caller that has
// none.
func boundedClient(client *http.Client, timeout time.Duration) *http.Client {
	if client != nil {
		return client
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}
