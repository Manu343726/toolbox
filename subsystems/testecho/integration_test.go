package testecho_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolsbox/pkg/cli"
	"github.com/Manu343726/toolsbox/pkg/cliapp"
	"github.com/Manu343726/toolsbox/pkg/core"
	"github.com/Manu343726/toolsbox/pkg/discovery"
	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
	toolsboxmcp "github.com/Manu343726/toolsbox/pkg/mcp"
	"github.com/Manu343726/toolsbox/pkg/subsystem"
	testecho "github.com/Manu343726/toolsbox/subsystems/testecho"
	echov1 "github.com/Manu343726/toolsbox/subsystems/testecho/echov1"
	"github.com/Manu343726/toolsbox/subsystems/testecho/echov1/echov1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
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

func TestStandaloneCommandAutomaticallyIncludesMCP(t *testing.T) {
	var output bytes.Buffer
	err := cliapp.Run(context.Background(), cliapp.Options{
		Name:        testecho.Name,
		Description: "Reference echo subsystem",
		Factory: func() (*subsystem.Server, error) {
			return testecho.New(testecho.Options{})
		},
		Output: &output,
		Args:   []string{"mcp", "--help"},
	})
	// Run has no args here, so the generated root prints its help.
	require.NoError(t, err)
	assert.Contains(t, output.String(), "mcp")
	assert.Contains(t, output.String(), "Launch a Model Context Protocol server")
	assert.Contains(t, output.String(), "--service")
}

func TestGeneratedMCPExposesIntrospectsAndGatesFeatures(t *testing.T) {
	server, err := testechoServer(t)
	require.NoError(t, err)
	defer func() { require.NoError(t, server.Shutdown(context.Background())) }()

	singleService, err := toolsboxmcp.NewFromSubsystemService(context.Background(), server, "toolsbox.testecho.v1.EchoService", toolsboxmcp.Options{})
	require.NoError(t, err)
	assert.Len(t, singleService.Features(), 2)
	_, err = toolsboxmcp.NewFromSubsystemService(context.Background(), server, "missing.v1.Service", toolsboxmcp.Options{})
	assert.Error(t, err)

	resolved, err := toolsboxmcp.NewFromResolver(context.Background(), core.NewStaticResolver(core.Endpoint{
		Name:         "echo",
		URL:          server.Endpoint(),
		ServiceNames: []string{"toolsbox.testecho.v1.EchoService"},
		Capabilities: []string{"testecho.echo"},
	}), []string{"toolsbox.testecho.v1.EchoService"}, toolsboxmcp.Options{})
	require.NoError(t, err)
	assert.Len(t, resolved.Features(), 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge, err := toolsboxmcp.NewFromSubsystem(ctx, server, toolsboxmcp.Options{
		Name:            "testecho-mcp",
		InitialExposure: toolsboxmcp.ExposeAllowedFeatures,
	})
	require.NoError(t, err)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() { serverErr <- bridge.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	assert.Contains(t, names, toolsboxmcp.ToolListFeatures)
	assert.Contains(t, names, toolsboxmcp.ToolExposeFeature)
	assert.Contains(t, names, toolsboxmcp.ToolCallRPC)
	assert.Contains(t, names, "echo__echo")
	assert.NotContains(t, names, "echo__stream_echo")

	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "echo__echo",
		Arguments: map[string]any{"message": "mcp", "uppercase": true},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "MCP")

	featureID := "toolsbox.testecho.v1.EchoService/Echo"
	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      toolsboxmcp.ToolHideFeature,
		Arguments: map[string]any{"feature": featureID},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	tools, err = session.ListTools(ctx, nil)
	require.NoError(t, err)
	assert.NotContains(t, toolNames(tools.Tools), "echo__echo")

	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: toolsboxmcp.ToolCallRPC,
		Arguments: map[string]any{
			"service": "toolsbox.testecho.v1.EchoService",
			"method":  "Echo",
			"request": map[string]any{"message": "hidden"},
		},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "hidden")

	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      toolsboxmcp.ToolExposeFeature,
		Arguments: map[string]any{"feature": featureID},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: toolsboxmcp.ToolCallRPC,
		Arguments: map[string]any{
			"service": "toolsbox.testecho.v1.EchoService",
			"method":  "Echo",
			"request": map[string]any{"message": "again"},
		},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "again")

	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      toolsboxmcp.ToolReadFeatureDocumentation,
		Arguments: map[string]any{"feature": featureID},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "Text to echo")

	cancel()
	select {
	case err := <-serverErr:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("MCP server did not stop after context cancellation")
	}
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

func toolNames(tools []*sdkmcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
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
