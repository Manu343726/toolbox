// Package apigrpc is the gRPC provider subsystem: it turns a protobuf service
// contract into the framework's standard API description, and it invokes those
// operations over Connect, gRPC, or gRPC-Web.
//
// Like every other provider, it implements the two framework contracts and
// nothing else. The framework does not know that "grpc" exists as a format; it
// knows that a parser subsystem can describe a contract and an adapter can
// invoke one. This subsystem's contribution is the descriptor it reports for
// the format it owns, and the contract it serves.
package apigrpc

import (
	"context"
	"net/http"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/Manu343726/toolbox/pkg/subsystem"
)

const (
	// Name is the stable subsystem name.
	Name = "apigrpc"
	// Version is the reference implementation version.
	Version = "0.1.0"

	// FormatGRPC is the description format this parser handles. The same
	// contract is served over Connect, gRPC, and gRPC-Web, so one format
	// identifier covers all three.
	FormatGRPC api.Format = "grpc"
	// TransportConnectRPC is the transport this adapter reaches by default.
	TransportConnectRPC api.Transport = "connectrpc"
	// TransportGRPC is the gRPC variant of the same transport.
	TransportGRPC api.Transport = "grpc"
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
// both extension-point contracts: the parser and the adapter.
func New(options Options) (*subsystem.Server, error) {
	version := options.Version
	if version == "" {
		version = Version
	}
	parserPath, parserHandler := apiv1connect.NewApiParserServiceHandler(NewParser(ParserOptions{}))
	adapterPath, adapterHandler := apiv1connect.NewApiInvokerServiceHandler(NewAdapter(AdapterOptions{
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
				Name:         apiv1connect.ApiInvokerServiceName,
				Path:         adapterPath,
				Handler:      adapterHandler,
				Capabilities: []string{api.InvokeCapability(TransportConnectRPC), api.InvokeCapability(TransportGRPC)},
			},
		},
	})
}

// FormatDescriptor describes the format this parser handles.
func FormatDescriptor() api.FormatDescriptor {
	return api.FormatDescriptor{
		ID:                   FormatGRPC,
		Name:                 "Protocol Buffers service contract",
		Version:              Version,
		SpecificationVersion: "proto3",
		Description:          "Protobuf service contracts served over Connect, gRPC, or gRPC-Web, read from a FileDescriptorSet or from a live endpoint's reflection.",
		MediaTypes:           []string{"application/x-protobuf", "application/octet-stream"},
		FileExtensions:       []string{"protoset", "desc", "binpb"},
		Provider:             Name,
	}
}

// TransportDescriptors describes the transports this adapter reaches.
func TransportDescriptors() []api.TransportDescriptor {
	return []api.TransportDescriptor{
		{
			ID:                TransportConnectRPC,
			Name:              "ConnectRPC",
			Version:           Version,
			Description:       "Unary Connect requests against a protobuf service contract.",
			Schemes:           []string{"http", "https"},
			SupportsStreaming: true,
			Provider:          Name,
		},
		{
			ID:                TransportGRPC,
			Name:              "gRPC",
			Version:           Version,
			Description:       "Unary gRPC requests against a protobuf service contract.",
			Schemes:           []string{"http", "https"},
			SupportsStreaming: true,
			Provider:          Name,
		},
	}
}

// Providers describes this subsystem to a catalog's provider directory. This
// subsystem implements two contracts — it reads a protobuf contract and it calls
// one — so it contributes a provider record for each.
func Providers(endpoint string) []api.Provider {
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
			ID:                    Name + "-connect",
			Subsystem:             Name,
			Role:                  api.ProviderInvoker,
			Transports:            []api.Transport{TransportConnectRPC, TransportGRPC},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiInvokerServiceName},
			Capabilities:          []string{api.InvokeCapability(TransportConnectRPC), api.InvokeCapability(TransportGRPC)},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
		{
			// A convenience record so a deployment that only needs the service
			// names can resolve this subsystem without knowing its roles.
			ID:                    Name,
			Subsystem:             Name,
			Role:                  api.ProviderParser,
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiParserServiceName, apiv1connect.ApiInvokerServiceName},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
	}
}

// newDiscoveryClient creates the discovery client an adapter or a reflection
// based parse uses for one endpoint.
func newDiscoveryClient(endpoint string, client *http.Client) *discovery.Client {
	options := []discovery.Option{}
	if client != nil {
		options = append(options, discovery.WithHTTPClient(client))
	}
	return discovery.New(endpoint, options...)
}
