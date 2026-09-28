package main

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	apimodel "github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/config"
	apitoolsv1 "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1"
	apitoolsv1connect "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1/apitoolsv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The default policy makes a claim in two places — the flag's help text and the document embedded
// at `cmd/toolbox/policy/toolbox.policy` — and neither had a test behind it. The claim is:
//
//	every read is permitted, and nothing that changes state is.
//
// It is a claim about *every* operation in the deployment, so it is asserted over every operation
// in the deployment rather than over a chosen few. A deployment that adds a subsystem cannot
// quietly fail the promise its own policy document makes, and a subsystem whose `@toolbox.
// side-effects` annotation is dropped or mistyped is caught here rather than by an agent finding
// out that the operation it needs is missing.
//
// It is stated as an implication in both directions because each one is a distinct mistake:
//
//   - a read that is denied is a deployment where an operation exists and cannot be called;
//   - a write that is permitted is a deployment where the default has granted authority nobody
//     chose to grant it, which the policy document calls out as the outcome to avoid.
func TestTheDefaultPolicyGrantsEveryReadAndNothingThatChangesState(t *testing.T) {
	resolved, err := config.New(config.Options{WorkDir: t.TempDir()}).Resolve("")
	require.NoError(t, err)

	h, catalog, err := buildHost(hostComposition{config: resolved})
	require.NoError(t, err)
	// Every subsystem, because the claim is about the deployment and not about a subset of it.
	require.NoError(t, h.Select())
	require.NoError(t, h.Start(context.Background()))
	defer func() { require.NoError(t, h.Shutdown(context.Background())) }()

	seed, err := catalog.registerSubsystems(context.Background(), false)
	require.NoError(t, err)
	require.NotEmpty(t, seed.Seeded)

	client := apitoolsv1connect.NewApiToolsServiceClient(
		http.DefaultClient, h.Servers()["apitools"].Endpoint())
	listed, err := client.ListApis(context.Background(), connect.NewRequest(&apitoolsv1.ListApisRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, listed.Msg.GetApis(),
		"the deployment registered APIs, or there is nothing for the policy to govern")

	var reads, writes, unclassified int
	for _, summary := range listed.Msg.GetApis() {
		got, err := client.GetApi(context.Background(), connect.NewRequest(&apitoolsv1.GetApiRequest{
			Id: summary.GetId(),
		}))
		require.NoError(t, err, "api %s", summary.GetId())
		for _, service := range got.Msg.GetApi().GetServices() {
			for _, operation := range service.GetOperations() {
				effects := operation.GetSideEffects()
				described, err := client.DescribeOperation(
					context.Background(), connect.NewRequest(&apitoolsv1.DescribeOperationRequest{
						ApiId:       summary.GetId(),
						OperationId: operation.GetId(),
					}))
				require.NoError(t, err, "%s", operation.GetId())
				allowed := described.Msg.GetAllowed()

				switch {
				case len(effects) == 0:
					// Unclassified is not a read. The default names `read` and says
					// nothing about this class, so it is not granted — which is the
					// safe direction and the reason the class exists.
					unclassified++
					assert.False(t, allowed,
						"%s declares no side effects, so it is unclassified, and the "+
							"default policy does not grant unclassified operations",
						operation.GetId())
				case apimodel.ReadOnly(effects):
					reads++
					assert.True(t, allowed,
						"%s declares %v, and the default policy grants every read, so a "+
							"deployment offering it and denying it is a policy that does "+
							"not do what its own document says",
						operation.GetId(), effects)
				default:
					writes++
					assert.False(t, allowed,
						"%s declares %v, and the default policy grants nothing that "+
							"changes state",
						operation.GetId(), effects)
				}
			}
		}
	}

	// The two counts are the test's own worthiness: a deployment that registered no
	// operations, or none of either class, would pass the loop above by asserting nothing.
	assert.Positive(t, reads, "the deployment has reads, and the default grants them")
	assert.Positive(t, writes, "and writes, which the default withholds")
	t.Logf("under the default policy: %d reads granted, %d writes withheld, %d unclassified withheld",
		reads, writes, unclassified)
}

// The git-backed catalog provider is the one subsystem whose operations are almost entirely
// state-changing, and it is the case that decides what the default policy's narrow half is for.
//
// Registering, creating, syncing, pushing, unregistering and deleting a catalog all change state
// that outlives the process: a deployment that forgot its catalogs on reboot would serve a
// different set of skills each time it came up, and a project's `skills:` list would then name
// references that resolve to nothing. So withholding them by default is correct, and the reason
// the policy document gives — that granting a write is a decision somebody has to make rather
// than a default somebody has to accept — is the whole of it.
//
// `ListCheckouts` is the exception that proves the rule rather than testing it. It declares
// `read_only` and changes nothing, so the default grants it. If it were withheld, the claim above
// would be false for a real operation in the shipped deployment, and this is the one that would
// catch it.
func TestTheCatalogProviderIsWithheldByDefaultAndItsOneReadIsNot(t *testing.T) {
	resolved, err := config.New(config.Options{WorkDir: t.TempDir()}).Resolve("")
	require.NoError(t, err)

	h, catalog, err := buildHost(hostComposition{config: resolved})
	require.NoError(t, err)
	require.NoError(t, h.Select("skillgit", "apitools", "policy"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { require.NoError(t, h.Shutdown(context.Background())) }()

	_, err = catalog.registerSubsystems(context.Background(), false)
	require.NoError(t, err)

	client := apitoolsv1connect.NewApiToolsServiceClient(
		http.DefaultClient, h.Servers()["apitools"].Endpoint())
	listed, err := client.ListApis(context.Background(), connect.NewRequest(&apitoolsv1.ListApisRequest{}))
	require.NoError(t, err)

	byMethod := map[string]bool{}
	for _, summary := range listed.Msg.GetApis() {
		got, err := client.GetApi(context.Background(), connect.NewRequest(&apitoolsv1.GetApiRequest{
			Id: summary.GetId(),
		}))
		require.NoError(t, err)
		for _, service := range got.Msg.GetApi().GetServices() {
			for _, operation := range service.GetOperations() {
				if service.GetName() != "toolbox.skillgit.v1.SkillGitService" {
					continue
				}
				described, err := client.DescribeOperation(
					context.Background(), connect.NewRequest(&apitoolsv1.DescribeOperationRequest{
						ApiId:       summary.GetId(),
						OperationId: operation.GetId(),
					}))
				require.NoError(t, err, "%s", operation.GetId())
				byMethod[operation.GetMethod()] = described.Msg.GetAllowed()
			}
		}
	}

	require.NotEmpty(t, byMethod, "the catalog provider registered its operations")
	for _, method := range []string{
		"RegisterCatalog", "CreateCatalog", "SyncCatalog",
		"PushCatalog", "UnregisterCatalog", "DeleteCatalog",
	} {
		assert.False(t, byMethod[method],
			"%s changes state that outlives the process, so the default withholds it", method)
	}
	assert.True(t, byMethod["ListCheckouts"],
		"and ListCheckouts declares read_only, so the default grants it: a deployment "+
			"that could not even be asked which catalogs it has would be withholding "+
			"information rather than authority")
}
