package api

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// SchemaForMessage converts a protobuf message descriptor into the standard
// value description.
//
// This is the gRPC side of the standard model, and it lives here rather than in
// an adapter because a protobuf contract is something the framework already
// speaks: any component that needs to describe a protobuf message in standard
// terms — the MCP gateway, a gRPC provider subsystem, documentation — uses this
// one conversion instead of growing its own.
//
// Names are the protobuf JSON names, because those are the names callers and
// agent tooling use when they supply a request.
func SchemaForMessage(message protoreflect.MessageDescriptor) *Schema {
	return schemaForMessage(message, make(map[protoreflect.FullName]bool))
}

func schemaForMessage(message protoreflect.MessageDescriptor, visiting map[protoreflect.FullName]bool) *Schema {
	if message == nil {
		// An unknown message is described permissively rather than not at all: a
		// tool that accepts any object is better than a tool that cannot be
		// described.
		return &Schema{Type: TypeObject, AdditionalPropertiesAllowed: true}
	}
	schema := &Schema{
		Type:     TypeObject,
		Ref:      string(message.FullName()),
		Required: []string{},
	}
	fields := message.Fields()
	required := make([]string, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.IsExtension() {
			continue
		}
		name := field.JSONName()
		schema.Properties = append(schema.Properties, Property{
			Name:   name,
			Schema: schemaForField(field, visiting),
		})
		// A message field is the practical required set for an agent call: a
		// scalar left at its zero value is usually still a meaningful answer,
		// while an absent message is not.
		if field.HasPresence() && !field.IsList() && !field.IsMap() {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	schema.Required = required
	return schema
}

func schemaForField(field protoreflect.FieldDescriptor, visiting map[protoreflect.FullName]bool) *Schema {
	if field == nil {
		return &Schema{}
	}
	switch {
	case field.IsMap():
		// A map is an object with arbitrary keys and a declared value shape.
		return &Schema{
			Type:                 TypeObject,
			AdditionalProperties: schemaForValue(field.MapValue(), visiting),
		}
	case field.IsList():
		return &Schema{
			Type:  TypeArray,
			Items: schemaForValue(field, visiting),
		}
	default:
		return schemaForValue(field, visiting)
	}
}

func schemaForValue(field protoreflect.FieldDescriptor, visiting map[protoreflect.FullName]bool) *Schema {
	if field == nil {
		return &Schema{}
	}
	switch field.Kind() {
	case protoreflect.BoolKind:
		return &Schema{Type: TypeBoolean}
	case protoreflect.StringKind:
		return &Schema{Type: TypeString}
	case protoreflect.BytesKind:
		return &Schema{Type: TypeString, Format: "base64"}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return &Schema{Type: TypeNumber}
	case protoreflect.EnumKind:
		enum := field.Enum()
		values := make([]string, 0, enum.Values().Len())
		for i := 0; i < enum.Values().Len(); i++ {
			values = append(values, string(enum.Values().Get(i).Name()))
		}
		return &Schema{Type: TypeString, Enum: values}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		message := field.Message()
		if message == nil {
			return &Schema{Type: TypeObject, AdditionalPropertiesAllowed: true}
		}
		if visiting[message.FullName()] {
			// A recursive message is described by its reference rather than
			// expanded, so a schema always terminates.
			return &Schema{
				Type:        TypeObject,
				Ref:         string(message.FullName()),
				Description: fmt.Sprintf("recursive %s", message.FullName()),
			}
		}
		visiting[message.FullName()] = true
		defer delete(visiting, message.FullName())
		schema := schemaForMessage(message, visiting)
		// The nested shape is inlined; the reference stays on the nested value
		// only where recursion required it.
		schema.Ref = string(message.FullName())
		return schema
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return &Schema{Type: TypeInteger, Format: "int64"}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return &Schema{Type: TypeInteger, Format: "uint64", Minimum: Float64(0)}
	default:
		return &Schema{}
	}
}

// DescribeComment attaches a description taken from the source documentation of
// a message or field, when the descriptor set was generated with source info.
func DescribeComment(schema *Schema, comment string) *Schema {
	if schema == nil || comment == "" {
		return schema
	}
	schema.Description = comment
	return schema
}
