package model

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	modelv1 "github.com/Manu343726/toolbox/subsystems/model/modelv1"
	"github.com/Manu343726/toolbox/subsystems/model/modelv1/modelv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultModelCatalogAndGeneration(t *testing.T) {
	handler := NewHandler(Options{})
	ctx := context.Background()
	models, err := handler.ListModels(ctx, connect.NewRequest(&modelv1.ListModelsRequest{}))
	require.NoError(t, err)
	require.Len(t, models.Msg.GetModels(), 1)
	assert.Equal(t, "reference/echo", models.Msg.GetModels()[0].GetId())

	generated, err := handler.Generate(ctx, connect.NewRequest(&modelv1.GenerateRequest{ModelId: "reference/echo", Prompt: "hello"}))
	require.NoError(t, err)
	assert.Equal(t, "hello", generated.Msg.GetText())
	_, err = handler.Generate(ctx, connect.NewRequest(&modelv1.GenerateRequest{ModelId: "missing"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCustomProviderAndFilter(t *testing.T) {
	handler := NewHandler(Options{
		Models: []*modelv1.ModelDescriptor{{Id: "custom/model", Provider: "custom", DisplayName: "Custom"}},
		Generate: func(_ context.Context, req *modelv1.GenerateRequest) (string, error) {
			return req.GetPrompt() + "-custom", nil
		},
	})
	models, err := handler.ListModels(context.Background(), connect.NewRequest(&modelv1.ListModelsRequest{Provider: "custom"}))
	require.NoError(t, err)
	require.Len(t, models.Msg.GetModels(), 1)
	generated, err := handler.Generate(context.Background(), connect.NewRequest(&modelv1.GenerateRequest{ModelId: "custom/model", Prompt: "x"}))
	require.NoError(t, err)
	assert.Equal(t, "x-custom", generated.Msg.GetText())
}

// A provider says what went wrong, and the code a caller sees follows from that.
// Reporting every failure as not-found sent a caller looking for a model that
// exists, and told a caller that cancelled to investigate a request it had
// already abandoned.
func TestGenerateReportsWhatTheProviderActuallyFailedWith(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		failure error
		want    connect.Code
	}{
		{"an absent model", api.Errorf(api.KindNotFound, "no such model"), connect.CodeNotFound},
		{"a provider that is down", api.Errorf(api.KindUnavailable, "the provider is down"), connect.CodeUnavailable},
		{"a request the provider refused", api.Errorf(api.KindInvalid, "prompt too long"), connect.CodeInvalidArgument},
		{"a provider that gave up", api.Errorf(api.KindInternal, "upstream exploded"), connect.CodeInternal},
		{"a caller that stopped waiting", context.Canceled, connect.CodeCanceled},
		{"a deadline that passed", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			handler := NewHandler(Options{
				Models:   []*modelv1.ModelDescriptor{{Id: "custom/model", Provider: "custom"}},
				Generate: func(context.Context, *modelv1.GenerateRequest) (string, error) { return "", testCase.failure },
			})
			_, err := handler.Generate(context.Background(), connect.NewRequest(&modelv1.GenerateRequest{
				ModelId: "custom/model",
			}))
			require.Error(t, err)
			assert.Equal(t, testCase.want, connect.CodeOf(err))
		})
	}
}

// The catalog is copied into the handler and copied again into each response, so
// a caller cannot rewrite the deployment's catalog by editing what it was handed.
func TestTheCatalogIsNotReachableThroughAResponse(t *testing.T) {
	handler := NewHandler(Options{Models: []*modelv1.ModelDescriptor{{
		Id: "custom/model", Provider: "custom", DisplayName: "Original",
	}}})

	first, err := handler.ListModels(context.Background(), connect.NewRequest(&modelv1.ListModelsRequest{}))
	require.NoError(t, err)
	first.Msg.GetModels()[0].DisplayName = "Rewritten"
	first.Msg.GetModels()[0].Id = "rewritten/model"

	second, err := handler.ListModels(context.Background(), connect.NewRequest(&modelv1.ListModelsRequest{}))
	require.NoError(t, err)
	require.Len(t, second.Msg.GetModels(), 1)
	assert.Equal(t, "Original", second.Msg.GetModels()[0].GetDisplayName())
	assert.Equal(t, "custom/model", second.Msg.GetModels()[0].GetId())

	// And the handler's own copy is not the caller's to begin with: handing
	// Options a slice and then mutating it must not change what is served.
	assert.NotSame(t, first.Msg.GetModels()[0], second.Msg.GetModels()[0])
}

// A listing is what a client picks from, so its order is part of the behaviour
// rather than whatever the slice happened to be in.
func TestListModelsIsOrderedByIdentifier(t *testing.T) {
	handler := NewHandler(Options{Models: []*modelv1.ModelDescriptor{
		{Id: "zebra/model", Provider: "z"},
		{Id: "alpha/model", Provider: "a"},
		{Id: "middle/model", Provider: "m"},
	}})

	listed, err := handler.ListModels(context.Background(), connect.NewRequest(&modelv1.ListModelsRequest{}))
	require.NoError(t, err)
	identifiers := make([]string, 0, len(listed.Msg.GetModels()))
	for _, model := range listed.Msg.GetModels() {
		identifiers = append(identifiers, model.GetId())
	}
	assert.Equal(t, []string{"alpha/model", "middle/model", "zebra/model"}, identifiers)
}

// A filter naming a provider with no models is an empty answer, not a failure: a
// client asking what a provider offers should not have to treat "none" as an
// error and retry with no filter.
func TestListModelsFiltersAndAbsence(t *testing.T) {
	handler := NewHandler(Options{Models: []*modelv1.ModelDescriptor{
		{Id: "a/one", Provider: "a"},
		{Id: "b/two", Provider: "b"},
	}})

	matching, err := handler.ListModels(context.Background(), connect.NewRequest(&modelv1.ListModelsRequest{Provider: "a"}))
	require.NoError(t, err)
	require.Len(t, matching.Msg.GetModels(), 1)
	assert.Equal(t, "a/one", matching.Msg.GetModels()[0].GetId())

	absent, err := handler.ListModels(context.Background(), connect.NewRequest(&modelv1.ListModelsRequest{Provider: "nobody"}))
	require.NoError(t, err)
	assert.Empty(t, absent.Msg.GetModels())

	// A request that names no provider is a listing, not a refusal.
	all, err := handler.ListModels(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, all.Msg.GetModels(), 2)
}

// Generation without a model is the caller's mistake and says so, before any
// provider is asked.
func TestGenerateRefusesARequestWithNoModel(t *testing.T) {
	asked := false
	handler := NewHandler(Options{
		Models:   []*modelv1.ModelDescriptor{{Id: "a/one"}},
		Generate: func(context.Context, *modelv1.GenerateRequest) (string, error) { asked = true; return "", nil },
	})
	for _, request := range []*connect.Request[modelv1.GenerateRequest]{
		nil,
		connect.NewRequest(&modelv1.GenerateRequest{}),
		connect.NewRequest(&modelv1.GenerateRequest{ModelId: "  "}),
	} {
		_, err := handler.Generate(context.Background(), request)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
	assert.False(t, asked, "no provider is asked for a request that names no model")
}

// The catalog is read by every client of a shared subsystem at once, so the lock
// that guards it is part of the contract rather than an optimisation.
func TestListModelsIsSafeWhileTheCatalogIsRead(t *testing.T) {
	handler := NewHandler(Options{Models: []*modelv1.ModelDescriptor{
		{Id: "a/one", Provider: "a"},
		{Id: "b/two", Provider: "b"},
	}})

	const readers = 16
	var group sync.WaitGroup
	failures := make(chan error, readers)
	for range readers {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 20 {
				if _, err := handler.ListModels(context.Background(), connect.NewRequest(&modelv1.ListModelsRequest{})); err != nil {
					failures <- err
					return
				}
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("listing the catalog concurrently: %v", err)
	}
}

// What a deployment launches is part of the subsystem's behaviour: the name it
// registers under and the services it claims are how a host finds it.
func TestNewDeclaresItsContract(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)

	descriptor := server.Descriptor()
	assert.Equal(t, Name, descriptor.SubsystemName)
	assert.Equal(t, Version, descriptor.ImplementationVersion)
	assert.Equal(t, []string{modelv1connect.ModelServiceName}, descriptor.ServiceNames)
}
