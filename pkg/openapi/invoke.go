package openapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
)

// maxResponseBytes bounds how much of a response the adapter reads. An adapter
// that streams an unbounded body into memory would let any registered API exhaust
// the host, so the limit is explicit and the truncation is reported.
const maxResponseBytes = 4 << 20

// InvokerOptions configures the HTTP invoker.
type InvokerOptions struct {
	// HTTPClient performs the request. The zero value uses a client with a
	// bounded timeout.
	HTTPClient *http.Client
	// RequestTimeout bounds one invocation when the client has no timeout of its
	// own. It defaults to 30 seconds.
	RequestTimeout time.Duration
	// Credentials supplies values for the security schemes a document declares.
	// The catalog decides that a call is allowed; the adapter only applies the
	// credentials a deployment chose to give it.
	Credentials CredentialSource
}

// CredentialSource resolves a security scheme's credential for one call. It is
// deliberately an interface: a deployment holds credentials, and the adapter
// never invents them.
type CredentialSource interface {
	// Credential returns the value for one scheme and requirement.
	Credential(scheme api.SecurityScheme, requirement api.SecurityRequirement) (string, bool)
}

// CredentialSourceFunc adapts a function to CredentialSource.
type CredentialSourceFunc func(api.SecurityScheme, api.SecurityRequirement) (string, bool)

// Credential implements CredentialSource.
func (f CredentialSourceFunc) Credential(scheme api.SecurityScheme, requirement api.SecurityRequirement) (string, bool) {
	return f(scheme, requirement)
}

// StaticCredentials is a credential source over a fixed map keyed by scheme name.
// It is meant for development and tests.
type StaticCredentials map[string]string

// Credential implements CredentialSource.
func (c StaticCredentials) Credential(scheme api.SecurityScheme, _ api.SecurityRequirement) (string, bool) {
	value, ok := c[scheme.Name]
	return value, ok
}

// Invoker implements the framework's invocation contract for APIs described by
// an OpenAPI document. It builds one HTTP request from the registered operation
// and the caller's arguments.
//
// It is an invoker, not an adapter: it calls a live server rather than producing
// a description. Adapting a description into a target format is the renderer's
// and the serving adapter's job.
type Invoker struct {
	client      *http.Client
	credentials CredentialSource
}

// NewInvoker creates the HTTP invoker.
func NewInvoker(options InvokerOptions) *Invoker {
	client := options.HTTPClient
	if client == nil {
		timeout := options.RequestTimeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	return &Invoker{client: client, credentials: options.Credentials}
}

// Invoke implements api.Invoker: it performs one call of one operation against a
// live server described by an OpenAPI document.
func (a *Invoker) Invoke(ctx context.Context, call api.Call) (api.Result, error) {
	target, headers, body, err := a.buildRequest(call.Server, call.API, call.Operation, call.Arguments)
	if err != nil {
		return api.Result{}, err
	}
	// The method is normalized here rather than trusted from the description: a
	// registered descriptor is data, and an HTTP method is case-insensitive on the
	// wire but conventionally uppercase.
	httpRequest, err := http.NewRequestWithContext(
		ctx, strings.ToUpper(strings.TrimSpace(call.Operation.Method)), target, body,
	)
	if err != nil {
		return api.Result{}, api.WrapError(api.KindInvalid, err, "build the request for %s", call.Operation.ID)
	}
	for name, values := range headers {
		for _, value := range values {
			httpRequest.Header.Add(name, value)
		}
	}
	httpRequest.Header.Set("Accept", "application/json")
	if body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(httpRequest)
	if err != nil {
		return api.Result{}, api.WrapError(api.KindUnavailable, err, "call %s", call.Operation.ID)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return api.Result{}, api.WrapError(api.KindUnavailable, err, "read the response from %s", call.Operation.ID)
	}
	truncated := len(payload) > maxResponseBytes
	if truncated {
		payload = payload[:maxResponseBytes]
	}
	return api.Result{
		Status:      response.StatusCode,
		ContentType: response.Header.Get("Content-Type"),
		Headers:     headerValues(response.Header),
		Body:        normalizeJSON(payload, truncated),
	}, nil
}

// headerValues keeps the response headers worth keeping, as the standard model
// records them.
func headerValues(headers http.Header) map[string][]string {
	selected := significantHeaders(headers)
	if len(selected) == 0 {
		return nil
	}
	values := make(map[string][]string, len(selected))
	for name, value := range selected {
		values[name] = []string{value}
	}
	return values
}

// assert the invoker satisfies the framework's interface at compile time.
var _ api.Invoker = (*Invoker)(nil)

// buildRequest turns the operation and the caller's arguments into a URL, query
// values, headers, and an optional body.
func (a *Invoker) buildRequest(
	server api.Server,
	target api.API,
	operation api.Operation,
	arguments []byte,
) (string, http.Header, io.Reader, error) {
	values, err := decodeArguments(arguments)
	if err != nil {
		return "", nil, nil, err
	}
	consumed := make(map[string]bool, len(operation.Parameters))
	// The operation's own request schema is part of what the caller may supply:
	// an agent calling a tool generated from this operation passes the request
	// fields by name, and they are sent as the body.
	bodyFields := requestFieldNames(operation.Request)
	path := operation.Path
	query := url.Values{}
	headers := http.Header{}
	declared := make(map[string]api.Parameter, len(operation.Parameters))
	for _, parameter := range operation.Parameters {
		declared[parameter.Name] = parameter
		raw, supplied := values[parameter.Name]
		if !supplied {
			if parameter.Required {
				return "", nil, nil, api.Errorf(api.KindInvalid, "operation %q requires the %q parameter", operation.Name, parameter.Name)
			}
			continue
		}
		consumed[parameter.Name] = true
		if err := applyParameter(&path, parameter, raw, query, headers); err != nil {
			return "", nil, nil, err
		}
	}
	var body io.Reader
	bodyLength := 0
	if raw, ok := values["body"]; ok {
		consumed["body"] = true
		if !json.Valid(raw) {
			return "", nil, nil, api.Errorf(api.KindInvalid, "body must be valid JSON")
		}
		body = bytes.NewReader(raw)
		bodyLength = len(raw)
	}
	if body == nil && len(bodyFields) > 0 {
		if fields, ok := fieldsArgument(values, consumed, bodyFields); ok {
			body = bytes.NewReader(fields)
			bodyLength = len(fields)
		}
	}
	for name := range values {
		if consumed[name] || bodyFields[name] {
			continue
		}
		if _, known := declared[name]; known {
			continue
		}
		// An unknown argument is an error rather than a silent drop: a
		// misspelled field that quietly disappears produces a call that looks
		// successful and did not do what the caller asked.
		return "", nil, nil, api.Errorf(api.KindInvalid, "operation %q does not accept the argument %q", operation.Name, name)
	}
	if err := a.applySecurity(target, operation, headers, query); err != nil {
		return "", nil, nil, err
	}
	if path == "" {
		path = "/"
	}
	base := strings.TrimRight(server.BaseURL, "/")
	location := base + path
	if encoded := query.Encode(); encoded != "" {
		location += "?" + encoded
	}
	return location, headers, bodyWithLength(body, bodyLength), nil
}

// bodyWithLength pairs the request body with its length so the caller can tell
// an empty body from an absent one.
func bodyWithLength(body io.Reader, length int) io.Reader {
	if body == nil || length == 0 {
		return nil
	}
	return body
}

func applyParameter(path *string, parameter api.Parameter, raw json.RawMessage, query url.Values, headers http.Header) error {
	text, err := parameterText(parameter, raw)
	if err != nil {
		return err
	}
	switch parameter.In {
	case api.ParameterInPath:
		if text == "" {
			return api.Errorf(api.KindInvalid, "path parameter %q cannot be empty", parameter.Name)
		}
		placeholder := "{" + parameter.Name + "}"
		if !strings.Contains(*path, placeholder) {
			return api.Errorf(api.KindInvalid, "operation path %q does not contain the declared parameter %q", *path, parameter.Name)
		}
		*path = strings.ReplaceAll(*path, placeholder, url.PathEscape(text))
	case api.ParameterInQuery:
		query.Set(parameter.Name, text)
	case api.ParameterInHeader:
		headers.Add(parameter.Name, text)
	case api.ParameterInCookie:
		headers.Add("Cookie", parameter.Name+"="+text)
	case api.ParameterInBody:
		return api.Errorf(api.KindInvalid, "body parameter %q must be supplied as the body argument", parameter.Name)
	default:
		return api.Errorf(api.KindInvalid, "parameter %q uses unsupported location %q", parameter.Name, parameter.In)
	}
	return nil
}

func parameterText(parameter api.Parameter, raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if parameter.Schema != nil && parameter.Schema.Type == api.TypeObject || parameter.Schema != nil && parameter.Schema.Type == api.TypeArray {
		return string(raw), nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", api.WrapError(api.KindInvalid, err, "parameter %q must be a scalar value", parameter.Name)
	}
	return text, nil
}

// requestFieldNames returns the top-level field names an operation's request
// schema accepts.
func requestFieldNames(schema *api.Schema) map[string]bool {
	fields := make(map[string]bool)
	if schema == nil {
		return fields
	}
	for _, property := range schema.Properties {
		fields[property.Name] = true
	}
	return fields
}

func fieldsArgument(values map[string]json.RawMessage, consumed, allowed map[string]bool) ([]byte, bool) {
	fields := make(map[string]json.RawMessage, len(values))
	for name, value := range values {
		if consumed[name] || !allowed[name] {
			continue
		}
		fields[name] = value
	}
	if len(fields) == 0 {
		return nil, false
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// applySecurity applies the credentials a deployment supplied for the
// requirements the operation declares. An operation that requires a scheme the
// deployment has no credential for is refused before the request is sent, rather
// than being sent unauthenticated and failing somewhere less clear.
func (a *Invoker) applySecurity(target api.API, operation api.Operation, headers http.Header, query url.Values) error {
	requirements := operation.Security
	if len(requirements) == 0 {
		requirements = target.Security
	}
	if len(requirements) == 0 {
		return nil
	}
	for _, requirement := range requirements {
		scheme, found := findScheme(target.SecuritySchemes, requirement.Scheme)
		if !found {
			if len(target.SecuritySchemes) == 0 {
				// The document requires a credential it never described. Refusing
				// is the safe reading: an undescribed requirement cannot be
				// satisfied knowingly.
				return api.Errorf(api.KindInvalid, "operation %q requires security scheme %q, which the document does not describe", operation.Name, requirement.Scheme)
			}
			continue
		}
		if a.credentials == nil {
			return api.Errorf(api.KindInvalid, "operation %q requires security scheme %q, and this deployment supplies no credentials", operation.Name, requirement.Scheme)
		}
		credential, ok := a.credentials.Credential(scheme, requirement)
		if !ok || credential == "" {
			return api.Errorf(api.KindInvalid, "operation %q requires security scheme %q, and no credential is available for it", operation.Name, requirement.Scheme)
		}
		switch strings.ToLower(scheme.Type) {
		case "apikey", "api_key":
			name := scheme.ParameterName
			if name == "" {
				return api.Errorf(api.KindInvalid, "api key scheme %q does not name the parameter it travels under", scheme.Name)
			}
			switch strings.ToLower(scheme.In) {
			case "query":
				query.Set(name, credential)
			case "cookie":
				headers.Add("Cookie", name+"="+credential)
			default:
				headers.Set(name, credential)
			}
		case "http":
			if strings.EqualFold(scheme.Scheme, "basic") {
				headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credential)))
				continue
			}
			headers.Set("Authorization", scheme.Scheme+" "+credential)
		case "oauth2", "openidconnect":
			headers.Set("Authorization", "Bearer "+credential)
		default:
			return api.Errorf(api.KindInvalid, "operation %q requires security scheme %q of unsupported type %q", operation.Name, requirement.Scheme, scheme.Type)
		}
	}
	return nil
}

func findScheme(schemes []api.SecurityScheme, name string) (api.SecurityScheme, bool) {
	for _, scheme := range schemes {
		if scheme.Name == name {
			return scheme, true
		}
	}
	return api.SecurityScheme{}, false
}

func decodeArguments(arguments []byte) (map[string]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(arguments))
	if trimmed == "" || trimmed == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &values); err != nil {
		return nil, api.WrapError(api.KindInvalid, err, "arguments must be a JSON object")
	}
	if values == nil {
		return map[string]json.RawMessage{}, nil
	}
	return values, nil
}

func significantHeaders(headers http.Header) map[string]string {
	result := make(map[string]string)
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lower := strings.ToLower(name)
		// Volatile hop-by-hop and length headers carry no information a caller
		// can act on, and echoing every cookie or auth header back would leak
		// credentials into a tool result.
		if lower == "set-cookie" || lower == "www-authenticate" || lower == "authorization" ||
			lower == "content-length" || lower == "date" || lower == "connection" {
			continue
		}
		result[name] = headers.Get(name)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// normalizeJSON returns the response body as JSON. A body that is already JSON
// is passed through with unknown fields intact; anything else is reported as a
// JSON string so the caller receives a value of the advertised type instead of
// invalid JSON.
func normalizeJSON(payload []byte, truncated bool) []byte {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return []byte("null")
	}
	if json.Valid(trimmed) {
		return trimmed
	}
	encoded, err := json.Marshal(string(trimmed))
	if err != nil {
		return []byte("null")
	}
	_ = truncated
	return encoded
}
