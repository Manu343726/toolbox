package openapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleSwaggerUI = "<!doctype html><title>stub</title>"

// petAPI is a small description with one templated path, used to prove the
// adapted surface tunnels to the original server.
func petAPI(t *testing.T) api.API {
	t.Helper()
	described := api.API{
		ID:      "shop",
		Name:    "shop",
		Version: "1.0.0",
		Title:   "Shop",
		Format:  Format,
		Services: []api.Service{{
			Name: "pets",
			Operations: []api.Operation{{
				Name:        "getPet",
				Method:      "get",
				Path:        "/pets/{petId}",
				Summary:     "Fetch one pet",
				SideEffects: []api.SideEffect{api.SideEffectReadOnly},
				Parameters: []api.Parameter{{
					Name:     "petId",
					In:       api.ParameterInPath,
					Required: true,
					Schema:   api.StringSchema(),
				}},
				Response: api.ObjectSchema(api.Property{Name: "name", Schema: api.StringSchema()}),
			}},
		}},
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)
	return normalized
}

// originalServer is the API the adapted surface tunnels to. It records what it
// received so a test can prove the tunnel passes the path and query through.
type originalServer struct {
	*httptest.Server
	received []*http.Request
}

func startOriginalServer(t *testing.T) *originalServer {
	t.Helper()
	original := &originalServer{}
	original.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original.received = append(original.received, r.Clone(context.Background()))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Original", "yes")
		_, _ = w.Write([]byte(`{"name":"rex"}`))
	}))
	t.Cleanup(original.Close)
	return original
}

// serveForTest starts one adapted surface and stops it when the test ends.
func serveForTest(t *testing.T, surfaces *Surfaces, described api.API, server api.Server) *Served {
	t.Helper()
	served, err := surfaces.Serve(described, server, ServeOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { surfaces.Stop(served.InstanceID) })
	return served
}

// origin strips the base path from a served endpoint, leaving the scheme and
// authority. The response's other paths are absolute, so a caller combines the
// two rather than appending to the endpoint.
func origin(endpoint string) string {
	trimmed := strings.TrimPrefix(endpoint, "http://")
	if index := strings.Index(trimmed, "/"); index >= 0 {
		return "http://" + trimmed[:index]
	}
	return "http://" + trimmed
}

// newSurfaces creates a serving registry, with a documentation UI configured the
// way a deployment would.
func newSurfaces(t *testing.T) *Surfaces {
	t.Helper()
	return NewSurfaces(SurfaceOptions{SwaggerUI: []byte(sampleSwaggerUI)})
}

// newSurfacesWithoutUI creates a serving registry with no configured UI, so the
// generated documentation page is the one under test.
func newSurfacesWithoutUI(t *testing.T) *Surfaces {
	t.Helper()
	return NewSurfaces(SurfaceOptions{})
}

func TestServedSurfaceTunnelsToOriginalServer(t *testing.T) {
	original := startOriginalServer(t)
	surfaces := newSurfaces(t)
	served := serveForTest(t, surfaces, petAPI(t), api.Server{
		ID:      "shop-host",
		Name:    "Shop host",
		BaseURL: original.URL,
		Format:  Format,
	})

	require.NotEmpty(t, served.Paths)
	assert.Equal(t, Target, served.Target)
	assert.Equal(t, "/_toolbox/adapted", served.BasePath)

	response, err := http.Get(served.Endpoint + "/pets/7")
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.JSONEq(t, `{"name":"rex"}`, string(body))
	assert.Equal(t, "yes", response.Header.Get("X-Original"), "the original server's response reaches the caller")

	require.Len(t, original.received, 1)
	assert.Equal(t, "/pets/7", original.received[0].URL.Path, "the captured path parameter is put back")
}

func TestServedSurfaceServesDocumentationWithoutAConfiguredUI(t *testing.T) {
	// With no UI supplied, the documentation endpoint still exists and still
	// describes this API, because the page is generated from the schema the same
	// adapter produced.
	original := startOriginalServer(t)
	surfaces := newSurfacesWithoutUI(t)
	served := serveForTest(t, surfaces, petAPI(t), api.Server{ID: "shop-host", Name: "Shop", BaseURL: original.URL, Format: Format})

	response, err := http.Get(origin(served.Endpoint) + served.DocumentationEndpoint)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, string(body), "Shop")
	assert.Contains(t, string(body), schemaFileJSON, "the page links the schema download")
}

func TestServedSurfaceServesSwaggerUIOnDedicatedPath(t *testing.T) {
	original := startOriginalServer(t)
	surfaces := newSurfaces(t)
	served := serveForTest(t, surfaces, petAPI(t), api.Server{ID: "shop-host", Name: "Shop", BaseURL: original.URL, Format: Format})

	require.NotEmpty(t, served.DocumentationEndpoint)
	assert.Equal(t, "/_toolbox/adapted/"+reservedDocsSegment+"/", served.DocumentationEndpoint)

	response, err := http.Get(origin(served.Endpoint) + served.DocumentationEndpoint)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, sampleSwaggerUI, string(body), "a deployment's supplied UI is served as supplied")

	// The documentation path is not one of the API's own paths.
	for _, path := range served.Paths {
		assert.NotEqual(t, served.DocumentationEndpoint, path)
	}
}

func TestSchemaIsDownloadable(t *testing.T) {
	original := startOriginalServer(t)
	surfaces := newSurfaces(t)
	served := serveForTest(t, surfaces, petAPI(t), api.Server{ID: "shop-host", Name: "Shop", BaseURL: original.URL, Format: Format})

	require.NotEmpty(t, served.SchemaEndpoint)
	assert.Equal(t, "/_toolbox/adapted/"+reservedSchemaSegment+"/"+schemaFileJSON, served.SchemaEndpoint)

	// The JSON schema is served inline for a plain request. The reported endpoint
	// is the full path, so a caller never has to rebuild it.
	response, err := http.Get(origin(served.Endpoint) + served.SchemaEndpoint)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "application/json", response.Header.Get("Content-Type"))
	assert.Empty(t, response.Header.Get("Content-Disposition"), "a plain request is not a download")
	var document map[string]any
	require.NoError(t, json.Unmarshal(body, &document))
	assert.Equal(t, "3.1.0", document["openapi"])
	paths, ok := document["paths"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, paths, "/pets/{petId}")

	// The same schema is downloadable as a file.
	download, err := http.Get(origin(served.Endpoint) + served.SchemaEndpoint + "?download=true")
	require.NoError(t, err)
	defer func() { _ = download.Body.Close() }()
	assert.Equal(t, `attachment; filename="openapi.json"`, download.Header.Get("Content-Disposition"))

	// And the YAML form is available from the same place.
	yamlResponse, err := http.Get(origin(served.Endpoint) + strings.TrimSuffix(served.SchemaEndpoint, schemaFileJSON) + schemaFileYAML + "?download=true")
	require.NoError(t, err)
	defer func() { _ = yamlResponse.Body.Close() }()
	yamlBody, err := io.ReadAll(yamlResponse.Body)
	require.NoError(t, err)
	assert.Equal(t, `attachment; filename="openapi.yaml"`, yamlResponse.Header.Get("Content-Disposition"))
	assert.Contains(t, string(yamlBody), "openapi: 3.1.0")

	// The directory lists what can be downloaded.
	listing, err := http.Get(origin(served.Endpoint) + served.BasePath + "/" + reservedSchemaSegment + "/")
	require.NoError(t, err)
	defer func() { _ = listing.Body.Close() }()
	listingBody, err := io.ReadAll(listing.Body)
	require.NoError(t, err)
	assert.Contains(t, string(listingBody), schemaFileJSON)
	assert.Contains(t, string(listingBody), schemaFileYAML)
}

func TestSchemaEndpointRejectsAnotherFile(t *testing.T) {
	original := startOriginalServer(t)
	surfaces := newSurfaces(t)
	served := serveForTest(t, surfaces, petAPI(t), api.Server{ID: "shop-host", Name: "Shop", BaseURL: original.URL, Format: Format})

	response, err := http.Get(origin(served.Endpoint) + served.BasePath + "/" + reservedSchemaSegment + "/secrets.json")
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	assert.Equal(t, http.StatusNotFound, response.StatusCode)
}

func TestBasePathIsRefusedWhenItWouldCollide(t *testing.T) {
	original := startOriginalServer(t)
	surfaces := newSurfaces(t)

	// A base path that would shadow an adapted operation is refused rather than
	// silently moved, because a moved prefix would invalidate the URLs the caller
	// was told about.
	_, err := surfaces.Serve(petAPI(t), api.Server{ID: "h", Name: "H", BaseURL: original.URL, Format: Format}, ServeOptions{BasePath: "/pets"})
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "cannot be used")
}

func TestBasePathWarnsWhenItShadowsTheOriginalServerPath(t *testing.T) {
	// An original server that already serves under a prefix is reported, so the
	// operator knows the adapted surface was placed elsewhere on purpose.
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer original.Close()
	surfaces := newSurfaces(t)

	served := serveForTest(t, surfaces, petAPI(t), api.Server{
		ID:      "shop-host",
		Name:    "Shop",
		BaseURL: original.URL + "/shop",
		Format:  Format,
	})
	assert.NotEmpty(t, served.Warnings)
}

func TestServeApiRequiresTheOriginalServer(t *testing.T) {
	surfaces := newSurfaces(t)

	// An adapted surface has to forward somewhere; without an original server
	// there is nothing to tunnel to, and the request is refused rather than
	// answered with a surface that always fails.
	_, err := surfaces.Serve(petAPI(t), api.Server{ID: "shop-host", Name: "Shop", Format: Format}, ServeOptions{})
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "base url")
}

func TestStopEndsTheSurface(t *testing.T) {
	original := startOriginalServer(t)
	surfaces := newSurfaces(t)
	described := petAPI(t)
	server := api.Server{ID: "shop-host", Name: "Shop", BaseURL: original.URL, Format: Format}

	served, err := surfaces.Serve(described, server, ServeOptions{})
	require.NoError(t, err)
	endpoint := served.Endpoint

	assert.True(t, surfaces.Stop(served.InstanceID), "a running surface is stopped")
	// Stopping again is reported, not treated as an error: a stop for an unknown
	// instance is answered honestly.
	assert.False(t, surfaces.Stop(served.InstanceID))

	client := &http.Client{Timeout: 2 * time.Second}
	if _, err := client.Get(endpoint + "/pets/1"); err == nil {
		t.Fatal("the adapted surface is still answering after it was stopped")
	}
}

func TestServingTheSameAPITwiceIsReported(t *testing.T) {
	original := startOriginalServer(t)
	surfaces := newSurfaces(t)
	described := petAPI(t)
	server := api.Server{ID: "shop-host", Name: "Shop", BaseURL: original.URL, Format: Format}

	first, err := surfaces.Serve(described, server, ServeOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { surfaces.Stop(first.InstanceID) })

	_, err = surfaces.Serve(described, server, ServeOptions{})
	require.Error(t, err)
	assert.Equal(t, api.KindAlreadyExists, api.KindOf(err), "serving the same API twice is reported, not silently accepted")
}

func TestAdaptedPathMatching(t *testing.T) {
	segments := parsePathPattern("/pets/{petId}/toys/{toyId}")
	values, matched := matchSegments(segments, "/pets/7/toys/ball")
	require.True(t, matched)
	assert.Equal(t, "7", values["petId"])
	assert.Equal(t, "ball", values["toyId"])

	_, matched = matchSegments(segments, "/pets/7/toys")
	assert.False(t, matched, "a shorter path does not match a longer pattern")
	_, matched = matchSegments(segments, "/pets/7/other/ball")
	assert.False(t, matched)
}

func TestServedSurfaceReturnsNotFoundForUnknownPath(t *testing.T) {
	original := startOriginalServer(t)
	surfaces := newSurfaces(t)
	served := serveForTest(t, surfaces, petAPI(t), api.Server{ID: "shop-host", Name: "Shop", BaseURL: original.URL, Format: Format})

	response, err := http.Get(served.Endpoint + "/unknown/1")
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	assert.Equal(t, http.StatusNotFound, response.StatusCode)
	assert.Empty(t, original.received, "an unmapped path is not forwarded")
}

func TestDocumentationPageLinksTheSchemaDownload(t *testing.T) {
	original := startOriginalServer(t)
	surfaces := newSurfacesWithoutUI(t)
	served := serveForTest(t, surfaces, petAPI(t), api.Server{ID: "shop-host", Name: "Shop", BaseURL: original.URL, Format: Format})

	response, err := http.Get(origin(served.Endpoint) + served.DocumentationEndpoint)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), schemaFileJSON)
	assert.Contains(t, string(body), "download=true")
	assert.True(t, strings.Contains(string(body), served.BasePath))
}
