package api

import (
	"encoding/json"
	"sort"
	"strings"
)

// This file reads a JSON Schema document into the standard model. The model can
// already produce JSON Schema, and a format whose source *is* JSON Schema — an MCP
// tool's input schema, an OpenAPI component — needs the other direction.
//
// It reads the keywords the model carries, which is the same subset the OpenAPI
// reader interprets. A keyword the model does not carry is not interpreted and not
// guessed at: the description says what the model can represent, and a caller that
// needs more keeps the original document.

// JSON Schema keywords this reader interprets. A document may contain others; they
// are left alone.
const (
	keywordType        = "type"
	keywordTitle       = "title"
	keywordDescription = "description"
	keywordFormat      = "format"
	keywordDeprecated  = "deprecated"
	keywordReadOnly    = "readOnly"
	keywordProperties  = "properties"
	keywordRequired    = "required"
	keywordItems       = "items"
	keywordEnum        = "enum"
	keywordDefault     = "default"
	keywordMinimum     = "minimum"
	keywordMaximum     = "maximum"
	keywordMinLength   = "minLength"
	keywordMaxLength   = "maxLength"
	keywordPattern     = "pattern"
	keywordAdditional  = "additionalProperties"
	keywordOneOf       = "oneOf"
	keywordAnyOf       = "anyOf"
	keywordRef         = "$ref"
)

// SchemaFromJSONSchema reads a JSON Schema document into the standard model.
//
// A nil or empty document reads as an absent schema: a description that declares
// no shape is a legitimate thing to describe, and the caller learns it from the nil
// return rather than from an error it cannot act on.
func SchemaFromJSONSchema(document map[string]any) *Schema {
	if len(document) == 0 {
		return nil
	}
	schema := &Schema{}
	if reference, ok := document[keywordRef].(string); ok {
		schema.Ref = strings.TrimSpace(reference)
	}
	schema.Type = schemaTypeFromJSON(document[keywordType])
	schema.Title = stringValue(document[keywordTitle])
	schema.Description = stringValue(document[keywordDescription])
	schema.Format = stringValue(document[keywordFormat])
	schema.Deprecated = boolValue(document[keywordDeprecated])
	schema.ReadOnly = boolValue(document[keywordReadOnly])
	schema.Minimum = numberValue(document[keywordMinimum])
	schema.Maximum = numberValue(document[keywordMaximum])
	schema.MinLength = intValue(document[keywordMinLength])
	schema.MaxLength = intValue(document[keywordMaxLength])
	schema.Pattern = stringValue(document[keywordPattern])
	schema.Default = document[keywordDefault]
	schema.Enum = stringList(document[keywordEnum])
	schema.Required = stringList(document[keywordRequired])
	schema.Properties = readProperties(document[keywordProperties], schema.Required)
	schema.Items = SchemaFromJSONSchema(childDocument(document[keywordItems]))
	if additional, ok := document[keywordAdditional].(map[string]any); ok {
		schema.AdditionalProperties = SchemaFromJSONSchema(additional)
	} else if allowed, ok := document[keywordAdditional].(bool); ok {
		schema.AdditionalPropertiesAllowed = allowed
	}
	schema.OneOf = readSchemaList(document[keywordOneOf])
	schema.AnyOf = readSchemaList(document[keywordAnyOf])
	return schema
}

func readProperties(value any, required []string) []Property {
	properties, ok := value.(map[string]any)
	if !ok || len(properties) == 0 {
		return nil
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	// A stable order keeps two reads of the same document equal, which is what
	// makes a description comparable and its digest meaningful.
	sort.Strings(names)
	must := make(map[string]bool, len(required))
	for _, name := range required {
		must[name] = true
	}
	result := make([]Property, 0, len(names))
	for _, name := range names {
		result = append(result, Property{
			Name:     name,
			Schema:   SchemaFromJSONSchema(childDocument(properties[name])),
			Required: must[name],
		})
	}
	return result
}

func readSchemaList(value any) []*Schema {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	list := make([]*Schema, 0, len(entries))
	for _, entry := range entries {
		list = append(list, SchemaFromJSONSchema(childDocument(entry)))
	}
	if len(list) == 0 {
		return nil
	}
	return list
}

func childDocument(value any) map[string]any {
	document, ok := value.(map[string]any)
	if !ok {
		// A child that is not an object describes nothing, which is reported as an
		// absent schema rather than as a failure of the whole document.
		return nil
	}
	return document
}

// schemaTypeFromJSON reads a schema's type. JSON Schema allows a list of types,
// which the model has no single value for; the first is taken, because a document
// that lists several is describing an ambiguity the standard model does not carry.
func schemaTypeFromJSON(value any) SchemaType {
	switch typed := value.(type) {
	case string:
		return SchemaType(typed)
	case []any:
		for _, entry := range typed {
			if name, ok := entry.(string); ok {
				return SchemaType(name)
			}
		}
	}
	return TypeUnspecified
}

func stringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func boolValue(value any) bool {
	flag, ok := value.(bool)
	return ok && flag
}

func numberValue(value any) *float64 {
	switch typed := value.(type) {
	case float64:
		return Float64(typed)
	case float32:
		return Float64(float64(typed))
	case int:
		return Float64(float64(typed))
	case int64:
		return Float64(float64(typed))
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return nil
		}
		return Float64(number)
	default:
		return nil
	}
}

func intValue(value any) *int64 {
	switch typed := value.(type) {
	case float64:
		return Int64(int64(typed))
	case int:
		return Int64(int64(typed))
	case int64:
		return Int64(typed)
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return nil
		}
		return Int64(number)
	default:
		return nil
	}
}

func stringList(value any) []string {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	list := make([]string, 0, len(entries))
	for _, entry := range entries {
		if text, ok := entry.(string); ok {
			list = append(list, text)
		}
	}
	if len(list) == 0 {
		return nil
	}
	return list
}
