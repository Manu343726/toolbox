package knowledge

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledge/knowledgev1"
	"github.com/Manu343726/toolbox/subsystems/knowledge/knowledgev1/knowledgev1connect"
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
	_, err := New(Options{})
	require.NoError(t, err)
}

// A search is what a retrieval client depends on, and its three inputs each change
// the answer in a way the caller has to be able to predict: the query, the tags,
// and the limit. None of them was covered.
func TestSearchFiltersByQueryTagsAndLimit(t *testing.T) {
	store := NewStore()
	for _, source := range []*knowledgev1.KnowledgeSource{
		{Id: "alpha", Name: "Alpha guide", Type: "markdown", Location: "docs/alpha.md", Tags: []string{"guide", "start"}},
		{Id: "beta", Name: "Beta guide", Type: "markdown", Location: "docs/beta.md", Tags: []string{"guide"}},
		{Id: "gamma", Name: "Gamma notes", Type: "note", Location: "notes/gamma.md", Tags: []string{"internal"}},
	} {
		_, err := store.Put(source)
		require.NoError(t, err)
	}

	// An empty query is a listing of everything, not a search for the empty string
	// that would match everything by accident rather than by decision.
	assert.Len(t, store.Search("", 0, nil), 3)

	// The query matches the metadata, case-insensitively.
	byName := store.Search("ALPHA", 0, nil)
	require.Len(t, byName, 1)
	assert.Equal(t, "alpha", byName[0].GetSourceId())

	// Every requested tag must be present, so narrowing narrows.
	byTag := store.Search("", 0, []string{"guide"})
	require.Len(t, byTag, 2)
	assert.Equal(t, "alpha", byTag[0].GetSourceId(), "results are ordered by identifier, whatever the map held")
	assert.Equal(t, "beta", byTag[1].GetSourceId())

	both := store.Search("", 0, []string{"guide", "start"})
	require.Len(t, both, 1)
	assert.Equal(t, "alpha", both[0].GetSourceId())

	// A tag nothing carries is an empty result, not every result: requiring a tag
	// that is absent is how a caller would get a document it did not ask for.
	assert.Empty(t, store.Search("", 0, []string{"nobody-has-this"}))

	// A limit is honoured, and a non-positive one is a default rather than nothing.
	assert.Len(t, store.Search("", 1, nil), 1)
	assert.Equal(t, "alpha", store.Search("", 1, nil)[0].GetSourceId())
	assert.Len(t, store.Search("", 0, nil), 3, "a limit of zero is a default, not an empty answer")
	assert.Len(t, store.Search("", -5, nil), 3)

	// A query that matches nothing is empty rather than everything.
	assert.Empty(t, store.Search("no such text", 0, nil))

	// The passage reports the source's location, which is what a caller retrieves
	// next, and a score the caller can rely on being present.
	require.NotEmpty(t, byName)
	assert.Equal(t, "docs/alpha.md", byName[0].GetText())
	assert.Equal(t, float64(1), byName[0].GetScore())
}

// Two identical searches must answer identically, because a retrieval client
// paging or retrying a search cannot tell a reordering from a change.
func TestSearchIsDeterministic(t *testing.T) {
	store := NewStore()
	for _, id := range []string{"delta", "alpha", "charlie", "bravo", "echo"} {
		_, err := store.Put(&knowledgev1.KnowledgeSource{Id: id, Name: id + " guide", Location: id + ".md"})
		require.NoError(t, err)
	}
	first := store.Search("guide", 0, nil)
	for range 5 {
		again := store.Search("guide", 0, nil)
		require.Len(t, again, len(first))
		for i := range first {
			assert.Equal(t, first[i].GetSourceId(), again[i].GetSourceId())
		}
	}
	identifiers := make([]string, 0, len(first))
	for _, passage := range first {
		identifiers = append(identifiers, passage.GetSourceId())
	}
	assert.Equal(t, []string{"alpha", "bravo", "charlie", "delta", "echo"}, identifiers)
}

// A stored source is a copy, so a caller that edits what it wrote or what it was
// handed back cannot change what the store holds.
func TestAStoredSourceIsNotReachableThroughItsMessage(t *testing.T) {
	store := NewStore()
	written := &knowledgev1.KnowledgeSource{Id: "alpha", Location: "docs/alpha.md", Tags: []string{"guide"}}
	stored, err := store.Put(written)
	require.NoError(t, err)

	written.Location = "rewritten"
	written.Tags = append(written.Tags, "invented")
	stored.Tags[0] = "rewritten"

	read, err := store.Get("alpha")
	require.NoError(t, err)
	assert.Equal(t, "docs/alpha.md", read.GetLocation())
	assert.Equal(t, []string{"guide"}, read.GetTags())
}

// Storing the same identifier again replaces the source, which is how a document
// is revised, and the replacement is the one served afterwards.
func TestPutReplacesASourceWithTheSameIdentifier(t *testing.T) {
	store := NewStore()
	_, err := store.Put(&knowledgev1.KnowledgeSource{Id: "alpha", Location: "first.md"})
	require.NoError(t, err)
	_, err = store.Put(&knowledgev1.KnowledgeSource{Id: "alpha", Location: "second.md"})
	require.NoError(t, err)

	read, err := store.Get("alpha")
	require.NoError(t, err)
	assert.Equal(t, "second.md", read.GetLocation())
	assert.Len(t, store.Search("", 0, nil), 1, "a replacement is not a second source")
}

// A source with no identifier cannot be retrieved, looked up, or replaced, so
// storing one would only move the failure to whoever tried to use it.
func TestTheStoreRefusesASourceItCannotServe(t *testing.T) {
	store := NewStore()
	_, err := store.Put(nil)
	require.Error(t, err)
	_, err = store.Put(&knowledgev1.KnowledgeSource{})
	require.Error(t, err)
	_, err = store.Put(&knowledgev1.KnowledgeSource{Id: "  "})
	require.Error(t, err, "an identifier of only whitespace names nothing")
	assert.Empty(t, store.Search("", 0, nil), "nothing refused was stored")
}

func TestAnEmptyStoreSearchesToNothing(t *testing.T) {
	store := NewStore()
	assert.Empty(t, store.Search("", 0, nil))
	assert.Empty(t, store.Search("anything", 0, nil))
	_, err := store.Get("absent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absent", "the error names what was not found")
}

// An identifier of only whitespace names nothing, and accepting one sent the
// caller looking for a source that was right there in the store.
func TestTheHandlerRefusesAnIdentifierThatNamesNothing(t *testing.T) {
	handler := NewHandler(NewStore())
	for _, request := range []*connect.Request[knowledgev1.GetSourceRequest]{
		nil,
		connect.NewRequest(&knowledgev1.GetSourceRequest{}),
		connect.NewRequest(&knowledgev1.GetSourceRequest{Id: "  "}),
	} {
		_, err := handler.GetSource(context.Background(), request)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
			"an identifier that names nothing is the caller's mistake, not a missing source")
	}

	// A search with no query is a listing, not a refusal: a client that sends
	// nothing is asking for everything.
	listed, err := handler.Search(context.Background(), connect.NewRequest(&knowledgev1.SearchRequest{}))
	require.NoError(t, err)
	assert.Empty(t, listed.Msg.GetPassages())
}

func TestTheStoreIsSafeUnderConcurrentUse(t *testing.T) {
	store := NewStore()
	_, err := store.Put(&knowledgev1.KnowledgeSource{Id: "seed", Location: "seed.md"})
	require.NoError(t, err)

	const workers = 12
	var group sync.WaitGroup
	failures := make(chan error, workers)
	for worker := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			id := fmt.Sprintf("source-%d", worker)
			source := &knowledgev1.KnowledgeSource{Id: id, Name: id + " guide", Location: id + ".md"}
			for range 10 {
				if _, err := store.Put(source); err != nil {
					failures <- err
					return
				}
				if _, err := store.Get(id); err != nil {
					failures <- err
					return
				}
				store.Search("guide", 0, nil)
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("using the store concurrently: %v", err)
	}
	// The default limit is ten passages, so a search over more sources than that
	// returns ten and not everything: a limit nobody asked for is still a limit,
	// and returning everything would make the field undependable.
	// The seed is named "seed" and carries no "guide", so the query excludes it
	// whatever the limit is — which is the query doing its job under concurrency.
	assert.Len(t, store.Search("guide", 0, nil), 10)
	assert.Len(t, store.Search("guide", int32(workers), nil), workers,
		"a caller that asks for more than the default gets what it asked for")
	assert.Len(t, store.Search("", 0, nil), 10, "the seed counts towards the default limit")
	assert.Len(t, store.Search("", int32(workers+1), nil), workers+1)
}

// What a deployment launches is part of the subsystem's behaviour: the name it
// registers under and the services it claims are how a host finds it.
func TestNewDeclaresItsContract(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)

	descriptor := server.Descriptor()
	assert.Equal(t, Name, descriptor.SubsystemName)
	assert.Equal(t, []string{knowledgev1connect.KnowledgeServiceName}, descriptor.ServiceNames)
}
