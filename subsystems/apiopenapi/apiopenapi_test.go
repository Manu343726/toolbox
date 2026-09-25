package apiopenapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const petStoreJSON = `{
  "openapi": "3.1.0",
  "info": {"title": "Pet Store", "version": "1.4.0", "description": "A sample API"},
  "servers": [{"url": "https://api.example.test/v1", "description": "production"}],
  "tags": [{"name": "pets", "description": "Pet operations"}],
  "paths": {
    "/pets/{petId}": {
      "parameters": [{"name": "petId", "in": "path", "schema": {"type": "string"}}],
      "get": {
        "operationId": "getPetById",
        "summary": "Fetch one pet",
        "tags": ["pets"],
        "x-toolbox-capabilities": ["pet.read"],
        "parameters": [{"name": "verbose", "in": "query", "schema": {"type": "boolean"}}],
        "responses": {
          "200": {
            "description": "The pet",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Pet"}}}
          },
          "404": {"description": "Not found"}
        }
      },
      "delete": {
        "operationId": "deletePet",
        "summary": "Remove a pet",
        "tags": ["pets"],
        "x-toolbox-capabilities": ["pet.write"],
        "x-toolbox-side-effects": ["irreversible"],
        "responses": {"204": {"description": "Removed"}}
      }
    },
    "/pets": {
      "post": {
        "operationId": "createPet",
        "tags": ["pets"],
        "x-toolbox-capabilities": ["pet.write"],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/NewPet"}}}
        },
        "responses": {"201": {"description": "Created", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Pet"}}}}}
      }
    }
  },
  "components": {
    "schemas": {
      "Pet": {
        "type": "object",
        "required": ["id", "name"],
        "properties": {
          "id": {"type": "integer", "format": "int64", "description": "Pet identifier"},
          "name": {"type": "string"},
          "status": {"type": "string", "enum": ["available", "pending", "sold"]},
          "tags": {"type": "array", "items": {"type": "string"}},
          "owner": {"$ref": "#/components/schemas/Owner"}
        }
      },
      "NewPet": {
        "type": "object",
        "required": ["name"],
        "properties": {"name": {"type": "string"}, "tag": {"type": "string"}}
      },
      "Owner": {
        "type": "object",
        "properties": {"name": {"type": "string"}, "pet": {"$ref": "#/components/schemas/Pet"}}
      }
    },
    "securitySchemes": {
      "apiKeyAuth": {"type": "apiKey", "in": "header", "name": "X-API-Key"},
      "bearerAuth": {"type": "http", "scheme": "bearer"}
    }
  }
}`

const petStoreYAML = `
openapi: "3.0.3"
info:
  title: Inventory
  version: "2.0"
paths:
  /items:
    get:
      operationId: listItems
      summary: List inventory
      tags: [inventory]
      x-toolbox-capabilities: [inventory.read]
      responses:
        "200":
          description: Items
          content:
            application/json:
              schema:
                type: array
                items:
                  type: object
                  properties:
                    sku: {type: string}
                    count: {type: integer}
`

func parseDocumentForTest(t *testing.T, document []byte) api.API {
	t.Helper()
	parsed, _, err := parseDocument(document, parseRequest{})
	require.NoError(t, err)
	return parsed
}

func TestParseDocumentReadsIdentityAndServers(t *testing.T) {
	parsed := parseDocumentForTest(t, []byte(petStoreJSON))

	assert.Equal(t, "pet-store-1-4-0", parsed.ID)
	assert.Equal(t, "pet-store-1-4-0", parsed.Name)
	assert.Equal(t, "1.4.0", parsed.Version)
	assert.Equal(t, "Pet Store", parsed.Title)
	assert.Equal(t, FormatOpenAPI, parsed.Format)
	assert.NotEmpty(t, parsed.Source.Digest)
	require.Len(t, parsed.DeclaredServers, 1)
	assert.Equal(t, "https://api.example.test/v1", parsed.DeclaredServers[0].URL)
	assert.Equal(t, "production", parsed.DeclaredServers[0].Description)
}

func TestParseDocumentDerivesStableIdentifiers(t *testing.T) {
	parsed := parseDocumentForTest(t, []byte(petStoreYAML))

	assert.Equal(t, []string{"inventory-2-0/inventory/listItems"}, parsed.OperationIDs())
}

func TestParseDocumentGroupsOperationsByTag(t *testing.T) {
	parsed := parseDocumentForTest(t, []byte(petStoreJSON))

	require.Len(t, parsed.Services, 1)
	service := parsed.Services[0]
	assert.Equal(t, "pets", service.Name)
	assert.Equal(t, "pet-store-1-4-0/pets", service.ID)
	// Paths are visited in sorted order and methods in their conventional order,
	// so the same document always produces the same listing.
	assert.Equal(t, []string{"createPet", "getPetById", "deletePet"}, operationNames(service))
	assert.Equal(t, "Pet operations", service.Description)
}

func TestParseDocumentResolvesComponentReferences(t *testing.T) {
	parsed := parseDocumentForTest(t, []byte(petStoreJSON))
	get, ok := parsed.Operation("pet-store-1-4-0/pets/getPetById")
	require.True(t, ok)

	response, ok := get.Response.Property("name")
	require.True(t, ok)
	assert.Equal(t, api.TypeString, response.Schema.Type)
	status, ok := get.Response.Property("status")
	require.True(t, ok)
	assert.Equal(t, []string{"available", "pending", "sold"}, status.Schema.Enum)
	owner, ok := get.Response.Property("owner")
	require.True(t, ok)
	assert.Equal(t, "#/components/schemas/Owner", owner.Schema.Ref)
	// A recursive reference is described rather than expanded forever.
	pet, ok := owner.Schema.Property("pet")
	require.True(t, ok)
	assert.Contains(t, pet.Schema.Description, "recursive")
}

func TestParseDocumentMergesPathLevelParameters(t *testing.T) {
	parsed := parseDocumentForTest(t, []byte(petStoreJSON))
	get, ok := parsed.Operation("pet-store-1-4-0/pets/getPetById")
	require.True(t, ok)

	names := make([]string, 0, len(get.Parameters))
	for _, parameter := range get.Parameters {
		names = append(names, parameter.Name)
	}
	assert.Equal(t, []string{"petId", "verbose"}, names)
	for _, parameter := range get.Parameters {
		if parameter.Name == "petId" {
			assert.Equal(t, api.ParameterInPath, parameter.In)
			// A path parameter is required even when the document omits the flag,
			// because the request cannot be built without it.
			assert.True(t, parameter.Required)
		}
	}
}

func TestParseDocumentCarriesCapabilitiesAndSideEffects(t *testing.T) {
	parsed := parseDocumentForTest(t, []byte(petStoreJSON))

	get, _ := parsed.Operation("pet-store-1-4-0/pets/getPetById")
	assert.Equal(t, []string{"pet.read"}, get.Capabilities)
	assert.Equal(t, []api.SideEffect{api.SideEffectReadOnly}, get.SideEffects)

	remove, _ := parsed.Operation("pet-store-1-4-0/pets/deletePet")
	assert.Equal(t, []api.SideEffect{api.SideEffectDelete, api.SideEffectIrreversible}, remove.SideEffects,
		"the method implies delete and the document adds one")

	create, _ := parsed.Operation("pet-store-1-4-0/pets/createPet")
	assert.Equal(t, []api.SideEffect{api.SideEffectCreate}, create.SideEffects)
	request, ok := create.Request.Property("name")
	require.True(t, ok)
	assert.True(t, request.Required)
}

func TestParseDocumentLeavesUndeclaredCapabilitiesEmpty(t *testing.T) {
	// An operation that declares no capabilities must come out of the parser
	// with none, so the catalog's policy can refuse it. Capabilities are never
	// inferred from a path or an operation name.
	const document = `
openapi: "3.1.0"
info: {title: Bare, version: "1"}
paths:
  /things:
    get:
      operationId: listThings
      responses:
        "200": {description: ok}
`
	parsed := parseDocumentForTest(t, []byte(document))
	// An untagged operation lands in the default group.
	operation, ok := parsed.Operation("bare-1/default/listThings")
	require.True(t, ok)
	assert.Empty(t, operation.Capabilities)
	assert.Empty(t, parsed.Capabilities)
}

func TestParseDocumentReadsSecuritySchemes(t *testing.T) {
	parsed := parseDocumentForTest(t, []byte(petStoreJSON))
	require.Len(t, parsed.SecuritySchemes, 2)
	assert.Equal(t, "apiKeyAuth", parsed.SecuritySchemes[0].Name)
	assert.Equal(t, "apiKey", parsed.SecuritySchemes[0].Type)
	assert.Equal(t, "X-API-Key", parsed.SecuritySchemes[0].ParameterName)
	assert.Equal(t, "header", parsed.SecuritySchemes[0].In)
	assert.Equal(t, "bearerAuth", parsed.SecuritySchemes[1].Name)
	assert.Equal(t, "bearer", parsed.SecuritySchemes[1].Scheme)
}

func TestParseDocumentDerivesNameWhenOperationIDMissing(t *testing.T) {
	const document = `
openapi: "3.1.0"
info: {title: Minimal, version: "1"}
paths:
  /health:
    get:
      responses:
        "200": {description: ok}
`
	parsed, warnings, err := parseDocument([]byte(document), parseRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"minimal-1/default/get-health"}, parsed.OperationIDs())
	assert.NotEmpty(t, warnings, "a derived name is reported rather than chosen silently")
}

func TestParseDocumentRejectsUnusableDocuments(t *testing.T) {
	tests := []struct {
		name     string
		document string
		contains string
	}{
		{name: "empty", document: "", contains: "empty"},
		{name: "not a document", document: "\t\tthis: [is not", contains: "decode"},
		{name: "wrong version", document: `{"openapi":"2.0","info":{"title":"Old","version":"1"}}`, contains: "unsupported OpenAPI version"},
		{name: "no title", document: `{"openapi":"3.1.0","info":{"version":"1"}}`, contains: "info.title"},
		{name: "no operations", document: `{"openapi":"3.1.0","info":{"title":"Empty","version":"1"},"paths":{}}`, contains: "no operations"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseDocument([]byte(test.document), parseRequest{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.contains)
		})
	}
}

func TestParseDocumentWarnsAboutMissingComponent(t *testing.T) {
	const document = `
openapi: "3.1.0"
info: {title: Dangling, version: "1"}
paths:
  /things:
    get:
      operationId: listThings
      tags: [things]
      x-toolbox-capabilities: [things.read]
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Missing"
`
	parsed, warnings, err := parseDocument([]byte(document), parseRequest{})
	require.NoError(t, err, "a describable document is still returned")
	assert.NotEmpty(t, warnings)
	assert.Contains(t, warnings[0], "not declared in components")
	assert.Equal(t, []string{"dangling-1/things/listThings"}, parsed.OperationIDs())
}

func TestParserServiceRejectsForeignFormat(t *testing.T) {
	parser := NewParser(ParserOptions{})
	assert.Equal(t, []api.Format{FormatOpenAPI}, parser.Formats())

	_, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   "smithy",
		Document: []byte(petStoreJSON),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "openapi")

	_, err = parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{Document: []byte(petStoreJSON)}))
	require.Error(t, err, "a document with no declared format is not guessed at")
}

func TestParserServiceReportsFormatsItImplements(t *testing.T) {
	parser := NewParser(ParserOptions{})
	response, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatOpenAPI,
		Document: []byte(petStoreJSON),
		ApiId:    "petstore",
		Source:   &apiv1.ApiSource{Kind: "file", Location: "petstore.json"},
	}))
	require.NoError(t, err)

	assert.Equal(t, "petstore", response.Msg.GetApi().GetId())
	assert.Equal(t, "file", response.Msg.GetApi().GetSource().GetKind())
	require.Len(t, response.Msg.GetFormats(), 1, "the parser reports the descriptors a catalog should index")
	assert.Equal(t, FormatOpenAPI, response.Msg.GetFormats()[0].GetId())
	assert.Equal(t, Name, response.Msg.GetFormats()[0].GetProvider())
}

func TestParserServiceAppliesCallerBaseURLWhenDocumentDeclaresNone(t *testing.T) {
	const document = `
openapi: "3.1.0"
info: {title: No Servers, version: "1"}
paths:
  /ping:
    get:
      operationId: ping
      tags: [system]
      x-toolbox-capabilities: [system.read]
      responses: {"200": {description: ok}}
`
	parser := NewParser(ParserOptions{})
	response, err := parser.ParseApi(context.Background(), connect.NewRequest(&apiv1.ParseApiRequest{
		Format:   FormatOpenAPI,
		Document: []byte(document),
		BaseUrl:  "http://localhost:9090",
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetApi().GetDeclaredServers(), 1)
	assert.Equal(t, "http://localhost:9090", response.Msg.GetApi().GetDeclaredServers()[0].GetUrl())
}

// --- adapter ---

func invokerForTest(t *testing.T, credentials CredentialSource) *Invoker {
	t.Helper()
	return NewInvoker(InvokerOptions{Credentials: credentials})
}

func registeredOperation(t *testing.T, method, path string, parameters ...api.Parameter) (api.API, api.Server, api.Operation) {
	t.Helper()
	target := api.API{
		ID:     "shop",
		Name:   "shop",
		Format: FormatOpenAPI,
		Services: []api.Service{{
			Name: "pets",
			Operations: []api.Operation{{
				Name:       "call",
				Method:     method,
				Path:       path,
				Parameters: parameters,
				Request:    api.ObjectSchema(api.Property{Name: "name", Schema: api.StringSchema(), Required: true}),
				Response:   api.ObjectSchema(api.Property{Name: "ok", Schema: api.StringSchema()}),
			}},
		}},
	}
	normalized, err := target.Normalize()
	require.NoError(t, err)
	registered := api.Server{
		ID:        "shop-host",
		Name:      "Shop host",
		BaseURL:   "http://shop.test/api",
		Format:    FormatOpenAPI,
		Transport: TransportHTTP,
	}
	operation, found := normalized.Operation(normalized.OperationIDs()[0])
	require.True(t, found)
	return normalized, registered, operation
}

func invoke(t *testing.T, invoker *Invoker, target api.API, server api.Server, operation api.Operation, arguments string) *apiv1.InvokeApiResponse {
	t.Helper()
	response, err := invoker.InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		ServerId:      server.ID,
		ApiId:         target.ID,
		OperationId:   operation.ID,
		ArgumentsJson: json.RawMessage(arguments),
		Server:        server.ToProto(),
		Api:           target.ToProto(),
		Operation:     operation.ToProto(),
	}))
	require.NoError(t, err)
	return response.Msg
}

func TestAdapterBuildsRequestFromOperationAndArguments(t *testing.T) {
	var received *http.Request
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Clone(context.Background())
		buf := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(buf)
		}
		receivedBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "abc")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":"yes"}`))
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "post", "/pets/{petId}",
		api.Parameter{Name: "petId", In: api.ParameterInPath, Required: true, Schema: api.StringSchema()},
		api.Parameter{Name: "verbose", In: api.ParameterInQuery, Schema: api.StringSchema()},
		api.Parameter{Name: "X-Trace", In: api.ParameterInHeader, Schema: api.StringSchema()},
	)
	server.BaseURL = upstream.URL + "/api"

	response := invoke(t, invokerForTest(t, nil), target, server, operation, `{"petId":"7","verbose":"true","X-Trace":"t-1","name":"Rex"}`)

	assert.Equal(t, int32(http.StatusOK), response.GetStatus())
	assert.JSONEq(t, `{"ok":"yes"}`, string(response.GetBodyJson()))
	assert.Equal(t, "abc", response.GetHeaders()["X-Request-Id"])
	assert.NotContains(t, response.GetHeaders(), "Set-Cookie", "credential-bearing headers are not echoed back")

	require.NotNil(t, received)
	assert.Equal(t, "POST", received.Method, "the method is normalized for the wire")
	assert.Equal(t, "/api/pets/7", received.URL.Path)
	assert.Equal(t, "true", received.URL.Query().Get("verbose"))
	assert.Equal(t, "t-1", received.Header.Get("X-Trace"))
	assert.JSONEq(t, `{"name":"Rex"}`, receivedBody)
}

func TestAdapterRejectsMissingRequiredParameter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request must not be sent when a required parameter is missing")
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets/{petId}",
		api.Parameter{Name: "petId", In: api.ParameterInPath, Required: true, Schema: api.StringSchema()},
	)
	server.BaseURL = upstream.URL

	_, err := invokerForTest(t, nil).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		OperationId:   operation.ID,
		ArgumentsJson: json.RawMessage(`{}`),
		Server:        server.ToProto(),
		Api:           target.ToProto(),
		Operation:     operation.ToProto(),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "petId")
}

func TestAdapterRejectsParameterAbsentFromPath(t *testing.T) {
	target, server, operation := registeredOperation(t, "get", "/pets",
		api.Parameter{Name: "petId", In: api.ParameterInPath, Required: true, Schema: api.StringSchema()},
	)
	server.BaseURL = "http://shop.test"

	// A description that declares a path parameter the path does not contain is
	// inconsistent, and building the URL from it would silently drop a value.
	_, err := invokerForTest(t, nil).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		OperationId:   operation.ID,
		ArgumentsJson: json.RawMessage(`{"petId":"7"}`),
		Server:        server.ToProto(),
		Api:           target.ToProto(),
		Operation:     operation.ToProto(),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not contain the declared parameter")
}

func TestAdapterRejectsUnknownArgument(t *testing.T) {
	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = "http://shop.test"

	_, err := invokerForTest(t, nil).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		OperationId:   operation.ID,
		ArgumentsJson: json.RawMessage(`{"nmae":"typo"}`),
		Server:        server.ToProto(),
		Api:           target.ToProto(),
		Operation:     operation.ToProto(),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `does not accept the argument "nmae"`,
		"a misspelled argument is reported instead of silently dropped")
}

func TestAdapterAppliesAPIKeyCredential(t *testing.T) {
	var received *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Clone(context.Background())
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = upstream.URL
	target.SecuritySchemes = []api.SecurityScheme{{Name: "apiKeyAuth", Type: "apiKey", In: "header", ParameterName: "X-API-Key"}}
	target.Security = []api.SecurityRequirement{{Scheme: "apiKeyAuth"}}
	normalized, err := target.Normalize()
	require.NoError(t, err)

	invoker := invokerForTest(t, StaticCredentials{"apiKeyAuth": "secret"})
	invoke(t, invoker, normalized, server, operation, `{}`)

	require.NotNil(t, received)
	assert.Equal(t, "secret", received.Header.Get("X-API-Key"))
}

func TestAdapterRefusesOperationWithoutAvailableCredential(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an unauthenticated request must not be sent")
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = upstream.URL
	target.Security = []api.SecurityRequirement{{Scheme: "apiKeyAuth"}}
	target.SecuritySchemes = []api.SecurityScheme{{Name: "apiKeyAuth", Type: "apiKey", In: "header", ParameterName: "X-API-Key"}}
	normalized, err := target.Normalize()
	require.NoError(t, err)

	// A deployment with no credential source at all is reported differently from
	// one that simply lacks this scheme's value, so the cause is obvious.
	_, err = invokerForTest(t, nil).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		OperationId: operation.ID,
		Server:      server.ToProto(),
		Api:         normalized.ToProto(),
		Operation:   operation.ToProto(),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supplies no credentials")

	_, err = invokerForTest(t, StaticCredentials{}).InvokeApi(context.Background(), connect.NewRequest(&apiv1.InvokeApiRequest{
		OperationId: operation.ID,
		Server:      server.ToProto(),
		Api:         normalized.ToProto(),
		Operation:   operation.ToProto(),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credential is available")
}

func TestAdapterReportsUpstreamErrorStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = upstream.URL

	response := invoke(t, invokerForTest(t, nil), target, server, operation, `{}`)
	assert.Equal(t, int32(http.StatusTeapot), response.GetStatus())
	assert.JSONEq(t, `{"error":"nope"}`, string(response.GetBodyJson()))
}

func TestAdapterWrapsNonJSONResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("plain text body"))
	}))
	defer upstream.Close()

	target, server, operation := registeredOperation(t, "get", "/pets")
	server.BaseURL = upstream.URL

	response := invoke(t, invokerForTest(t, nil), target, server, operation, `{}`)
	// The value stays valid JSON so a tool result is never invalid JSON.
	assert.JSONEq(t, `"plain text body"`, string(response.GetBodyJson()))
}

func operationNames(service api.Service) []string {
	names := make([]string, 0, len(service.Operations))
	for _, operation := range service.Operations {
		names = append(names, operation.Name)
	}
	return names
}
