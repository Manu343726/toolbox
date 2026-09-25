package testecho_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/cli"
	"github.com/Manu343726/toolsbox/pkg/core"
	"github.com/Manu343726/toolsbox/pkg/discovery"
	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	testecho "github.com/Manu343726/toolsbox/subsystems/testecho"
	echov1 "github.com/Manu343726/toolsbox/subsystems/testecho/echov1"
	"github.com/Manu343726/toolsbox/subsystems/testecho/echov1/echov1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestDiscoveryAndDynamicInvocation(t *testing.T) {
	server, err := testechoServer(t)
	require.NoError(t, err)
	defer func() { require.NoError(t, server.Shutdown(context.Background())) }()

	client := discovery.New(server.Endpoint())
	ctx := context.Background()
	services, err := client.ListServices(ctx)
	require.NoError(t, err)
	assert.Contains(t, services, "toolsbox.testecho.v1.EchoService")

	schema, err := client.DescribeService(ctx, "toolsbox.testecho.v1.EchoService")
	require.NoError(t, err)
	require.Len(t, schema.Methods, 2)
	require.NotNil(t, schema.Documentation)
	catalogDoc, catalogErr := shareddocs.DefaultCatalog().Get("toolsbox.testecho.v1.EchoService")
	require.NoError(t, catalogErr)
	assert.Equal(t, catalogDoc.Description, schema.Documentation.Description)
	assert.Contains(t, schema.Documentation.Description, "small service")
	assert.Equal(t, protoreflect.Name("EchoRequest"), schema.Methods[0].Input.Name())

	response, err := client.Invoke(ctx, "toolsbox.testecho.v1.EchoService", "Echo", &echov1.EchoRequest{Message: "hello", Uppercase: true})
	require.NoError(t, err)
	dynamic, ok := response.(*dynamicpb.Message)
	require.True(t, ok)
	assert.Equal(t, "HELLO", dynamic.Get(fieldByName(t, dynamic, "message")).String())

	_, err = client.Invoke(ctx, "toolsbox.testecho.v1.EchoService", "StreamEcho", &echov1.EchoRequest{})
	assert.Error(t, err)

	jsonResponse, err := client.InvokeJSON(ctx, "toolsbox.testecho.v1.EchoService", "Echo", []byte(`{"message":"world"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":"world","length":5,"tags":[]}`, string(jsonResponse))
}

func TestCoreTypedBindingAndDynamicCall(t *testing.T) {
	server, err := testechoServer(t)
	require.NoError(t, err)
	defer func() { require.NoError(t, server.Shutdown(context.Background())) }()

	resolver := core.NewStaticResolver(core.Endpoint{
		Name: "echo", URL: server.Endpoint(), ServiceNames: []string{"toolsbox.testecho.v1.EchoService"},
	})
	client := core.NewClient(core.ClientOptions{Resolver: resolver})
	typed, err := core.Bind(context.Background(), client, "toolsbox.testecho.v1.EchoService", echov1connect.NewEchoServiceClient)
	require.NoError(t, err)
	result, err := typed.Echo(context.Background(), connect.NewRequest(&echov1.EchoRequest{Message: "typed"}))
	require.NoError(t, err)
	assert.Equal(t, "typed", result.Msg.GetMessage())

	dynamic, err := client.Invoke(context.Background(), "toolsbox.testecho.v1.EchoService", "Echo", &echov1.EchoRequest{Message: "dynamic"})
	require.NoError(t, err)
	encoded, err := protojson.Marshal(dynamic)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "dynamic")
}

func TestGeneratedCLIUsesReflectedSchema(t *testing.T) {
	server, err := testechoServer(t)
	require.NoError(t, err)
	defer func() { require.NoError(t, server.Shutdown(context.Background())) }()

	discoveryClient := discovery.New(server.Endpoint())
	generator := cli.NewGenerator(discoveryClient, cli.Options{CommandName: "echoctl"})
	root, err := generator.Generate(context.Background(), "toolsbox.testecho.v1.EchoService")
	require.NoError(t, err)
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"echo", "echo", "--message", "generated", "--uppercase"})
	require.NoError(t, root.Execute())
	assert.Contains(t, output.String(), "GENERATED")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &decoded))
	assert.Equal(t, "GENERATED", decoded["message"])

	var help bytes.Buffer
	root.SetOut(&help)
	root.SetErr(&help)
	root.SetArgs([]string{"echo", "echo", "--help"})
	require.NoError(t, root.Execute())
	assert.Contains(t, help.String(), "Text to echo")
}

func testechoServer(t *testing.T) (*subsystem.Server, error) {
	t.Helper()
	server, err := testecho.New(testecho.Options{})
	if err != nil {
		return nil, err
	}
	if err := server.Start(context.Background()); err != nil {
		return nil, err
	}
	return server, nil
}

func fieldByName(t *testing.T, message *dynamicpb.Message, name string) protoreflect.FieldDescriptor {
	t.Helper()
	field := message.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, field)
	return field
}
