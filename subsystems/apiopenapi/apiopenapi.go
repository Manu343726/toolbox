// Package apiopenapi exposes the framework's OpenAPI implementation as a provider
// subsystem.
//
// The work lives in [openapi], a plain Go package, because the framework needs it
// in process: a host renders a description with it, an adapted surface is served
// with it, and the MCP gateway reads the descriptions it produces. This subsystem
// is the addressable form — it serves the parser, adapter, and invoker contracts
// so a catalog in another process, or a deployment whose providers run separately,
// can reach the same implementation over ConnectRPC.
//
// Nothing about the translation is duplicated here. A subsystem that exists only
// to make a package addressable is the cheapest kind of subsystem: there is no
// logic to keep in step, because there is no logic.
package apiopenapi

import (
	"context"
	"net/http"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/openapi"
	"github.com/Manu343726/toolbox/pkg/subsystem"
)

const (
	// Name is the stable subsystem name.
	Name = "apiopenapi"
	// Version is the reference implementation version.
	Version = openapi.Version

	// FormatOpenAPI is the description format this provider reads. This subsystem
	// owns that identifier; nothing in the framework branches on it.
	FormatOpenAPI api.Format = openapi.Format
	// TargetOpenAPI is the representation this provider renders into. It is the
	// same identifier as the format it reads, because an OpenAPI document is both
	// what a caller supplies and what this provider publishes.
	TargetOpenAPI = openapi.Target
	// TransportHTTP is the transport this provider reaches.
	TransportHTTP api.Transport = openapi.TransportHTTP
	// TransportHTTPS is the TLS variant of the same transport.
	TransportHTTPS api.Transport = openapi.TransportHTTPS
)

// Options configures the OpenAPI provider subsystem.
type Options struct {
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
	// HTTPClient is used for invocation. The zero value uses a client with a
	// bounded timeout.
	HTTPClient *http.Client
	// RequestTimeout bounds one invocation. It defaults to 30 seconds.
	RequestTimeout time.Duration
	// SwaggerUI is an OpenAPI documentation UI, such as the official swagger-ui
	// bundle, served on adapted surfaces' documentation paths. When empty, each
	// surface serves a documentation page generated from its own schema.
	SwaggerUI []byte
	// Background is optional provider work.
	Background func(context.Context) error
}

// New is the programmatic in-process entrypoint for the OpenAPI provider. It
// mounts all three extension-point contracts: the parser, the adapter, and the
// invoker.
func New(options Options) (*subsystem.Server, error) {
	version := options.Version
	if version == "" {
		version = Version
	}
	parserPath, parserHandler := apiv1connect.NewApiParserServiceHandler(NewParser(ParserOptions{}))
	adapterPath, adapterHandler := apiv1connect.NewApiAdapterServiceHandler(NewAdapter(AdapterOptions{
		SwaggerUI:      options.SwaggerUI,
		RequestTimeout: options.RequestTimeout,
	}))
	invokerPath, invokerHandler := apiv1connect.NewApiInvokerServiceHandler(NewInvoker(InvokerOptions{
		HTTPClient:     options.HTTPClient,
		RequestTimeout: options.RequestTimeout,
	}))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Parses OpenAPI 3.x documents into the standard API model, renders descriptions into them, serves adapted surfaces, and invokes their operations over HTTP.",
		ListenAddress: options.ListenAddress,
		Background:    options.Background,
		Services: []subsystem.Service{
			{
				Name:    apiv1connect.ApiParserServiceName,
				Path:    parserPath,
				Handler: parserHandler,
			},
			{
				Name:    apiv1connect.ApiAdapterServiceName,
				Path:    adapterPath,
				Handler: adapterHandler,
			},
			{
				Name:    apiv1connect.ApiInvokerServiceName,
				Path:    invokerPath,
				Handler: invokerHandler,
			},
		},
	})
}

// FormatDescriptor describes the format this provider reads, stamped with the
// subsystem that contributed it.
func FormatDescriptor() api.FormatDescriptor {
	descriptor := openapi.FormatDescriptor()
	descriptor.ID = string(FormatOpenAPI)
	descriptor.Version = Version
	descriptor.Provider = Name
	return descriptor
}

// TargetDescriptor describes the representation this provider renders.
func TargetDescriptor() api.FormatDescriptor {
	descriptor := openapi.TargetDescriptor()
	descriptor.Version = Version
	descriptor.Provider = Name
	return descriptor
}

// TransportDescriptors describes the transports this provider reaches.
func TransportDescriptors() []api.TransportDescriptor {
	descriptors := openapi.TransportDescriptors()
	for i := range descriptors {
		descriptors[i].Version = Version
		descriptors[i].Provider = Name
	}
	return descriptors
}

// Providers describes this subsystem to a catalog's provider directory. It
// implements three contracts — it reads an OpenAPI document, it renders one, and
// it calls the operations one describes — so it contributes a provider record for
// each.
func Providers(endpoint string) []api.Provider {
	return []api.Provider{
		{
			ID:                    Name + "-parser",
			Subsystem:             Name,
			Role:                  api.ProviderParser,
			Formats:               []api.Format{FormatOpenAPI},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiParserServiceName},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
		{
			ID:                    Name + "-adapter",
			Subsystem:             Name,
			Role:                  api.ProviderAdapter,
			Targets:               []string{TargetOpenAPI},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiAdapterServiceName},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
		{
			ID:                    Name + "-invoker",
			Subsystem:             Name,
			Role:                  api.ProviderInvoker,
			Transports:            []api.Transport{TransportHTTP, TransportHTTPS},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiInvokerServiceName},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
	}
}
