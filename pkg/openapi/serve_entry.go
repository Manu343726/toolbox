package openapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
)

// Serve mounts an API in the OpenAPI target format and forwards its requests to
// the original server.
//
// The description says what the surface looks like; the server says where the
// work happens. This package never implements an operation: it renders the
// target's schema, mounts the operations the description declares, and tunnels
// every request to the original server, so the served surface and the original
// API cannot drift apart.
func (s *Surfaces) Serve(described api.API, original api.Server, options ServeOptions) (*Served, error) {
	if strings.TrimSpace(original.BaseURL) == "" {
		return nil, api.Errorf(
			api.KindInvalid,
			"the original server has no base url; an adapted surface has to forward somewhere",
		)
	}
	baseURL, err := url.Parse(original.BaseURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, api.Errorf(api.KindInvalid, "the original server has an invalid base url %q", original.BaseURL)
	}
	routes, warnings, err := adaptedRoutes(described)
	if err != nil {
		return nil, api.WrapError(api.KindInvalid, err, "adapt the operations of %q", described.ID)
	}
	// The base path is chosen so that nothing this package mounts can collide with
	// anything the adapted API itself serves. A caller may propose one; it is
	// still refused when the proposal would collide, because a moved prefix would
	// invalidate the URLs the caller was told about.
	basePath, collision, err := resolveBasePath(strings.TrimSpace(options.BasePath), routes, original.BaseURL)
	if err != nil {
		return nil, err
	}
	if collision != "" {
		warnings = append(warnings, fmt.Sprintf(
			"the original server already serves %q; the adapted surface uses a different base path", collision,
		))
	}
	// The schema is rendered once per surface, in both serializations, so the
	// documentation page, the mounted routes, and the downloadable schema cannot
	// describe different APIs.
	jsonDocument, err := s.schema(described, "application/json")
	if err != nil {
		return nil, err
	}
	yamlDocument, err := s.schema(described, "application/yaml")
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: s.timeout}
	instanceID := instanceIdentifier(described.ID, basePath)
	listener, err := net.Listen("tcp", listenAddress(options.ListenAddress))
	if err != nil {
		return nil, api.WrapError(api.KindUnavailable, err, "listen for the adapted surface")
	}
	mux := http.NewServeMux()
	handler := s.handler(instanceID, basePath, baseURL, jsonDocument, yamlDocument, routes, client)
	mux.Handle(basePath+"/", handler)
	mux.Handle(basePath, handler)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	instance := &servedInstance{
		server:   server,
		endpoint: "http://" + listener.Addr().String() + basePath,
		client:   client,
	}
	s.mu.Lock()
	if existing, duplicate := s.instances[instanceID]; duplicate {
		s.mu.Unlock()
		_ = server.Close()
		return nil, api.Errorf(api.KindAlreadyExists, "api %q is already served at %s", described.ID, existing.endpoint)
	}
	s.instances[instanceID] = instance
	s.mu.Unlock()

	paths := make([]string, 0, len(routes))
	for _, route := range routes {
		paths = append(paths, basePath+route.path)
	}
	sort.Strings(paths)
	return &Served{
		Target:                Target,
		InstanceID:            instanceID,
		Endpoint:              instance.endpoint,
		BasePath:              basePath,
		Paths:                 paths,
		DocumentationEndpoint: basePath + "/" + reservedDocsSegment + "/",
		SchemaEndpoint:        basePath + "/" + reservedSchemaSegment + "/" + schemaFileJSON,
		Warnings:              warnings,
	}, nil
}

// schema renders the target's schema in one serialization.
func (s *Surfaces) schema(described api.API, mediaType string) ([]byte, error) {
	rendered, err := Render(described, RenderOptions{MediaType: mediaType})
	if err != nil {
		return nil, err
	}
	document, err := primaryDocument(rendered.Files)
	if err != nil {
		return nil, err
	}
	return document, nil
}

// Stop ends one adapted surface. It reports whether a surface was running, so a
// stop for an unknown instance is answered honestly rather than refused.
func (s *Surfaces) Stop(instanceID string) bool {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return false
	}
	s.mu.Lock()
	instance, ok := s.instances[instanceID]
	delete(s.instances, instanceID)
	s.mu.Unlock()
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = instance.server.Shutdown(ctx)
	return true
}

// Instances returns the identifiers of the surfaces currently served.
func (s *Surfaces) Instances() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	identifiers := make([]string, 0, len(s.instances))
	for identifier := range s.instances {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)
	return identifiers
}
