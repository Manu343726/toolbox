// Package apimcp exposes the framework's Model Context Protocol implementation as a
// provider subsystem.
//
// The work lives in [mcp], a plain Go package, because the framework needs it in
// process: a host describes an MCP server with it, and a catalog that already holds
// a description renders it as MCP tools with it. This subsystem is the addressable
// form — it serves the parser, adapter, and invoker contracts so a catalog in
// another process can reach the same implementation over ConnectRPC.
//
// Nothing about the translation is duplicated here. A subsystem that exists only to
// make a package addressable is the cheapest kind of subsystem: there is no logic
// to keep in step, because there is no logic.
package apimcp

import (
	"context"
	"net/http"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
	"github.com/Manu343726/toolbox/pkg/subsystem"
)

const (
	// Name is the stable subsystem name.
	Name = "apimcp"
	// Version is the reference implementation version.
	Version = toolboxmcp.Version

	// FormatMCP is the description format this provider reads: a Model Context
	// Protocol server's own tool list, and the manifests published from one.
	FormatMCP api.Format = toolboxmcp.Format
	// TargetMCP is the representation this provider renders into.
	TargetMCP = string(toolboxmcp.Format)
	// TransportMCP is the transport this provider reaches.
	TransportMCP api.Transport = toolboxmcp.Transport
)

// Options configures the Model Context Protocol provider subsystem.
type Options struct {
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
	// HTTPClient performs the protocol calls. The zero value uses a client with a
	// bounded timeout.
	HTTPClient *http.Client
	// RequestTimeout bounds one protocol call, including the session handshake. It
	// defaults to 30 seconds.
	RequestTimeout time.Duration
	// Background is optional provider work.
	Background func(context.Context) error
}

// New is the programmatic in-process entrypoint for the Model Context Protocol
// provider. It mounts all three extension-point contracts: the parser, the adapter,
// and the invoker.
func New(options Options) (*subsystem.Server, error) {
	version := options.Version
	if version == "" {
		version = Version
	}
	describeOptions := toolboxmcp.DescribeOptions{
		HTTPClient:     options.HTTPClient,
		RequestTimeout: options.RequestTimeout,
	}
	parserPath, parserHandler := apiv1connect.NewApiParserServiceHandler(NewParser(ParserOptions{
		Describe: describeOptions,
	}))
	adapterPath, adapterHandler := apiv1connect.NewApiAdapterServiceHandler(NewAdapter(AdapterOptions{
		HTTPClient:     options.HTTPClient,
		RequestTimeout: options.RequestTimeout,
	}))
	invokerPath, invokerHandler := apiv1connect.NewApiInvokerServiceHandler(NewInvoker(InvokerOptions{
		HTTPClient:     options.HTTPClient,
		RequestTimeout: options.RequestTimeout,
	}))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Reads Model Context Protocol servers into the standard API model, renders descriptions as MCP tool manifests, and calls their tools.",
		ListenAddress: options.ListenAddress,
		Background:    options.Background,
		Services: []subsystem.Service{{
			Name:         apiv1connect.ApiParserServiceName,
			Path:         parserPath,
			Handler:      parserHandler,
			Capabilities: []string{api.ParseCapability(FormatMCP)},
		}, {
			Name:         apiv1connect.ApiAdapterServiceName,
			Path:         adapterPath,
			Handler:      adapterHandler,
			Capabilities: []string{api.RenderCapability(TargetMCP)},
		}, {
			Name:         apiv1connect.ApiInvokerServiceName,
			Path:         invokerPath,
			Handler:      invokerHandler,
			Capabilities: []string{api.InvokeCapability(TransportMCP)},
		}},
	})
}

// FormatDescriptor describes the format this provider reads, stamped with the
// subsystem that contributed it.
func FormatDescriptor() api.FormatDescriptor {
	descriptor := toolboxmcp.FormatDescriptor()
	descriptor.ID = string(FormatMCP)
	descriptor.Version = Version
	descriptor.Provider = Name
	return descriptor
}

// TargetDescriptor describes the representation this provider renders.
func TargetDescriptor() api.FormatDescriptor {
	descriptor := toolboxmcp.TargetDescriptor()
	descriptor.ID = TargetMCP
	descriptor.Version = Version
	descriptor.Provider = Name
	return descriptor
}

// TransportDescriptors describes the transports this provider reaches.
func TransportDescriptors() []api.TransportDescriptor {
	descriptors := toolboxmcp.TransportDescriptors()
	for i := range descriptors {
		descriptors[i].Version = Version
		descriptors[i].Provider = Name
	}
	return descriptors
}

// Providers describes this subsystem to a catalog's provider directory. It
// implements three contracts — it reads an MCP server, it renders a description as
// one, and it calls a tool — so it contributes a provider record for each.
func Providers(endpoint string) []api.Provider {
	return []api.Provider{
		{
			ID:                    Name + "-parser",
			Subsystem:             Name,
			Role:                  api.ProviderParser,
			Formats:               []api.Format{FormatMCP},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiParserServiceName},
			Capabilities:          []string{api.ParseCapability(FormatMCP)},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
		{
			ID:                    Name + "-adapter",
			Subsystem:             Name,
			Role:                  api.ProviderAdapter,
			Targets:               []string{TargetMCP},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiAdapterServiceName},
			Capabilities:          []string{api.RenderCapability(TargetMCP)},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
		{
			ID:                    Name + "-invoker",
			Subsystem:             Name,
			Role:                  api.ProviderInvoker,
			Transports:            []api.Transport{TransportMCP},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiInvokerServiceName},
			Capabilities:          []string{api.InvokeCapability(TransportMCP)},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
	}
}
