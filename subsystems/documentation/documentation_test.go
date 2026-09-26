package docs

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"
	documentationv1 "github.com/Manu343726/toolbox/subsystems/documentation/documentationv1"
	"github.com/Manu343726/toolbox/subsystems/documentation/documentationv1/documentationv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentationHandler(t *testing.T) {
	catalog := shareddocs.NewCatalog()
	require.NoError(t, catalog.Add(shareddocs.Service{
		Name: "example.v1.Service", Description: "Example service",
		Methods: []shareddocs.Method{{Name: "Run", Description: "Run it", InputType: "example.v1.Request", OutputType: "example.v1.Response", Parameters: []shareddocs.Parameter{{Name: "id", TypeName: "string"}}}},
	}))
	handler := NewHandler(catalog)
	got, err := handler.GetDocumentation(context.Background(), connect.NewRequest(&documentationv1.GetDocumentationRequest{ServiceName: "example.v1.Service"}))
	require.NoError(t, err)
	assert.Equal(t, "Run it", got.Msg.GetDocumentation().GetMethods()[0].GetDescription())
	all, err := handler.ListDocumentation(context.Background(), connect.NewRequest(&documentationv1.ListDocumentationRequest{}))
	require.NoError(t, err)
	assert.Len(t, all.Msg.GetServices(), 1)
}

func TestDocumentationHandlerErrors(t *testing.T) {
	handler := NewHandler(nil)
	_, err := handler.GetDocumentation(context.Background(), connect.NewRequest(&documentationv1.GetDocumentationRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = handler.GetDocumentation(context.Background(), connect.NewRequest(&documentationv1.GetDocumentationRequest{ServiceName: "missing"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// The conversion to the wire shape is recursive, and a parameter's own fields are
// the part a caller reads to build a request. A field dropped one level down is a
// documented parameter the caller does not know it may send.
func TestHandlerConvertsNestedParametersAndStreamingFlags(t *testing.T) {
	catalog := shareddocs.NewCatalog()
	require.NoError(t, catalog.Add(shareddocs.Service{
		Name: "example.v1.Service",
		Methods: []shareddocs.Method{{
			Name:            "Stream",
			Description:     "Stream it",
			InputType:       "example.v1.Request",
			OutputType:      "example.v1.Response",
			ClientStreaming: true,
			ServerStreaming: true,
			Parameters: []shareddocs.Parameter{{
				Name:         "filter",
				Description:  "What to match",
				TypeName:     "example.v1.Filter",
				Required:     true,
				DefaultValue: "none",
				Fields: []shareddocs.Parameter{
					{Name: "terms", TypeName: "string", Repeated: true},
					{
						Name:     "range",
						TypeName: "example.v1.Range",
						Fields: []shareddocs.Parameter{
							{Name: "from", TypeName: "string"},
							{Name: "to", TypeName: "string"},
						},
					},
				},
			}},
		}},
	}))

	got, err := NewHandler(catalog).GetDocumentation(
		context.Background(),
		connect.NewRequest(&documentationv1.GetDocumentationRequest{ServiceName: "example.v1.Service"}),
	)
	require.NoError(t, err)
	method := got.Msg.GetDocumentation().GetMethods()[0]
	assert.True(t, method.GetClientStreaming())
	assert.True(t, method.GetServerStreaming())

	filter := method.GetParameters()[0]
	assert.Equal(t, "What to match", filter.GetDescription())
	assert.Equal(t, "example.v1.Filter", filter.GetTypeName())
	assert.True(t, filter.GetRequired())
	assert.Equal(t, "none", filter.GetDefaultValue())
	require.Len(t, filter.GetFields(), 2)

	assert.Equal(t, "terms", filter.GetFields()[0].GetName())
	assert.True(t, filter.GetFields()[0].GetRepeated())
	// Two levels down, not one: a nested field that stopped recursing would leave
	// the innermost range unknown to whoever is building the request.
	require.Len(t, filter.GetFields()[1].GetFields(), 2)
	assert.Equal(t, "from", filter.GetFields()[1].GetFields()[0].GetName())
	assert.Equal(t, "to", filter.GetFields()[1].GetFields()[1].GetName())
}

// Listing is what a host reads to decide what exists, so its order has to be
// stated rather than left to whatever the catalog happened to hold.
func TestHandlerListsDeterministicallyAndFilters(t *testing.T) {
	catalog := shareddocs.NewCatalog()
	for _, name := range []string{"zebra.v1.Service", "alpha.v1.Service", "middle.v1.Service"} {
		require.NoError(t, catalog.Add(shareddocs.Service{Name: name}))
	}
	handler := NewHandler(catalog)

	all, err := handler.ListDocumentation(context.Background(), connect.NewRequest(&documentationv1.ListDocumentationRequest{}))
	require.NoError(t, err)
	names := make([]string, 0, len(all.Msg.GetServices()))
	for _, service := range all.Msg.GetServices() {
		names = append(names, service.GetName())
	}
	assert.Equal(t, []string{"alpha.v1.Service", "middle.v1.Service", "zebra.v1.Service"}, names)

	// A filter narrows to one service rather than listing everything that happens
	// to match a prefix.
	one, err := handler.ListDocumentation(context.Background(), connect.NewRequest(&documentationv1.ListDocumentationRequest{
		ServiceName: "middle.v1.Service",
	}))
	require.NoError(t, err)
	require.Len(t, one.Msg.GetServices(), 1)
	assert.Equal(t, "middle.v1.Service", one.Msg.GetServices()[0].GetName())

	// An absent request is a listing, not a refusal: a client that sends nothing
	// is asking for everything.
	none, err := handler.ListDocumentation(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, none.Msg.GetServices(), 3)
}

// A filter naming a service that does not exist is a not-found on both methods,
// so a client learns the difference between "nothing matched" and "you asked for
// something absent" without inferring it from an empty list.
func TestHandlerReportsAnAbsentServiceOnBothMethods(t *testing.T) {
	handler := NewHandler(shareddocs.NewCatalog())

	_, err := handler.ListDocumentation(context.Background(), connect.NewRequest(&documentationv1.ListDocumentationRequest{
		ServiceName: "absent.v1.Service",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	empty, err := handler.ListDocumentation(context.Background(), connect.NewRequest(&documentationv1.ListDocumentationRequest{}))
	require.NoError(t, err)
	assert.Empty(t, empty.Msg.GetServices(), "a catalog with nothing in it lists nothing rather than failing")
}

// A subsystem that is handed no catalog serves the framework's own, because
// documentation is not optional: a handler with none would answer every request
// with a not-found and look like a broken deployment.
func TestNewHandlerWithoutACatalogUsesTheFrameworksOwn(t *testing.T) {
	handler := NewHandler(nil)
	listed, err := handler.ListDocumentation(context.Background(), connect.NewRequest(&documentationv1.ListDocumentationRequest{}))
	require.NoError(t, err)
	assert.NotEmpty(t, listed.Msg.GetServices(),
		"the default catalog describes the framework's own contracts")
}

// The subsystem is what a deployment launches, so what it declares and where it
// listens are part of its behaviour rather than an implementation detail.
func TestNewDeclaresItsContractAndServesIt(t *testing.T) {
	server, err := New(Options{Catalog: shareddocs.NewCatalog()})
	require.NoError(t, err)

	descriptor := server.Descriptor()
	assert.Equal(t, Name, descriptor.SubsystemName)
	assert.Equal(t, []string{documentationv1connect.DocumentationServiceName}, descriptor.ServiceNames)
	assert.NotNil(t, server.Catalog(), "a subsystem that serves documentation carries it")

	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	require.NotEmpty(t, server.Endpoint())

	// Over the real transport, with the real client, so a handler that compiles
	// but is not registered is caught here rather than by a deployment.
	client := documentationv1connect.NewDocumentationServiceClient(http.DefaultClient, server.Endpoint())
	_, err = client.ListDocumentation(t.Context(), connect.NewRequest(&documentationv1.ListDocumentationRequest{}))
	require.NoError(t, err)
	_, err = client.GetDocumentation(t.Context(), connect.NewRequest(&documentationv1.GetDocumentationRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
		"the contract's own validation applies over the wire as well as in process")
}
