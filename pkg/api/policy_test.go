package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The semantics a policy has to keep, each of which was a decision rather than an
// implementation detail: the namespace includes the API, the most specific rule
// decides, a refusal can be written inside a grant, and nothing matched is a
// refusal.

func readFacts(id string) OperationFacts {
	return OperationFacts{ID: id, SideEffects: []SideEffect{SideEffectReadOnly}}
}

func writeFacts(id string) OperationFacts {
	return OperationFacts{ID: id, SideEffects: []SideEffect{SideEffectCreate, SideEffectUpdate}}
}

func bareFacts(id string) OperationFacts {
	return OperationFacts{ID: id}
}

func TestAnEmptyPolicyRefusesEverything(t *testing.T) {
	// No stated policy is not the same as a permissive one. A deployment that has
	// written nothing down has authorized nothing.
	policy := Policy{}
	assert.False(t, policy.Allows(readFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/Search")))
	assert.False(t, policy.Matched(readFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/Search")),
		"a caller can tell a refusal from a policy that said nothing")
	assert.Equal(t, DecisionDeny, policy.Decide(readFacts("knowledge/x/Y/Z")))

	assert.False(t, DenyAll().Allows(readFacts("knowledge/x/Y/Z")))
	assert.True(t, AllowAll().Allows(readFacts("knowledge/x/Y/Z")))
	assert.True(t, AllowAll().Allows(bareFacts("knowledge/x/Y/Z")),
		"a policy that trusts every operation trusts the unclassified ones too")
}

func TestAPatternAddressesOneAPIsOperations(t *testing.T) {
	// The identifier includes the API that owns the operation, which is what makes
	// it unique when several subsystems serve one contract. A rule therefore has to
	// be able to name one provider rather than the contract all of them serve.
	policy, err := NewPolicy(Rule{
		Pattern:  "apimcp/toolbox.api.v1.ApiParserService/ParseApi",
		Decision: DecisionAllow,
	})
	require.NoError(t, err)

	// A rule that names no class is a statement about every operation, so it covers
	// one nothing classified.
	assert.True(t, policy.Allows(bareFacts("apimcp/toolbox.api.v1.ApiParserService/ParseApi")))
	assert.False(t, policy.Allows(bareFacts("apigrpc/toolbox.api.v1.ApiParserService/ParseApi")),
		"naming one provider does not authorize the others that serve the same contract")
}

func TestTheMostSpecificRuleDecides(t *testing.T) {
	// A broad grant and a narrow refusal, written in an order that is deliberately
	// the wrong way round: the order a policy is written in must not change what it
	// says.
	policy, err := NewPolicy(
		Rule{Pattern: "*", Decision: DecisionAllow},
		Rule{Pattern: "registry/**", Decision: DecisionDeny},
		Rule{Pattern: "registry/toolbox.registry.v1.RegistryService/Deregister", Decision: DecisionAllow},
	)
	require.NoError(t, err)

	assert.False(t, policy.Allows(bareFacts("registry/toolbox.registry.v1.RegistryService/Register")),
		"the narrower refusal wins over the broad grant")
	assert.True(t, policy.Allows(bareFacts("registry/toolbox.registry.v1.RegistryService/Deregister")),
		"the narrowest grant wins over both")
	assert.True(t, policy.Allows(bareFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/Search")),
		"everything else falls through to the broad grant")
}

func TestASideEffectClassIsAFilterNotAGrant(t *testing.T) {
	// The point of separating classification from grant: a deployment says "reads,
	// no writes" and does not have to name anything.
	policy, err := NewPolicy(
		Rule{Pattern: "knowledge/**", Classes: []EffectClass{EffectClassRead}, Decision: DecisionAllow},
		Rule{Pattern: "knowledge/**", Classes: []EffectClass{EffectClassWrite}, Decision: DecisionDeny},
		Rule{Pattern: "knowledge/**", Classes: []EffectClass{EffectClassUnclassified}, Decision: DecisionAllow},
	)
	require.NoError(t, err)

	assert.True(t, policy.Allows(readFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/Search")))
	assert.False(t, policy.Allows(writeFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/PutSource")))
	assert.True(t, policy.Allows(bareFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/DoThing")),
		"a deployment can deliberately trust an unclassified operation, by saying so")
}

func TestAnUnclassifiedOperationIsNotARead(t *testing.T) {
	// The safe direction, and the reason a wildcard does not cover everything: a
	// rule that names the read class does not match an operation nobody classified.
	policy, err := NewPolicy(
		Rule{Pattern: "*", Classes: []EffectClass{EffectClassRead}, Decision: DecisionAllow},
	)
	require.NoError(t, err)
	assert.False(t, policy.Allows(bareFacts("knowledge/x/Y/Z")))
	assert.False(t, policy.Allows(writeFacts("knowledge/x/Y/Z")))

	// A rule with no class is a statement about every operation, which is the other
	// way to trust an unclassified one.
	every, err := NewPolicy(Rule{Pattern: "*", Decision: DecisionAllow})
	require.NoError(t, err)
	assert.True(t, every.Allows(bareFacts("knowledge/x/Y/Z")))
}

func TestAWildcardMatchesOneSegmentAndNoMore(t *testing.T) {
	policy, err := NewPolicy(Rule{Pattern: "knowledge/toolbox.knowledge.v1.KnowledgeService/*", Decision: DecisionAllow})
	require.NoError(t, err)

	assert.True(t, policy.Allows(bareFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/Search")),
		"a service name is one segment, however many dots it has")
	assert.False(t, policy.Allows(bareFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/Search/Extra")),
		"a pattern that names three segments does not reach a fourth")
	assert.False(t, policy.Allows(bareFacts("knowledge-archive/toolbox.knowledge.v1.KnowledgeService/Search")),
		"a segment is matched whole, so a prefix does not match")
	assert.False(t, policy.Allows(bareFacts("other/toolbox.knowledge.v1.KnowledgeService/Search")))

	// The trailing rest is what lets a rule say "this API" without naming the
	// arity of an operation identifier.
	api, err := NewPolicy(Rule{Pattern: "knowledge/**", Decision: DecisionAllow})
	require.NoError(t, err)
	assert.True(t, api.Allows(bareFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/Search")))
	assert.True(t, api.Allows(bareFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/Search/Deep")),
		"a trailing rest reaches everything below it")
	assert.False(t, api.Allows(bareFacts("knowledge-archive/x/Y/Z")))

	_, err = NewPolicy(Rule{Pattern: "knowledge/**/Search", Decision: DecisionAllow})
	assert.Error(t, err, "a rest that is not last would be ambiguous")
}

func TestEqualSpecificityKeepsTheOrderRulesWereWritten(t *testing.T) {
	// Two rules matching exactly the same operations are decided by a person having
	// put them in an order, and a set of equally specific rules is still
	// deterministic.
	first, err := NewPolicy(
		Rule{Pattern: "knowledge/**", Decision: DecisionAllow},
		Rule{Pattern: "knowledge/**", Decision: DecisionDeny},
	)
	require.NoError(t, err)
	assert.True(t, first.Allows(bareFacts("knowledge/x/Y/Z")))

	second, err := NewPolicy(
		Rule{Pattern: "knowledge/**", Decision: DecisionDeny},
		Rule{Pattern: "knowledge/**", Decision: DecisionAllow},
	)
	require.NoError(t, err)
	assert.False(t, second.Allows(bareFacts("knowledge/x/Y/Z")))
}

func TestAPolicyRefusesWhatItCannotRead(t *testing.T) {
	_, err := NewPolicy(Rule{Pattern: "", Decision: DecisionAllow})
	assert.Error(t, err, "a rule with no pattern would match everything by accident")

	_, err = NewPolicy(Rule{Pattern: "knowledge/*", Decision: "permit"})
	assert.Error(t, err)

	_, err = NewPolicy(Rule{Pattern: "knowledge/**", Classes: []EffectClass{"maybe"}, Decision: DecisionAllow})
	assert.Error(t, err, "an unknown class would otherwise read as no filter at all")

	_, err = NewPolicy(Rule{Pattern: "knowledge/know*", Decision: DecisionAllow})
	assert.Error(t, err, "a wildcard inside a segment is a prefix match, which is not what it looks like")

	_, err = NewPolicy(Rule{Pattern: "knowledge//Search", Decision: DecisionAllow})
	assert.Error(t, err)
}

func TestAPolicyIsQueryable(t *testing.T) {
	policy, err := NewPolicy(
		Rule{Pattern: "knowledge/*", Decision: DecisionDeny},
		Rule{Pattern: "*", Classes: []EffectClass{EffectClassRead}, Decision: DecisionAllow},
	)
	require.NoError(t, err)
	assert.Equal(t, 2, policy.Len())
	rules := policy.Rules()
	require.Len(t, rules, 2)
	assert.Equal(t, "knowledge/*", rules[0].Pattern, "the rules come back in the order they are consulted")
	assert.Equal(t, DecisionDeny, rules[0].Decision)
	assert.Equal(t, []EffectClass{EffectClassRead}, rules[1].Classes)
}

func TestAPolicyDocumentRoundTrips(t *testing.T) {
	// One document, read the same way whether it lives in a file, in the policy
	// subsystem's storage, or in a fixture.
	const document = `
# Every read in the deployment, and nothing else, except the two services a
# deployment has decided are safe to write to.
allow *                        read
deny  registry/**              write read unclassified
allow  knowledge/**            write read unclassified
allow  apimcp/**               read
`
	policy, err := ParsePolicy(document)
	require.NoError(t, err)

	assert.True(t, policy.Allows(readFacts("documentation/toolbox.documentation.v1.DocumentationService/GetDocumentation")),
		"a broad grant needs only a class")
	assert.False(t, policy.Allows(writeFacts("documentation/toolbox.documentation.v1.DocumentationService/GetDocumentation")),
		"and it really does exclude the writes")
	assert.False(t, policy.Allows(bareFacts("registry/toolbox.registry.v1.RegistryService/Register")))
	assert.True(t, policy.Allows(writeFacts("knowledge/toolbox.knowledge.v1.KnowledgeService/PutSource")))
	assert.True(t, policy.Allows(readFacts("apimcp/toolbox.api.v1.ApiParserService/ParseApi")))
	assert.False(t, policy.Allows(bareFacts("apimcp/toolbox.api.v1.ApiParserService/ParseApi")),
		"an operation that lost its classification is no longer covered by a read grant, which is the point")

	rendered := policy.Format()
	reparsed, err := ParsePolicy(rendered)
	require.NoError(t, err)
	assert.Equal(t, policy.Rules(), reparsed.Rules(), "a policy read back is the policy")

	// The round trip is through the consulted order, which is the order that
	// decides, so the document a person reads back says what the policy does.
	assert.Contains(t, rendered, "deny registry/**")
	assert.Equal(t, "deny", strings.Fields(rendered)[0])
}

func TestAPolicyDocumentRefusesWhatItCannotRead(t *testing.T) {
	// A policy that silently skipped half its rules would authorize less than its
	// author believes, which is the direction that gets noticed last.
	_, err := ParsePolicy("allow\n")
	assert.ErrorContains(t, err, "line 1")

	_, err = ParsePolicy("# a comment\n\nallow knowledge/**\npermit registry/**\n")
	assert.ErrorContains(t, err, "line 4", "the error names the line a person has to fix, comments and blanks included")

	_, err = ParsePolicy("allow knowledge/** maybe\n")
	assert.ErrorContains(t, err, "line 1")
}
