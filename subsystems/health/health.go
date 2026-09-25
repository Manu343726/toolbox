// Package health implements the standalone health subservice.
package health

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	healthv1 "github.com/Manu343726/toolbox/subsystems/health/healthv1"
	"github.com/Manu343726/toolbox/subsystems/health/healthv1/healthv1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "health"
	// Version is the reference implementation version.
	Version = "0.1.0"
)

// Options configures the health subsystem.
type Options struct {
	// ComponentName is reported by Check and defaults to the subsystem name.
	ComponentName string
	// Version overrides the implementation version.
	Version string
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Check supplies an application-specific readiness check.
	Check func(context.Context) error
}

// Handler implements HealthService.
type Handler struct {
	componentName string
	version       string
	check         func(context.Context) error
}

// NewHandler creates a health handler.
func NewHandler(options Options) *Handler {
	componentName := options.ComponentName
	if componentName == "" {
		componentName = Name
	}
	version := options.Version
	if version == "" {
		version = Version
	}
	return &Handler{componentName: componentName, version: version, check: options.Check}
}

// New is the programmatic in-process entrypoint for the health service.
func New(options Options) (*subsystem.Server, error) {
	if options.ComponentName == "" {
		options.ComponentName = Name
	}
	if options.Version == "" {
		options.Version = Version
	}
	path, handler := healthv1connect.NewHealthServiceHandler(NewHandler(options))
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       options.Version,
		Description:   "Health and readiness reporting for Toolbox subsystems.",
		ListenAddress: options.ListenAddress,
		Health:        options.Check,
		Services: []subsystem.Service{{
			Name:    healthv1connect.HealthServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
}

// Check returns health for the requested component.
func (h *Handler) Check(ctx context.Context, req *connect.Request[healthv1.CheckRequest]) (*connect.Response[healthv1.CheckResponse], error) {
	component := ""
	if req != nil && req.Msg != nil {
		component = req.Msg.GetComponentName()
	}
	if component != "" && component != h.componentName {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("component %q is not hosted by %q", component, h.componentName))
	}
	if h.check != nil {
		if err := h.check(ctx); err != nil {
			return connect.NewResponse(&healthv1.CheckResponse{
				Status:  healthv1.HealthStatus_HEALTH_STATUS_NOT_SERVING,
				Message: err.Error(),
				Version: h.version,
			}), nil
		}
	}
	return connect.NewResponse(&healthv1.CheckResponse{
		Status:  healthv1.HealthStatus_HEALTH_STATUS_SERVING,
		Message: "subsystem is serving",
		Version: h.version,
	}), nil
}
