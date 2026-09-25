package subsystem

import "context"

// Factory constructs a ready-to-start subsystem server. Feature packages
// expose this as their programmatic in-process entrypoint, for example:
//
//	server, err := workflow.New(workflow.Options{})
//
// The returned server can be started directly, embedded in a host, or handed
// to a standalone command runner.
type Factory func() (*Server, error)

// Component is the small interface consumed by hosts and command runners.
// Server is the reference implementation.
type Component interface {
	Start(context.Context) error
	Serve(context.Context) error
	Shutdown(context.Context) error
	Endpoint() string
	Descriptor() *Descriptor
	Services() []Service
}
