package mcp

import (
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A feature reference is how a client names an operation, and every form of it has to be
// one a client can actually construct. Two of them could not be:
//
//   - `feature_exposure` documents a "fully-qualified service name" filter and compared it
//     for equality against the API-qualified name the catalog holds, so it matched nothing.
//     The answer was all zeros, which reads as "this service has no operations" rather than
//     as "you named it in a form nothing matches".
//   - `describe_feature` accepts a service and a method separately and built the reference
//     from them, which produced a name no client holds, because the stored id carries the
//     API identifier as a prefix.
//
// Both are the same defect — a reference expressed in a form the caller cannot produce — so
// they are fixed together, by accepting both the bare contract name and the qualified one.
//
// A bare contract name can match more than one feature, because several providers serving
// one contract is this framework's design rather than an accident. So a reference that
// matches several is refused with the candidates, never resolved to the first.

// testServerWithFeatures builds a server whose surface is the given features, without a
// subsystem behind it. The reference rules are about names, so names are what these tests
// need and a handler is not.
func testServerWithFeatures(t *testing.T, features ...Feature) *Server {
	t.Helper()
	server := &Server{
		sdk:     sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "0.1.0"}, nil),
		entries: make(map[string]*featureEntry, len(features)),
		order:   make([]string, 0, len(features)),
	}
	for _, feature := range features {
		id := normalizeFeatureID(feature.ID)
		server.entries[id] = &featureEntry{feature: feature}
		server.order = append(server.order, id)
	}
	return server
}

// knowledgeFeature is the shape a subsystem feature actually has: the service carries the
// API it was registered under, and the id is that service plus the method.
func knowledgeFeature(method string, allowed, exposed bool) Feature {
	service := "knowledge.toolbox.knowledge.v1.KnowledgeService"
	return Feature{
		ID:      service + "/" + method,
		Service: service,
		Method:  method,
		// The tool name is qualified by the owner, so a bare contract name alone would
		// not identify the operation either.
		ToolName: "knowledge__" + strings.ToLower(method),
		Allowed:  allowed,
		Exposed:  exposed,
	}
}

// apiToolsFeature is a second API serving its own contract, so the test can tell the two
// apart and can also make the ambiguous case on purpose.
func apiToolsFeature(method string) Feature {
	service := "apitools.toolbox.apitools.v1.ApiToolsService"
	return Feature{
		ID:       service + "/" + method,
		Service:  service,
		Method:   method,
		ToolName: "api_tools__" + strings.ToLower(method),
		Allowed:  true,
		Exposed:  true,
	}
}

// A caller naming the contract gets the feature. This is the reference form a client
// actually holds, from list_services.
func TestAFeatureIsFoundByItsContractAndMethod(t *testing.T) {
	server := testServerWithFeatures(t, knowledgeFeature("PutSource", true, true))

	entry, err := server.lookupFeature("toolbox.knowledge.v1.KnowledgeService/PutSource")
	require.NoError(t, err, "a caller holding a contract name can name an operation with it")
	assert.Equal(t, "PutSource", entry.feature.Method)
}

// The qualified form still works, because that is what the catalog hands out and a client
// may have stored one.
func TestAFeatureIsFoundByItsQualifiedReference(t *testing.T) {
	server := testServerWithFeatures(t, knowledgeFeature("PutSource", true, true))

	entry, err := server.lookupFeature("knowledge.toolbox.knowledge.v1.KnowledgeService/PutSource")
	require.NoError(t, err)
	assert.Equal(t, "PutSource", entry.feature.Method)
}

// A generated tool name identifies a feature too, and did before this change; it must keep
// working, because it is the name a client sees in tools/list.
func TestAFeatureIsFoundByItsToolName(t *testing.T) {
	server := testServerWithFeatures(t, knowledgeFeature("PutSource", true, true))

	entry, err := server.lookupFeature("knowledge__putsource")
	require.NoError(t, err)
	assert.Equal(t, "PutSource", entry.feature.Method)
}

// A name that matches nothing says so, and says what it looked for.
func TestAFeatureThatDoesNotExistIsRefused(t *testing.T) {
	server := testServerWithFeatures(t, knowledgeFeature("PutSource", true, true))

	_, err := server.lookupFeature("toolbox.knowledge.v1.KnowledgeService/DeleteEverything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DeleteEverything")

	_, err = server.lookupFeature("toolbox.other.v1.OtherService/Thing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OtherService")
}

// A method named in the wrong case still finds the feature, because a client reading a
// method name off a list and retyping it should not have to match the generator's casing.
func TestAMethodNameIsMatchedWithoutRegardToCase(t *testing.T) {
	server := testServerWithFeatures(t, knowledgeFeature("PutSource", true, true))

	entry, err := server.lookupFeature("toolbox.knowledge.v1.KnowledgeService/putsource")
	require.NoError(t, err)
	assert.Equal(t, "PutSource", entry.feature.Method)
}

// Two APIs serving one contract is the design, so a bare contract name is ambiguous. The
// reference is refused with the candidates rather than resolved to whichever was added
// first: a caller who meant one provider and got another would be reading another
// deployment's answers and would have no way to tell.
func TestAnAmbiguousContractIsRefusedWithItsCandidates(t *testing.T) {
	first := knowledgeFeature("PutSource", true, true)
	second := first
	second.Service = "other.toolbox.knowledge.v1.KnowledgeService"
	second.ID = second.Service + "/PutSource"
	second.ToolName = "other__putsource"
	server := testServerWithFeatures(t, first, second)

	_, err := server.lookupFeature("toolbox.knowledge.v1.KnowledgeService/PutSource")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 features", "the refusal says how many matched")
	assert.Contains(t, err.Error(), "knowledge.toolbox.knowledge.v1.KnowledgeService/PutSource")
	assert.Contains(t, err.Error(), "other.toolbox.knowledge.v1.KnowledgeService/PutSource",
		"and names them, because the caller's next move is to pick one")
}

// The qualified form disambiguates, so a caller who was told there are two can make the call.
func TestTheQualifiedReferenceDisambiguates(t *testing.T) {
	first := knowledgeFeature("PutSource", true, true)
	second := first
	second.Service = "other.toolbox.knowledge.v1.KnowledgeService"
	second.ID = second.Service + "/PutSource"
	second.ToolName = "other__putsource"
	server := testServerWithFeatures(t, first, second)

	entry, err := server.lookupFeature("other.toolbox.knowledge.v1.KnowledgeService/PutSource")
	require.NoError(t, err)
	assert.Equal(t, "other__putsource", entry.feature.ToolName)
}

// A suffix match is a match on a whole segment, so a caller may name the trailing segment
// on its own — which is how a contract is usually written down — while half a word is not a
// match at all. Without that, "Service" would match "KnowledgeService" and a caller who
// named half a word would be served an operation from a different contract.
func TestAServiceNameIsMatchedOnWholeSegmentsOnly(t *testing.T) {
	const stored = "knowledge.toolbox.knowledge.v1.KnowledgeService"

	assert.True(t, matchesService(stored, "toolbox.knowledge.v1.KnowledgeService"),
		"the qualified name without the API prefix")
	assert.True(t, matchesService(stored, "KnowledgeService"),
		"the trailing segment on its own, which is how a contract is written down")
	assert.True(t, matchesService(stored, stored), "an exact match")

	assert.False(t, matchesService(stored, "Service"), "half a segment is not a match")
	assert.False(t, matchesService(stored, "Knowledge"), "and neither is a prefix of one")
	assert.False(t, matchesService(stored, ""), "an empty name matches nothing")
	assert.False(t, matchesService("", stored), "and an empty stored name matches nothing")
}

// The exposure filter accepts what its own description says it accepts. It previously
// compared for equality against the API-qualified name, so the documented form matched
// nothing and the report came back all zeros.
func TestTheExposureFilterAcceptsTheNameItDocuments(t *testing.T) {
	server := testServerWithFeatures(t,
		knowledgeFeature("Search", true, true),
		knowledgeFeature("PutSource", true, true),
		apiToolsFeature("ListApis"),
	)

	for _, name := range []string{
		"toolbox.knowledge.v1.KnowledgeService",
		"knowledge.toolbox.knowledge.v1.KnowledgeService",
	} {
		t.Run(name, func(t *testing.T) {
			features := filterFeaturesByService(server.Features(), name)
			assert.Len(t, features, 2, "both knowledge features and nothing else")
		})
	}

	assert.Len(t, filterFeaturesByService(server.Features(), "toolbox.apitools.v1.ApiToolsService"), 1)
	assert.Empty(t, filterFeaturesByService(server.Features(), "toolbox.absent.v1.AbsentService"),
		"a service nothing serves reports nothing, which is true")
}

// The filter and the reference resolver agree on what a service name is. Two
// implementations of one rule would agree until one changed, and a client filtering by one
// and naming by the other would be told a feature existed and then that it did not.
func TestTheFilterAndTheResolverAgreeOnAServiceName(t *testing.T) {
	feature := knowledgeFeature("Search", true, true)
	server := testServerWithFeatures(t, feature)

	for _, name := range []string{
		feature.Service,
		"toolbox.knowledge.v1.KnowledgeService",
	} {
		assert.NotEmpty(t, filterFeaturesByService(server.Features(), name),
			"the filter accepts %q", name)
		_, err := server.lookupFeature(name + "/Search")
		assert.NoError(t, err, "the resolver accepts %q", name)
	}
}
