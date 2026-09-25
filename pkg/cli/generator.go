// Package cli generates Cobra commands from reflected protobuf services.
// Subsystem commands use this package instead of hand-written method or flag
// definitions.
package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/Manu343726/toolbox/pkg/docs"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Source supplies reflected schemas and dynamic unary calls.
type Source interface {
	ListServices(context.Context) ([]string, error)
	DescribeService(context.Context, string) (*discovery.ServiceSchema, error)
	Invoke(context.Context, string, string, proto.Message) (proto.Message, error)
}

// DocumentationSource is an optional extension implemented by discovery
// clients that can query a remote DocumentationService.
type DocumentationSource interface {
	GetDocumentation(context.Context, string) (*docs.Service, error)
}

// Options controls generated command names and visibility.
type Options struct {
	// CommandName is the root command name. Empty uses "toolbox".
	CommandName string
	// Description is displayed in root help.
	Description string
	// IncludeInfrastructure includes reflection, health, registry, and
	// documentation services. It is false by default.
	IncludeInfrastructure bool
}

// Generator creates a command tree from a discovery source.
type Generator struct {
	source  Source
	options Options
}

// NewGenerator creates a schema-driven CLI generator.
func NewGenerator(source Source, options Options) *Generator {
	if options.CommandName == "" {
		options.CommandName = "toolbox"
	}
	return &Generator{source: source, options: options}
}

// Generate builds commands for the requested services. With no names, it
// discovers all user-facing services from the source.
func (g *Generator) Generate(ctx context.Context, serviceNames ...string) (*cobra.Command, error) {
	if g == nil || g.source == nil {
		return nil, fmt.Errorf("CLI generator source is required")
	}
	if len(serviceNames) == 0 {
		var err error
		serviceNames, err = g.source.ListServices(ctx)
		if err != nil {
			return nil, err
		}
	}
	root := &cobra.Command{
		Use:           g.options.CommandName,
		Short:         firstLine(g.options.Description),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	if root.Short == "" {
		root.Short = "Generated Toolbox service commands"
	}
	for _, name := range serviceNames {
		if !g.options.IncludeInfrastructure && isInfrastructure(name) {
			continue
		}
		schema, err := g.source.DescribeService(ctx, name)
		if err != nil {
			return nil, err
		}
		serviceCommand, err := g.commandForSchema(schema)
		if err != nil {
			return nil, err
		}
		root.AddCommand(serviceCommand)
	}
	return root, nil
}

// CommandForSchema builds the command subtree for one reflected service.
func (g *Generator) CommandForSchema(schema *discovery.ServiceSchema) (*cobra.Command, error) {
	return g.commandForSchema(schema)
}

func (g *Generator) commandForSchema(schema *discovery.ServiceSchema) (*cobra.Command, error) {
	if schema == nil || schema.Name == "" {
		return nil, fmt.Errorf("service schema and name are required")
	}
	if schema.Documentation == nil || schema.Documentation.Description == "" {
		if source, ok := g.source.(DocumentationSource); ok {
			if documentation, err := source.GetDocumentation(context.Background(), schema.Name); err == nil {
				schema.Documentation = documentation
			}
		}
	}
	serviceName := shortServiceName(schema.Name)
	description := ""
	if schema.Documentation != nil {
		description = schema.Documentation.Description
	}
	serviceCommand := &cobra.Command{
		Use:   serviceName,
		Short: firstLine(description),
		Long:  schema.Name,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	for i := range schema.Methods {
		method := schema.Methods[i]
		methodDescription := ""
		if schema.Documentation != nil {
			for _, documented := range schema.Documentation.Methods {
				if documented.Name == method.Name {
					methodDescription = documented.Description
					break
				}
			}
		}
		methodCommand := &cobra.Command{
			Use:   camelToKebab(method.Name),
			Short: firstLine(methodDescription),
			Long:  fmt.Sprintf("%s/%s\n\n%s", schema.Name, method.Name, methodDescription),
		}
		if method.ClientStreaming || method.ServerStreaming {
			methodCommand.RunE = func(cmd *cobra.Command, _ []string) error {
				return fmt.Errorf("streaming method %s.%s is not supported by the unary CLI generator", schema.Name, method.Name)
			}
		} else {
			parameterDocs := map[string]string{}
			if schema.Documentation != nil {
				for _, documentedMethod := range schema.Documentation.Methods {
					if documentedMethod.Name == method.Name {
						for _, parameter := range documentedMethod.Parameters {
							parameterDocs[parameter.Name] = parameter.Description
						}
					}
				}
			}
			bindings := addMessageFlags(methodCommand, method.Input, "", parameterDocs)
			methodCommand.RunE = func(cmd *cobra.Command, _ []string) error {
				request := dynamicpb.NewMessage(method.Input)
				if err := applyBindings(cmd, request, bindings); err != nil {
					return fmt.Errorf("build %s.%s request: %w", schema.Name, method.Name, err)
				}
				response, err := g.source.Invoke(cmd.Context(), schema.Name, method.Name, request)
				if err != nil {
					return err
				}
				output, err := (protojson.MarshalOptions{Indent: "  ", UseProtoNames: true, EmitUnpopulated: true}).Marshal(response)
				if err != nil {
					return fmt.Errorf("encode response: %w", err)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(output))
				return err
			}
		}
		serviceCommand.AddCommand(methodCommand)
	}
	return serviceCommand, nil
}

type binding struct {
	flag  string
	path  []int
	field protoreflect.FieldDescriptor
	kind  flagKind
}

type flagKind uint8

const (
	flagString flagKind = iota
	flagBool
	flagInt
	flagUint
	flagFloat
	flagEnum
	flagBytes
	flagMessage
	flagMap
)

func addMessageFlags(command *cobra.Command, message protoreflect.MessageDescriptor, prefix string, parameterDocs map[string]string) []binding {
	if message == nil {
		return nil
	}
	var bindings []binding
	fields := message.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		name := prefix + camelToKebab(string(field.Name()))
		path := []int{field.Index()}
		kind := kindForField(field)
		description := parameterDocs[string(field.Name())]
		if description == "" {
			description = fieldKindString(field)
		}
		switch {
		case field.IsMap():
			command.Flags().String(name, "", fmt.Sprintf("%s (map; use key=value)", description))
			bindings = append(bindings, binding{flag: name, path: path, field: field, kind: flagMap})
		case field.IsList():
			switch kind {
			case flagInt:
				command.Flags().Int64Slice(name, nil, description+" (repeated)")
			case flagFloat:
				command.Flags().Float64Slice(name, nil, description+" (repeated)")
			default:
				command.Flags().StringSlice(name, nil, description+" (repeated)")
			}
			bindings = append(bindings, binding{flag: name, path: path, field: field, kind: kind})
		case kind == flagBool:
			command.Flags().Bool(name, false, description)
			bindings = append(bindings, binding{flag: name, path: path, field: field, kind: kind})
		case kind == flagInt:
			command.Flags().Int64(name, 0, description)
			bindings = append(bindings, binding{flag: name, path: path, field: field, kind: kind})
		case kind == flagUint:
			command.Flags().Uint64(name, 0, description)
			bindings = append(bindings, binding{flag: name, path: path, field: field, kind: kind})
		case kind == flagFloat:
			command.Flags().Float64(name, 0, description)
			bindings = append(bindings, binding{flag: name, path: path, field: field, kind: kind})
		case kind == flagEnum:
			command.Flags().String(name, firstEnumValue(field), description)
			bindings = append(bindings, binding{flag: name, path: path, field: field, kind: kind})
		case kind == flagMessage:
			command.Flags().String(name, "", description+" (JSON)")
			bindings = append(bindings, binding{flag: name, path: path, field: field, kind: kind})
		default:
			command.Flags().String(name, "", description)
			bindings = append(bindings, binding{flag: name, path: path, field: field, kind: kind})
		}
	}
	return bindings
}

func applyBindings(command *cobra.Command, message *dynamicpb.Message, bindings []binding) error {
	for _, item := range bindings {
		if !command.Flags().Changed(item.flag) {
			continue
		}
		values, err := flagValues(command, item)
		if err != nil {
			return fmt.Errorf("flag --%s: %w", item.flag, err)
		}
		if err := setAtPath(message, item.path, item.field, values); err != nil {
			return fmt.Errorf("flag --%s: %w", item.flag, err)
		}
	}
	return nil
}

func flagValues(command *cobra.Command, item binding) ([]string, error) {
	if item.field.IsList() {
		switch item.kind {
		case flagInt:
			values, err := command.Flags().GetInt64Slice(item.flag)
			if err != nil {
				return nil, err
			}
			result := make([]string, len(values))
			for i, value := range values {
				result[i] = strconv.FormatInt(value, 10)
			}
			return result, nil
		case flagFloat:
			values, err := command.Flags().GetFloat64Slice(item.flag)
			if err != nil {
				return nil, err
			}
			result := make([]string, len(values))
			for i, value := range values {
				result[i] = strconv.FormatFloat(value, 'g', -1, 64)
			}
			return result, nil
		default:
			return command.Flags().GetStringSlice(item.flag)
		}
	}
	value, err := command.Flags().GetString(item.flag)
	if err == nil {
		return []string{value}, nil
	}
	if boolValue, boolErr := command.Flags().GetBool(item.flag); boolErr == nil {
		return []string{strconv.FormatBool(boolValue)}, nil
	}
	if intValue, intErr := command.Flags().GetInt64(item.flag); intErr == nil {
		return []string{strconv.FormatInt(intValue, 10)}, nil
	}
	if uintValue, uintErr := command.Flags().GetUint64(item.flag); uintErr == nil {
		return []string{strconv.FormatUint(uintValue, 10)}, nil
	}
	if floatValue, floatErr := command.Flags().GetFloat64(item.flag); floatErr == nil {
		return []string{strconv.FormatFloat(floatValue, 'g', -1, 64)}, nil
	}
	return nil, err
}

func setAtPath(message *dynamicpb.Message, path []int, field protoreflect.FieldDescriptor, values []string) error {
	if len(path) == 0 {
		return fmt.Errorf("empty field path")
	}
	current := message.ProtoReflect()
	for _, index := range path[:len(path)-1] {
		parent := current.Descriptor().Fields().Get(index)
		if parent.Kind() != protoreflect.MessageKind && parent.Kind() != protoreflect.GroupKind {
			return fmt.Errorf("field %s is not a message", parent.Name())
		}
		current = current.Mutable(parent).Message()
	}
	return setField(current, field, values)
}

func setField(message protoreflect.Message, field protoreflect.FieldDescriptor, values []string) error {
	if field.IsMap() {
		return setMap(message, field, values)
	}
	if field.IsList() {
		list := message.Mutable(field).List()
		for _, value := range values {
			parsed, err := parseValue(field, value)
			if err != nil {
				if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
					return err
				}
				nested := dynamicpb.NewMessage(field.Message())
				if err := (protojson.UnmarshalOptions{}).Unmarshal([]byte(value), nested); err != nil {
					return err
				}
				parsed = protoreflect.ValueOfMessage(nested)
			}
			list.Append(parsed)
		}
		return nil
	}
	if len(values) != 1 {
		return fmt.Errorf("expected one value")
	}
	parsed, err := parseValue(field, values[0])
	if err != nil {
		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			return err
		}
		nested := dynamicpb.NewMessage(field.Message())
		if err := (protojson.UnmarshalOptions{}).Unmarshal([]byte(values[0]), nested); err != nil {
			return err
		}
		parsed = protoreflect.ValueOfMessage(nested)
	}
	message.Set(field, parsed)
	return nil
}

func setMap(message protoreflect.Message, field protoreflect.FieldDescriptor, values []string) error {
	if field.MapKey().Kind() == protoreflect.MessageKind || field.MapKey().Kind() == protoreflect.GroupKind {
		return fmt.Errorf("message map keys are not supported")
	}
	mapValue := message.Mutable(field).Map()
	for _, value := range values {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("map values use key=value")
		}
		key, err := parseValue(field.MapKey(), parts[0])
		if err != nil {
			return err
		}
		valueParsed, err := parseValue(field.MapValue(), parts[1])
		if err != nil {
			return err
		}
		mapValue.Set(key.MapKey(), valueParsed)
	}
	return nil
}

func parseValue(field protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	switch field.Kind() {
	case protoreflect.BoolKind:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfBool(value), nil
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw), nil
	case protoreflect.BytesKind:
		value, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfBytes(value), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind, protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		if field.Kind() == protoreflect.Int32Kind || field.Kind() == protoreflect.Sint32Kind || field.Kind() == protoreflect.Sfixed32Kind {
			if value < -2147483648 || value > 2147483647 {
				return protoreflect.Value{}, fmt.Errorf("value out of int32 range")
			}
		}
		return protoreflect.ValueOfInt64(value), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		if field.Kind() == protoreflect.Uint32Kind || field.Kind() == protoreflect.Fixed32Kind {
			if value > 4294967295 {
				return protoreflect.Value{}, fmt.Errorf("value out of uint32 range")
			}
		}
		return protoreflect.ValueOfUint64(value), nil
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat64(value), nil
	case protoreflect.EnumKind:
		enum := field.Enum()
		for i := 0; i < enum.Values().Len(); i++ {
			value := enum.Values().Get(i)
			if raw == string(value.Name()) || raw == strconv.Itoa(int(value.Number())) {
				return protoreflect.ValueOfEnum(value.Number()), nil
			}
		}
		return protoreflect.Value{}, fmt.Errorf("unknown enum value %q", raw)
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return protoreflect.Value{}, fmt.Errorf("message field requires JSON input")
	default:
		return protoreflect.Value{}, fmt.Errorf("unsupported field kind %s", field.Kind())
	}
}

func kindForField(field protoreflect.FieldDescriptor) flagKind {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return flagBool
	case protoreflect.StringKind:
		return flagString
	case protoreflect.BytesKind:
		return flagBytes
	case protoreflect.EnumKind:
		return flagEnum
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return flagMessage
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind, protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return flagInt
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return flagUint
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return flagFloat
	default:
		return flagString
	}
}

func fieldKindString(field protoreflect.FieldDescriptor) string {
	if field.Kind() == protoreflect.EnumKind {
		return "enum " + string(field.Enum().FullName())
	}
	if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
		return "message " + string(field.Message().FullName())
	}
	return field.Kind().String()
}

func firstEnumValue(field protoreflect.FieldDescriptor) string {
	if field.Kind() != protoreflect.EnumKind || field.Enum().Values().Len() == 0 {
		return ""
	}
	return string(field.Enum().Values().Get(0).Name())
}

func isInfrastructure(name string) bool {
	return discovery.IsReflectionService(name) || strings.HasSuffix(name, ".HealthService") || strings.HasSuffix(name, ".DocumentationService") || strings.HasSuffix(name, ".RegistryService")
}

func shortServiceName(name string) string {
	parts := strings.Split(name, ".")
	value := parts[len(parts)-1]
	value = strings.TrimSuffix(value, "Service")
	return camelToKebab(value)
}

func camelToKebab(value string) string {
	var builder strings.Builder
	for i, r := range value {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(r + ('a' - 'A'))
		} else {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func firstLine(value string) string {
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return strings.TrimSpace(value[:index])
	}
	return strings.TrimSpace(value)
}
