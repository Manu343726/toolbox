package skill

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	skillv1 "github.com/Manu343726/toolsbox/subsystems/skill/skillv1"
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
