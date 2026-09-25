package protocontract

import (
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	apiv1connect "github.com/Manu343726/toolbox/pkg/api/apiv1/apiv1connect"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// A ConnectRPC method is always a POST, so nothing about the transport says what
// invoking one does. These tests pin the only thing that can: the contract's own
// comment, read back out of a real descriptor, with a service declaring once and a
// method overriding.
//
// The source paths follow protobuf's own numbering: field 6 of a file is its
// services, and field 2 of a service is its methods.

// comment is one leading comment, attached to the element at a path.
type comment struct {
	path []int32
	text string
}

// atMethod builds a comment on the first method of the first service.
func atMethod(index int32, text string) comment {
	return comment{path: []int32{6, 0, 2, index}, text: text}
}

// atService builds a comment on the first service.
func atService(text string) comment {
	return comment{path: []int32{6, 0}, text: text}
}

func sourceInfo(comments ...comment) *descriptorpb.SourceCodeInfo {
	info := &descriptorpb.SourceCodeInfo{}
	for index, entry := range comments {
		info.Location = append(info.Location, &descriptorpb.SourceCodeInfo_Location{
			Path:            entry.path,
			Span:            []int32{int32(index), 0, int32(index) + 1},
			LeadingComments: proto.String(entry.text),
		})
	}
	return info
}

func marshalFile(t *testing.T, file *descriptorpb.FileDescriptorProto) []byte {
	t.Helper()
	encoded, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{file}})
	require.NoError(t, err)
	return encoded
}

func effectsOf(described api.API) map[string][]api.SideEffect {
	operations := described.Operations()
	result := make(map[string][]api.SideEffect, len(operations))
	for _, operation := range operations {
		result[operation.Name] = operation.SideEffects
	}
	return result
}

func TestDescriptorSetReadsSideEffectsFromComments(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("effects.proto"),
		Package: proto.String("effects.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Request"),
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("EffectService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{Name: proto.String("List"), InputType: proto.String(".effects.v1.Request"), OutputType: proto.String(".effects.v1.Request")},
				{Name: proto.String("Create"), InputType: proto.String(".effects.v1.Request"), OutputType: proto.String(".effects.v1.Request")},
			},
		}},
		SourceCodeInfo: sourceInfo(
			atService(" Effect operations.\n\n@toolbox.side-effects read_only"),
			atMethod(0, " Lists things.\n\n@toolbox.side-effects read_only"),
			atMethod(1, " Creates a thing.\n\n@toolbox.side-effects create irreversible"),
		),
	}

	described, warnings, err := NewDescriptor(Descriptor{}).FromDescriptorSet(marshalFile(t, file))
	require.NoError(t, err)
	assert.Empty(t, warnings)

	service, ok := described.Service("effects.v1.EffectService")
	require.True(t, ok)
	assert.Equal(t, []api.SideEffect{api.SideEffectReadOnly}, service.SideEffects,
		"a uniformly read-only service states it once")

	byName := effectsOf(described)
	assert.Equal(t, []api.SideEffect{api.SideEffectReadOnly}, byName["List"],
		"the service's declaration covers its methods")
	assert.Equal(t, []api.SideEffect{api.SideEffectCreate, api.SideEffectIrreversible}, byName["Create"],
		"a method's own declaration replaces the service's rather than adding to it")
}

func TestDescriptorSetLeavesAnUnannotatedMethodUnclassified(t *testing.T) {
	// The safe default: nothing about the method says what it does, so nothing is
	// claimed, and an unclassified method is not a read. A policy has to be told to
	// trust it rather than infer it.
	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("bare.proto"),
		Package: proto.String("bare.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Request"),
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("BareService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name: proto.String("DoThing"), InputType: proto.String(".bare.v1.Request"), OutputType: proto.String(".bare.v1.Request"),
			}},
		}},
	}

	described, _, err := NewDescriptor(Descriptor{}).FromDescriptorSet(marshalFile(t, file))
	require.NoError(t, err)
	operations := described.Operations()
	require.Len(t, operations, 1)
	assert.Empty(t, operations[0].SideEffects)
	assert.False(t, api.ReadOnly(operations[0].SideEffects))
}

func TestDescriptorSetKeepsAnnotationsOutOfTheDescription(t *testing.T) {
	// The prose is what generated help and an agent's tool description are built
	// from, so a machine-readable line left in it is noise the reader pays for.
	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("prose.proto"),
		Package: proto.String("prose.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Request"),
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("ProseService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name: proto.String("Read"), InputType: proto.String(".prose.v1.Request"), OutputType: proto.String(".prose.v1.Request"),
			}},
		}},
		SourceCodeInfo: sourceInfo(
			atMethod(0, " Reads a thing.\n\nReturns it, or NotFound.\n\n@toolbox.side-effects read_only"),
		),
	}

	described, _, err := NewDescriptor(Descriptor{}).FromDescriptorSet(marshalFile(t, file))
	require.NoError(t, err)
	operations := described.Operations()
	require.Len(t, operations, 1)
	assert.Equal(t, "Reads a thing.\n\nReturns it, or NotFound.", operations[0].Summary)
	assert.NotContains(t, operations[0].Summary, "toolbox")
}

func TestEndpointDescriptionCarriesSideEffectsToo(t *testing.T) {
	// The classification has to arrive on the path a host actually uses, which is
	// reflection against a live server rather than a descriptor set in hand.
	//
	// A generated Go descriptor omits source locations, so the reader is pointed at
	// the documentation catalog the contract's descriptor set registered itself
	// into. That wiring is what makes the effect arrive, and without it every
	// operation of a live subsystem would arrive unclassified.
	described, warnings, err := NewDescriptor(Descriptor{
		Documentation: shareddocs.DefaultCatalog(),
	}).FromEndpoint(t.Context(), startParserServer(t))
	require.NoError(t, err)
	assert.Empty(t, warnings)

	operation, ok := described.Operation(api.OperationID(described.ID, apiv1connect.ApiParserServiceName, "ParseApi"))
	require.True(t, ok, "the method was described from real reflection")
	assert.Equal(t, []api.SideEffect{api.SideEffectReadOnly}, operation.SideEffects,
		"the effect the contract's own comment declares arrived over reflection")
}

func TestEndpointDescriptionWithoutDocumentationStaysUnclassified(t *testing.T) {
	// The other half of the same fact: a reader with no documentation to consult
	// sees the contract's shape and nothing else. That is the safe direction, and
	// it is why a deployment in one process has to wire the catalog in rather than
	// assume reflection carried the comments.
	described, _, err := NewDescriptor(Descriptor{}).FromEndpoint(t.Context(), startParserServer(t))
	require.NoError(t, err)

	operation, ok := described.Operation(api.OperationID(described.ID, apiv1connect.ApiParserServiceName, "ParseApi"))
	require.True(t, ok)
	assert.Empty(t, operation.SideEffects)
}

// startParserServer serves the framework's parser contract and returns its
// endpoint. Its descriptor set carries the annotation, so nothing here fakes it.
func startParserServer(t *testing.T) string {
	t.Helper()
	path, handler := apiv1connect.NewApiParserServiceHandler(&apiv1connect.UnimplementedApiParserServiceHandler{})
	server, err := subsystem.NewServer(subsystem.Config{
		Name: "fixture",
		Services: []subsystem.Service{{
			Name:    apiv1connect.ApiParserServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
	require.NoError(t, err)
	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(t.Context()) })
	return server.Endpoint()
}
