package mcp

import (
	"fmt"
	"sort"

	"github.com/Manu343726/toolbox/pkg/docs"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// jsonSchemaForMessage converts a protobuf request or response descriptor into
// a JSON Schema object suitable for an MCP tool definition. It intentionally
// uses protobuf JSON names because those are the names accepted by protojson
// and by generated MCP clients.
func jsonSchemaForMessage(message protoreflect.MessageDescriptor, parameters []docs.Parameter) map[string]any {
	return jsonSchemaForMessageWithVisited(message, parameters, make(map[protoreflect.FullName]bool))
}

func jsonSchemaForMessageWithVisited(message protoreflect.MessageDescriptor, parameters []docs.Parameter, visited map[protoreflect.FullName]bool) map[string]any {
	if message == nil {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	properties := make(map[string]any)
	required := make([]string, 0)
	fields := message.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.IsExtension() {
			continue
		}
		name := field.JSONName()
		parameter := findParameter(parameters, string(field.Name()))
		properties[name] = jsonSchemaForField(field, parameter, visited)
		if field.HasPresence() && !field.IsList() && !field.IsMap() {
			required = append(required, name)
		}
	}
	// Message fields are the practical required set for agent calls. Do not
	// mark ordinary proto3 scalars required merely because their zero value is
	// meaningful; this matches the existing CLI generator's behavior.
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

func findParameter(parameters []docs.Parameter, name string) *docs.Parameter {
	for i := range parameters {
		if parameters[i].Name == name {
			return &parameters[i]
		}
	}
	return nil
}

func jsonSchemaForField(field protoreflect.FieldDescriptor, parameter *docs.Parameter, visited map[protoreflect.FullName]bool) map[string]any {
	schema := jsonSchemaForSingular(field, parameter, visited)
	if field.IsMap() {
		schema["type"] = "object"
		// The same visited set, not a fresh one. A map's value type is reached by the same
		// walk as any other field, and a fresh set here would forget every ancestor — which
		// makes the cycle guard useless for exactly the shape that recurses through a map.
		// `google.protobuf.Struct` is such a shape: Struct → map<string, Value> → Value →
		// Struct, and with a fresh set at the map the walk never terminates.
		schema["additionalProperties"] = jsonSchemaForValue(field.MapValue(), nil, visited)
		delete(schema, "items")
		return schema
	}
	if field.IsList() {
		schema["type"] = "array"
		schema["items"] = jsonSchemaForValue(field, parameter, visited)
		return schema
	}
	return schema
}

func jsonSchemaForSingular(field protoreflect.FieldDescriptor, parameter *docs.Parameter, visited map[protoreflect.FullName]bool) map[string]any {
	schema := jsonSchemaForValue(field, parameter, visited)
	if parameter != nil && parameter.Description != "" {
		schema["description"] = parameter.Description
	}
	return schema
}

func jsonSchemaForValue(field protoreflect.FieldDescriptor, parameter *docs.Parameter, visited map[protoreflect.FullName]bool) map[string]any {
	if field == nil {
		return map[string]any{}
	}
	switch field.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	case protoreflect.EnumKind:
		values := make([]string, 0, field.Enum().Values().Len())
		for i := 0; i < field.Enum().Values().Len(); i++ {
			values = append(values, string(field.Enum().Values().Get(i).Name()))
		}
		return map[string]any{"type": "string", "enum": values}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		message := field.Message()
		if message == nil {
			return map[string]any{"type": "object", "additionalProperties": true}
		}
		if visited[message.FullName()] {
			return map[string]any{"type": "object", "additionalProperties": true, "description": fmt.Sprintf("recursive %s", message.FullName())}
		}
		visited[message.FullName()] = true
		schema := jsonSchemaForMessageWithVisited(message, nil, visited)
		delete(schema, "description")
		delete(visited, message.FullName())
		return schema
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return map[string]any{"type": "integer", "minimum": 0}
	default:
		return map[string]any{}
	}
}
