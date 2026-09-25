package docs

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	shareddocs "github.com/Manu343726/toolsbox/pkg/docs"
	documentationv1 "github.com/Manu343726/toolsbox/subsystems/documentation/documentationv1"
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
