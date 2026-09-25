// Package apitools is the repository of registered APIs and the servers that
// host them, and the index of the description formats and transports this
// deployment understands.
//
// It is the subsystem where an agent gains API-based tools. A description
// document is handed to a parser provider — a subsystem implementing the
// framework's parser contract — the resulting API is registered here, its
// operations are exposed on demand, and calls are routed to an adapter provider
// implementing the invocation contract.
//
// The subsystem never imports a provider, and it never parses or invokes
// anything itself. Both are extension points: a user integrates a new API
// format or transport by adding a subsystem, and the deployment tells this one
// where the providers are. Format and transport identifiers are open strings,
// and the descriptors indexed here are what make a user-defined format
// discoverable to an agent.
package apitools

import (
	"context"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	apitoolsv1connect "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1/apitoolsv1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "apitools"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Capability names the subsystem grants. The read capabilities cover catalog and
// index inspection, the write capability covers registration and exposure
// changes, and the call capability covers routed invocations.
const (
	CapabilityRead  = "api.catalog.read"
	CapabilityIndex = "api.index.read"
	CapabilityWrite = "api.catalog.write"
	CapabilityCall  = "api.operation.call"
)

// Options configures the API catalog subsystem.
type Options struct {
	// Store optionally supplies an existing catalog.
	Store *Memory
	// Directory supplies the parser and adapter providers available in this
	// deployment. Without it the catalog still works: registrations, listings,
	// indexing, and exposure all function, while parse and call operations
	// report that no provider is configured.
	Directory ProviderDirectory
	// Policy authorizes operation exposure. The zero value denies operations
	// that declare no capability.
	Policy OperationPolicy
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
	// Background is optional catalog work.
	Background func(context.Context) error
}

// New is the programmatic in-process entrypoint for the API catalog subsystem.
func New(options Options) (*subsystem.Server, error) {
	store := options.Store
	if store == nil {
		store = NewMemory(StoreOptions{Policy: options.Policy})
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := apitoolsv1connect.NewApiToolsServiceHandler(NewService(store, options.Directory))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Catalog of registered APIs, index of API formats, and on-demand API tool exposure.",
		ListenAddress: options.ListenAddress,
		Background:    options.Background,
		Services: []subsystem.Service{{
			Name:         apitoolsv1connect.ApiToolsServiceName,
			Path:         path,
			Handler:      handler,
			Capabilities: []string{CapabilityRead, CapabilityIndex, CapabilityWrite, CapabilityCall},
		}},
	})
}

// ProviderCapability returns the capability a provider subsystem advertises so
// a deployment can discover it through the registry. A parser advertises
// api.parse.<format> for each format it parses and an adapter advertises
// api.invoke.<transport> for each transport it reaches.
func ProviderCapability(role string, identifier string) string {
	prefix := api.CapabilityParse
	if role == api.ProviderAdapter {
		prefix = api.CapabilityInvoke
	}
	return prefix + "." + identifier
}

// ParserServiceName is the fully-qualified name of the framework's parser
// contract. A provider subsystem serving it is discoverable by service name.
const ParserServiceName = apiv1connect.ApiParserServiceName

// InvokerServiceName is the fully-qualified name of the framework's invocation
// contract.
const InvokerServiceName = apiv1connect.ApiInvokerServiceName
