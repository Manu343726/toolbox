package knowledge_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Manu343726/toolbox/pkg/knowledge"
)

func identity() knowledge.OwnershipIdentity {
	return knowledge.OwnershipIdentity{
		BackendOrigin: "http://127.0.0.1:8888",
		BaseID:        "project",
		CorpusRoot:    "/srv/docs",
		Namespace:     "docs",
	}
}

func TestOwnershipRecordRoundTrips(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rec", "ownership.json")
	saved := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	want := &knowledge.OwnershipRecord{
		Version:  1,
		Identity: identity(),
		Files: map[string]knowledge.OwnershipEntry{
			"docs:runbooks/restore.md": {
				ID: "wiki:restore", DeclaredID: true, Root: "docs", Path: "runbooks/restore.md",
				Digest: "sha256:abc", ModTime: saved, Size: 12,
				SourceCommit: "deadbeef", ReconciledAt: saved, Title: "Restore",
				Tags: []string{"toolbox:origin=authored", "vault:docs"},
			},
		},
		LastCommit: "deadbeef", LastReconciledAt: saved,
	}
	require.NoError(t, knowledge.SaveOwnership(path, want))

	got, err := knowledge.LoadOwnership(path, identity())
	require.NoError(t, err)
	assert.Equal(t, want.Files, got.Files, "a record that loses a field loses the ability to describe what it owns")
	assert.Equal(t, "deadbeef", got.LastCommit)
	assert.Empty(t, got.Degraded)
}

func TestAMissingRecordIsAnEmptyRecordAndNotAnError(t *testing.T) {
	t.Parallel()

	rec, err := knowledge.LoadOwnership(filepath.Join(t.TempDir(), "absent.json"), identity())
	require.NoError(t, err, "a lost record costs a full re-ingest, which the digest filter makes cheap, and nothing has gone wrong that a person has to fix")
	assert.Empty(t, rec.Files)
	assert.Equal(t, identity(), rec.Identity)
	ok, why := rec.CanPrune()
	assert.True(t, ok)
	assert.Empty(t, why)
}

func TestARecordForAnotherDestinationIsRefusedAndNamesEveryDifference(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ownership.json")
	require.NoError(t, knowledge.SaveOwnership(path, &knowledge.OwnershipRecord{
		Version: 1, Identity: identity(), Files: map[string]knowledge.OwnershipEntry{},
	}))

	other := identity()
	other.BaseID = "another"
	other.Namespace = "other"
	other.CorpusRoot = "/elsewhere"

	_, err := knowledge.LoadOwnership(path, other)
	require.Error(t, err)

	var idErr *knowledge.IdentityError
	require.ErrorAs(t, err, &idErr)
	assert.Equal(t, "mismatch", idErr.Reason)
	assert.Len(t, idErr.Differences, 3, "a caller that fixes one field and re-runs should not have to discover the next by iteration")
	for _, d := range idErr.Differences {
		assert.NotEmpty(t, d.Have)
		assert.NotEmpty(t, d.Want)
		assert.Contains(t, err.Error(), d.Field, "the refusal names the field")
		assert.Contains(t, err.Error(), d.Have)
		assert.Contains(t, err.Error(), d.Want)
	}
	assert.Contains(t, err.Error(), "deleting documents in a base this reconciler never wrote to",
		"reusing a record across destinations is what authorises deleting documents in a base this reconciler never wrote to")
}

func TestSaveOwnershipRefusesToWriteAnUnboundRecord(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ownership.json")
	err := knowledge.SaveOwnership(path, &knowledge.OwnershipRecord{
		Files: map[string]knowledge.OwnershipEntry{},
	})
	require.Error(t, err,
		"writing a record that does not say where it was built recreates the exact state a later load has to refuse")
	assert.Contains(t, err.Error(), "no destination binding")
	assert.Contains(t, err.Error(), "backend_origin")
	assert.Contains(t, err.Error(), "base_id")

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "nothing is written, so the failure surfaces here rather than on the next run")
}

func TestSaveOwnershipCanonicalisesTheOriginItStores(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ownership.json")
	id := identity()
	id.BackendOrigin = "https://user:pw@host:443/v1/default?x=1"

	require.NoError(t, knowledge.SaveOwnership(path, &knowledge.OwnershipRecord{Identity: id}))

	// A caller passing the short form must not be refused as a mismatch by its
	// own earlier record.
	short := identity()
	short.BackendOrigin = "https://host"
	_, err := knowledge.LoadOwnership(path, short)
	require.NoError(t, err)
}

func TestARecordWithNoBindingIsRefusedRatherThanAdopted(t *testing.T) {
	t.Parallel()

	// The shape written before target binding existed: no identity at all.
	path := filepath.Join(t.TempDir(), "ownership.json")
	legacy := map[string]any{
		"syncIndex":  map[string]any{"a.md": map[string]any{"hash": "sha256:1"}},
		"lastSyncAt": "2026-01-01T00:00:00.000Z",
	}
	blob, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, blob, 0o644))

	_, err = knowledge.LoadOwnership(path, identity())
	require.Error(t, err)
	var idErr *knowledge.IdentityError
	require.ErrorAs(t, err, &idErr)
	assert.Equal(t, "predates_binding", idErr.Reason)
	assert.Contains(t, err.Error(), "predates target binding")
	assert.Contains(t, err.Error(), "delete it to start a fresh record")
}

func TestACorruptRecordDegradesAndNamesWhatIsNoLongerPossible(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ownership.json")
	require.NoError(t, os.WriteFile(path, []byte("{ this is not json"), 0o644))

	rec, err := knowledge.LoadOwnership(path, identity())
	require.NoError(t, err, "crashing here would leave a deployment unable to read its own documentation")
	assert.Empty(t, rec.Files)

	ok, why := rec.CanPrune()
	assert.False(t, ok, "claiming a working record would let a prune delete content it has no record of having created")
	assert.Contains(t, why, "orphaned documents are NOT pruned")
	assert.Contains(t, why, "re-ingested")
}

func TestScopeIsDeliberatelyNotPartOfTheBinding(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ownership.json")
	require.NoError(t, knowledge.SaveOwnership(path, &knowledge.OwnershipRecord{
		Version: 1, Identity: identity(), Files: map[string]knowledge.OwnershipEntry{},
	}))

	// Two reconciles over the same destination with different scope reuse the
	// record, because narrowing scope legitimately means "these are not mine".
	rec, err := knowledge.LoadOwnership(path, identity())
	require.NoError(t, err)
	assert.NotNil(t, rec, "narrowing the scope of a reconcile over the same destination is not a different destination")
}

func TestSaveOwnershipIsAtomicAndLeavesNoTemporaryFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "ownership.json")
	require.NoError(t, knowledge.SaveOwnership(path, &knowledge.OwnershipRecord{
		Version: 1, Identity: identity(), Files: map[string]knowledge.OwnershipEntry{},
	}))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a truncated ownership record is a deployment that cannot tell what it owns")
	assert.Equal(t, "ownership.json", entries[0].Name())

	// Overwriting an existing record leaves exactly one file too.
	require.NoError(t, knowledge.SaveOwnership(path, &knowledge.OwnershipRecord{
		Version: 1, Identity: identity(), Files: map[string]knowledge.OwnershipEntry{"a": {ID: "a"}},
	}))
	entries, err = os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestSaveOwnershipClearsDegradedAndStampsTheVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ownership.json")
	rec := &knowledge.OwnershipRecord{Identity: identity(), Degraded: "something went wrong last time"}
	require.NoError(t, knowledge.SaveOwnership(path, rec))
	assert.Empty(t, rec.Degraded, "a record that saved successfully is no longer degraded")

	got, err := knowledge.LoadOwnership(path, identity())
	require.NoError(t, err)
	ok, _ := got.CanPrune()
	assert.True(t, ok)
}

func TestCanonicalBackendOriginStripsEverythingButSchemeHostAndPort(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"https://user:pw@host:8888/v1/default?x=1": "https://host:8888",
		"https://host/v1/default":                  "https://host",
		"https://host:443/":                        "https://host",
		"http://host:80/":                          "http://host",
		"HTTPS://Host.Example.COM":                 "https://host.example.com",
		"  https://host  ":                         "https://host",
		// A default port is not part of an origin's identity.
		"https://host:443/v1": "https://host",
		// A non-default port is.
		"https://host:8443/v1": "https://host:8443",
	}
	for in, want := range tests {
		assert.Equal(t, want, knowledge.CanonicalBackendOrigin(in), "input %q", in)
	}
	assert.Empty(t, knowledge.CanonicalBackendOrigin("not a url"),
		"there is no safe canonical form for this, and storing it verbatim would bind the record to whatever the caller happened to type")
	assert.Empty(t, knowledge.CanonicalBackendOrigin(""))
}

func TestDefaultRecordPathSeparatesDestinations(t *testing.T) {
	t.Parallel()

	a := identity()
	b := identity()
	b.BaseID = "other"

	assert.NotEqual(t, knowledge.DefaultRecordPath("/var/lib", a), knowledge.DefaultRecordPath("/var/lib", b),
		"the refusal is the real safety property; the fingerprint is what stops it being the everyday outcome")

	same := knowledge.Fingerprint(a)
	assert.Equal(t, same, knowledge.Fingerprint(a), "a fingerprint is stable, or every run would look for a different file")
	assert.Len(t, same, 12)
	assert.NotEqual(t, same, knowledge.Fingerprint(b))
}

func TestOwnedIDsAreDeduplicatedAndSorted(t *testing.T) {
	t.Parallel()

	rec := &knowledge.OwnershipRecord{Files: map[string]knowledge.OwnershipEntry{
		"b": {ID: "z"},
		"a": {ID: "a"},
		"c": {ID: "z"},
	}}
	assert.Equal(t, []string{"a", "z"}, rec.OwnedIDs(),
		"two locations claiming one identifier is a corpus problem, and a prune should act on the identifier once")
}
