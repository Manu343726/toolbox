package docs_test

import (
	"testing"

	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestExtractServiceDocumentationFromSourceInfo(t *testing.T) {
	file, err := protodesc.NewFile(testFileDescriptor(), protoregistry.GlobalFiles)
	require.NoError(t, err)
	service := file.Services().ByName("ExampleService")
	require.NotNil(t, service)

	doc := shareddocs.ExtractServiceDocumentation(service)
	require.NotNil(t, doc)
	assert.Equal(t, "test.docs.ExampleService", doc.Name)
	assert.Equal(t, "Example service documentation.", doc.Description)
	require.Len(t, doc.Methods, 1)
	method := doc.Methods[0]
	assert.Equal(t, "Run", method.Name)
	assert.Contains(t, method.Description, "Run the example")
	assert.Equal(t, "test.docs.RunRequest", method.InputType)
	assert.Equal(t, "test.docs.RunResponse", method.OutputType)
	require.Len(t, method.Parameters, 2)
	assert.Equal(t, "name", method.Parameters[0].Name)
	assert.Contains(t, method.Parameters[0].Description, "Name to run")
	assert.Equal(t, "string", method.Parameters[0].TypeName)
	assert.Equal(t, "tags", method.Parameters[1].Name)
	assert.True(t, method.Parameters[1].Repeated)
}

func TestCatalogCopiesAndListsDocumentation(t *testing.T) {
	catalog := shareddocs.NewCatalog()
	file, err := protodesc.NewFile(testFileDescriptor(), protoregistry.GlobalFiles)
	require.NoError(t, err)
	require.NoError(t, catalog.AddFile(file))

	first, err := catalog.Get("test.docs.ExampleService")
	require.NoError(t, err)
	first.Description = "outside mutation"
	second, err := catalog.Get("test.docs.ExampleService")
	require.NoError(t, err)
	assert.NotEqual(t, "outside mutation", second.Description)

	all := catalog.List()
	require.Len(t, all, 1)
	assert.Equal(t, "test.docs.ExampleService", all[0].Name)

	_, err = catalog.Get("missing")
	assert.ErrorIs(t, err, shareddocs.ErrNotFound)
}

func TestCatalogRejectsInvalidInput(t *testing.T) {
	catalog := shareddocs.NewCatalog()
	assert.ErrorIs(t, catalog.Add(shareddocs.Service{}), shareddocs.ErrInvalidDescriptor)
	assert.ErrorIs(t, catalog.AddFile(nil), shareddocs.ErrInvalidDescriptor)
	assert.ErrorIs(t, catalog.AddService(nil), shareddocs.ErrInvalidDescriptor)
}

func testFileDescriptor() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test/docs.proto"),
		Package: proto.String("test.docs"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("RunRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("name"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					{Name: proto.String("tags"), Number: proto.Int32(2), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				},
			},
			{Name: proto.String("RunResponse")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("ExampleService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Run"),
				InputType:  proto.String(".test.docs.RunRequest"),
				OutputType: proto.String(".test.docs.RunResponse"),
			}},
		}},
		SourceCodeInfo: &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{
			{Path: []int32{6, 0}, Span: []int32{0, 0, 1}, LeadingComments: proto.String(" Example service documentation.\n")},
			{Path: []int32{6, 0, 2, 0}, Span: []int32{1, 0, 2}, LeadingComments: proto.String(" Run the example.\n")},
			{Path: []int32{4, 0, 2, 0}, Span: []int32{3, 0, 4}, LeadingComments: proto.String(" Name to run.\n")},
		}},
	}
}
