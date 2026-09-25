package api

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalid is returned when a description violates the standard API model.
var ErrInvalid = errors.New("invalid api description")

// ValidationError describes one violated rule. Callers can match it with
// errors.Is(err, ErrInvalid) and still read the specific message.
type ValidationError struct {
	// Path locates the violation inside the description.
	Path string
	// Reason explains the violation.
	Reason string
}

// Error implements error.
func (e *ValidationError) Error() string {
	if e == nil {
		return ErrInvalid.Error()
	}
	if e.Path == "" {
		return fmt.Sprintf("%s: %s", ErrInvalid.Error(), e.Reason)
	}
	return fmt.Sprintf("%s: %s: %s", ErrInvalid.Error(), e.Path, e.Reason)
}

// Unwrap returns ErrInvalid.
func (e *ValidationError) Unwrap() error { return ErrInvalid }

func invalid(path, format string, args ...any) error {
	return &ValidationError{Path: path, Reason: fmt.Sprintf(format, args...)}
}

// Validate reports whether the schema is a well-formed value description.
func (s *Schema) Validate(path string) error {
	if s == nil {
		return nil
	}
	names := make(map[string]bool, len(s.Properties))
	for i, property := range s.Properties {
		propertyPath := fmt.Sprintf("%s.properties[%d]", path, i)
		if strings.TrimSpace(property.Name) == "" {
			return invalid(propertyPath, "property name is required")
		}
		if names[property.Name] {
			return invalid(propertyPath, "property %q is declared more than once", property.Name)
		}
		names[property.Name] = true
		if err := property.Schema.Validate(propertyPath); err != nil {
			return err
		}
	}
	for _, required := range s.Required {
		if !names[required] {
			return invalid(path, "required property %q is not declared", required)
		}
	}
	if s.Type == TypeArray && s.Items == nil {
		return invalid(path, "array schema requires an items schema")
	}
	if s.Minimum != nil && s.Maximum != nil && *s.Minimum > *s.Maximum {
		return invalid(path, "minimum %v is greater than maximum %v", *s.Minimum, *s.Maximum)
	}
	if err := s.Items.Validate(path + ".items"); err != nil {
		return err
	}
	if err := s.AdditionalProperties.Validate(path + ".additionalProperties"); err != nil {
		return err
	}
	for i, alternative := range s.OneOf {
		if err := alternative.Validate(fmt.Sprintf("%s.oneOf[%d]", path, i)); err != nil {
			return err
		}
	}
	for i, alternative := range s.AnyOf {
		if err := alternative.Validate(fmt.Sprintf("%s.anyOf[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

// Validate reports whether the parameter is well formed.
func (p Parameter) Validate(path string) error {
	if strings.TrimSpace(p.Name) == "" {
		return invalid(path, "parameter name is required")
	}
	if strings.TrimSpace(p.In) == "" {
		return invalid(path, "parameter %q has no location", p.Name)
	}
	return p.Schema.Validate(path + "." + p.Name)
}

// Validate reports whether the operation is well formed.
func (o Operation) Validate(path string) error {
	if strings.TrimSpace(o.Name) == "" {
		return invalid(path, "operation name is required")
	}
	if strings.TrimSpace(o.APIID) == "" {
		return invalid(path, "operation %q has no API id", o.Name)
	}
	seen := make(map[string]bool, len(o.Parameters))
	for i, parameter := range o.Parameters {
		parameterPath := fmt.Sprintf("%s.parameters[%d]", path, i)
		if err := parameter.Validate(parameterPath); err != nil {
			return err
		}
		if seen[parameter.Name] {
			return invalid(parameterPath, "parameter %q is declared more than once", parameter.Name)
		}
		seen[parameter.Name] = true
	}
	if err := o.Request.Validate(path + ".request"); err != nil {
		return err
	}
	if err := o.Response.Validate(path + ".response"); err != nil {
		return err
	}
	for i, response := range o.Responses {
		if err := response.Schema.Validate(fmt.Sprintf("%s.responses[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

// Validate reports whether the service is well formed.
func (s Service) Validate(path string) error {
	if strings.TrimSpace(s.Name) == "" {
		return invalid(path, "service name is required")
	}
	seen := make(map[string]bool, len(s.Operations))
	for i, operation := range s.Operations {
		operationPath := fmt.Sprintf("%s.operations[%d]", path, i)
		if err := operation.Validate(operationPath); err != nil {
			return err
		}
		if seen[operation.Name] {
			return invalid(operationPath, "operation %q is declared more than once", operation.Name)
		}
		seen[operation.Name] = true
	}
	return nil
}

// Validate reports whether the API is well formed, and that every operation
// identifier matches the API, service, and operation names it is built from.
// Identifiers must stay derivable, because they are what an exposure decision
// or a tool name refers to.
//
// The format identifier is only required to be present. The framework does not
// know which formats exist, so it cannot reject one it has not seen; deciding
// whether a deployment can actually parse a format belongs to the parser
// provider and the catalog's provider index.
func (a API) Validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return invalid("api", "api id is required")
	}
	if strings.TrimSpace(a.Name) == "" {
		return invalid("api", "api name is required")
	}
	if strings.TrimSpace(a.Format) == "" {
		return invalid("api", "api %q has no format", a.ID)
	}
	servers := make(map[string]bool, len(a.ServerIDs))
	for _, serverID := range a.ServerIDs {
		if strings.TrimSpace(serverID) == "" {
			return invalid("api.server_ids", "server id cannot be empty")
		}
		if servers[serverID] {
			return invalid("api.server_ids", "server %q is referenced more than once", serverID)
		}
		servers[serverID] = true
	}
	if len(a.Services) == 0 {
		return invalid("api.services", "api %q declares no services", a.ID)
	}
	seenServices := make(map[string]bool, len(a.Services))
	operationIDs := make(map[string]bool)
	for i, service := range a.Services {
		path := fmt.Sprintf("api.services[%d]", i)
		if err := service.Validate(path); err != nil {
			return err
		}
		if seenServices[service.Name] {
			return invalid(path, "service %q is declared more than once", service.Name)
		}
		seenServices[service.Name] = true
		if service.ID != "" && service.ID != ServiceID(a.ID, service.Name) {
			return invalid(path, "service %q has id %q, expected %q", service.Name, service.ID, ServiceID(a.ID, service.Name))
		}
		for _, operation := range service.Operations {
			expected := OperationID(a.ID, service.Name, operation.Name)
			if operation.ID != expected {
				return invalid(path, "operation %q has id %q, expected %q", operation.Name, operation.ID, expected)
			}
			if operation.APIID != a.ID || operation.Service != service.Name {
				return invalid(path, "operation %q does not belong to %q/%q", operation.Name, a.ID, service.Name)
			}
			if operationIDs[operation.ID] {
				return invalid(path, "operation id %q is used more than once", operation.ID)
			}
			operationIDs[operation.ID] = true
		}
	}
	return nil
}

// Validate reports whether the server registration is well formed.
func (s Server) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return invalid("server", "server id is required")
	}
	if strings.TrimSpace(s.Name) == "" {
		return invalid("server", "server name is required")
	}
	if strings.TrimSpace(s.BaseURL) == "" {
		return invalid("server", "server %q has no base url", s.ID)
	}
	return nil
}

// Validate reports whether the format descriptor is well formed. A descriptor
// only needs an identifier and a name: everything else is metadata a consumer
// may or may not use.
func (d FormatDescriptor) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return invalid("format", "format descriptor id is required")
	}
	if strings.TrimSpace(d.Name) == "" {
		return invalid("format", "format descriptor %q has no name", d.ID)
	}
	return nil
}

// Validate reports whether the transport descriptor is well formed.
func (d TransportDescriptor) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return invalid("transport", "transport descriptor id is required")
	}
	if strings.TrimSpace(d.Name) == "" {
		return invalid("transport", "transport descriptor %q has no name", d.ID)
	}
	return nil
}

// Normalize trims and fills the defaults a server registration needs before it
// is stored.
func (s *Server) Normalize(now time.Time) error {
	if s == nil {
		return fmt.Errorf("server is nil")
	}
	s.ID = strings.TrimSpace(s.ID)
	s.Name = strings.TrimSpace(s.Name)
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	s.Format = strings.TrimSpace(s.Format)
	s.Transport = strings.TrimSpace(s.Transport)
	s.Description = strings.TrimSpace(s.Description)
	if s.Status == ServerStatusUnspecified {
		s.Status = ServerStatusUnknown
	}
	if s.RegisteredAt.IsZero() && !now.IsZero() {
		s.RegisteredAt = now
	}
	s.Capabilities = normalizeStrings(s.Capabilities)
	return s.Validate()
}

// Normalize trims and fills the defaults a description needs before it is
// stored: identifiers are annotated, repeated values are normalized, and the
// result is validated. It returns the normalized API.
func (a *API) Normalize() (API, error) {
	if a == nil {
		return API{}, fmt.Errorf("api is nil")
	}
	normalized := a.Clone()
	normalized.ID = strings.TrimSpace(normalized.ID)
	normalized.Name = strings.TrimSpace(normalized.Name)
	normalized.Title = strings.TrimSpace(normalized.Title)
	normalized.Version = strings.TrimSpace(normalized.Version)
	normalized.Format = strings.TrimSpace(normalized.Format)
	normalized.Source.Kind = strings.TrimSpace(normalized.Source.Kind)
	normalized.Source.Location = strings.TrimSpace(normalized.Source.Location)
	normalized.ServerIDs = normalizeStrings(normalized.ServerIDs)
	normalized.Capabilities = normalizeStrings(normalized.Capabilities)
	normalized.Tags = normalizeStrings(normalized.Tags)
	for i := range normalized.Services {
		normalized.Services[i].Capabilities = normalizeStrings(normalized.Services[i].Capabilities)
		for j := range normalized.Services[i].Operations {
			operation := &normalized.Services[i].Operations[j]
			operation.Capabilities = normalizeStrings(operation.Capabilities)
			operation.Tags = normalizeStrings(operation.Tags)
			operation.SideEffects = normalizeSideEffects(operation.SideEffects)
			operation.Name = strings.TrimSpace(operation.Name)
			operation.Service = strings.TrimSpace(operation.Service)
			operation.APIID = strings.TrimSpace(operation.APIID)
			operation.ID = strings.TrimSpace(operation.ID)
			operation.Summary = strings.TrimSpace(operation.Summary)
			if operation.Summary == "" {
				operation.Summary = strings.TrimSpace(operation.Description)
			}
		}
	}
	if err := normalized.AnnotateOperationIDs(); err != nil {
		return API{}, err
	}
	if err := normalized.Validate(); err != nil {
		return API{}, err
	}
	return normalized, nil
}

// normalizeSideEffects keeps the declared effects in a stable order and
// preserves unknown identifiers: a format the framework does not know may
// declare effects this framework has never seen, and dropping them would make
// a consequential operation look harmless.
func normalizeSideEffects(values []SideEffect) []SideEffect {
	normalized := normalizeStrings(values)
	sortStable(normalized)
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeStrings(values []string) []string {
	if values == nil {
		return nil
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
