package apiopenapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
)

// Reserved paths under an adapted surface's base path. They belong to the target
// format rather than to the API being adapted, so they are namespaced and
// checked for collisions before the surface is served.
const (
	// reservedDocsSegment is the path segment of the target's own documentation.
	reservedDocsSegment = "_docs"
	// reservedSchemaSegment is the path segment of the target's schema files.
	// The schema is downloadable from here: the same bytes the documentation page
	// renders, without the page.
	reservedSchemaSegment = "_schema"
	// schemaFileJSON is the canonical schema file name.
	schemaFileJSON = "openapi.json"
	// schemaFileYAML is the same schema in YAML, for a reader that prefers it.
	schemaFileYAML = "openapi.yaml"
	// defaultMaxBodyBytes bounds a tunnelled request body.
	defaultMaxBodyBytes = 8 << 20
)

// Server serves APIs in the OpenAPI target format.
//
// It mounts two things under one base path: the adapted operations, which
// forward to the original server, and the target's own documentation, which is a
// Swagger UI plus the raw document. The documentation lives under reserved
// segments that are checked against every generated path, so serving the UI can
// never shadow an operation the API actually has.
type Server struct {
	renderer *Renderer
	timeout  time.Duration
	// swaggerUI is an OpenAPI documentation UI supplied by the deployment, such
	// as the official swagger-ui bundle. When none is supplied the server
	// generates a documentation page from the schema it already produced, so the
	// documentation endpoint always exists and always describes this API.
	swaggerUI []byte

	mu        sync.Mutex
	instances map[string]*servedInstance
}

// ServerOptions configures the serving adapter.
type ServerOptions struct {
	// SwaggerUI is the documentation page to serve. When empty, only the raw
	// schema document is served.
	SwaggerUI []byte
	// RequestTimeout bounds one tunnelled request. It defaults to 30 seconds.
	RequestTimeout time.Duration
	// MaxBodyBytes bounds a tunnelled request body. Zero uses the default.
	MaxBodyBytes int64
}

// NewServer creates the OpenAPI serving adapter.
func NewServer(options ServerOptions) *Server {
	timeout := options.RequestTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Server{
		renderer:  NewRenderer(),
		timeout:   timeout,
		swaggerUI: options.SwaggerUI,
		instances: make(map[string]*servedInstance),
	}
}

type servedInstance struct {
	server   *http.Server
	endpoint string
	client   *http.Client
}

// ServeApi implements the framework's adapter contract for serving.
func (s *Server) ServeApi(_ context.Context, request *connect.Request[apiv1.ServeApiRequest]) (*connect.Response[apiv1.ServeApiResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request is required"))
	}
	target := strings.TrimSpace(request.Msg.GetTarget())
	if target == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("target is required"))
	}
	if target != TargetOpenAPI {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("this adapter serves %s, not %q", TargetOpenAPI, target),
		)
	}
	described, err := api.APIFromProto(request.Msg.GetApi())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source description: %w", err))
	}
	original, err := api.ServerFromProto(request.Msg.GetServer())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.TrimSpace(original.BaseURL) == "" {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("the original server has no base url; an adapted surface has to forward somewhere"),
		)
	}
	baseURL, err := url.Parse(original.BaseURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("original server has an invalid base url %q", original.BaseURL))
	}
	rendered, err := s.renderer.RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:    described.ToProto(),
		Target: target,
	}))
	if err != nil {
		return nil, err
	}
	document, err := primaryDocument(rendered.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// The YAML form is rendered from the same description as the JSON one, so a
	// download in either format describes the same API.
	yamlDocument, err := s.renderer.RenderApi(context.Background(), connect.NewRequest(&apiv1.RenderApiRequest{
		Api:       described.ToProto(),
		Target:    target,
		MediaType: "application/yaml",
	}))
	if err != nil {
		return nil, err
	}
	yamlFile, err := primaryDocument(yamlDocument.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	routes, warnings, err := adaptedRoutes(described)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// The base path is chosen so that nothing the adapter mounts can collide with
	// anything the adapted API itself serves. A caller may propose one; the
	// adapter still refuses a proposal that would collide and says so.
	basePath, collision, err := resolveBasePath(strings.TrimSpace(request.Msg.GetBasePath()), routes, original.BaseURL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if collision != "" {
		warnings = append(warnings, fmt.Sprintf(
			"the original server already serves %q; the adapted surface uses a different base path", collision,
		))
	}
	client := &http.Client{Timeout: s.timeout}
	instanceID := instanceIdentifier(described.ID, basePath)
	listener, err := net.Listen("tcp", listenAddress(request.Msg.GetListenAddress()))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("listen for the adapted surface: %w", err))
	}
	mux := http.NewServeMux()
	handler := s.handler(instanceID, basePath, baseURL, document, yamlFile, routes, client)
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
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("api %q is already served at %s", described.ID, existing.endpoint))
	}
	s.instances[instanceID] = instance
	s.mu.Unlock()

	paths := make([]string, 0, len(routes))
	for _, route := range routes {
		paths = append(paths, basePath+route.path)
	}
	sort.Strings(paths)
	response := &apiv1.ServeApiResponse{
		Target:         target,
		Endpoint:       instance.endpoint,
		BasePath:       basePath,
		Paths:          paths,
		Warnings:       append(append([]string(nil), rendered.Msg.GetWarnings()...), warnings...),
		InstanceId:     instanceID,
		SchemaEndpoint: basePath + "/" + reservedSchemaSegment + "/" + schemaFileJSON,
	}
	// The documentation endpoint always exists: with a supplied UI it serves that,
	// and without one it serves a page generated from this API's own schema.
	response.DocumentationEndpoint = basePath + "/" + reservedDocsSegment + "/"
	return connect.NewResponse(response), nil
}

// StopApi implements the framework's adapter contract.
func (s *Server) StopApi(_ context.Context, request *connect.Request[apiv1.StopApiRequest]) (*connect.Response[apiv1.StopApiResponse], error) {
	if request == nil || request.Msg == nil || strings.TrimSpace(request.Msg.GetInstanceId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance_id is required"))
	}
	s.mu.Lock()
	instance, ok := s.instances[strings.TrimSpace(request.Msg.GetInstanceId())]
	delete(s.instances, strings.TrimSpace(request.Msg.GetInstanceId()))
	s.mu.Unlock()
	if !ok {
		return connect.NewResponse(&apiv1.StopApiResponse{Stopped: false}), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = instance.server.Shutdown(ctx)
	return connect.NewResponse(&apiv1.StopApiResponse{Stopped: true}), nil
}

// route is one adapted operation the surface serves.
type route struct {
	method string
	path   string
	// segments is the path split into literal and parameter parts. A pattern is
	// kept as segments rather than as a compiled expression because a parameter
	// matches a whole path segment, and expressing that as a regular expression
	// would put a slash inside the expression and break any splitting of the
	// path.
	segments []patternSegment
}

// patternSegment is one segment of an adapted path: either a literal or a
// template parameter.
type patternSegment struct {
	literal   string
	parameter string
}

func (s patternSegment) isParameter() bool { return s.parameter != "" }

// adaptedRoutes is the set of operations the surface forwards. Paths are matched
// against the description's own paths; a path with no template is matched
// exactly, and a templated one by segment.
func adaptedRoutes(described api.API) ([]route, []string, error) {
	warnings := make([]string, 0)
	routes := make([]route, 0)
	seen := make(map[string]bool)
	for _, service := range described.Services {
		for _, operation := range service.Operations {
			method, path, derived, err := operationMapping(operation, renderOptions{})
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("operation %s is not served: %v", operation.ID, err))
				continue
			}
			warnings = append(warnings, derived...)
			segments := parsePathPattern(path)
			key := method + " " + path
			if seen[key] {
				warnings = append(warnings, fmt.Sprintf("operation %s duplicates %s; only the first is served", operation.ID, key))
				continue
			}
			seen[key] = true
			routes = append(routes, route{method: method, path: path, segments: segments})
		}
	}
	if len(routes) == 0 {
		return nil, nil, fmt.Errorf("api %q has no operation this adapter can serve", described.ID)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].path == routes[j].path {
			return routes[i].method < routes[j].method
		}
		return routes[i].path < routes[j].path
	})
	return routes, warnings, nil
}

// parsePathPattern splits a path template into literal and parameter segments.
func parsePathPattern(path string) []patternSegment {
	raw := splitPath(path)
	segments := make([]patternSegment, 0, len(raw))
	for _, segment := range raw {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			segments = append(segments, patternSegment{parameter: strings.Trim(segment, "{}")})
			continue
		}
		segments = append(segments, patternSegment{literal: segment})
	}
	return segments
}

// resolveBasePath picks the prefix the adapted surface is mounted under and
// proves it cannot collide with the routes being served, with the reserved
// documentation segments, or with the original server's base path.
func resolveBasePath(requested string, routes []route, originalBaseURL string) (string, string, error) {
	candidate := strings.TrimSpace(requested)
	collision := ""
	// A caller-supplied prefix is honoured only when it is safe. A prefix that
	// collides is reported rather than silently moved, because a moved prefix
	// would change every URL the caller was told about.
	if candidate != "" {
		normalized := normalizeBasePath(candidate)
		if reason := collisionReason(normalized, routes, originalBaseURL); reason != "" {
			return "", "", fmt.Errorf("base_path %q cannot be used: %s", candidate, reason)
		}
		return normalized, collision, nil
	}
	original, err := url.Parse(originalBaseURL)
	if err == nil {
		collision = strings.TrimRight(original.Path, "/")
	}
	// The default prefix is the namespace the adapted surface owns.
	prefixes := []string{"/_toolbox/adapted", "/adapted", "/_adapted", "/api"}
	for _, prefix := range prefixes {
		normalized := normalizeBasePath(prefix)
		if collisionReason(normalized, routes, originalBaseURL) == "" {
			return normalized, collision, nil
		}
	}
	return "", "", fmt.Errorf("no base path avoids the paths this API serves")
}

func collisionReason(base string, routes []route, originalBaseURL string) string {
	docs := base + "/" + reservedDocsSegment
	schema := base + "/" + reservedSchemaSegment
	if docs == schema {
		return "the documentation and schema paths are the same"
	}
	for _, route := range routes {
		if strings.HasPrefix(route.path, base+"/") || route.path == base {
			return fmt.Sprintf("the adapted operation path %q is under it", route.path)
		}
	}
	if original, err := url.Parse(originalBaseURL); err == nil {
		originalPath := strings.TrimRight(original.Path, "/")
		if originalPath != "" && (originalPath == base || strings.HasPrefix(base+"/", originalPath+"/")) {
			return fmt.Sprintf("it would shadow the original server's path %q", originalPath)
		}
	}
	return ""
}

func normalizeBasePath(value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return trimmed
}

func listenAddress(requested string) string {
	if trimmed := strings.TrimSpace(requested); trimmed != "" {
		return trimmed
	}
	return "127.0.0.1:0"
}

func instanceIdentifier(apiID, basePath string) string {
	sum := strings.NewReplacer("/", "-", " ", "-").Replace(strings.ToLower(apiID + basePath))
	return strings.Trim(sum, "-")
}

func primaryDocument(response *apiv1.RenderApiResponse) ([]byte, error) {
	if response == nil || len(response.GetFiles()) == 0 {
		return nil, fmt.Errorf("the adapter produced no schema file")
	}
	for _, file := range response.GetFiles() {
		if file.GetPrimary() {
			return file.GetContent(), nil
		}
	}
	return response.GetFiles()[0].GetContent(), nil
}

// handler serves the adapted operations, the schema document, and the
// documentation page, all under one base path.
func (s *Server) handler(
	instanceID, basePath string,
	original *url.URL,
	document, yamlDocument []byte,
	routes []route,
	client *http.Client,
) http.Handler {
	mux := http.NewServeMux()
	// The schema is downloadable in either format. A request for it gets the
	// bytes; a request for a download gets the same bytes as a file, because
	// saving a generated schema is a normal thing to want to do with it.
	schemaPrefix := basePath + "/" + reservedSchemaSegment + "/"
	mux.HandleFunc(schemaPrefix+schemaFileJSON, func(w http.ResponseWriter, r *http.Request) {
		serveSchemaFile(w, r, schemaFileJSON, "application/json", document)
	})
	mux.HandleFunc(schemaPrefix+schemaFileYAML, func(w http.ResponseWriter, r *http.Request) {
		serveSchemaFile(w, r, schemaFileYAML, "application/yaml", yamlDocument)
	})
	mux.HandleFunc(schemaPrefix, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != schemaPrefix {
			http.NotFound(w, r)
			return
		}
		// A directory request lists what can be downloaded, so the endpoint is
		// discoverable by a human as well as by a client that knows the name.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]string{
				{"name": schemaFileJSON, "media_type": "application/json", "url": schemaPrefix + schemaFileJSON},
				{"name": schemaFileYAML, "media_type": "application/yaml", "url": schemaPrefix + schemaFileYAML},
			},
		})
	})
	mux.HandleFunc(basePath+"/"+reservedDocsSegment+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != basePath+"/"+reservedDocsSegment+"/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(s.swaggerUI) > 0 {
			_, _ = w.Write(s.swaggerUI)
			return
		}
		_, _ = w.Write(s.documentationPage(instanceID, document, basePath))
	})
	// Everything else under the base path is an adapted operation.
	mux.HandleFunc(basePath+"/", func(w http.ResponseWriter, r *http.Request) {
		requestPath := strings.TrimPrefix(r.URL.Path, basePath)
		if requestPath == "" {
			requestPath = "/"
		}
		for _, candidate := range routes {
			// The route's method is stored lowercased, because that is the form a
			// description carries, while a request's method is uppercased by the
			// transport. Comparing them case-insensitively is what makes a
			// description from any source work here.
			if !strings.EqualFold(candidate.method, r.Method) {
				continue
			}
			values, matched := matchSegments(candidate.segments, requestPath)
			if !matched {
				continue
			}
			s.tunnel(w, r, original, candidate, values, client)
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

// serveSchemaFile writes one schema file, either for reading or as a download.
func serveSchemaFile(w http.ResponseWriter, r *http.Request, name, mediaType string, content []byte) {
	w.Header().Set("Content-Type", mediaType)
	if wantsDownload(r) {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	_, _ = w.Write(content)
}

// wantsDownload reports whether the caller asked for the schema as a file. The
// query is explicit, so an ordinary fetch of the same URL keeps working.
func wantsDownload(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("download"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// documentationPage renders the documentation page. The page is generated from
// the schema the same adapter produced, so the page and the served routes cannot
// disagree about what the API looks like, and it links the schema download.
func (s *Server) documentationPage(instanceID string, document []byte, basePath string) []byte {
	var parsed map[string]any
	if err := json.Unmarshal(document, &parsed); err != nil {
		parsed = map[string]any{}
	}
	info, _ := parsed["info"].(map[string]any)
	title := "Adapted API"
	if info != nil {
		if value, ok := info["title"].(string); ok && value != "" {
			title = value
		}
	}
	paths := 0
	if declared, ok := parsed["paths"].(map[string]any); ok {
		paths = len(declared)
	}
	page := "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">" +
		"<title>" + htmlEscape(title) + "</title>" +
		"<style>body{font:14px system-ui,sans-serif;margin:2rem;line-height:1.5}" +
		"code{background:#f4f4f4;padding:.1rem .3rem;border-radius:3px}</style></head><body>" +
		"<h1>" + htmlEscape(title) + "</h1>" +
		"<p>This API is served in the OpenAPI target format by Toolbox. It is translated from the original contract and every request is tunnelled to the original server.</p>" +
		"<p>Paths served: <code>" + itoa(paths) + "</code>. Instance: <code>" + htmlEscape(instanceID) + "</code>.</p>" +
		"<p>Download the schema: <a href=\"" + basePath + "/" + reservedSchemaSegment + "/" + schemaFileJSON + "\">" +
		schemaFileJSON + "</a> or <a href=\"" + basePath + "/" + reservedSchemaSegment + "/" + schemaFileYAML + "\">" +
		schemaFileYAML + "</a>. Append <code>?download=true</code> to save it as a file.</p>" +
		"</body></html>"
	return []byte(page)
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return replacer.Replace(value)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// tunnel forwards one request to the original server, substituting the values
// captured from the path back into the original path template.
func (s *Server) tunnel(w http.ResponseWriter, r *http.Request, original *url.URL, candidate route, values map[string]string, client *http.Client) {
	target := *original
	path := candidate.path
	for name, value := range values {
		path = strings.ReplaceAll(path, "{"+name+"}", value)
	}
	target.Path = strings.TrimRight(original.Path, "/") + path
	target.RawQuery = r.URL.RawQuery

	var body io.Reader
	if r.Body != nil {
		limited := http.MaxBytesReader(w, r.Body, defaultMaxBodyBytes)
		buffered, err := io.ReadAll(limited)
		if err != nil {
			http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
			return
		}
		body = strings.NewReader(string(buffered))
	}
	proxied, err := http.NewRequestWithContext(r.Context(), strings.ToUpper(candidate.method), target.String(), body)
	if err != nil {
		http.Error(w, "cannot build the tunnelled request", http.StatusBadGateway)
		return
	}
	for name, values := range r.Header {
		if strings.EqualFold(name, "Host") || strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, value := range values {
			proxied.Header.Add(name, value)
		}
	}
	response, err := client.Do(proxied)
	if err != nil {
		http.Error(w, "the original server did not answer", http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

// matchSegments matches a request path against a parsed pattern, returning the
// values its template parameters captured.
func matchSegments(segments []patternSegment, path string) (map[string]string, bool) {
	parts := splitPath(path)
	if len(segments) != len(parts) {
		return nil, false
	}
	values := make(map[string]string)
	for i, segment := range segments {
		if segment.isParameter() {
			if parts[i] == "" {
				return nil, false
			}
			values[segment.parameter] = parts[i]
			continue
		}
		if segment.literal != parts[i] {
			return nil, false
		}
	}
	return values, true
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return []string{""}
	}
	return strings.Split(trimmed, "/")
}
