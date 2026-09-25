// Package subsystem provides the transport and lifecycle SDK shared by
// independent Toolbox subsystems. It intentionally knows nothing about any
// feature's protobuf contract; each subsystem supplies its own services.
package subsystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/grpcreflect"
	"github.com/Manu343726/toolbox/pkg/docs"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Service binds a fully-qualified protobuf service name to its ConnectRPC
// handler and process metadata.
type Service struct {
	// Name is the fully-qualified protobuf service name.
	Name string
	// Description is optional metadata for registries and documentation.
	Description string
	// Path is the generated ConnectRPC handler path.
	Path string
	// Handler is the generated ConnectRPC HTTP handler.
	Handler http.Handler
	// Capabilities are semantic operations intentionally exposed by this
	// service. Reflection alone never creates capabilities.
	Capabilities []string
	// Dependencies are subsystem or capability names required by this service.
	Dependencies []string
}

// Config configures a subsystem server.
type Config struct {
	// Name is the stable subsystem name used in the registry.
	Name string
	// Version is the implementation version.
	Version string
	// Description is human-readable subsystem documentation.
	Description string
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Services are the ConnectRPC services mounted by this subsystem.
	Services []Service
	// Documentation is populated from linked descriptors and is available to
	// the subsystem's own documentation service. The common server does not
	// mount a documentation protocol on its own.
	Documentation *docs.Catalog
	// Health optionally determines readiness beyond process liveness.
	Health func(context.Context) error
	// Background runs after the HTTP listener starts. It should return when
	// its context is cancelled.
	Background func(context.Context) error
	// HandshakeWriter receives a JSON handshake after the listener is ready.
	HandshakeWriter io.Writer
	// Capabilities are subsystem-level capabilities merged into the descriptor.
	Capabilities []string
	// Dependencies are subsystem-level dependencies merged into the descriptor.
	Dependencies []string
}

// Handshake is emitted by a running subsystem for a supervising process.
type Handshake struct {
	Protocol          string   `json:"protocol"`
	Endpoint          string   `json:"endpoint"`
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	Description       string   `json:"description"`
	ServiceNames      []string `json:"service_names"`
	ReflectionEnabled bool     `json:"reflection_enabled"`
}

// HandshakeProtocol identifies the process handshake format.
const HandshakeProtocol = "toolbox-subsystem-v1"

// Server owns one subsystem's HTTP/ConnectRPC server lifecycle.
type Server struct {
	config Config
	mux    *http.ServeMux
	http   *http.Server

	mu       sync.Mutex
	listener net.Listener
	endpoint string
	started  bool
	closed   bool
	done     chan struct{}
	serveErr chan error
	bgCancel context.CancelFunc
	bgDone   chan struct{}
}

// NewServer validates cfg and constructs a ready-to-start server.
func NewServer(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, fmt.Errorf("subsystem name is required")
	}
	if strings.TrimSpace(cfg.Version) == "" {
		cfg.Version = "dev"
	}
	if strings.TrimSpace(cfg.ListenAddress) == "" {
		cfg.ListenAddress = "127.0.0.1:0"
	}
	if cfg.Documentation == nil {
		cfg.Documentation = docs.DefaultCatalog()
	}

	mux := http.NewServeMux()
	serviceNames := make([]string, 0, len(cfg.Services))
	paths := make(map[string]string)
	for _, service := range cfg.Services {
		if err := validateService(service); err != nil {
			return nil, err
		}
		if previous, exists := paths[service.Path]; exists {
			return nil, fmt.Errorf("duplicate service path %q (%s and %s)", service.Path, previous, service.Name)
		}
		paths[service.Path] = service.Name
		mux.Handle(service.Path, service.Handler)
		serviceNames = append(serviceNames, service.Name)
		if descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service.Name)); err == nil {
			if serviceDescriptor, ok := descriptor.(protoreflect.ServiceDescriptor); ok {
				if _, existingErr := cfg.Documentation.Get(service.Name); existingErr == nil {
					// Prefer descriptor-set documentation, which retains source
					// comments that generated Go descriptors may omit.
					continue
				}
				_ = cfg.Documentation.AddService(serviceDescriptor)
			}
		}
	}

	reflector := grpcreflect.NewStaticReflector(serviceNames...)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	return &Server{
		config:   cfg,
		mux:      mux,
		http:     &http.Server{Handler: h2c.NewHandler(mux, &http2.Server{})},
		done:     make(chan struct{}),
		serveErr: make(chan error, 1),
	}, nil
}

// Start binds the listener and starts serving in the background.
func (s *Server) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("subsystem %q already started", s.config.Name)
	}
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("subsystem %q is closed", s.config.Name)
	}
	listener, err := net.Listen("tcp", s.config.ListenAddress)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("listen for subsystem %q: %w", s.config.Name, err)
	}
	s.listener = listener
	s.endpoint = endpointForListener(listener)
	s.started = true
	s.mu.Unlock()

	go func() {
		err := s.http.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.serveErr <- err
		close(s.serveErr)
	}()

	if s.config.HandshakeWriter != nil {
		if err := s.WriteHandshake(s.config.HandshakeWriter); err != nil {
			_ = s.Shutdown(context.Background())
			return err
		}
	}

	if s.config.Background != nil {
		backgroundContext, cancel := context.WithCancel(ctx)
		backgroundDone := make(chan struct{})
		s.mu.Lock()
		s.bgCancel = cancel
		s.bgDone = backgroundDone
		s.mu.Unlock()
		go func() {
			defer close(backgroundDone)
			if err := s.config.Background(backgroundContext); err != nil && !errors.Is(err, context.Canceled) {
				select {
				case s.serveErr <- err:
				default:
				}
			}
		}()
	}
	return nil
}

// Serve starts the server and blocks until ctx is cancelled or the HTTP server
// exits with an error.
func (s *Server) Serve(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}
	return s.Wait(ctx)
}

// Wait blocks for a server that has already been started.
func (s *Server) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return s.Shutdown(context.Background())
	case err := <-s.serveErr:
		return err
	}
}

// Endpoint returns the bound HTTP base URL after Start succeeds.
func (s *Server) Endpoint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endpoint
}

// Catalog exposes the documentation catalog built by the server.
func (s *Server) Catalog() *docs.Catalog {
	return s.config.Documentation
}

// Descriptor returns registration metadata for this subsystem.
func (s *Server) Descriptor() *Descriptor {
	s.mu.Lock()
	endpoint := s.endpoint
	s.mu.Unlock()
	serviceNames := make([]string, 0, len(s.config.Services))
	capabilities := append([]string(nil), s.config.Capabilities...)
	dependencies := append([]string(nil), s.config.Dependencies...)
	for _, service := range s.config.Services {
		serviceNames = append(serviceNames, service.Name)
		capabilities = append(capabilities, service.Capabilities...)
		dependencies = append(dependencies, service.Dependencies...)
	}
	return &Descriptor{
		SubsystemName:         s.config.Name,
		Endpoint:              endpoint,
		ImplementationVersion: s.config.Version,
		APIVersion:            "v1",
		ServiceNames:          uniqueSorted(serviceNames),
		Capabilities:          uniqueSorted(capabilities),
		Dependencies:          uniqueSorted(dependencies),
		Description:           s.config.Description,
	}
}

// Descriptor is provider-neutral registration metadata. The registry package
// converts it to its own protobuf contract, keeping subsystem implementations
// independent from any one registry protocol.
type Descriptor struct {
	SubsystemName         string
	Endpoint              string
	ImplementationVersion string
	APIVersion            string
	ServiceNames          []string
	Capabilities          []string
	Dependencies          []string
	Description           string
}

// WriteHandshake writes one machine-readable handshake line.
func (s *Server) WriteHandshake(writer io.Writer) error {
	if writer == nil {
		return nil
	}
	return json.NewEncoder(writer).Encode(Handshake{
		Protocol:          HandshakeProtocol,
		Endpoint:          s.Endpoint(),
		Name:              s.config.Name,
		Version:           s.config.Version,
		Description:       s.config.Description,
		ServiceNames:      uniqueSorted(s.config.ServicesNames()),
		ReflectionEnabled: true,
	})
}

// Shutdown stops background work and gracefully closes the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.bgCancel
	bgDone := s.bgDone
	server := s.http
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	var err error
	if server != nil {
		err = server.Shutdown(ctx)
	}
	if bgDone != nil {
		select {
		case <-bgDone:
		case <-ctx.Done():
		}
	}
	close(s.done)
	return err
}

func (s *Server) Services() []Service {
	return append([]Service(nil), s.config.Services...)
}

func (s *Config) ServicesNames() []string {
	result := make([]string, 0, len(s.Services))
	for _, service := range s.Services {
		result = append(result, service.Name)
	}
	return result
}

func validateService(service Service) error {
	if strings.TrimSpace(service.Name) == "" {
		return fmt.Errorf("service name is required")
	}
	if service.Handler == nil {
		return fmt.Errorf("service %q has no handler", service.Name)
	}
	if service.Path == "" {
		return fmt.Errorf("service %q has no path", service.Name)
	}
	if !strings.HasPrefix(service.Path, "/") {
		return fmt.Errorf("service %q path must start with '/'", service.Name)
	}
	return nil
}

func endpointForListener(listener net.Listener) string {
	address := listener.Addr().String()
	if host, port, err := net.SplitHostPort(address); err == nil && (host == "" || host == "::" || host == "0.0.0.0") {
		return "http://127.0.0.1:" + port
	}
	return "http://" + address
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j] < result[j-1]; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}
