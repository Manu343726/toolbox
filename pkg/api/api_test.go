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

func TestCapabilityNamesAreOpenIdentifiers(t *testing.T) {
	assert.Equal(t, "api.parse.smithy", ParseCapability("smithy"))
	assert.Equal(t, "api.render.mcp", RenderCapability("mcp"))
	assert.Equal(t, "api.invoke.carrier-pigeon", InvokeCapability("carrier-pigeon"))

	identifier, ok := IdentifierFromCapability("api.parse.smithy", CapabilityParse)
	require.True(t, ok)
	assert.Equal(t, "smithy", identifier)

	// A user-defined identifier resolves like any other; the framework has no list.
	identifier, ok = IdentifierFromCapability("api.render.never-heard-of-it", CapabilityRender)
	require.True(t, ok)
	assert.Equal(t, "never-heard-of-it", identifier)

	_, ok = IdentifierFromCapability("api.parse", CapabilityParse)
	assert.False(t, ok, "a role capability without an identifier does not name one")
	_, ok = IdentifierFromCapability("api.parse.smithy", CapabilityInvoke)
	assert.False(t, ok, "the role prefix must match")
	_, ok = IdentifierFromCapability("knowledge.search", CapabilityParse)
	assert.False(t, ok, "an unrelated capability names no identifier")
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
