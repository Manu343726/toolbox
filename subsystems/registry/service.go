package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
)

// Service exposes a Memory registry through the platform RegistryService.
type Service struct {
	store *Memory
}

// NewService creates a ConnectRPC registry handler.
func NewService(store *Memory) *Service {
	if store == nil {
		store = NewMemory(MemoryOptions{})
	}
	return &Service{store: store}
}

// Store returns the underlying in-memory store for embedding and tests.
func (s *Service) Store() *Memory {
	return s.store
}

// Register stores a service descriptor.
func (s *Service) Register(_ context.Context, req *connect.Request[registryv1.RegisterRequest]) (*connect.Response[registryv1.RegisterResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetDescriptor_() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("descriptor is required"))
	}
	lease, err := durationFromSeconds(req.Msg.GetLeaseSeconds())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	descriptor, err := s.store.Register(req.Msg.GetDescriptor_(), lease)
	if err != nil {
		return nil, registryError(err)
	}
	return connect.NewResponse(&registryv1.RegisterResponse{Descriptor_: descriptor}), nil
}

// Heartbeat extends a service lease.
func (s *Service) Heartbeat(_ context.Context, req *connect.Request[registryv1.HeartbeatRequest]) (*connect.Response[registryv1.HeartbeatResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("heartbeat request is required"))
	}
	lease, err := durationFromSeconds(req.Msg.GetLeaseSeconds())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	descriptor, err := s.store.Heartbeat(req.Msg.GetSubsystemName(), lease)
	if err != nil {
		return nil, registryError(err)
	}
	return connect.NewResponse(&registryv1.HeartbeatResponse{Descriptor_: descriptor}), nil
}

// Deregister removes a service registration.
func (s *Service) Deregister(_ context.Context, req *connect.Request[registryv1.DeregisterRequest]) (*connect.Response[registryv1.DeregisterResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetSubsystemName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("subsystem_name is required"))
	}
	return connect.NewResponse(&registryv1.DeregisterResponse{Removed: s.store.Deregister(req.Msg.GetSubsystemName())}), nil
}

// GetService returns one active service registration.
func (s *Service) GetService(_ context.Context, req *connect.Request[registryv1.GetServiceRequest]) (*connect.Response[registryv1.GetServiceResponse], error) {
	if req == nil || req.Msg == nil || req.Msg.GetSubsystemName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("subsystem_name is required"))
	}
	descriptor, err := s.store.Get(req.Msg.GetSubsystemName())
	if err != nil {
		return nil, registryError(err)
	}
	return connect.NewResponse(&registryv1.GetServiceResponse{Descriptor_: descriptor}), nil
}

// ListServices returns matching active registrations.
func (s *Service) ListServices(_ context.Context, req *connect.Request[registryv1.ListServicesRequest]) (*connect.Response[registryv1.ListServicesResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("list request is required"))
	}
	return connect.NewResponse(&registryv1.ListServicesResponse{
		Services: s.store.List(req.Msg.GetSubsystemName(), req.Msg.GetRequiredCapabilities(), req.Msg.GetIncludeExpired()),
	}), nil
}

func durationFromSeconds(seconds int64) (time.Duration, error) {
	if seconds < 0 {
		return 0, fmt.Errorf("lease_seconds cannot be negative")
	}
	return time.Duration(seconds) * time.Second, nil
}

func registryError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrLeaseExpired):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrInvalidRegistration):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
