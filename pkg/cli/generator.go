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
	// IncludeReflection includes the protocol's reflection services, which
	// describe a contract rather than provide a capability. It is false by
	// default, because a caller that already knows the contract has no use for it.
	//
	// Nothing else is excluded. A health check, a registry, and a documentation
	// service are commands a caller may want, and a subsystem that mounts one has
	// said so by declaring its capability.
	IncludeReflection bool
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
		Short:         firstSentence(g.options.Description),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	if root.Short == "" {
		root.Short = "Generated Toolbox service commands"
	}
	included := make([]string, 0, len(serviceNames))
	for _, name := range serviceNames {
		if !g.options.IncludeReflection && isInfrastructure(name) {
			continue
		}
		included = append(included, name)
	}
	// Every service is described before any command is named, because whether a name
	// needs qualifying depends on which other services are present. Naming as it went
	// would make the first claimant's name depend on the order services arrived in.
	schemas := make([]*discovery.ServiceSchema, 0, len(included))
	for _, name := range included {
		schema, err := g.source.DescribeService(ctx, name)
		if err != nil {
			return nil, err
		}
		schemas = append(schemas, schema)
	}
	namer := newCommandNamer(included)
	for _, schema := range schemas {
		built, err := g.commandForSchemaUnder(root.Name(), namer.name(schema.Name), schema)
		if err != nil {
			return nil, err
		}
		if built.group != nil {
			root.AddCommand(built.group)
			continue
		}
		root.AddCommand(built.methods...)
	}
	InstallHelpLayout(root)
	return root, nil
}

// CommandForSchema builds the command subtree for one reflected service.
func (g *Generator) CommandForSchema(schema *discovery.ServiceSchema) (*cobra.Command, error) {
	return g.commandForSchema(schema)
}

func (g *Generator) commandForSchema(schema *discovery.ServiceSchema) (*cobra.Command, error) {
	built, err := g.commandForSchemaUnder("", shortServiceName(schema.Name), schema)
	if err != nil {
		return nil, err
	}
	if built.group != nil {
		return built.group, nil
	}
	// Asked for one service, given its methods. Wrapping them keeps the shape a caller
	// of this function expects: a command that holds a service's methods.
	holder := &cobra.Command{
		Use:   shortServiceName(schema.Name),
		Short: firstSentence(documentationDescription(schema)),
		Long:  schema.Name,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	holder.AddCommand(built.methods...)
	InstallHelpLayout(holder)
	return holder, nil
}

// serviceCommands is what one service contributes to a parent: either a namespace
// command holding its methods, or the methods on their own.
//
// It is two fields rather than one command because a flattened service has to be
// spliced into the parent. Marking the group hidden does not work: cobra does not list
// the children of a hidden command either, so the methods would disappear from help and
// from shell completion, which is the exact problem being fixed.
type serviceCommands struct {
	group   *cobra.Command
	methods []*cobra.Command
}

func documentationDescription(schema *discovery.ServiceSchema) string {
	if schema == nil || schema.Documentation == nil {
		return ""
	}
	return schema.Documentation.Description
}

// commandForSchemaUnder builds a service's commands, flattening the service level away
// when it would only repeat a name the caller already has to type.
//
// A service command is a namespace, and a namespace that costs the caller an extra word
// per invocation while naming nothing they did not already name is noise. Two cases
// produce one, and both were found by running the commands rather than reading them:
//
//   - the service's short name is the command's own name, so every call reads
//     "knowledge knowledge search". The contract is a package, the command is already
//     that package's short name, and the level says nothing.
//   - a method of the service has the service's own name, so the method is a child of a
//     command with the same name. Cobra resolves the bare word to the parent, so the
//     method disappears from help and from shell completion while still working if typed
//     out in full. EchoService.Echo is the case that showed this up.
//
// In both cases the methods attach to the caller instead. A caller who wants the
// qualified name still has it in the help text, which states the fully-qualified service
// and method.
func (g *Generator) commandForSchemaUnder(parentName, name string, schema *discovery.ServiceSchema) (serviceCommands, error) {
	if schema == nil || schema.Name == "" {
		return serviceCommands{}, fmt.Errorf("service schema and name are required")
	}
	if schema.Documentation == nil || schema.Documentation.Description == "" {
		if source, ok := g.source.(DocumentationSource); ok {
			if documentation, err := source.GetDocumentation(context.Background(), schema.Name); err == nil {
				schema.Documentation = documentation
			}
		}
	}
	serviceName := name
	redundant := sameCommandName(parentName, serviceName)
	for i := range schema.Methods {
		if sameCommandName(serviceName, camelToKebab(schema.Methods[i].Name)) {
			redundant = true
			break
		}
	}
	if redundant {
		built, err := g.methodsForSchema(schema)
		if err != nil {
			return serviceCommands{}, err
		}
		return serviceCommands{methods: built}, nil
	}
	description := ""
	if schema.Documentation != nil {
		description = schema.Documentation.Description
	}
	serviceCommand := &cobra.Command{
		Use:   serviceName,
		Short: firstSentence(description),
		Long:  schema.Name,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	built, err := g.methodsForSchema(schema)
	if err != nil {
		return serviceCommands{}, err
	}
	serviceCommand.AddCommand(built...)
	return serviceCommands{group: serviceCommand}, nil
}

// methodsForSchema builds one command per method of a service.
//
// It is separate from the service command so the flattening decision lives in one place:
// whether a service gets a level of its own is decided once, and the method commands
// themselves are built the same way either way.
func (g *Generator) methodsForSchema(schema *discovery.ServiceSchema) ([]*cobra.Command, error) {
	commands := make([]*cobra.Command, 0, len(schema.Methods))
	for i := range schema.Methods {
		method := schema.Methods[i]
		methodDescription := ""
		parameterDocs := map[string]string{}
		if schema.Documentation != nil {
			for _, documented := range schema.Documentation.Methods {
				if documented.Name != method.Name {
					continue
				}
				methodDescription = documented.Description
				parameterDocs = parameterDescriptions(documented.Parameters)
			}
		}
		methodName := camelToKebab(method.Name)
		methodCommand := &cobra.Command{
			Use:   methodName,
			Short: firstSentence(methodDescription),
			Long:  fmt.Sprintf("%s/%s\n\n%s", schema.Name, method.Name, unwrap(methodDescription)),
		}
		// A streaming method gets its input flags too. It cannot be invoked by this
		// generator, and saying so is the answer — but a command that accepts nothing
		// cannot show the caller what the method takes, and for a method whose only
		// interesting input is the stream itself, that is the whole contract. So the
		// help is honest and the limitation is explicit.
		bindings := addMessageFlags(methodCommand, method.Input, "", parameterDocs)
		if method.ClientStreaming || method.ServerStreaming {
			streaming := "This method streams, and the generated CLI invokes unary methods only."
			methodCommand.Short = strings.TrimSpace(firstSentence(methodDescription) + " (streaming)")
			// Stated in the long help as well as the short one: a caller who ran the
			// method's own --help is the one who needs to know before typing it, and the
			// short form is only visible from the parent's command list.
			methodCommand.Long = strings.TrimSpace(methodCommand.Long + "\n\n" + streaming)
			// Marked so a caller that composes the tree can tell a method that will be
			// refused from one that will be called, without matching on the message.
			if methodCommand.Annotations == nil {
				methodCommand.Annotations = map[string]string{}
			}
			methodCommand.Annotations["toolbox.streaming"] = "true"
			methodCommand.RunE = func(cmd *cobra.Command, _ []string) error {
				return fmt.Errorf("streaming method %s.%s is not supported by the unary CLI generator", schema.Name, method.Name)
			}
		} else {
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
		commands = append(commands, methodCommand)
	}
	return commands, nil
}

type binding struct {
	flag  string
	path  []int
	field protoreflect.FieldDescriptor
	kind  flagKind
	// read returns the flag's values as strings, or an error saying why it could not.
	//
	// It is stored per binding rather than guessed at apply time by trying one pflag
	// getter after another. Guessing is what let a map flag be registered as a plain
	// string: the getter for a slice failed, the code fell through to the string getter,
	// and a caller who passed three pairs got the last one with no error. A flag that
	// silently discards the values before it is a lie the caller cannot see.
	read func(*cobra.Command, string) ([]string, error)
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

// parameterDescriptions flattens documented parameters into flag-name keys.
//
// The keys are dotted — "source", "source.id" — because a nested message is flattened
// into dotted flags, and a description filed under the bare field name would not be
// found for the flag that sets it. The documentation model is already a tree, so
// flattening it here is the only translation; a second one that lost the nesting would
// leave every sub-flag documented as its own type.
func parameterDescriptions(parameters []docs.Parameter) map[string]string {
	described := map[string]string{}
	var walk func(prefix string, list []docs.Parameter)
	walk = func(prefix string, list []docs.Parameter) {
		for _, parameter := range list {
			key := parameter.Name
			if prefix != "" {
				key = prefix + "." + parameter.Name
			}
			if parameter.Description != "" {
				described[key] = parameter.Description
			}
			walk(key, parameter.Fields)
		}
	}
	walk("", parameters)
	return described
}

func addMessageFlags(command *cobra.Command, message protoreflect.MessageDescriptor, prefix string, parameterDocs map[string]string) []binding {
	return addMessageFlagsAt(command, message, prefix, "", parameterDocs, nil, 0, nil)
}

// maxFlagDepth bounds how deep a nested message is flattened into flags.
//
// A request message nests — a document holding documents, a call holding options —
// and each level of JSON the user has to hand-write is a level where a guessed field
// name produces "unknown field" rather than a flag that does not exist. So a singular
// message field is also flattened, and the depth is bounded because a message that
// contains itself would otherwise never stop. A message already on the path stops the
// walk too, which handles mutual recursion that no depth limit would.
const maxFlagDepth = 4

// indexPath is the chain of field indices from the top-level request down to message,
// because a sub-field's binding addresses a nested field and the path it is applied
// against is the whole chain, not the last step.
// docPrefix is the documentation path, which is the protobuf field names joined by dots
// — "source.id". It is not the flag prefix, which is the same path kebab-cased
// ("source.id" happens to match, but "source_id" and "source-id" do not), so the two
// are tracked separately rather than converted between.
func addMessageFlagsAt(command *cobra.Command, message protoreflect.MessageDescriptor, prefix string, docPrefix string, parameterDocs map[string]string, path []protoreflect.FullName, depth int, indexPath []int) []binding {
	if message == nil || depth > maxFlagDepth {
		return nil
	}
	path = append(path, message.FullName())
	var bindings []binding
	fields := message.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		name := prefix + camelToKebab(string(field.Name()))
		bindPath := make([]int, 0, len(indexPath)+1)
		bindPath = append(bindPath, indexPath...)
		bindPath = append(bindPath, field.Index())
		kind := kindForField(field)
		// The description is flattened here rather than at each use, because a flag's help is
		// laid out in one column beside its name: a line break inherited from the source file
		// is a ragged row, and a paragraph break in the middle of one renders as a blank line
		// and a fresh indent, which reads as three separate flags.
		description := flatten(parameterDocs[docPrefix+string(field.Name())])
		if description == "" {
			description = fieldKindString(field)
		}
		switch {
		case field.IsMap():
			// Repeatable, because a map is more than one entry and a caller needs to
			// know they may pass several pairs rather than one that overwrites itself.
			// StringArray, not StringSlice: a slice splits its value on commas, and a
			// map entry's value may legitimately contain one. This is also what makes
			// the flag accumulate, so three pairs stay three pairs.
			command.Flags().StringArray(name, nil, fmt.Sprintf("%s (map; use key=value, repeatable)", description))
			bindings = append(bindings, binding{flag: name, path: bindPath, field: field, kind: flagMap, read: readStringArray})
		case field.IsList():
			// A repeated message takes one JSON object per value rather than a JSON
			// array, so a caller can build a list of any length from repeated flags and
			// the help says so. There is deliberately no flattening here: no flag
			// addresses "--sources.0.id", so a dotted sub-flag would be a lie about
			// which element it sets.
			note := description + " (repeated)"
			read := readStringSlice
			switch kind {
			case flagInt:
				command.Flags().Int64Slice(name, nil, note)
				read = readInt64Slice
			case flagUint:
				// pflag has no unsigned slice. The flag is an int64 slice and the
				// parse is still unsigned, so a negative value is refused where it
				// would be sent, rather than silently wrapping.
				command.Flags().Int64Slice(name, nil, note)
				read = readInt64Slice
			case flagFloat:
				command.Flags().Float64Slice(name, nil, note)
				read = readFloat64Slice
			case flagMessage:
				// StringArray, because a JSON object is full of commas and a slice
				// would take them for separators and hand back fragments.
				note = description + " (repeated; one JSON object per value)"
				command.Flags().StringArray(name, nil, note)
				read = readStringArray
			default:
				command.Flags().StringSlice(name, nil, note)
			}
			bindings = append(bindings, binding{flag: name, path: bindPath, field: field, kind: kind, read: read})
		case kind == flagBool:
			command.Flags().Bool(name, false, description)
			bindings = append(bindings, binding{flag: name, path: bindPath, field: field, kind: kind, read: readBool})
		case kind == flagInt:
			command.Flags().Int64(name, 0, description)
			bindings = append(bindings, binding{flag: name, path: bindPath, field: field, kind: kind, read: readInt64})
		case kind == flagUint:
			command.Flags().Uint64(name, 0, description)
			bindings = append(bindings, binding{flag: name, path: bindPath, field: field, kind: kind, read: readUint64})
		case kind == flagFloat:
			command.Flags().Float64(name, 0, description)
			bindings = append(bindings, binding{flag: name, path: bindPath, field: field, kind: kind, read: readFloat64})
		case kind == flagEnum:
			// An enum flag takes a name or a number, and the help lists what is
			// allowed: the values are the contract, and a caller who has to read the
			// proto to learn them is doing the generator's job by hand.
			command.Flags().String(name, "", fmt.Sprintf("%s (one of: %s)", description, enumValueList(field)))
			bindings = append(bindings, binding{flag: name, path: bindPath, field: field, kind: kind, read: readString})
		case kind == flagMessage:
			// The JSON flag stays, so nothing becomes unreachable, and it is declared
			// first so a sub-flag below overrides it rather than the other way round.
			command.Flags().String(name, "", description+" (JSON)")
			bindings = append(bindings, binding{flag: name, path: bindPath, field: field, kind: kind, read: readString})
			// Only a singular message is flattened. A repeated one has no addressable
			// elements from a flag — there is no "--sources.0.id" — so it stays JSON.
			if !containsName(path, field.Message().FullName()) {
				bindings = append(bindings, addMessageFlagsAt(command, field.Message(), name+".", docPrefix+string(field.Name())+".", parameterDocs, path, depth+1, bindPath)...)
			}
		default:
			command.Flags().String(name, "", description)
			bindings = append(bindings, binding{flag: name, path: bindPath, field: field, kind: kind, read: readString})
		}
	}
	return bindings
}

// containsName reports whether a message is already on the current path, which is how
// a self-referential message stops the walk.
func containsName(path []protoreflect.FullName, name protoreflect.FullName) bool {
	for _, seen := range path {
		if seen == name {
			return true
		}
	}
	return false
}

// enumValueList renders an enum's values for a flag's help, in declaration order.
func enumValueList(field protoreflect.FieldDescriptor) string {
	values := field.Enum().Values()
	names := make([]string, 0, values.Len())
	for i := 0; i < values.Len(); i++ {
		names = append(names, string(values.Get(i).Name()))
	}
	return strings.Join(names, ", ")
}

func applyBindings(command *cobra.Command, message *dynamicpb.Message, bindings []binding) error {
	for _, item := range bindings {
		if !command.Flags().Changed(item.flag) {
			continue
		}
		values, err := item.read(command, item.flag)
		if err != nil {
			return fmt.Errorf("read flag --%s: %w", item.flag, err)
		}
		if err := setAtPath(message, item.path, item.field, values); err != nil {
			return fmt.Errorf("flag --%s: %w", item.flag, err)
		}
	}
	return nil
}

// The readers. Each is the getter for exactly the type its flag was registered as, so
// asking for a value cannot silently answer with a different flag's type.

func readString(command *cobra.Command, name string) ([]string, error) {
	value, err := command.Flags().GetString(name)
	if err != nil {
		return nil, err
	}
	return []string{value}, nil
}

func readStringSlice(command *cobra.Command, name string) ([]string, error) {
	return command.Flags().GetStringSlice(name)
}

func readStringArray(command *cobra.Command, name string) ([]string, error) {
	return command.Flags().GetStringArray(name)
}

func readBool(command *cobra.Command, name string) ([]string, error) {
	value, err := command.Flags().GetBool(name)
	if err != nil {
		return nil, err
	}
	return []string{strconv.FormatBool(value)}, nil
}

func readInt64(command *cobra.Command, name string) ([]string, error) {
	value, err := command.Flags().GetInt64(name)
	if err != nil {
		return nil, err
	}
	return []string{strconv.FormatInt(value, 10)}, nil
}

func readUint64(command *cobra.Command, name string) ([]string, error) {
	value, err := command.Flags().GetUint64(name)
	if err != nil {
		return nil, err
	}
	return []string{strconv.FormatUint(value, 10)}, nil
}

func readFloat64(command *cobra.Command, name string) ([]string, error) {
	value, err := command.Flags().GetFloat64(name)
	if err != nil {
		return nil, err
	}
	return []string{strconv.FormatFloat(value, 'g', -1, 64)}, nil
}

func readInt64Slice(command *cobra.Command, name string) ([]string, error) {
	values, err := command.Flags().GetInt64Slice(name)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strconv.FormatInt(value, 10)
	}
	return result, nil
}

func readFloat64Slice(command *cobra.Command, name string) ([]string, error) {
	values, err := command.Flags().GetFloat64Slice(name)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strconv.FormatFloat(value, 'g', -1, 64)
	}
	return result, nil
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
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		value, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%q is not a 32-bit signed integer: %w", raw, err)
		}
		return protoreflect.ValueOfInt32(int32(value)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%q is not a 64-bit signed integer: %w", raw, err)
		}
		return protoreflect.ValueOfInt64(value), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%q is not a 32-bit unsigned integer: %w", raw, err)
		}
		return protoreflect.ValueOfUint32(uint32(value)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%q is not a 64-bit unsigned integer: %w", raw, err)
		}
		return protoreflect.ValueOfUint64(value), nil
	case protoreflect.FloatKind:
		value, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%q is not a 32-bit float: %w", raw, err)
		}
		return protoreflect.ValueOfFloat32(float32(value)), nil
	case protoreflect.DoubleKind:
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%q is not a 64-bit float: %w", raw, err)
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

// isInfrastructure reports whether a service belongs to the protocol rather than
// to a subsystem: the reflection services, which exist so a client can discover a
// contract. They are not a capability, so they are not commands.
func isInfrastructure(name string) bool {
	return discovery.IsReflectionService(name)
}

// sameCommandName compares two command names the way a caller would read them, ignoring
// the punctuation and case that separate a subsystem from its own service: "apitools"
// and "api-tools" are the same word to somebody typing a command, and a level of
// nesting between them costs a word on every invocation to say nothing.
func sameCommandName(a, b string) bool {
	return normaliseCommandName(a) == normaliseCommandName(b)
}

func normaliseCommandName(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
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
