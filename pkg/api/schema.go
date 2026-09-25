package api

import "sort"

// Schema is a provider-neutral description of a value. It is shaped like JSON
// Schema because that is the interchange format agent tooling already speaks,
// but it stays small enough to be produced from any description language and to
// be stored in a catalog without loss of the parts an agent needs.
type Schema struct {
	// Ref is a reference to another schema, such as an OpenAPI component or a
	// protobuf message name.
	Ref string
	// Type is the JSON type of the value.
	Type SchemaType
	// Format refines the type, such as int64, date-time, or uuid.
	Format string
	// Title is a short label for the value.
	Title string
	// Description explains the value.
	Description string
	// Deprecated states that the value should not be used for new work.
	Deprecated bool
	// ReadOnly states that the value is returned but not accepted as input.
	ReadOnly bool
	// Properties are the object members, in a stable order.
	Properties []Property
	// Required are the property names that must be present.
	Required []string
	// Items is the element schema of an array.
	Items *Schema
	// AdditionalProperties is the schema of unlisted object members, when the
	// description language allows them.
	AdditionalProperties *Schema
	// AdditionalPropertiesAllowed states that unlisted object members are
	// accepted without a declared schema.
	AdditionalPropertiesAllowed bool
	// Enum are the allowed literal values.
	Enum []string
	// OneOf are mutually exclusive alternatives.
	OneOf []*Schema
	// AnyOf are alternatives of which at least one applies.
	AnyOf []*Schema
	// Default is the declared default value.
	Default any
	// Minimum and Maximum bound numeric values.
	Minimum *float64
	Maximum *float64
	// MinLength and MaxLength bound string and array lengths.
	MinLength *int64
	MaxLength *int64
	// Pattern is a regular expression constraining a string value.
	Pattern string
}

// Float64 returns a pointer to a float64, for building schemas.
func Float64(value float64) *float64 { return &value }

// Int64 returns a pointer to an int64, for building schemas.
func Int64(value int64) *int64 { return &value }

// Clone returns a deep copy of the schema.
func (s *Schema) Clone() *Schema {
	if s == nil {
		return nil
	}
	clone := *s
	clone.Required = append([]string(nil), s.Required...)
	clone.Enum = append([]string(nil), s.Enum...)
	clone.Properties = make([]Property, 0, len(s.Properties))
	for _, property := range s.Properties {
		clone.Properties = append(clone.Properties, property.Clone())
	}
	clone.Items = s.Items.Clone()
	clone.AdditionalProperties = s.AdditionalProperties.Clone()
	clone.OneOf = cloneSchemas(s.OneOf)
	clone.AnyOf = cloneSchemas(s.AnyOf)
	if s.Minimum != nil {
		value := *s.Minimum
		clone.Minimum = &value
	}
	if s.Maximum != nil {
		value := *s.Maximum
		clone.Maximum = &value
	}
	if s.MinLength != nil {
		value := *s.MinLength
		clone.MinLength = &value
	}
	if s.MaxLength != nil {
		value := *s.MaxLength
		clone.MaxLength = &value
	}
	return &clone
}

func cloneSchemas(schemas []*Schema) []*Schema {
	if schemas == nil {
		return nil
	}
	clone := make([]*Schema, 0, len(schemas))
	for _, schema := range schemas {
		clone = append(clone, schema.Clone())
	}
	return clone
}

// ObjectSchema returns an object schema with the supplied properties. Property
// order is preserved, and required property names are sorted so the resulting
// JSON Schema is stable.
func ObjectSchema(properties ...Property) *Schema {
	schema := &Schema{Type: TypeObject, Properties: properties}
	required := make([]string, 0, len(properties))
	for _, property := range properties {
		if property.Required {
			required = append(required, property.Name)
		}
	}
	sort.Strings(required)
	schema.Required = required
	return schema
}

// ArraySchema returns an array schema with the supplied element schema.
func ArraySchema(items *Schema) *Schema {
	return &Schema{Type: TypeArray, Items: items}
}

// StringSchema returns a string schema.
func StringSchema() *Schema { return &Schema{Type: TypeString} }

// Property looks up one object member.
func (s *Schema) Property(name string) (Property, bool) {
	if s == nil {
		return Property{}, false
	}
	for _, property := range s.Properties {
		if property.Name == name {
			return property, true
		}
	}
	return Property{}, false
}

// IsRequired reports whether a property is required.
func (s *Schema) IsRequired(name string) bool {
	if s == nil {
		return false
	}
	for _, required := range s.Required {
		if required == name {
			return true
		}
	}
	return false
}

// JSONSchema converts the schema into the map form used by JSON Schema
// consumers such as MCP tool definitions. Property order follows the declared
// order, and the result is deterministic for a given schema.
func (s *Schema) JSONSchema() map[string]any {
	if s == nil {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	schema := map[string]any{}
	if s.Ref != "" {
		schema["$ref"] = s.Ref
	}
	if s.Type != TypeUnspecified {
		schema["type"] = s.Type.String()
	}
	if s.Format != "" {
		schema["format"] = s.Format
	}
	if s.Title != "" {
		schema["title"] = s.Title
	}
	if s.Description != "" {
		schema["description"] = s.Description
	}
	if s.Deprecated {
		schema["deprecated"] = true
	}
	if s.ReadOnly {
		schema["readOnly"] = true
	}
	if len(s.Properties) > 0 {
		properties := make(map[string]any, len(s.Properties))
		for _, property := range s.Properties {
			properties[property.Name] = property.Schema.JSONSchema()
		}
		schema["properties"] = properties
	}
	if len(s.Required) > 0 {
		required := append([]string(nil), s.Required...)
		sort.Strings(required)
		schema["required"] = required
	}
	if s.Items != nil {
		schema["items"] = s.Items.JSONSchema()
	}
	switch {
	case s.AdditionalProperties != nil:
		schema["additionalProperties"] = s.AdditionalProperties.JSONSchema()
	case s.AdditionalPropertiesAllowed:
		schema["additionalProperties"] = true
	}
	if len(s.Enum) > 0 {
		values := make([]any, 0, len(s.Enum))
		for _, value := range s.Enum {
			values = append(values, value)
		}
		schema["enum"] = values
	}
	if len(s.OneOf) > 0 {
		schema["oneOf"] = schemaList(s.OneOf)
	}
	if len(s.AnyOf) > 0 {
		schema["anyOf"] = schemaList(s.AnyOf)
	}
	if s.Default != nil {
		schema["default"] = s.Default
	}
	if s.Minimum != nil {
		schema["minimum"] = *s.Minimum
	}
	if s.Maximum != nil {
		schema["maximum"] = *s.Maximum
	}
	if s.MinLength != nil {
		schema["minLength"] = *s.MinLength
	}
	if s.MaxLength != nil {
		schema["maxLength"] = *s.MaxLength
	}
	if s.Pattern != "" {
		schema["pattern"] = s.Pattern
	}
	return schema
}

func schemaList(schemas []*Schema) []any {
	list := make([]any, 0, len(schemas))
	for _, schema := range schemas {
		list = append(list, schema.JSONSchema())
	}
	return list
}
