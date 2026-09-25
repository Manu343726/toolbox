package registry

import (
	"context"
	"time"

	"github.com/Manu343726/toolbox/pkg/subsystem"
	registryv1connect "github.com/Manu343726/toolbox/subsystems/registry/registryv1/registryv1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "registry"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the registry subsystem.
type Options struct {
	// Store optionally supplies an existing registry store.
	Store *Memory
	// DefaultLease is used when registrations do not specify a lease.
	DefaultLease time.Duration
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
	// Background is optional registry work.
	Background func(context.Context) error
}

// New is the programmatic in-process entrypoint for the registry subsystem.
func New(options Options) (*subsystem.Server, error) {
	store := options.Store
	if store == nil {
		store = NewMemory(MemoryOptions{DefaultLease: options.DefaultLease})
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	path, handler := registryv1connect.NewRegistryServiceHandler(NewService(store))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   "Stores and resolves independent subsystem registrations.",
		ListenAddress: options.ListenAddress,
		Background:    options.Background,
		Services: []subsystem.Service{{
			Name:    registryv1connect.RegistryServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
}
