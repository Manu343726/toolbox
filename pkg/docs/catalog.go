// Package docs extracts protobuf documentation into a framework-neutral model.
//
// The model deliberately does not import any generated service package. Each
// subservice can adapt these values to its own DocumentationService protocol,
// while the CLI can consume the same model without coupling to a registry
// implementation.
package docs

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

var defaultCatalog = NewCatalog()

// DefaultCatalog returns the process-wide catalog populated by generated
// descriptor-set registration files. It is a convenience for subsystem SDKs;
// callers may still create isolated catalogs for tests or tools.
func DefaultCatalog() *Catalog {
	return defaultCatalog
}

var (
	// ErrNotFound is returned when a service is not present in a catalog.
	ErrNotFound = errors.New("service documentation not found")
	// ErrInvalidDescriptor is returned when a descriptor cannot be documented.
	ErrInvalidDescriptor = errors.New("invalid protobuf descriptor")
)

// Service is the framework-neutral documentation for one protobuf service.
type Service struct {
	Name        string
	Description string
	// Annotations are the machine-readable lines the service's comment declared.
	// They are separate from Description because they are not prose: the CLI and
	// an MCP client read Description, and a package that owns a vocabulary reads
	// these.
	Annotations AnnotationSet
	Methods     []Method
}

// Method is the framework-neutral documentation for one RPC.
type Method struct {
	Name            string
	Description     string
	Annotations     AnnotationSet
	InputType       string
	OutputType      string
	ClientStreaming bool
	ServerStreaming bool
	Parameters      []Parameter
}

// Parameter is the framework-neutral documentation for one request field.
type Parameter struct {
	Name         string
	Description  string
	TypeName     string
	DefaultValue string
	Repeated     bool
	Required     bool
	Fields       []Parameter
}

// Catalog stores documentation indexed by fully-qualified service name.
type Catalog struct {
	mu       sync.RWMutex
	services map[string]Service
}

// NewCatalog creates an empty documentation catalog.
func NewCatalog() *Catalog {
	return &Catalog{services: make(map[string]Service)}
}

// AddService extracts and stores documentation for one service descriptor.
func (c *Catalog) AddService(service protoreflect.ServiceDescriptor) error {
	if service == nil {
		return fmt.Errorf("%w: nil service descriptor", ErrInvalidDescriptor)
	}
	doc := ExtractServiceDocumentation(service)
	if doc == nil {
		return fmt.Errorf("%w: service %q", ErrInvalidDescriptor, service.FullName())
	}
	return c.Add(*doc)
}

// AddFile extracts and stores documentation for every service in a file.
func (c *Catalog) AddFile(file protoreflect.FileDescriptor) error {
	if file == nil {
		return fmt.Errorf("%w: nil file descriptor", ErrInvalidDescriptor)
	}
	for i := 0; i < file.Services().Len(); i++ {
		if err := c.AddService(file.Services().Get(i)); err != nil {
			return err
		}
	}
	return nil
}

// Add stores a pre-built documentation entry. Values are copied before they
// enter the catalog.
func (c *Catalog) Add(doc Service) error {
	if strings.TrimSpace(doc.Name) == "" {
		return fmt.Errorf("%w: documentation requires a service name", ErrInvalidDescriptor)
	}
	c.mu.Lock()
	c.services[doc.Name] = cloneService(doc)
	c.mu.Unlock()
	return nil
}

// AddGlobalRegistry adds every protobuf service linked into the process.
func (c *Catalog) AddGlobalRegistry() error {
	var firstErr error
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if err := c.AddFile(file); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr == nil
	})
	return firstErr
}

// AddDescriptorSet adds every service in a serialized FileDescriptorSet. The
// set should be generated with --include_source_info when comments are needed.
func (c *Catalog) AddDescriptorSet(data []byte) error {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		return fmt.Errorf("decode file descriptor set: %w", err)
	}
	if len(set.GetFile()) == 0 {
		return fmt.Errorf("%w: descriptor set is empty", ErrInvalidDescriptor)
	}
	files, err := protodesc.NewFiles(&set)
	if err != nil {
		return fmt.Errorf("build descriptor set: %w", err)
	}
	var firstErr error
	var rangeErr error
	files.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if err := c.AddFile(file); err != nil {
			rangeErr = err
			return false
		}
		return true
	})
	if rangeErr != nil {
		firstErr = rangeErr
	}
	return firstErr
}

// RegisterEmbeddedFile adds a descriptor set to DefaultCatalog. Generated
// subsystem registration files call this from init.
func RegisterEmbeddedFile(data []byte) error {
	return defaultCatalog.AddDescriptorSet(data)
}

// Get returns a copy of one documentation entry.
func (c *Catalog) Get(name string) (Service, error) {
	c.mu.RLock()
	doc, ok := c.services[name]
	c.mu.RUnlock()
	if !ok {
		return Service{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return cloneService(doc), nil
}

// List returns a deterministic snapshot of all documentation entries.
func (c *Catalog) List() []Service {
	c.mu.RLock()
	result := make([]Service, 0, len(c.services))
	for _, doc := range c.services {
		result = append(result, cloneService(doc))
	}
	c.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// ExtractServiceDocumentation converts a protobuf service descriptor into the
// portable documentation model.
func ExtractServiceDocumentation(service protoreflect.ServiceDescriptor) *Service {
	if service == nil {
		return nil
	}
	// The documentation is read from a descriptor compiled from the original .proto, when this
	// process has it. A generated descriptor has the structure and none of the prose, and the
	// prose carries the annotations — so reading it from the generated descriptor loses a
	// service's side-effect declaration as well as its description, and an operation nobody
	// classified is the one state a policy cannot grant by naming read or write.
	documented := documentedService(service)
	if documented != nil {
		service = documented
	}
	file := service.ParentFile()
	prose, annotations := ParseAnnotations(commentAt(file, servicePath(service)))
	doc := &Service{
		Name:        string(service.FullName()),
		Description: prose,
		Annotations: annotations,
		Methods:     make([]Method, 0, service.Methods().Len()),
	}
	for i := 0; i < service.Methods().Len(); i++ {
		method := service.Methods().Get(i)
		methodProse, methodAnnotations := ParseAnnotations(
			commentAt(file, append(servicePath(service), fieldMethod, int32(method.Index()))),
		)
		methodDoc := Method{
			Name:            string(method.Name()),
			Description:     methodProse,
			Annotations:     methodAnnotations,
			InputType:       string(method.Input().FullName()),
			OutputType:      string(method.Output().FullName()),
			ClientStreaming: method.IsStreamingClient(),
			ServerStreaming: method.IsStreamingServer(),
		}
		if inputPath, ok := descriptorPath(file, method.Input()); ok {
			methodDoc.Parameters = messageParameters(file, inputPath, method.Input(), make(map[protoreflect.FullName]bool), 0)
		}
		doc.Methods = append(doc.Methods, methodDoc)
	}
	return doc
}

const (
	fieldService = int32(6)
	fieldMethod  = int32(2)
	fieldMessage = int32(4)
	fieldNested  = int32(3)
	fieldField   = int32(2)
)

// documentedService returns the same service as it was declared, read from the original
// source, or nil when this process does not have that source or the two disagree.
func documentedService(service protoreflect.ServiceDescriptor) protoreflect.ServiceDescriptor {
	file := service.ParentFile()
	if file == nil {
		return nil
	}
	compiled := documentedFile(file)
	if compiled == nil || compiled == file {
		return nil
	}
	return compiled.Services().ByName(service.Name())
}

func servicePath(service protoreflect.ServiceDescriptor) protoreflect.SourcePath {
	return protoreflect.SourcePath{fieldService, int32(service.Index())}
}

func commentAt(file protoreflect.FileDescriptor, path protoreflect.SourcePath) string {
	if file == nil {
		return ""
	}
	location := file.SourceLocations().ByPath(path)
	return cleanComment(location.LeadingComments)
}

func cleanComment(comment string) string {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return ""
	}
	lines := strings.Split(comment, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "//")
		line = strings.TrimPrefix(line, "/*")
		line = strings.TrimSuffix(line, "*/")
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func messageParameters(file protoreflect.FileDescriptor, path protoreflect.SourcePath, message protoreflect.MessageDescriptor, visited map[protoreflect.FullName]bool, depth int) []Parameter {
	if message == nil || depth > 8 || visited[message.FullName()] {
		return nil
	}
	visited[message.FullName()] = true
	defer delete(visited, message.FullName())

	fields := message.Fields()
	params := make([]Parameter, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		param := Parameter{
			Name:        string(field.Name()),
			Description: commentAt(file, append(append(protoreflect.SourcePath{}, path...), fieldField, int32(field.Index()))),
			TypeName:    fieldTypeName(field),
			Repeated:    field.IsList() || field.IsMap(),
			Required:    field.HasPresence(),
		}
		if field.HasDefault() {
			param.DefaultValue = fmt.Sprint(field.Default().Interface())
		}
		if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
			if nestedPath, ok := descriptorPath(file, field.Message()); ok {
				param.Fields = messageParameters(file, nestedPath, field.Message(), visited, depth+1)
			}
		}
		params = append(params, param)
	}
	return params
}

func fieldTypeName(field protoreflect.FieldDescriptor) string {
	switch field.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return string(field.Message().FullName())
	case protoreflect.EnumKind:
		return string(field.Enum().FullName())
	default:
		return field.Kind().String()
	}
}

func descriptorPath(file protoreflect.FileDescriptor, target protoreflect.MessageDescriptor) (protoreflect.SourcePath, bool) {
	if file == nil || target == nil || target.ParentFile() != file {
		return nil, false
	}
	var walk func(protoreflect.MessageDescriptors, protoreflect.SourcePath) (protoreflect.SourcePath, bool)
	walk = func(messages protoreflect.MessageDescriptors, prefix protoreflect.SourcePath) (protoreflect.SourcePath, bool) {
		for i := 0; i < messages.Len(); i++ {
			message := messages.Get(i)
			path := append(append(protoreflect.SourcePath{}, prefix...), int32(i))
			if message == target {
				return path, true
			}
			if found, ok := walk(message.Messages(), append(append(protoreflect.SourcePath{}, path...), fieldNested)); ok {
				return found, true
			}
		}
		return nil, false
	}
	return walk(file.Messages(), protoreflect.SourcePath{fieldMessage})
}

func cloneService(service Service) Service {
	clone := service
	clone.Methods = make([]Method, len(service.Methods))
	for i, method := range service.Methods {
		clone.Methods[i] = cloneMethod(method)
	}
	return clone
}

func cloneMethod(method Method) Method {
	clone := method
	clone.Parameters = cloneParameters(method.Parameters)
	return clone
}

func cloneParameters(parameters []Parameter) []Parameter {
	if parameters == nil {
		return nil
	}
	result := make([]Parameter, len(parameters))
	for i, parameter := range parameters {
		result[i] = parameter
		result[i].Fields = cloneParameters(parameter.Fields)
	}
	return result
}
