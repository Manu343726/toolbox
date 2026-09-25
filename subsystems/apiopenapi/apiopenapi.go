// Package apiopenapi is the OpenAPI provider subsystem: it parses OpenAPI 3.x
// description documents into the framework's standard API model, and it invokes
// the operations of an API described that way.
//
// It is one provider among many. The framework does not know what OpenAPI is —
// it knows that a parser subsystem can turn a document into a standard API
// description, and that an adapter subsystem can invoke one of its operations.
// The two contracts this subsystem implements are what make a user-defined
// format possible by writing one more subsystem with these same contracts.
//
// A useful consequence of that separation: this subsystem owns the identifiers
// "openapi" and "http". Nothing in the framework branches on them.
package apiopenapi

import (
	"context"
	"net/http"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
)

const (
	// Name is the stable subsystem name.
	Name = "apiopenapi"
	// Version is the reference implementation version.
	Version = "0.1.0"

	// FormatOpenAPI is the description format this parser handles.
	FormatOpenAPI api.Format = "openapi"
	// TransportHTTP is the transport this adapter reaches.
	TransportHTTP api.Transport = "http"
	// TransportHTTPS is the TLS variant of the same transport.
	TransportHTTPS api.Transport = "https"
	// TargetOpenAPI is the representation this subsystem renders into: an OpenAPI
	// document produced from a standard description, whatever that description
	// was parsed from.
	TargetOpenAPI = "openapi"
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
	// Background is optional provider work.
	Background func(context.Context) error
}

// New is the programmatic in-process entrypoint for the OpenAPI provider. It
// mounts both extension-point contracts: the parser and the adapter.
func New(options Options) (*subsystem.Server, error) {
	version := options.Version
	if version == "" {
		version = Version
	}
	client := options.HTTPClient
	if client == nil {
		timeout := options.RequestTimeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	parserPath, parserHandler := apiv1connect.NewApiParserServiceHandler(NewParser(ParserOptions{}))
	adapterPath, adapterHandler := apiv1connect.NewApiAdapterServiceHandler(NewAdapter(
		NewRenderer(),
		NewServer(ServerOptions{RequestTimeout: options.RequestTimeout}),
	))
	invokerPath, invokerHandler := apiv1connect.NewApiInvokerServiceHandler(NewInvoker(InvokerOptions{HTTPClient: client, RequestTimeout: options.RequestTimeout}))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Parses OpenAPI 3.x documents into the standard API model and invokes their operations over HTTP.",
		ListenAddress: options.ListenAddress,
		Background:    options.Background,
		Services: []subsystem.Service{
			{
				Name:         apiv1connect.ApiParserServiceName,
				Path:         parserPath,
				Handler:      parserHandler,
				Capabilities: []string{api.ParseCapability(FormatOpenAPI)},
			},
			{
				Name:         apiv1connect.ApiAdapterServiceName,
				Path:         adapterPath,
				Handler:      adapterHandler,
				Capabilities: []string{api.RenderCapability(TargetOpenAPI)},
			},
			{
				Name:         apiv1connect.ApiInvokerServiceName,
				Path:         invokerPath,
				Handler:      invokerHandler,
				Capabilities: []string{api.InvokeCapability(TransportHTTP), api.InvokeCapability(TransportHTTPS)},
			},
		},
	})
}

// FormatDescriptor describes the format this parser handles, so a catalog can
// index it and answer "can this deployment parse OpenAPI?".
func FormatDescriptor() api.FormatDescriptor {
	return api.FormatDescriptor{
		ID:                   FormatOpenAPI,
		Name:                 "OpenAPI",
		Version:              Version,
		SpecificationVersion: "3.0.3/3.1.0",
		Description:          "OpenAPI 3.x description documents served as JSON or YAML.",
		MediaTypes:           []string{"application/json", "application/yaml", "text/yaml"},
		FileExtensions:       []string{"json", "yaml", "yml"},
		Provider:             Name,
	}
}

// TransportDescriptors describes the transports this adapter reaches.
func TransportDescriptors() []api.TransportDescriptor {
	return []api.TransportDescriptor{
		{
			ID:          TransportHTTP,
			Name:        "HTTP",
			Version:     Version,
			Description: "Plain HTTP requests built from an OpenAPI operation.",
			Schemes:     []string{"http"},
			Provider:    Name,
		},
		{
			ID:          TransportHTTPS,
			Name:        "HTTPS",
			Version:     Version,
			Description: "TLS HTTP requests built from an OpenAPI operation.",
			Schemes:     []string{"https"},
			Provider:    Name,
		},
	}
}

// Providers describes this subsystem to a catalog's provider directory. It
// implements three contracts, so it contributes three provider records: one for
// reading OpenAPI documents, one for rendering descriptions into them, and one
// for calling the APIs they describe.
func Providers(endpoint string) []api.Provider {
	serviceNames := []string{
		apiv1connect.ApiParserServiceName,
		apiv1connect.ApiAdapterServiceName,
		apiv1connect.ApiInvokerServiceName,
	}
	return []api.Provider{
		{
			ID:                    Name + "-parser",
			Subsystem:             Name,
			Role:                  api.ProviderParser,
			Formats:               []api.Format{FormatOpenAPI},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiParserServiceName},
			Capabilities:          []string{api.ParseCapability(FormatOpenAPI)},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
		{
			ID:                    Name + "-renderer",
			Subsystem:             Name,
			Role:                  api.ProviderAdapter,
			Targets:               []string{TargetOpenAPI},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiAdapterServiceName},
			Capabilities:          []string{api.RenderCapability(TargetOpenAPI)},
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
		{
			ID:                    Name + "-http",
			Subsystem:             Name,
			Role:                  api.ProviderInvoker,
			Transports:            []api.Transport{TransportHTTP, TransportHTTPS},
			Endpoint:              endpoint,
			ServiceNames:          []string{apiv1connect.ApiInvokerServiceName},
			Capabilities:          []string{api.InvokeCapability(TransportHTTP), api.InvokeCapability(TransportHTTPS)},
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
			ServiceNames:          serviceNames,
			Status:                api.ServerStatusServing,
			ImplementationVersion: Version,
		},
	}
}
