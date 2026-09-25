package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A chain has to answer three different questions differently. These tests are about
// that, because flattening them is the failure a chain exists to avoid.

func TestTheFirstLegThatKnowsTheServiceWins(t *testing.T) {
	chain := NewChain(
		Leg{Name: "this process", Resolver: NewStaticResolver(Endpoint{
			Name: "knowledge", URL: "http://127.0.0.1:9001",
			ServiceNames: []string{"toolbox.knowledge.v1.KnowledgeService"},
		})},
		Leg{Name: "the core", Resolver: NewStaticResolver(Endpoint{
			Name: "knowledge", URL: "http://10.0.0.5:9001",
			ServiceNames: []string{"toolbox.knowledge.v1.KnowledgeService"},
		})},
	)
	endpoint, err := chain.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9001", endpoint.URL,
		"a local endpoint needs no network hop and is authoritative")

	leg, err := ServedBy(t.Context(), chain, "toolbox.knowledge.v1.KnowledgeService")
	require.NoError(t, err)
	assert.Equal(t, "this process", leg)
}

func TestALegThatAnswersNotThereIsTheNextLegsBusiness(t *testing.T) {
	// This is the ordinary case: the process did not start the peer, and the core
	// has it. Nothing about the local miss is a problem worth reporting.
	chain := NewChain(
		Leg{Name: "this process", Resolver: NewStaticResolver()},
		Leg{Name: "the core", Resolver: NewStaticResolver(Endpoint{
			Name: "knowledge", URL: "http://10.0.0.5:9001",
			ServiceNames: []string{"toolbox.knowledge.v1.KnowledgeService"},
		})},
	)
	endpoint, err := chain.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	require.NoError(t, err)
	assert.Equal(t, "http://10.0.0.5:9001", endpoint.URL)
}

func TestNobodyHavingItIsNotFound(t *testing.T) {
	chain := NewChain(
		Leg{Name: "this process", Resolver: NewStaticResolver()},
		Leg{Name: "the core", Resolver: NewStaticResolver()},
	)
	_, err := chain.Resolve(t.Context(), "toolbox.absent.v1.AbsentService")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.NotErrorIs(t, err, ErrUnreachable,
		"every leg was asked and answered, so this is a name that is wrong, not an outage")

	var missing *NotFoundError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, []string{"this process", "the core"}, missing.Tried,
		"the error says where it looked, so a wrong name is diagnosable")
}

func TestACoreThatCannotBeHeardIsNotTheSameAsNotFound(t *testing.T) {
	// The distinction the user asked for: a deployment may legitimately run without
	// a peer, and a caller has to be able to tell that from an outage. Reporting
	// both as "not found" would either refuse to start or hang on every call, and
	// neither would say why.
	outage := errors.New("connection refused")
	chain := NewChain(
		Leg{Name: "this process", Resolver: NewStaticResolver()},
		Leg{Name: "the core", Resolver: ResolverFunc(func(context.Context, string) (Endpoint, error) {
			return Endpoint{}, fmt.Errorf("dial the core: %w", outage)
		})},
	)
	_, err := chain.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnreachable)
	assert.NotErrorIs(t, err, ErrNotFound,
		"a core that could not answer is an outage, and must not read as a missing service")
	assert.ErrorIs(t, err, outage, "the transport cause survives, so a caller can tell a refused connection from a timeout")

	var unreachable *UnreachableError
	require.ErrorAs(t, err, &unreachable)
	require.Len(t, unreachable.Causes, 1)
	assert.Contains(t, unreachable.Causes[0].Error(), "the core",
		"the error says which place could not answer")
}

func TestAnUnreachableLegIsReportedEvenWhenAnotherOneAnswers(t *testing.T) {
	// A caller resolving a peer should not be refused because an unrelated leg is
	// down — but it should not be told nothing either. The answer wins, and the
	// outage is available to whoever asks about it.
	chain := NewChain(
		Leg{Name: "this process", Resolver: ResolverFunc(func(context.Context, string) (Endpoint, error) {
			return Endpoint{}, errors.New("dial failed")
		})},
		Leg{Name: "the core", Resolver: NewStaticResolver(Endpoint{
			Name: "knowledge", URL: "http://10.0.0.5:9001",
			ServiceNames: []string{"toolbox.knowledge.v1.KnowledgeService"},
		})},
	)
	endpoint, err := chain.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	require.NoError(t, err, "a leg that answered is the answer, whatever else is wrong")
	assert.Equal(t, "http://10.0.0.5:9001", endpoint.URL)
}

func TestAnAbsentLegIsSkippedRatherThanFailed(t *testing.T) {
	// A caller with no core configured leaves the leg out rather than passing
	// something that will fail every time.
	chain := NewChain(
		Leg{Name: "this process", Resolver: NewStaticResolver(Endpoint{
			Name: "knowledge", URL: "http://127.0.0.1:9001",
			ServiceNames: []string{"toolbox.knowledge.v1.KnowledgeService"},
		})},
		Leg{Name: "the core", Resolver: nil},
	)
	endpoint, err := chain.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9001", endpoint.URL)

	// And a chain with nothing to try says so, rather than reporting a missing
	// service it never had a way to look for.
	empty := NewChain()
	_, err = empty.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	assert.ErrorIs(t, err, ErrNoResolver)
}

func TestAnAnswerIsRememberedAndCanBeForgotten(t *testing.T) {
	// The cache is what makes a chain usable on a call path, and forgetting is what
	// keeps it honest: a caller that learned a peer's endpoint moved — from a
	// registry change event, or from a failed call — must not keep dialling the old
	// one.
	asks := 0
	remote := ResolverFunc(func(context.Context, string) (Endpoint, error) {
		asks++
		return Endpoint{
			Name: "knowledge", URL: fmt.Sprintf("http://10.0.0.5:%d", 9000+asks),
			ServiceNames: []string{"toolbox.knowledge.v1.KnowledgeService"},
		}, nil
	})
	chain := NewChain(
		Leg{Name: "this process", Resolver: NewStaticResolver()},
		Leg{Name: "the core", Resolver: remote},
	)

	first, err := chain.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	require.NoError(t, err)
	second, err := chain.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, asks, "a second call for the same peer does not go to the network again")

	chain.Forget("toolbox.knowledge.v1.KnowledgeService")
	moved, err := chain.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	require.NoError(t, err)
	assert.Equal(t, 2, asks)
	assert.NotEqual(t, first.URL, moved.URL, "after forgetting, the next answer comes from the core again")
}

func TestAChainIsSafeForConcurrentUse(t *testing.T) {
	// A resolver is on the call path of every subsystem that calls a peer, so the
	// race test is the test that says whether it can be one.
	chain := NewChain(
		Leg{Name: "the core", Resolver: NewStaticResolver(Endpoint{
			Name: "knowledge", URL: "http://10.0.0.5:9001",
			ServiceNames: []string{"toolbox.knowledge.v1.KnowledgeService"},
		})},
	)
	var group sync.WaitGroup
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			name := "toolbox.knowledge.v1.KnowledgeService"
			if index%2 == 0 {
				chain.Forget(name)
				return
			}
			_, _ = chain.Resolve(t.Context(), name)
		}(index)
	}
	group.Wait()
	assert.NotEmpty(t, chain.Legs())
}

func TestRegisteringALegReplacesItInPlace(t *testing.T) {
	// A leg's name identifies it, so a composition that installs a core later does
	// not end up with two legs called "the core" and no way to say which answered.
	chain := NewChain(Leg{Name: "the core", Resolver: NewStaticResolver()})
	chain.Register(Leg{Name: "the core", Resolver: NewStaticResolver(Endpoint{
		Name: "knowledge", URL: "http://10.0.0.5:9001",
		ServiceNames: []string{"toolbox.knowledge.v1.KnowledgeService"},
	})})
	require.Len(t, chain.Legs(), 1)

	endpoint, err := chain.Resolve(t.Context(), "toolbox.knowledge.v1.KnowledgeService")
	require.NoError(t, err)
	assert.Equal(t, "http://10.0.0.5:9001", endpoint.URL)
	assert.Equal(t, []string{"the core"}, SortedLegNames(chain.Legs()))
}

func TestAnEmptyNameIsRefused(t *testing.T) {
	chain := NewChain(Leg{Name: "the core", Resolver: NewStaticResolver()})
	_, err := chain.Resolve(t.Context(), "   ")
	assert.ErrorIs(t, err, ErrNotFound)
}
