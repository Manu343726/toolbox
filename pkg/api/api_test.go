package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sample() API {
	return API{
		ID:     "shop",
		Name:   "shop",
		Format: "openapi",
		Services: []Service{{
			Name: "pets",
			Operations: []Operation{{
				Name:     "getPet",
				Method:   "get",
				Path:     "/pets/{petId}",
				Response: ObjectSchema(Property{Name: "name", Schema: StringSchema()}),
			}},
		}},
	}
}

func TestNormalizeAnnotatesIdentifiers(t *testing.T) {
	described := sample()
	normalized, err := described.Normalize()
	require.NoError(t, err)

	assert.Equal(t, "shop/pets", normalized.Services[0].ID)
	assert.Equal(t, "shop/pets/getPet", normalized.Services[0].Operations[0].ID)
	assert.Equal(t, "shop", normalized.Services[0].Operations[0].APIID)
	assert.Equal(t, "pets", normalized.Services[0].Operations[0].Service)
}

func TestNormalizeRejectsDuplicateOperations(t *testing.T) {
	described := sample()
	described.Services[0].Operations = append(described.Services[0].Operations, Operation{Name: "getPet"})
	_, err := described.Normalize()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than once")
}

func TestNormalizeRejectsDuplicateServices(t *testing.T) {
	described := sample()
	described.Services = append(described.Services, described.Services[0])
	_, err := described.Normalize()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than once")
}

func TestNormalizeRequiresIdentityAndFormat(t *testing.T) {
	withoutID := sample()
	withoutID.ID = ""
	_, err := withoutID.Normalize()
	require.Error(t, err)

	withoutName := sample()
	withoutName.Name = ""
	_, err = withoutName.Normalize()
	require.Error(t, err)

	withoutFormat := sample()
	withoutFormat.Format = ""
	_, err = withoutFormat.Normalize()
	require.Error(t, err)
}

func TestNormalizeAcceptsAFormatTheFrameworkHasNeverSeen(t *testing.T) {
	// The framework does not know which formats exist, so it cannot reject one it
	// has not seen. A user-defined format normalizes exactly like a known one.
	for _, format := range []Format{"openapi", "grpc", "smithy", "graphql", "asyncapi", "x-made-up"} {
		described := sample()
		described.Format = format
		normalized, err := described.Normalize()
		require.NoError(t, err, "format %q", format)
		assert.Equal(t, format, normalized.Format)
	}
}

func TestNormalizeKeepsUnknownSideEffectsAndSortsThem(t *testing.T) {
	described := sample()
	described.Services[0].Operations[0].SideEffects = []SideEffect{
		"quantum_entanglement", "read_only", "quantum_entanglement", " ",
	}
	normalized, err := described.Normalize()
	require.NoError(t, err)
	assert.Equal(t, []SideEffect{"quantum_entanglement", "read_only"}, normalized.Services[0].Operations[0].SideEffects,
		"an unrecognized side effect is preserved rather than dropped, because dropping it would make a consequential operation look harmless")
}

func TestValidateRejectsMismatchedIdentifiers(t *testing.T) {
	source := sample()
	described, err := source.Normalize()
	require.NoError(t, err)
	described.Services[0].Operations[0].ID = "somewhere/else"
	require.Error(t, described.Validate())
}

func TestValidateRejectsRequiredWithoutProperty(t *testing.T) {
	schema := &Schema{Type: TypeObject, Required: []string{"missing"}}
	require.Error(t, schema.Validate("schema"))
}

func TestValidateRejectsArrayWithoutItems(t *testing.T) {
	require.Error(t, (&Schema{Type: TypeArray}).Validate("schema"))
}

func TestValidateRejectsInvertedBounds(t *testing.T) {
	schema := &Schema{Type: TypeNumber, Minimum: Float64(10), Maximum: Float64(1)}
	require.Error(t, schema.Validate("schema"))
}

func TestParameterRequiresALocation(t *testing.T) {
	require.Error(t, Parameter{Name: "x"}.Validate("parameter"))
	require.NoError(t, Parameter{Name: "x", In: ParameterInPath, Schema: StringSchema()}.Validate("parameter"))
}

func TestSchemaJSONSchemaIsDeterministic(t *testing.T) {
	schema := ObjectSchema(
		Property{Name: "b", Schema: StringSchema(), Required: true},
		Property{Name: "a", Schema: &Schema{Type: TypeArray, Items: StringSchema()}},
	)
	first, err := json.Marshal(schema.JSONSchema())
	require.NoError(t, err)
	second, err := json.Marshal(schema.JSONSchema())
	require.NoError(t, err)
	assert.JSONEq(t, string(first), string(second))
	assert.Contains(t, string(first), `"required":["b"]`, "required names are sorted so the schema is stable")
}

func TestSchemaJSONSchemaRendersEveryPartItCarries(t *testing.T) {
	schema := &Schema{
		Type:        TypeObject,
		Title:       "Pet",
		Description: "A pet",
		Format:      "custom",
		Deprecated:  true,
		ReadOnly:    true,
		Properties: []Property{
			{Name: "name", Schema: StringSchema(), Required: true},
			{Name: "kind", Schema: &Schema{Type: TypeString, Enum: []string{"cat", "dog"}}},
			{Name: "tags", Schema: ArraySchema(StringSchema())},
			{Name: "meta", Schema: &Schema{Type: TypeObject, AdditionalProperties: StringSchema()}},
			{Name: "free", Schema: &Schema{Type: TypeObject, AdditionalPropertiesAllowed: true}},
		},
		Required:  []string{"name"},
		Minimum:   Float64(1),
		Maximum:   Float64(9),
		MinLength: Int64(1),
		MaxLength: Int64(9),
		Pattern:   "^[a-z]+$",
		Default:   "rex",
		OneOf:     []*Schema{StringSchema()},
		AnyOf:     []*Schema{{Type: TypeNull}},
		Ref:       "#/components/schemas/Pet",
	}
	rendered := schema.JSONSchema()
	assert.Equal(t, "object", rendered["type"])
	assert.Equal(t, "Pet", rendered["title"])
	assert.Equal(t, "custom", rendered["format"])
	assert.Equal(t, true, rendered["deprecated"])
	assert.Equal(t, true, rendered["readOnly"])
	assert.Equal(t, 1.0, rendered["minimum"])
	assert.Equal(t, "^[a-z]+$", rendered["pattern"])
	assert.Equal(t, "rex", rendered["default"])
	assert.NotNil(t, rendered["oneOf"])
	assert.NotNil(t, rendered["anyOf"])

	properties, ok := rendered["properties"].(map[string]any)
	require.True(t, ok)
	meta, ok := properties["meta"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, meta, "additionalProperties")
	free, ok := properties["free"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, free["additionalProperties"])
}

func TestNilSchemaRendersPermissiveObject(t *testing.T) {
	var schema *Schema
	assert.Equal(t, map[string]any{"type": "object", "additionalProperties": true}, schema.JSONSchema())
}

func TestCloneIsDeep(t *testing.T) {
	original := sample()
	original.Services[0].Operations[0].Response.Properties[0].Schema.Description = "before"
	clone := original.Clone()
	clone.Services[0].Operations[0].Response.Properties[0].Schema.Description = "after"
	assert.Equal(t, "before", original.Services[0].Operations[0].Response.Properties[0].Schema.Description)
}

func TestServerNormalizeAppliesDefaults(t *testing.T) {
	moment := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	server := Server{ID: " s ", Name: " Shop ", BaseURL: "http://x.test/"}
	require.NoError(t, server.Normalize(moment))
	assert.Equal(t, "s", server.ID)
	assert.Equal(t, "Shop", server.Name)
	assert.Equal(t, "http://x.test", server.BaseURL)
	assert.Equal(t, ServerStatusUnknown, server.Status)
	assert.Equal(t, moment, server.RegisteredAt)
}

func TestServerValidateRequiresIdentityAndLocation(t *testing.T) {
	require.Error(t, Server{Name: "n", BaseURL: "http://x"}.Validate())
	require.Error(t, Server{ID: "i", BaseURL: "http://x"}.Validate())
	require.Error(t, Server{ID: "i", Name: "n"}.Validate())
}

func TestDescriptorValidation(t *testing.T) {
	require.Error(t, FormatDescriptor{Name: "n"}.Validate())
	require.NoError(t, FormatDescriptor{ID: "x", Name: "n"}.Validate())
	require.Error(t, TransportDescriptor{Name: "n"}.Validate())
	require.NoError(t, TransportDescriptor{ID: "x", Name: "n"}.Validate())
}

func TestOperationIdentifiersAreDerivable(t *testing.T) {
	assert.Equal(t, "shop/pets/getPet", OperationID("shop", "pets", "getPet"))
	assert.Equal(t, "shop/pets", ServiceID("shop", "pets"))
	assert.Equal(t, "shop", ServiceID("shop", ""))
	assert.Equal(t, "pets", ServiceID("", "pets"))
}

func TestSummarizeCountsServicesAndOperations(t *testing.T) {
	summary := sample().Summarize()
	assert.Equal(t, 1, summary.Services)
	assert.Equal(t, 1, summary.Operations)
	assert.Equal(t, "openapi", summary.Format)
}

func TestProviderHandlesClaims(t *testing.T) {
	provider := Provider{
		Role:       ProviderAdapter,
		Formats:    []Format{"openapi"},
		Targets:    []string{"openapi", "mcp"},
		Transports: []Transport{"http"},
	}
	assert.True(t, provider.HandlesFormat("openapi"))
	assert.True(t, provider.HandlesTarget("mcp"))
	assert.True(t, provider.HandlesTransport("http"))
	assert.False(t, provider.HandlesTarget("protoset"))
	assert.False(t, provider.HandlesFormat("grpc"))
}

func TestStaticCatalogReturnsCopies(t *testing.T) {
	catalog := &StaticCatalog{
		RegisteredServers: []Server{{ID: "s", Name: "n", BaseURL: "http://x"}},
		RegisteredAPIs:    []API{sample()},
	}
	servers, err := catalog.Servers(nil)
	require.NoError(t, err)
	require.Len(t, servers, 1)
	servers[0].Name = "mutated"

	again, err := catalog.Servers(nil)
	require.NoError(t, err)
	assert.Equal(t, "n", again[0].Name, "a caller cannot mutate the catalog through the view")
}

func TestValidationErrorMatchesInvalid(t *testing.T) {
	source := sample()
	source.ID = ""
	err := source.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestSchemaFromJSONSchemaReadsTheKeywordsTheModelCarries(t *testing.T) {
	read := SchemaFromJSONSchema(map[string]any{
		"$ref":        "#/components/schemas/Pet",
		"type":        "object",
		"title":       "New pet",
		"description": "what to create",
		"format":      "custom",
		"deprecated":  true,
		"readOnly":    true,
		"properties": map[string]any{
			"weight": map[string]any{"type": "number", "minimum": float64(1), "maximum": float64(9)},
			"name":   map[string]any{"type": "string", "minLength": float64(1), "maxLength": float64(40), "pattern": "^[a-z]+$"},
			"tags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"kind":   map[string]any{"type": "string", "enum": []any{"cat", "dog"}, "default": "cat"},
			"owner":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"free":   map[string]any{"type": "object", "additionalProperties": true},
		},
		"required": []any{"name"},
	})
	require.NotNil(t, read)
	assert.Equal(t, "#/components/schemas/Pet", read.Ref)
	assert.Equal(t, TypeObject, read.Type)
	assert.Equal(t, "New pet", read.Title)
	assert.Equal(t, "custom", read.Format)
	assert.True(t, read.Deprecated)
	assert.True(t, read.ReadOnly)
	assert.Equal(t, []string{"name"}, read.Required)

	// Properties are read in a stable order, so two reads of one document are
	// equal and a digest over them means something.
	require.Len(t, read.Properties, 6)
	assert.Equal(t, "free", read.Properties[0].Name)
	assert.Equal(t, "weight", read.Properties[5].Name)

	byName := map[string]*Schema{}
	required := map[string]bool{}
	for _, property := range read.Properties {
		byName[property.Name] = property.Schema
		required[property.Name] = property.Required
	}
	require.NotNil(t, byName["name"])
	assert.True(t, required["name"], "required is carried on the property, where a consumer reads it")
	assert.Equal(t, int64(1), *byName["name"].MinLength)
	assert.Equal(t, "^[a-z]+$", byName["name"].Pattern)
	require.NotNil(t, byName["tags"].Items)
	assert.Equal(t, TypeString, byName["tags"].Items.Type)
	assert.Equal(t, []string{"cat", "dog"}, byName["kind"].Enum)
	assert.Equal(t, "cat", byName["kind"].Default)
	assert.Equal(t, 1.0, *byName["weight"].Minimum)
	require.NotNil(t, byName["owner"].AdditionalProperties)
	assert.True(t, byName["free"].AdditionalPropertiesAllowed)
}

func TestSchemaFromJSONSchemaReadsAlternatives(t *testing.T) {
	read := SchemaFromJSONSchema(map[string]any{
		"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}},
		"anyOf": []any{map[string]any{"type": "null"}},
	})
	require.NotNil(t, read)
	require.Len(t, read.OneOf, 2)
	assert.Equal(t, TypeString, read.OneOf[0].Type)
	assert.Equal(t, TypeInteger, read.OneOf[1].Type)
	require.Len(t, read.AnyOf, 1)
	assert.Equal(t, TypeNull, read.AnyOf[0].Type)
}

func TestSchemaFromJSONSchemaTakesTheFirstOfSeveralTypes(t *testing.T) {
	// JSON Schema allows a list of types and the model has one value for the
	// field, so the first is taken rather than inventing a union.
	read := SchemaFromJSONSchema(map[string]any{"type": []any{"string", "null"}})
	require.NotNil(t, read)
	assert.Equal(t, TypeString, read.Type)
}

func TestSchemaFromJSONSchemaReadsAnAbsentDocument(t *testing.T) {
	assert.Nil(t, SchemaFromJSONSchema(nil), "no document describes no shape")
	assert.Nil(t, SchemaFromJSONSchema(map[string]any{}))
}

func TestSchemaFromJSONSchemaSurvivesUnreadableChildren(t *testing.T) {
	// A child that is not an object must not fail the whole document: a manifest
	// with one malformed entry still describes every other entry.
	read := SchemaFromJSONSchema(map[string]any{
		"type":       "object",
		"properties": map[string]any{"broken": "not a schema", "fine": map[string]any{"type": "string"}},
	})
	require.NotNil(t, read)
	require.Len(t, read.Properties, 2)
	assert.Nil(t, read.Properties[0].Schema)
	require.NotNil(t, read.Properties[1].Schema)
	assert.Equal(t, TypeString, read.Properties[1].Schema.Type)
}

func TestAPIRoundTripsItsTransportClaim(t *testing.T) {
	described := sample()
	described.Transport = "mcp"
	normalized, err := described.Normalize()
	require.NoError(t, err)
	assert.Equal(t, Transport("mcp"), normalized.Transport)

	restored, err := APIFromProto(normalized.ToProto())
	require.NoError(t, err)
	assert.Equal(t, Transport("mcp"), restored.Transport,
		"a description's transport claim survives the wire, because a catalog needs it to pick an invoker")
}

func TestAProviderDeclaresOpenIdentifiers(t *testing.T) {
	// The framework has no list of formats, targets, or transports. A provider
	// states what it handles, and an identifier nobody has heard of resolves like
	// any other — which is what lets a user add a format without changing anything
	// that has to recognize it.
	provider := Provider{
		ID:      "smithy",
		Role:    ProviderParser,
		Formats: []Format{"smithy"},
	}
	assert.True(t, provider.HandlesFormat("smithy"))
	assert.False(t, provider.HandlesTarget("smithy"))
	assert.False(t, provider.HandlesTransport("smithy"))

	target := Provider{ID: "local", Role: ProviderAdapter, Targets: []string{"never-heard-of-it"}}
	assert.True(t, target.HandlesTarget("never-heard-of-it"))
	assert.False(t, target.HandlesFormat("never-heard-of-it"))
}
