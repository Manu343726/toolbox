package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/discovery"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

type fakeSource struct {
	schema *discovery.ServiceSchema
	calls  int
}

func (s *fakeSource) ListServices(context.Context) ([]string, error) {
	return []string{s.schema.Name}, nil
}

func (s *fakeSource) DescribeService(_ context.Context, name string) (*discovery.ServiceSchema, error) {
	if name != s.schema.Name {
		return nil, errors.New("unknown service")
	}
	return s.schema, nil
}

func (s *fakeSource) Invoke(_ context.Context, _, method string, request proto.Message) (proto.Message, error) {
	s.calls++
	if method != "Echo" {
		return nil, errors.New("unsupported method")
	}
	response := dynamicpb.NewMessage(s.schema.Methods[0].Output)
	field := response.Descriptor().Fields().ByName("message")
	value := request.ProtoReflect().Get(request.ProtoReflect().Descriptor().Fields().ByName("message"))
	response.Set(field, value)
	return response, nil
}

func TestServerRejectsInvalidOptions(t *testing.T) {
	_, err := New(context.Background(), newFakeSource(t), Options{InitialExposure: InitialExposure(99)})
	assert.Error(t, err)
	_, err = New(context.Background(), nil, Options{})
	assert.Error(t, err)
}

func TestServerGeneratesGatedToolsAndIntrospection(t *testing.T) {
	ctx := context.Background()
	source := newFakeSource(t)
	bridge, err := New(ctx, source, Options{
		Name:            "test-mcp",
		Policy:          APIPolicy(),
		InitialExposure: ExposeAllowedFeatures,
	})
	require.NoError(t, err)

	session, stop := connectTestServer(t, bridge)
	defer stop()
	names := listToolNames(t, session)
	assert.Contains(t, names, "echo__echo")
	assert.Contains(t, names, ToolListFeatures)
	assert.NotContains(t, names, "echo__stream_echo")

	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "echo__echo",
		Arguments: map[string]any{"message": "hello"},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "hello")
	assert.Equal(t, 1, source.calls)

	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      ToolHideFeature,
		Arguments: map[string]any{"feature": "test.v1.EchoService/Echo"},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotContains(t, listToolNames(t, session), "echo__echo")

	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: ToolCallRPC,
		Arguments: map[string]any{
			"service": "test.v1.EchoService",
			"method":  "Echo",
			"request": map[string]any{"message": "blocked"},
		},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "hidden")
	assert.Equal(t, 1, source.calls, "hidden features must be gated before invocation")

	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      ToolExposeFeature,
		Arguments: map[string]any{"feature": "test.v1.EchoService/Echo"},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, listToolNames(t, session), "echo__echo")

	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      ToolExposeFeature,
		Arguments: map[string]any{"feature": "test.v1.EchoService/StreamEcho"},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "streaming")
}

func TestServerMinimalStartsWithIntrospectionOnly(t *testing.T) {
	bridge, err := New(context.Background(), newFakeSource(t), Options{
		Policy:          APIPolicy(),
		InitialExposure: ExposeNoFeatures,
	})
	require.NoError(t, err)
	session, stop := connectTestServer(t, bridge)
	defer stop()
	names := listToolNames(t, session)
	assert.Contains(t, names, ToolListFeatures)
	assert.Contains(t, names, ToolCallRPC)
	assert.NotContains(t, names, "echo__echo")
}

func TestPolicyIsSeparateFromReflection(t *testing.T) {
	source := newFakeSource(t)
	bridge, err := New(context.Background(), source, Options{
		Policy: api.DenyAll(),
	})
	require.NoError(t, err)
	session, stop := connectTestServer(t, bridge)
	defer stop()
	assert.NotContains(t, listToolNames(t, session), "echo__echo")

	result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolExposeFeature,
		Arguments: map[string]any{"feature": "test.v1.EchoService/Echo"},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "policy")

	result, err = session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolListFeatures,
		Arguments: map[string]any{"include_disallowed": true},
	})
	require.NoError(t, err)
	assert.Contains(t, resultText(t, result), "test.v1.EchoService/Echo")
}

func TestHTTPHandlerServesTheGeneratedSurface(t *testing.T) {
	bridge, err := New(context.Background(), newFakeSource(t), Options{
		Policy:          APIPolicy(),
		InitialExposure: ExposeNoFeatures,
	})
	require.NoError(t, err)
	httpServer := httptest.NewServer(bridge.HTTPHandler())
	defer httpServer.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "http-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	require.NoError(t, err)
	defer func() { _ = session.Close() }()
	assert.Contains(t, listToolNames(t, session), ToolListFeatures)
	assert.NotContains(t, listToolNames(t, session), "echo__echo")
}

func TestExposureStateIsConcurrencySafe(t *testing.T) {
	bridge, err := New(context.Background(), newFakeSource(t), Options{Policy: APIPolicy()})
	require.NoError(t, err)
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			for j := 0; j < 50; j++ {
				if (index+j)%2 == 0 {
					assert.NoError(t, bridge.Expose("test.v1.EchoService/Echo"))
				} else {
					assert.NoError(t, bridge.Hide("test.v1.EchoService/Echo"))
				}
				_ = bridge.Features()
			}
		}(i)
	}
	wait.Wait()
}

func TestAGatewayDeniesAServiceThatDeclaredNothing(t *testing.T) {
	// The defect this replaced: a service whose contract said nothing about what
	// calling it does was authorized by a sibling's declaration, because a
	// flattened capability list was copied onto every service of a subsystem. The
	// two services here declare differently, and only the one that declared is
	// offered.
	source := newFakeSource(t)
	source.schema.Documentation = &shareddocs.Service{
		Name: "test.v1.EchoService",
		Methods: []shareddocs.Method{{
			Name:        "Echo",
			Annotations: mustAnnotations(t, "@toolbox.side-effects read_only"),
		}, {
			Name:        "DoThing",
			Annotations: mustAnnotations(t, ""),
		}},
	}
	policy, err := api.NewPolicy(api.Rule{
		Pattern:  "test.v1.EchoService/Echo",
		Decision: api.DecisionAllow,
	})
	require.NoError(t, err)

	bridge, err := New(context.Background(), source, Options{Policy: policy})
	require.NoError(t, err)
	allowed := map[string]bool{}
	for _, feature := range bridge.Features() {
		allowed[feature.Method] = feature.Allowed
	}
	assert.True(t, allowed["Echo"], "a method whose contract declared an effect is the one the rule names")
	assert.False(t, allowed["DoThing"],
		"a method whose contract declared nothing is not covered by a rule about another method")
}

func TestAGatewayCanGrantAWholeClassAcrossEveryService(t *testing.T) {
	// What separating classification from grant buys: a policy that is coarse
	// enough to be worth writing, which a per-capability list could not express.
	source := newFakeSource(t)
	source.schema.Documentation = &shareddocs.Service{
		Name: "test.v1.EchoService",
		Methods: []shareddocs.Method{{
			Name:        "Echo",
			Annotations: mustAnnotations(t, "@toolbox.side-effects read_only"),
		}, {
			Name:        "Wipe",
			Annotations: mustAnnotations(t, "@toolbox.side-effects delete"),
		}},
	}
	policy, err := api.NewPolicy(
		api.Rule{Pattern: "*", Classes: []api.EffectClass{api.EffectClassRead}, Decision: api.DecisionAllow},
		api.Rule{Pattern: "*", Classes: []api.EffectClass{api.EffectClassWrite}, Decision: api.DecisionDeny},
	)
	require.NoError(t, err)

	bridge, err := New(context.Background(), source, Options{Policy: policy})
	require.NoError(t, err)
	allowed := map[string]bool{}
	for _, feature := range bridge.Features() {
		allowed[feature.Method] = feature.Allowed
	}
	assert.True(t, allowed["Echo"], "every read, named or not")
	assert.False(t, allowed["Wipe"], "and no write, without either having been enumerated")
}

func mustAnnotations(t *testing.T, comment string) shareddocs.AnnotationSet {
	t.Helper()
	_, annotations := shareddocs.ParseAnnotations(comment)
	return annotations
}

func TestJSONSchemaUsesProtobufJSONNamesAndTypes(t *testing.T) {
	source := newFakeSource(t)
	schema := jsonSchemaForMessage(source.schema.Methods[0].Input, nil)
	assert.Equal(t, "object", schema["type"])
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "message")
	assert.Equal(t, "string", properties["message"].(map[string]any)["type"])
	assert.Contains(t, properties, "repeat")
	assert.Equal(t, "integer", properties["repeat"].(map[string]any)["type"])
}

func TestJSONSchemaBoundsRecursiveMessages(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("recursive.proto"),
		Package: proto.String("test.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Node"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("child"), JsonName: proto.String("child"), Number: proto.Int32(1),
				Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".test.v1.Node"),
			}},
		}},
	}
	descriptor, err := protodesc.NewFile(file, nil)
	require.NoError(t, err)
	schema := jsonSchemaForMessage(descriptor.Messages().Get(0), nil)
	child := schema["properties"].(map[string]any)["child"].(map[string]any)
	assert.Equal(t, "object", child["type"])
	leaf := child["properties"].(map[string]any)["child"].(map[string]any)
	assert.Equal(t, true, leaf["additionalProperties"])
}

func connectTestServer(t *testing.T, bridge *Server) (*sdkmcp.ClientSession, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() { serverErr <- bridge.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	return session, func() {
		_ = session.Close()
		cancel()
		select {
		case err := <-serverErr:
			if err != nil {
				assert.ErrorIs(t, err, context.Canceled)
			}
		case <-time.After(time.Second):
			t.Fatal("MCP server did not stop")
		}
	}
}

func listToolNames(t *testing.T, session *sdkmcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func resultText(t *testing.T, result *sdkmcp.CallToolResult) string {
	t.Helper()
	if result == nil {
		return ""
	}
	for _, content := range result.Content {
		if text, ok := content.(*sdkmcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func newFakeSource(t *testing.T) *fakeSource {
	t.Helper()
	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("echo.proto"),
		Package: proto.String("test.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("EchoRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("message"), JsonName: proto.String("message"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					{Name: proto.String("repeat"), JsonName: proto.String("repeat"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
				},
			},
			{
				Name: proto.String("EchoResponse"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("message"), JsonName: proto.String("message"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("EchoService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{Name: proto.String("Echo"), InputType: proto.String(".test.v1.EchoRequest"), OutputType: proto.String(".test.v1.EchoResponse")},
					{Name: proto.String("StreamEcho"), InputType: proto.String(".test.v1.EchoRequest"), OutputType: proto.String(".test.v1.EchoResponse"), ServerStreaming: proto.Bool(true)},
				},
			},
		},
	}
	descriptor, err := protodesc.NewFile(file, nil)
	require.NoError(t, err)
	service := descriptor.Services().Get(0)
	methods := make([]discovery.MethodSchema, 0, service.Methods().Len())
	for i := 0; i < service.Methods().Len(); i++ {
		method := service.Methods().Get(i)
		methods = append(methods, discovery.MethodSchema{
			Name:            string(method.Name()),
			Input:           method.Input(),
			Output:          method.Output(),
			ServerStreaming: method.IsStreamingServer(),
		})
	}
	return &fakeSource{schema: &discovery.ServiceSchema{
		Name:       string(service.FullName()),
		Descriptor: service,
		Methods:    methods,
	}}
}
