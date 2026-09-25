package openapi

import (
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
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

func TestParseDocumentReadsIdentityAndServers(t *testing.T) {
	parsed := parseDocumentForTest(t, []byte(petStoreJSON))

	assert.Equal(t, "pet-store-1-4-0", parsed.ID)
	assert.Equal(t, "pet-store-1-4-0", parsed.Name)
	assert.Equal(t, "1.4.0", parsed.Version)
	assert.Equal(t, "Pet Store", parsed.Title)
	assert.Equal(t, Format, parsed.Format)
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

func TestParseDocumentInfersSideEffectsAndAddsWhatTheDocumentDeclares(t *testing.T) {
	parsed := parseDocumentForTest(t, []byte(petStoreJSON))

	get, _ := parsed.Operation("pet-store-1-4-0/pets/getPetById")
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

func TestParseDocumentLeavesAnUndeclaredEffectToTheHTTPMethod(t *testing.T) {
	// A document that extends nothing still gets the effect its method implies,
	// because an HTTP verb does say something about what the call does. Nothing is
	// inferred from the path or the operation name.
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
	assert.Equal(t, []api.SideEffect{api.SideEffectReadOnly}, operation.SideEffects)
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

func parseDocumentForTest(t *testing.T, document []byte) api.API {
	t.Helper()
	parsed, _, err := Parse(document, ParseOptions{})
	require.NoError(t, err)
	return parsed
}

func operationNames(service api.Service) []string {
	names := make([]string, 0, len(service.Operations))
	for _, operation := range service.Operations {
		names = append(names, operation.Name)
	}
	return names
}
