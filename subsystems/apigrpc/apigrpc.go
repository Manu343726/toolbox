// Package apigrpc exposes the framework's protobuf-contract implementation as a
// provider subsystem.
//
// The work lives in [protocontract], a plain Go package, because the framework
// needs it in process: a host describes its own subsystems with it, and the MCP
// gateway reads the same standard description it produces. This subsystem is the
// addressable form — it serves the parser and invoker contracts so a catalog in
// another process, or a deployment whose providers are deployed separately, can
// reach the same implementation over ConnectRPC.
//
// Nothing about the translation is duplicated here. A subsystem that exists only
// to make a package addressable is the cheapest kind of subsystem: there is no
// logic to keep in step, because there is no logic.
package apigrpc

import (
	"context"
	"net/http"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/protocontract"
	"github.com/Manu343726/toolbox/pkg/subsystem"
)

const (
	// Name is the stable subsystem name.
	Name = "apigrpc"
	// Version is the reference implementation version.
	Version = "0.1.0"

	// FormatGRPC is the description format this provider handles. The same contract
	// is served over Connect, gRPC, and gRPC-Web, so one format identifier covers
	// all three.
	FormatGRPC api.Format = protocontract.Format
	// TransportConnectRPC is the transport this provider reaches by default.
	TransportConnectRPC api.Transport = "connectrpc"
	// TransportGRPC is the gRPC variant of the same transport.
	TransportGRPC api.Transport = "grpc"
	// TransportGRPCWeb is the gRPC-Web variant, for callers that cannot speak
	// HTTP/2.
	TransportGRPCWeb api.Transport = "grpc-web"
)

// Options configures the gRPC provider subsystem.
type Options struct {
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
	// HTTPClient is used for reflection and invocation. The zero value uses a
	// client with a bounded timeout.
	HTTPClient *http.Client
	// RequestTimeout bounds one invocation. It defaults to 30 seconds.
	RequestTimeout time.Duration
	// Background is optional provider work.
	Background func(context.Context) error
}

// New is the programmatic in-process entrypoint for the gRPC provider. It mounts
// both extension-point contracts: the parser and the invoker.
func New(options Options) (*subsystem.Server, error) {
	version := options.Version
	if version == "" {
		version = Version
	}
	parserPath, parserHandler := apiv1connect.NewApiParserServiceHandler(NewParser(ParserOptions{}))
	invokerPath, invokerHandler := apiv1connect.NewApiInvokerServiceHandler(NewInvoker(InvokerOptions{
		HTTPClient:     options.HTTPClient,
		RequestTimeout: options.RequestTimeout,
	}))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Parses protobuf service contracts into the standard API model and invokes their methods over Connect, gRPC, or gRPC-Web.",
		ListenAddress: options.ListenAddress,
		Background:    options.Background,
		Services: []subsystem.Service{
			{
				Name:         apiv1connect.ApiParserServiceName,
				Path:         parserPath,
				Handler:      parserHandler,
				Capabilities: []string{api.ParseCapability(FormatGRPC)},
			},
			{
				Name:    apiv1connect.ApiInvokerServiceName,
				Path:    invokerPath,
				Handler: invokerHandler,
				Capabilities: []string{
					api.InvokeCapability(TransportConnectRPC),
					api.InvokeCapability(TransportGRPC),
					api.InvokeCapability(TransportGRPCWeb),
				},
			},
		},
	})
}

// FormatDescriptor describes the format this provider reads, stamped with the
// subsystem that contributed it.
func FormatDescriptor() api.FormatDescriptor {
	descriptor := protocontract.FormatDescriptor()
	descriptor.ID = string(FormatGRPC)
	descriptor.Version = Version
	descriptor.Provider = Name
	return descriptor
}

// TransportDescriptors describes the transports this provider reaches.
func TransportDescriptors() []api.TransportDescriptor {
	descriptors := protocontract.TransportDescriptors()
	for i := range descriptors {
		descriptors[i].Version = Version
		descriptors[i].Provider = Name
	}
	return descriptors
}

// Providers describes this subsystem to a catalog's provider directory. It
// implements two contracts — it reads a protobuf contract and it calls one — so
// it contributes a provider record for each, plus a convenience record that names
// both for a deployment that only needs the service names.
func Providers(endpoint string) []api.Provider {
	invokerCapabilities := []string{
		api.InvokeCapability(TransportConnectRPC),
		api.InvokeCapability(TransportGRPC),
		api.InvokeCapability(TransportGRPCWeb),
	}
	return []api.Provider{
		{
			ID:                    Name + "-parser",
			Subsystem:             Name,
			Role:                  api.ProviderParser,
			Formats:               []api.Format{FormatGRPC},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiParserServiceName},
			Capabilities:          []string{api.ParseCapability(FormatGRPC)},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
		{
			ID:                    Name + "-invoker",
			Subsystem:             Name,
			Role:                  api.ProviderInvoker,
			Transports:            []api.Transport{TransportConnectRPC, TransportGRPC, TransportGRPCWeb},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiInvokerServiceName},
			Capabilities:          invokerCapabilities,
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
		{
			ID:                    Name,
			Subsystem:             Name,
			Role:                  api.ProviderParser,
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiParserServiceName, apiv1connect.ApiInvokerServiceName},
			Capabilities:          append([]string{api.ParseCapability(FormatGRPC)}, invokerCapabilities...),
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
	}
}
