package skill

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	skillv1 "github.com/Manu343726/toolbox/subsystems/skill/skillv1"
	"github.com/Manu343726/toolbox/subsystems/skill/skillv1/skillv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSkill() *skillv1.Skill {
	return &skillv1.Skill{Id: "review", Name: "Review", Version: "1", Instructions: "Review carefully", RequiredCapabilities: []string{"knowledge.search"}}
}

func TestSkillStoreAndHandler(t *testing.T) {
	store := NewStore()
	handler := NewHandler(store)
	ctx := context.Background()
	put, err := handler.PutSkill(ctx, connect.NewRequest(&skillv1.PutSkillRequest{Skill: testSkill()}))
	require.NoError(t, err)
	assert.Equal(t, "review", put.Msg.GetSkill().GetId())

	got, err := handler.GetSkill(ctx, connect.NewRequest(&skillv1.GetSkillRequest{Id: "review"}))
	require.NoError(t, err)
	assert.Equal(t, "Review carefully", got.Msg.GetSkill().GetInstructions())

	_, err = handler.GetSkill(ctx, connect.NewRequest(&skillv1.GetSkillRequest{Id: "missing"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = handler.PutSkill(ctx, connect.NewRequest(&skillv1.PutSkillRequest{Skill: &skillv1.Skill{Id: "x"}}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSkillStoreRejectsDuplicateAndListsVersions(t *testing.T) {
	store := NewStore()
	_, err := store.Put(testSkill(), true)
	require.NoError(t, err)
	_, err = store.Put(testSkill(), true)
	assert.Error(t, err)
	second := testSkill()
	second.Version = "2"
	_, err = store.Put(second, false)
	require.NoError(t, err)
	all := store.List("rev")
	require.Len(t, all, 2)
	assert.Equal(t, "1", all[0].GetVersion())
	assert.Equal(t, "2", all[1].GetVersion())
}

// A skill with no version named is the latest one, and "latest" is a number
// rather than a piece of text: comparing the versions as strings served the
// ninth revision of a skill that had been revised ten times, because "10" sorts
// before "9".
func TestTheLatestVersionIsTheGreatestNotTheLastAlphabetical(t *testing.T) {
	store := NewStore()
	for _, at := range []string{"1", "2", "9", "10", "11"} {
		skill := testSkill()
		skill.Version = at
		_, err := store.Put(skill, false)
		require.NoError(t, err)
	}

	latest, err := store.Get("review", "")
	require.NoError(t, err)
	assert.Equal(t, "11", latest.GetVersion(), "the greatest version, not the last in text order")

	// And a revision past nine is found by number too, not only the greatest.
	ninth, err := store.Get("review", "9")
	require.NoError(t, err)
	assert.Equal(t, "9", ninth.GetVersion())
}

// A skill is stored as a copy. A caller that kept the message it wrote, or edited
// the message it was handed back, must not be able to change what the store holds.
func TestAStoredSkillIsNotReachableThroughItsMessage(t *testing.T) {
	store := NewStore()
	written := testSkill()
	stored, err := store.Put(written, false)
	require.NoError(t, err)

	// The caller keeps its own message and edits it.
	written.Instructions = "Rewritten by the caller"
	written.RequiredCapabilities = append(written.RequiredCapabilities, "invented")

	// And the store hands back a copy, which the caller edits too.
	stored.Instructions = "Rewritten through the response"
	stored.RequiredCapabilities[0] = "rewritten"

	read, err := store.Get("review", "1")
	require.NoError(t, err)
	assert.Equal(t, "Review carefully", read.GetInstructions())
	assert.Equal(t, []string{"knowledge.search"}, read.GetRequiredCapabilities())

	// The same holds through the listing.
	listed := store.List("review")
	require.Len(t, listed, 1)
	assert.Equal(t, "Review carefully", listed[0].GetInstructions())
	listed[0].Instructions = "Rewritten through the listing"

	again, err := store.Get("review", "1")
	require.NoError(t, err)
	assert.Equal(t, "Review carefully", again.GetInstructions())
}

// A listing is what a client chooses from, so its order is behaviour rather than
// whatever the map happened to yield.
func TestListSkillsIsOrderedByIdentifierThenVersion(t *testing.T) {
	store := NewStore()
	for _, entry := range []struct{ id, version string }{
		{"zebra", "1"}, {"alpha", "2"}, {"alpha", "10"}, {"alpha", "1"}, {"middle", "1"},
	} {
		skill := testSkill()
		skill.Id, skill.Version = entry.id, entry.version
		_, err := store.Put(skill, false)
		require.NoError(t, err)
	}

	all := store.List("")
	got := make([]string, 0, len(all))
	for _, skill := range all {
		got = append(got, skill.GetId()+"@"+skill.GetVersion())
	}
	assert.Equal(t, []string{"alpha@1", "alpha@2", "alpha@10", "middle@1", "zebra@1"}, got,
		"versions of one skill are ordered by value, not as text")

	// A prefix narrows the listing, and one that matches nothing is empty rather
	// than a failure.
	assert.Len(t, store.List("alpha"), 3)
	assert.Empty(t, store.List("nobody"))
}

// An empty store is a subsystem with nothing to serve, not a broken one.
func TestAnEmptyStoreListsNothing(t *testing.T) {
	assert.Empty(t, NewStore().List(""))
	_, err := NewStore().Get("absent", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absent", "the error names what was not found")
}

// Validation happens at the store, so an embedded caller and a remote one are held
// to the same rules. A skill with no instructions cannot be followed by anyone, so
// storing one would only move the failure to whoever tried to use it.
func TestTheStoreRefusesASkillItCannotServe(t *testing.T) {
	store := NewStore()
	for _, testCase := range []struct {
		name  string
		skill *skillv1.Skill
	}{
		{"nothing at all", nil},
		{"no identifier", &skillv1.Skill{Version: "1", Name: "n", Instructions: "i"}},
		{"a blank identifier", &skillv1.Skill{Id: "  ", Version: "1", Name: "n", Instructions: "i"}},
		{"no version", &skillv1.Skill{Id: "review", Name: "n", Instructions: "i"}},
		{"no name", &skillv1.Skill{Id: "review", Version: "1", Instructions: "i"}},
		{"no instructions", &skillv1.Skill{Id: "review", Version: "1", Name: "n"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := store.Put(testCase.skill, false)
			require.Error(t, err)
		})
	}
	assert.Empty(t, store.List(""), "nothing refused was stored")
}

// The handler is the contract's boundary, so its refusals carry the codes the
// contract declares rather than one code for every problem.
func TestTheHandlerCodesItsRefusals(t *testing.T) {
	handler := NewHandler(NewStore())

	for _, request := range []*connect.Request[skillv1.GetSkillRequest]{
		nil,
		connect.NewRequest(&skillv1.GetSkillRequest{}),
		connect.NewRequest(&skillv1.GetSkillRequest{Id: "  "}),
	} {
		_, err := handler.GetSkill(context.Background(), request)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}

	for _, request := range []*connect.Request[skillv1.PutSkillRequest]{
		nil,
		connect.NewRequest(&skillv1.PutSkillRequest{}),
	} {
		_, err := handler.PutSkill(context.Background(), request)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}

	// An absent skill is a not-found, on both the exact and the latest lookup.
	_, err := handler.GetSkill(context.Background(), connect.NewRequest(&skillv1.GetSkillRequest{Id: "absent"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = handler.GetSkill(context.Background(), connect.NewRequest(&skillv1.GetSkillRequest{Id: "absent", Version: "1"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// Storing a skill that is already there is a replacement, and saying so is the
// difference between a version being revised and a version being refused.
func TestPutReplacesUnlessTheCallerRefusesReplacement(t *testing.T) {
	store := NewStore()
	_, err := store.Put(testSkill(), true)
	require.NoError(t, err)

	revised := testSkill()
	revised.Instructions = "Review more carefully"
	replaced, err := store.Put(revised, false)
	require.NoError(t, err)
	assert.Equal(t, "Review more carefully", replaced.GetInstructions())

	_, err = store.Put(testSkill(), true)
	require.Error(t, err, "a caller that refuses replacement is told the skill is there")

	// A different version is not a replacement of this one, so it is accepted even
	// when the caller refuses replacement.
	next := testSkill()
	next.Version = "2"
	_, err = store.Put(next, true)
	require.NoError(t, err)
	assert.Len(t, store.List("review"), 2)
}

// The store is shared by every client of a running subsystem, so concurrent
// writes and reads are part of what it has to be correct about.
func TestTheStoreIsSafeUnderConcurrentUse(t *testing.T) {
	store := NewStore()
	_, err := store.Put(testSkill(), false)
	require.NoError(t, err)

	const workers = 12
	var group sync.WaitGroup
	failures := make(chan error, workers*3)
	for worker := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			at := fmt.Sprintf("v%d", worker)
			skill := testSkill()
			skill.Version = at
			for range 10 {
				if _, err := store.Put(skill, false); err != nil {
					failures <- err
					return
				}
				if _, err := store.Get("review", at); err != nil {
					failures <- err
					return
				}
				store.List("review")
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("using the store concurrently: %v", err)
	}
	// The version seeded above, plus one per worker.
	assert.Len(t, store.List("review"), workers+1)
}

// What a deployment launches is part of the subsystem's behaviour: the name it
// registers under and the services it claims are how a host finds it.
func TestNewDeclaresItsContract(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)

	descriptor := server.Descriptor()
	assert.Equal(t, Name, descriptor.SubsystemName)
	assert.Equal(t, []string{skillv1connect.SkillServiceName}, descriptor.ServiceNames)
}
