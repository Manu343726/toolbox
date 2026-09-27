package knowledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OwnershipIdentity is the destination an ownership record is valid for.
//
// Every field here changes what the record's contents *mean*, which is why they
// are all bound. A record is a statement of the form "these files, with these
// digests, have been reconciled into that base"; if any of these four changes,
// the statement is about a different place and reusing it would be answering a
// question nobody asked.
//
// The scope of a walk — include and exclude folders — is deliberately NOT part
// of this. Narrowing the scope of a reconcile over the same destination
// legitimately means "these files are no longer mine", and pruning them is the
// correct behaviour. Every harm the binding prevents requires the destination to
// have changed, so binding scope would refuse a legitimate operation in order to
// prevent an illegitimate one that binding scope does not actually prevent.
type OwnershipIdentity struct {
	// BackendOrigin is the API origin the base was written to. Credentials,
	// path and query are stripped: an ownership record is a file on disk and must
	// not hold a secret.
	BackendOrigin string `json:"backend_origin"`
	// BaseID is the base within that origin.
	BaseID string `json:"base_id"`
	// CorpusRoot is the absolute path of the corpus root this record covers.
	// A record from one clone of a repository does not describe another clone.
	CorpusRoot string `json:"corpus_root"`
	// Namespace is the identifier namespace the record's entries were built with.
	// Changing it renumbers every identity, so it is bound rather than inferred.
	Namespace string `json:"namespace"`
}

// CanonicalBackendOrigin reduces an API URL to the part an ownership record may
// legitimately store: scheme, host and port.
//
// Everything else is dropped — userinfo, because it is a credential; path and
// query, because they are part of how one deployment addresses a base rather
// than of which deployment it is. Two records that differ only in a trailing
// slash or a default-port spelling are the same record.
func CanonicalBackendOrigin(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	scheme, rest, found := strings.Cut(s, "://")
	if !found {
		// Not a URL with an authority. There is no safe canonical form for this,
		// and storing it verbatim would bind the record to whatever the caller
		// happened to type.
		return ""
	}
	authority := rest
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	lower := strings.ToLower(scheme)
	// A default port is not part of the origin's identity.
	switch {
	case lower == "http" && strings.HasSuffix(authority, ":80"):
		authority = strings.TrimSuffix(authority, ":80")
	case lower == "https" && strings.HasSuffix(authority, ":443"):
		authority = strings.TrimSuffix(authority, ":443")
	}
	return lower + "://" + strings.ToLower(authority)
}

// OwnershipEntry is one file the reconciler owns, and what it last put in the
// base for it.
type OwnershipEntry struct {
	// ID is the document identifier this file was ingested as.
	ID string `json:"id"`
	// DeclaredID reports whether the file declared that identity in its
	// frontmatter. It is what makes a move possible to distinguish from a delete.
	DeclaredID bool `json:"declared_id,omitempty"`
	// Root and Path locate the file in the corpus, and together form the map key.
	Root string `json:"root"`
	Path string `json:"path"`
	// Digest is the content digest that was ingested.
	Digest string `json:"digest"`
	// ModTime and Size are the file's own at ingest time, and are what the next
	// walk's cheap filter compares. Neither is identity.
	ModTime time.Time `json:"mod_time"`
	Size    int64     `json:"size"`
	// SourceCommit is the commit this version was reconciled from.
	SourceCommit string `json:"source_commit,omitempty"`
	// ReconciledAt is when it was ingested.
	ReconciledAt time.Time `json:"reconciled_at"`
	// Binary records that the entry is not text, so that a later walk can skip it
	// without reading it and still know.
	Binary bool `json:"binary,omitempty"`
	// Title, Front and Tags are carried so that an unchanged file's display
	// title, declared metadata and scope survive a walk that skipped the read,
	// and so that a plan can show what a file it is about to delete was ingested
	// with.
	Title string      `json:"title,omitempty"`
	Front Frontmatter `json:"front,omitempty"`
	Tags  []string    `json:"tags,omitempty"`
}

// OwnershipRecord is the record of what one reconcile owns in one base.
//
// It is the input to a prune and nothing else. A prune walks this record; it
// does not list the base, because listing a base also returns content nobody
// owns — a retained conversation, a second corpus reconciled by somebody else —
// and a reconciler that deleted those is indistinguishable from data loss.
type OwnershipRecord struct {
	// Version is the record format. A record with no version predates target
	// binding and is refused rather than adopted.
	Version int `json:"version"`
	// Identity is the destination this record is valid for.
	Identity OwnershipIdentity `json:"identity"`
	// Files maps a source key — root name and relative path — to what this
	// reconciler ingested for it.
	Files map[string]OwnershipEntry `json:"files"`
	// LastCommit is the commit the most recent successful reconcile used.
	LastCommit string `json:"last_commit,omitempty"`
	// LastReconciledAt is when that was.
	LastReconciledAt time.Time `json:"last_reconciled_at,omitempty"`
	// Degraded names a capability that is not currently available, and why.
	// It is set when the record could not be loaded cleanly, and it is what stops
	// a lost record from quietly becoming a lost ability to prune.
	Degraded string `json:"degraded,omitempty"`
}

// ownershipVersion is the only version this package writes.
const ownershipVersion = 1

// IdentityError is a refusal to use an ownership record for the destination the
// caller asked for.
//
// It is a distinct type because the two refusals have different fixes, and a
// caller that cannot tell them apart will pick the wrong one: a record for
// another destination needs a new file, and a legacy record needs upgrading.
type IdentityError struct {
	// Reason is "mismatch" or "predates_binding".
	Reason string
	// Message names what differs and what to do.
	Message string
	// Differences lists the fields that moved, with both values.
	Differences []FieldDifference
}

// FieldDifference is one field of an identity that did not match.
type FieldDifference struct {
	Field string
	Have  string
	Want  string
}

// Error implements error.
func (e *IdentityError) Error() string { return e.Message }

// identityFieldNames is the fixed set, in a fixed order, so that a refusal
// always names its differences in the same sequence and a test can assert on it.
var identityFieldNames = []string{"backend_origin", "base_id", "corpus_root", "namespace"}

// LoadOwnership reads an ownership record for the given destination.
//
// Four outcomes, and the difference between them is the whole design:
//
//   - No file: an empty record bound to want, and no error. A lost record costs
//     a full re-ingest, which the digest filter makes cheap, and it is not an
//     error because nothing has gone wrong that a person has to fix.
//   - A record whose identity matches: the record.
//   - A record whose identity differs, or which has no identity at all: an
//     IdentityError naming every field that moved. Never a repair, never a
//     merge, and never a silent adoption — the same project shipped that bug as
//     a data-deletion defect and fixed it by refusing.
//   - A record that does not parse: an empty record, no error, and a Degraded
//     note saying that orphan pruning cannot run until the record is rebuilt.
//     Crashing here would leave a deployment unable to read its own
//     documentation; claiming a working record would let it delete content.
func LoadOwnership(path string, want OwnershipIdentity) (*OwnershipRecord, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &OwnershipRecord{Version: ownershipVersion, Identity: want, Files: map[string]OwnershipEntry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("knowledge: reading the ownership record %s: %w", path, err)
	}

	// The identity is read before anything else, so that a record which cannot be
	// used is refused before any of its contents are believed.
	var envelope struct {
		Version  *int               `json:"version"`
		Identity *OwnershipIdentity `json:"identity"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return degradedRecord(want, path,
			fmt.Sprintf("the ownership record at %s could not be parsed (%v), so this reconcile starts from empty: every file will be re-ingested, and orphaned documents are NOT pruned until the record is rebuilt", path, err)), nil
	}
	if envelope.Identity == nil {
		return nil, &IdentityError{
			Reason: "predates_binding",
			Message: fmt.Sprintf(
				"the ownership record at %s predates target binding and cannot be safely reused, because it does not record which backend, base, corpus root and identifier namespace it was built for; "+
					"delete it to start a fresh record (every file will be re-ingested), or point the configuration at the record that does have one", path),
		}
	}
	if diffs := DiffOwnershipIdentity(*envelope.Identity, want); len(diffs) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "the ownership record at %s was built for a different destination and is refused rather than reused, because reusing it would mark another target's files as already reconciled and authorise deleting documents in a base this reconciler never wrote to", path)
		for _, d := range diffs {
			fmt.Fprintf(&b, "\n  %s: record has %q, this reconcile wants %q", d.Field, d.Have, d.Want)
		}
		b.WriteString("\nUse a separate record per destination, or delete this one to start fresh for this destination.")
		return nil, &IdentityError{Reason: "mismatch", Message: b.String(), Differences: diffs}
	}

	rec := &OwnershipRecord{Version: ownershipVersion, Identity: want, Files: map[string]OwnershipEntry{}}
	var full struct {
		Version          int                       `json:"version"`
		Identity         OwnershipIdentity         `json:"identity"`
		Files            map[string]OwnershipEntry `json:"files"`
		LastCommit       string                    `json:"last_commit"`
		LastReconciledAt time.Time                 `json:"last_reconciled_at"`
	}
	if err := json.Unmarshal(raw, &full); err != nil {
		return degradedRecord(want, path,
			fmt.Sprintf("the ownership record at %s parsed far enough to check its destination but not far enough to use (%v), so this reconcile starts from empty: every file will be re-ingested, and orphaned documents are NOT pruned until the record is rebuilt", path, err)), nil
	}
	if full.Files != nil {
		rec.Files = full.Files
	}
	rec.LastCommit = full.LastCommit
	rec.LastReconciledAt = full.LastReconciledAt
	if full.Version != ownershipVersion {
		// A newer record is not silently downgraded: its contents may mean
		// something this version does not know, and pruning from a
		// misunderstood record is the failure this whole type exists to prevent.
		return nil, &IdentityError{
			Reason: "version",
			Message: fmt.Sprintf("the ownership record at %s is version %d and this build writes version %d; "+
				"an unreadable record must not be used to decide what to delete", path, full.Version, ownershipVersion),
		}
	}
	return rec, nil
}

func degradedRecord(want OwnershipIdentity, path, note string) *OwnershipRecord {
	return &OwnershipRecord{
		Version:  ownershipVersion,
		Identity: want,
		Files:    map[string]OwnershipEntry{},
		Degraded: note,
	}
}

// DiffOwnershipIdentity returns every field in which two identities differ, in a
// fixed order. A refusal names all of them, because a caller that fixes one and
// re-runs should not have to discover the next one by iteration.
func DiffOwnershipIdentity(have, want OwnershipIdentity) []FieldDifference {
	values := func(i OwnershipIdentity) map[string]string {
		return map[string]string{
			"backend_origin": i.BackendOrigin,
			"base_id":        i.BaseID,
			"corpus_root":    i.CorpusRoot,
			"namespace":      i.Namespace,
		}
	}
	h, w := values(have), values(want)
	var diffs []FieldDifference
	for _, name := range identityFieldNames {
		if h[name] != w[name] {
			diffs = append(diffs, FieldDifference{Field: name, Have: h[name], Want: w[name]})
		}
	}
	return diffs
}

// SaveOwnership writes the record atomically.
//
// The write goes to a temporary file in the same directory and is renamed over
// the target, so an interrupted write leaves the previous record intact rather
// than a half-written one. A truncated ownership record is a deployment that
// cannot tell what it owns.
func SaveOwnership(path string, rec *OwnershipRecord) error {
	if rec.Files == nil {
		rec.Files = map[string]OwnershipEntry{}
	}
	rec.Version = ownershipVersion
	rec.Degraded = ""

	// A record with no binding is refused rather than written, and this is the
	// same rule LoadOwnership applies in the other direction. Writing one would
	// recreate the exact state — a record that does not say where it was built —
	// that a later load has to refuse, so the file would be written only to be
	// unusable, and the failure would surface on the next run rather than here.
	var missing []string
	if rec.Identity.BackendOrigin == "" {
		missing = append(missing, "backend_origin")
	}
	if rec.Identity.BaseID == "" {
		missing = append(missing, "base_id")
	}
	if rec.Identity.CorpusRoot == "" {
		missing = append(missing, "corpus_root")
	}
	if rec.Identity.Namespace == "" {
		missing = append(missing, "namespace")
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"knowledge: refusing to write an ownership record with no destination binding; %s would be missing, and a record that does not say where it was built cannot be used to decide what to delete",
			strings.Join(missing, ", "))
	}
	// The origin is stored canonicalised, so that a caller passing
	// "https://host:443/v1" and one passing "https://host" write the same record
	// and are not refused as a mismatch by the next run.
	rec.Identity.BackendOrigin = CanonicalBackendOrigin(rec.Identity.BackendOrigin)
	if rec.Identity.BackendOrigin == "" {
		return fmt.Errorf("knowledge: refusing to write an ownership record whose backend origin %q has no canonical form", rec.Identity.BackendOrigin)
	}

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("knowledge: encoding the ownership record: %w", err)
	}
	blob = append(blob, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("knowledge: creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".ownership-*.tmp")
	if err != nil {
		return fmt.Errorf("knowledge: creating a temporary ownership record in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(blob); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("knowledge: writing the ownership record: %w", err)
	}
	// The record is a file a person may need to read and reason about, and it must
	// never be world-writable even when the directory is.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("knowledge: setting ownership record permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("knowledge: flushing the ownership record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("knowledge: closing the ownership record: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("knowledge: replacing the ownership record: %w", err)
	}
	return nil
}

// OwnedIDs returns the identifiers this record owns, sorted.
//
// A prune uses this and nothing else. It is deliberately a walk of the record
// rather than a listing of the base, and the doc comment on the type says why.
func (r *OwnershipRecord) OwnedIDs() []string {
	ids := make([]string, 0, len(r.Files))
	seen := make(map[string]struct{}, len(r.Files))
	for _, e := range r.Files {
		if e.ID == "" {
			continue
		}
		if _, dup := seen[e.ID]; dup {
			continue
		}
		seen[e.ID] = struct{}{}
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}

// EntryFor returns the recorded entry for a source key.
func (r *OwnershipRecord) EntryFor(key string) (OwnershipEntry, bool) {
	e, ok := r.Files[key]
	return e, ok
}

// EntryForID returns the recorded entry for an identifier, and is what a prune
// consults to discover that an owned document is no longer on disk.
func (r *OwnershipRecord) EntryForID(id string) (OwnershipEntry, bool) {
	for _, e := range r.Files {
		if e.ID == id {
			return e, true
		}
	}
	return OwnershipEntry{}, false
}

// CanPrune reports whether this record is safe to prune from, and says why not
// when it is not. A caller that ignores this and prunes anyway will delete
// documents it has no record of having created.
func (r *OwnershipRecord) CanPrune() (bool, string) {
	if r.Degraded != "" {
		return false, r.Degraded
	}
	return true, ""
}

// Fingerprint returns a short stable identifier for an identity, suitable for
// putting in a file name.
//
// It exists so that two destinations get two default record files rather than
// one file that both of them refuse. The refusal is the real safety property;
// the fingerprint is what stops it being the everyday outcome.
func Fingerprint(id OwnershipIdentity) string {
	d := Digest([]byte(id.BackendOrigin + "\x00" + id.BaseID + "\x00" + id.CorpusRoot + "\x00" + id.Namespace))
	hexPart, ok := ParseDigest(d)
	if !ok {
		// Unreachable: Digest always produces a well-formed value. Returning a
		// fixed string would collide every identity, so a distinct fallback is
		// better than a plausible one.
		return "000000000000"
	}
	return hexPart[:12]
}

// DefaultRecordPath returns where a record for this identity belongs, given a
// directory. The file name carries a sanitised base and namespace alongside the
// fingerprint, so that a person looking at the directory can tell which record
// is which without opening any of them.
func DefaultRecordPath(dir string, id OwnershipIdentity) string {
	name := sanitizeForFilename(id.BaseID)
	if id.Namespace != "" && id.Namespace != name {
		name += "-" + sanitizeForFilename(id.Namespace)
	}
	return filepath.Join(dir, name+"-"+Fingerprint(id)+".ownership.json")
}

func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "base"
	}
	return b.String()
}
