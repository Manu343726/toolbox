// Package openapi reads, writes, serves, and calls APIs described by OpenAPI 3.x
// documents.
//
// It is a plain Go package: it depends on the standard model and on the
// specification's own document shapes, and on no transport. A deployment uses it
// directly — to describe an API from a document it already holds, to publish a
// description in OpenAPI whatever language it was parsed from, to serve that
// published API, or to call an operation — and the OpenAPI provider subsystem
// wraps it when a parser, an adapter, or an invoker has to be addressable over
// ConnectRPC.
//
// The four capabilities are separate on purpose. Reading a document, writing one,
// serving one, and calling one are different jobs, and a deployment often needs
// only some of them.
package openapi

import (
	"github.com/Manu343726/toolbox/pkg/api"
)

const (
	// Version is the reference implementation version.
	Version = "0.1.0"

	// Format is the description format this package reads and writes: OpenAPI 3.x
	// documents, served as JSON or YAML.
	Format api.Format = "openapi"

	// Target is the representation this package renders into. It is the same
	// identifier as the format it reads, because an OpenAPI document is both what
	// a caller supplies and what this package publishes.
	Target = "openapi"

	// TransportHTTP is the plain HTTP transport this package calls over.
	TransportHTTP api.Transport = "http"
	// TransportHTTPS is the TLS variant of the same transport.
	TransportHTTPS api.Transport = "https"
)

// Formats returns the format identifiers this package claims.
func Formats() []api.Format { return []api.Format{Format} }

// Targets returns the representations this package renders into.
func Targets() []string { return []string{Target} }

// HandlesFormat reports whether this package reads a format.
func HandlesFormat(format api.Format) bool {
	return api.NormalizeIdentifier(string(format)) == string(Format)
}

// HandlesTarget reports whether this package renders into a target.
func HandlesTarget(target string) bool { return api.NormalizeIdentifier(target) == Target }

// FormatDescriptor describes the format for a catalog's index, so the index can
// answer "can this deployment read an OpenAPI document?" for a format identifier
// the framework itself never enumerates.
func FormatDescriptor() api.FormatDescriptor {
	return api.FormatDescriptor{
		ID:                   string(Format),
		Name:                 "OpenAPI",
		Version:              Version,
		SpecificationVersion: "3.0.3/3.1.0",
		Description:          "OpenAPI 3.x description documents served as JSON or YAML.",
		MediaTypes:           []string{"application/json", "application/yaml", "text/yaml"},
		FileExtensions:       []string{"json", "yaml", "yml"},
	}
}

// TransportDescriptors describes the transports this package can call over.
func TransportDescriptors() []api.TransportDescriptor {
	return []api.TransportDescriptor{
		{
			ID:          string(TransportHTTP),
			Name:        "HTTP",
			Version:     Version,
			Description: "Plain HTTP requests built from an OpenAPI operation.",
			Schemes:     []string{"http"},
		},
		{
			ID:          string(TransportHTTPS),
			Name:        "HTTPS",
			Version:     Version,
			Description: "TLS HTTP requests built from an OpenAPI operation.",
			Schemes:     []string{"https"},
		},
	}
}
