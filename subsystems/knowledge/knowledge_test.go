package knowledge

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledge/knowledgev1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func source() *knowledgev1.KnowledgeSource {
	return &knowledgev1.KnowledgeSource{Id: "runbook", Name: "Runbook", Type: "file", Location: "ops/deploy.md", Tags: []string{"ops"}}
}

func TestKnowledgeStoreSearchAndTags(t *testing.T) {
	store := NewStore()
	_, err := store.Put(source())
	require.NoError(t, err)
	found := store.Search("deploy", 10, nil)
	require.Len(t, found, 1)
	assert.Equal(t, "ops/deploy.md", found[0].GetText())
	assert.Empty(t, store.Search("deploy", 10, []string{"missing"}))
	assert.Len(t, store.Search("", 10, []string{"ops"}), 1)
}

func TestKnowledgeHandler(t *testing.T) {
	handler := NewHandler(nil)
	ctx := context.Background()
	_, err := handler.PutSource(ctx, connect.NewRequest(&knowledgev1.PutSourceRequest{Source: source()}))
	require.NoError(t, err)
	got, err := handler.GetSource(ctx, connect.NewRequest(&knowledgev1.GetSourceRequest{Id: "runbook"}))
	require.NoError(t, err)
	assert.Equal(t, "Runbook", got.Msg.GetSource().GetName())
	results, err := handler.Search(ctx, connect.NewRequest(&knowledgev1.SearchRequest{Query: "runbook"}))
	require.NoError(t, err)
	assert.Len(t, results.Msg.GetPassages(), 1)
	_, err = handler.GetSource(ctx, connect.NewRequest(&knowledgev1.GetSourceRequest{Id: "missing"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestKnowledgeServerExposesSearch(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)
	assert.Contains(t, server.Services()[0].Capabilities, "knowledge.search")
}
