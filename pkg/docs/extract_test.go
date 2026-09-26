package docs_test

import (
	"testing"

	"github.com/Manu343726/toolbox/pkg/docs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The documentation model is the source of every generated command's help: the command list,
// each method's summary, and every flag's description. A comment placed where this parser does
// not expect it does not fail — it produces help that quietly says less, which is the worst
// failure mode a help generator has. So what is tested here is what a comment becomes.
//
// The fixture is a hand-built descriptor with source info rather than a linked-in generated
// one, because a generated .pb.go carries no comments at all — which is the reason this project
// generates descriptor sets with --include_source_info and registers those instead. Testing
// against a generated descriptor would pass while testing nothing.

// richFileDescriptor carries a comment on every part worth checking: a service, a unary method, a
// server-streaming one, a plain field, a repeated field, a nested message, and a field of it.
func richFileDescriptor() *descriptorpb.FileDescriptorProto {
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	stringKind := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	messageKind := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	int32Kind := descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()

	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test/extract.proto"),
		Package: proto.String("test.extract"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Options"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("retries"), Number: proto.Int32(1), Label: optional, Type: int32Kind},
				},
			},
			{
				Name: proto.String("RunRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("name"), Number: proto.Int32(1), Label: optional, Type: stringKind},
					{Name: proto.String("tags"), Number: proto.Int32(2), Label: repeated, Type: stringKind},
					{Name: proto.String("options"), Number: proto.Int32(3), Label: optional, Type: messageKind, TypeName: proto.String(".test.extract.Options")},
				},
			},
			{Name: proto.String("RunResponse")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("ExampleService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{
					Name:       proto.String("Run"),
					InputType:  proto.String(".test.extract.RunRequest"),
					OutputType: proto.String(".test.extract.RunResponse"),
				},
				{
					Name:            proto.String("Watch"),
					InputType:       proto.String(".test.extract.RunRequest"),
					OutputType:      proto.String(".test.extract.RunResponse"),
					ServerStreaming: proto.Bool(true),
				},
			},
		}},
		SourceCodeInfo: &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{
			// The service comment, written across two lines the way a source file wraps it.
			{Path: []int32{6, 0}, Span: []int32{0, 0, 1},
				LeadingComments: proto.String(" Runs a thing. It is the service a caller reads\n to learn the commands it offers.\n")},
			{Path: []int32{6, 0, 2, 0}, Span: []int32{1, 0, 2},
				LeadingComments: proto.String(" Run the example. This is what the command's\n one-line summary is taken from.\n")},
			{Path: []int32{6, 0, 2, 1}, Span: []int32{2, 0, 3},
				LeadingComments: proto.String(" Watch the example, and never returns.\n")},
			// RunRequest is the second message in the fixture, so its fields are [4, 1, 2, n];
			// Options is the first, and its own sub-field is [4, 0, 2, 0]. Getting an index
			// wrong does not fail — it produces a field with no description, which is exactly
			// the failure these tests exist to catch.
			{Path: []int32{4, 1, 2, 0}, Span: []int32{3, 0, 4},
				LeadingComments: proto.String(" Name to run.\n")},
			{Path: []int32{4, 1, 2, 1}, Span: []int32{4, 0, 5},
				LeadingComments: proto.String(" Tags to apply, repeatable.\n")},
			{Path: []int32{4, 1, 2, 2}, Span: []int32{5, 0, 6},
				LeadingComments: proto.String(" Options for the run.\n")},
			{Path: []int32{4, 0, 2, 0}, Span: []int32{6, 0, 7},
				LeadingComments: proto.String(" How many times to retry.\n")},
		}},
	}
}

func extract(t *testing.T, name string) docs.Service {
	t.Helper()
	file, err := protodesc.NewFile(richFileDescriptor(), protoregistry.GlobalFiles)
	require.NoError(t, err)
	service := file.Services().ByName(protoreflect.Name(name))
	require.NotNil(t, service, "no service named %q", name)
	documentation := docs.ExtractServiceDocumentation(service)
	require.NotNil(t, documentation)
	return *documentation
}

func TestACommentBecomesTheServicesDescription(t *testing.T) {
	documentation := extract(t, "ExampleService")

	assert.Equal(t, "test.extract.ExampleService", documentation.Name)
	// The comment's line breaks are the source file's, not the thought's, and this package
	// keeps them: only the consumer knows the width it has to lay the text out in. pkg/cli
	// unwraps and re-wraps it, and that is the right place to know the width.
	assert.Contains(t, documentation.Description, "Runs a thing.")
	assert.Contains(t, documentation.Description, "the commands it offers.",
		"the second line of the comment was dropped")
}

func TestACommentBecomesAMethodsDescription(t *testing.T) {
	documentation := extract(t, "ExampleService")
	require.Len(t, documentation.Methods, 2)

	assert.Contains(t, documentation.Methods[0].Description, "Run the example.")
	// The second line of a method's comment is where a summary is taken from, so dropping it
	// would truncate every command's one-line description.
	assert.Contains(t, documentation.Methods[0].Description, "summary is taken from.")
	assert.Equal(t, "test.extract.RunRequest", documentation.Methods[0].InputType)
	assert.Equal(t, "test.extract.RunResponse", documentation.Methods[0].OutputType)
}

func TestStreamingIsReportedFromTheContractNotGuessed(t *testing.T) {
	documentation := extract(t, "ExampleService")
	require.Len(t, documentation.Methods, 2)

	assert.False(t, documentation.Methods[0].ServerStreaming, "a unary method was reported as streaming")
	assert.True(t, documentation.Methods[1].ServerStreaming,
		"a server-streaming method was not reported as one, so a caller would call it as if it were unary")
	assert.False(t, documentation.Methods[1].ClientStreaming,
		"a server-streaming method was also reported as client-streaming")
}

func TestEveryParameterCarriesTheFieldComment(t *testing.T) {
	documentation := extract(t, "ExampleService")
	require.NotEmpty(t, documentation.Methods)

	parameters := documentation.Methods[0].Parameters
	require.Len(t, parameters, 3, "a field is missing from the request's parameters")
	// A parameter with no description becomes a flag with no help, and a flag with no help is a
	// flag a caller has to open the .proto to use.
	for _, parameter := range parameters {
		assert.NotEmpty(t, parameter.Description, "parameter %q has no description", parameter.Name)
		assert.NotEmpty(t, parameter.TypeName, "parameter %q has no type", parameter.Name)
	}
	assert.Equal(t, "Name to run.", parameters[0].Description)
	assert.Equal(t, "string", parameters[0].TypeName)
}

func TestARepeatedParameterIsReportedAsRepeated(t *testing.T) {
	documentation := extract(t, "ExampleService")

	parameters := documentation.Methods[0].Parameters
	require.Len(t, parameters, 3)
	// A repeated field is a different flag kind, and a caller told it is a single value passes
	// one and silently loses the rest.
	assert.False(t, parameters[0].Repeated, "a singular field was reported as repeated")
	assert.True(t, parameters[1].Repeated, "a repeated field was not reported as repeated")
}

func TestAMessageFieldIsFlattenedIntoSubFields(t *testing.T) {
	documentation := extract(t, "ExampleService")

	parameters := documentation.Methods[0].Parameters
	require.Len(t, parameters, 3)
	options := parameters[2]
	require.Equal(t, "options", options.Name)
	assert.NotEmpty(t, options.TypeName, "a message field must say what it holds")

	// A nested message is flattened so a caller can address its fields, and the sub-field's own
	// comment has to come with it — a sub-field reached but not described is a flag with no
	// help, which is the thing this whole exercise exists to prevent.
	require.NotEmpty(t, options.Fields, "a message field produced no sub-fields")
	assert.Equal(t, "retries", options.Fields[0].Name)
	assert.Equal(t, "How many times to retry.", options.Fields[0].Description)
	assert.Equal(t, "int32", options.Fields[0].TypeName)
}

func TestTheFlatteningIsBoundedSoARecursiveMessageTerminates(t *testing.T) {
	// A message containing itself has no finite flattening. The bound is what stops the walk,
	// and the assertion is that the walk terminates at all rather than on a particular depth —
	// which is the property that matters and the one a depth value would not prove.
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("test/recursive.proto"),
		Package: proto.String("test.recursive"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Node"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{
					Name: proto.String("child"), Number: proto.Int32(1),
					Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:  descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
					// Points at the message that contains it.
					TypeName: proto.String(".test.recursive.Node"),
				},
			},
		}, {
			Name: proto.String("Request"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{
					Name: proto.String("root"), Number: proto.Int32(1),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
					TypeName: proto.String(".test.recursive.Node"),
				},
			},
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("RecursiveService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Run"),
				InputType:  proto.String(".test.recursive.Request"),
				OutputType: proto.String(".test.recursive.Request"),
			}},
		}},
	}, protoregistry.GlobalFiles)
	require.NoError(t, err)

	service := file.Services().ByName("RecursiveService")
	require.NotNil(t, service)
	documentation := docs.ExtractServiceDocumentation(service)
	require.NotNil(t, documentation)
	require.NotEmpty(t, documentation.Methods)
	assert.NotEmpty(t, documentation.Methods[0].Parameters)
}

func TestExtractionFromAGeneratedDescriptorYieldsStructureWithoutComments(t *testing.T) {
	// This is what a generated .pb.go gives, and it is why the project ships descriptor sets
	// with --include_source_info: the structure is there and the prose is not. Asserting it
	// means a change that stopped preferring descriptor-set documentation would show up as a
	// test failure rather than as every help page quietly losing its descriptions.
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(
		"toolbox.health.v1.HealthService")
	if err != nil {
		t.Skip("no descriptor is linked into this test binary")
	}
	service, ok := descriptor.(protoreflect.ServiceDescriptor)
	require.True(t, ok)

	documentation := docs.ExtractServiceDocumentation(service)
	require.NotNil(t, documentation)
	assert.NotEmpty(t, documentation.Methods, "the structure should still be there")
	assert.Empty(t, documentation.Description,
		"a generated descriptor carries no comments; documentation must come from a descriptor set")
}

func TestExtractOnNothingIsNilRatherThanEmpty(t *testing.T) {
	// A nil and an empty documentation are different to a caller: one is "nothing is known"
	// and the other is "nothing was said", and conflating them makes a missing contract look
	// like an undocumented one.
	assert.Nil(t, docs.ExtractServiceDocumentation(nil))
}
