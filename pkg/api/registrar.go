package api

import (
	"context"
	"strings"
)

// Registrar is the write side of a catalog. A deployment registers the servers
// it knows and the APIs they host; the catalog decides who may act on them.
//
// It is the counterpart of Catalog, and it exists so a composition can feed a
// catalog without importing the subsystem that implements one. Registering a
// server and describing an API are separate steps on purpose: a description is
// produced by a parser provider, which may be a different subsystem entirely, and
// the party doing the registering may want to enrich a description — with the
// capabilities a server declared, for instance — before it is stored.
type Registrar interface {
	// RegisterServer records a server, replacing an existing record with the same
	// identifier unless failIfExists is set. The second result reports whether a
	// previous record was replaced.
	RegisterServer(context.Context, Server) (Server, bool, error)
	// DescribeAPI asks a parser provider to interpret a description document, or
	// to read a live endpoint that describes itself, and returns the standard
	// description without storing it.
	DescribeAPI(context.Context, DescribeRequest) (DescribeResult, error)
	// RegisterAPI stores a standard description, bound to the named server.
	RegisterAPI(context.Context, API, string) (API, bool, error)
	// SetExposed records an exposure decision for one operation. The result
	// reports whether the decision changed anything. Exposing is refused — never
	// silently ignored — when the catalog's policy does not allow the operation.
	SetExposed(context.Context, string, string, bool) (bool, error)
}

// DescribeRequest asks for a standard description without storing it.
//
// Either Document or BaseURL describes where the contract comes from. A document
// is a description the caller already holds; a base URL is a live endpoint whose
// own parser provider reads its contract from the server itself, so a caller
// never has to obtain a descriptor to describe a service.
type DescribeRequest struct {
	// Document is the description document, when the caller holds one.
	Document []byte
	// Format identifies the language the document is written in, or the
	// contract the endpoint speaks.
	Format Format
	// BaseURL is a live endpoint that describes itself.
	BaseURL string
	// Source records how the description was obtained.
	Source Source
	// APIID is the identifier to give the described API. Empty lets the parser
	// derive one from the contract.
	APIID string
	// ParserID selects a parser provider explicitly. Empty selects the only
	// provider that claims the format, and is an error when several do.
	ParserID string
}

// DescribeResult is one described API.
type DescribeResult struct {
	// API is the described API.
	API API
	// ProviderID is the parser provider that produced the description.
	ProviderID string
	// Warnings are non-fatal findings about the description.
	Warnings []string
	// Formats are the format descriptors the parser reported, which is how a
	// catalog learns what its deployment can parse.
	Formats []FormatDescriptor
}

// ExposureSource is the optional read side a catalog may offer: what it has
// exposed, and what its policy allows.
//
// A consumer of a catalog that implements this treats the catalog's decisions as
// authoritative for the operations it knows, and falls back to its own policy
// only for operations the catalog has never seen. That fallback is what keeps a
// catalog from silently shrinking a surface it simply has not been told about,
// while a catalog that *has* been told never has its decisions overridden.
type ExposureSource interface {
	// Exposures returns the exposure state of the named operations, keyed by
	// operation identifier. An operation the catalog does not know is absent from
	// the result.
	Exposures(context.Context, []string) (map[string]Exposure, error)
}

// Exposure is one operation's exposure state.
type Exposure struct {
	// OperationID is the operation the state describes.
	OperationID string
	// Allowed reports whether the catalog's policy permits exposing the
	// operation. A denied operation is never a tool, whatever else is true.
	Allowed bool
	// Exposed reports whether the operation is currently exposed.
	Exposed bool
	// Invokable reports whether the operation can be called: it is unary and its
	// API is bound to a server.
	Invokable bool
}

// ExposureMap is an in-memory ExposureSource, used by tests, by a
// configuration-file catalog, and by a deployment that decides exposure before
// the catalog is running.
type ExposureMap map[string]Exposure

// Exposures implements ExposureSource. Operations the map does not mention are
// reported as absent, which is what lets a consumer fall back to its own policy.
func (m ExposureMap) Exposures(_ context.Context, operationIDs []string) (map[string]Exposure, error) {
	result := make(map[string]Exposure, len(operationIDs))
	for _, id := range operationIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if exposure, ok := m[id]; ok {
			result[id] = exposure
		}
	}
	return result, nil
}
