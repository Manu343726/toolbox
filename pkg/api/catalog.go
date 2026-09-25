package api

import (
	"context"
	"encoding/json"
)

// Catalog is the read side an adapter consumes: the servers that are
// registered and the APIs they host. It is deliberately narrow so the storage
// behind it can be an in-memory subsystem, a ConnectRPC client, or a
// configuration file. Implementations return deterministic, sorted results.
type Catalog interface {
	// Servers returns the registered servers, ordered by identifier.
	Servers(context.Context) ([]Server, error)
	// APIs returns the registered APIs, ordered by identifier.
	APIs(context.Context) ([]API, error)
}

// Call is one invocation of one operation.
type Call struct {
	// Server is the registered server that hosts the operation.
	Server Server
	// API is the API that owns the operation.
	API API
	// Operation is the operation to invoke.
	Operation Operation
	// Arguments carries the caller's values as a JSON object. A key that
	// matches a declared parameter supplies that parameter; a "body" key
	// supplies the request value. Keys that match neither are an error, so a
	// misspelled argument is reported instead of silently dropped.
	Arguments json.RawMessage
}

// Result is the outcome of one invocation.
type Result struct {
	// Status is the transport status code, or 0 when the transport has no
	// status concept.
	Status int
	// ContentType is the media type of the body.
	ContentType string
	// Headers are the response headers worth keeping.
	Headers map[string][]string
	// Body is the response value as JSON.
	Body json.RawMessage
}

// Invoker executes one operation of a registered API. Adapters implement it per
// description format; authorization and exposure are decided before an Invoker
// is called, never inside one.
type Invoker interface {
	Invoke(context.Context, Call) (Result, error)
}

// InvokerFunc adapts a function to Invoker.
type InvokerFunc func(context.Context, Call) (Result, error)

// Invoke implements Invoker.
func (f InvokerFunc) Invoke(ctx context.Context, call Call) (Result, error) {
	return f(ctx, call)
}

// StaticCatalog is an in-memory Catalog. It is useful for tests, for a
// configuration-file catalog, and as the catalog an adapter sees when a
// deployment registers APIs programmatically.
type StaticCatalog struct {
	// RegisteredServers are the registered servers.
	RegisteredServers []Server
	// RegisteredAPIs are the registered APIs.
	RegisteredAPIs []API
	// ExposedOperations are the exposure decisions this catalog holds, keyed by
	// operation identifier. A catalog that carries them answers for them, so a
	// gateway does not decide differently.
	ExposedOperations ExposureMap
}

// Exposures implements ExposureSource, so a static catalog's decisions are
// authoritative for the operations it carries.
func (c *StaticCatalog) Exposures(ctx context.Context, operationIDs []string) (map[string]Exposure, error) {
	if c == nil || c.ExposedOperations == nil {
		return nil, nil
	}
	return c.ExposedOperations.Exposures(ctx, operationIDs)
}

// Servers returns the registered servers, ordered by identifier.
func (c *StaticCatalog) Servers(context.Context) ([]Server, error) {
	if c == nil {
		return nil, nil
	}
	result := make([]Server, 0, len(c.RegisteredServers))
	for _, server := range c.RegisteredServers {
		result = append(result, server.Clone())
	}
	return result, nil
}

// APIs returns the registered APIs, ordered by identifier.
func (c *StaticCatalog) APIs(context.Context) ([]API, error) {
	if c == nil {
		return nil, nil
	}
	result := make([]API, 0, len(c.RegisteredAPIs))
	for _, api := range c.RegisteredAPIs {
		result = append(result, api.Clone())
	}
	return result, nil
}
