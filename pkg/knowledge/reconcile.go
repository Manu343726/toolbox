package knowledge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Plan is what a reconcile would do, before it does any of it.
//
// It is a value, not a side effect, and that is the whole point: reconciling a
// thousand files changes what every assistant in a deployment believes, and a
// person should see that happening before it happens. PlanReconcile reads the
// corpus and the base and writes nothing; ApplyReconcile takes a plan and a
// confirmation.
type Plan struct {
	// BaseID is the base this plan applies to.
	BaseID string
	// Commit is the revision of the corpus this plan was computed against. It is
	// recorded rather than being part of identity: a commit is a property of a
	// run, not of a document, and putting it in identity would mean every commit
	// re-extracts the whole corpus.
	Commit string
	// Created, Updated, Unchanged and Deleted are the four outcomes for every
	// file in scope, and between them they account for every file. A file that
	// is in none of them is a bug in this package, not an unclassified state.
	Created   []PlanEntry
	Updated   []PlanEntry
	Unchanged []PlanEntry
	Deleted   []PlanEntry
	// Moved is a file whose identity survived a change of location.
	Moved []PlanMove
	// Drift is derived knowledge the corpus contradicts.
	Drift []Drift
	// Warnings are conditions to surface that are not failures.
	Warnings []string
	// PlanDigest identifies this plan, so that a confirmation can be shown to
	// refer to the plan the person actually read rather than to a later one
	// computed over a directory that has since changed.
	PlanDigest string
	// UnreadFiles counts files the cheap filter skipped, so a caller can tell a
	// cheap plan from a thorough one.
	UnreadFiles int
}

// Empty reports whether the plan would change nothing.
func (p Plan) Empty() bool {
	return len(p.Created) == 0 && len(p.Updated) == 0 && len(p.Deleted) == 0 && len(p.Moved) == 0
}

// ChangedFiles returns the files this plan would create, update, delete or move,
// in a stable order, which is what an apply executes.
func (p Plan) ChangedFiles() []PlanEntry {
	all := make([]PlanEntry, 0, len(p.Created)+len(p.Updated)+len(p.Deleted)+len(p.Moved))
	all = append(all, p.Created...)
	all = append(all, p.Updated...)
	for _, m := range p.Moved {
		all = append(all, m.To)
	}
	all = append(all, p.Deleted...)
	sort.Slice(all, func(i, j int) bool {
		if all[i].Root != all[j].Root {
			return all[i].Root < all[j].Root
		}
		return all[i].Path < all[j].Path
	})
	return all
}

// PlanEntry is one file's fate in a plan.
type PlanEntry struct {
	// ID is the document identifier the file is or would be ingested as.
	ID string
	// DeclaredID reports whether that identity was declared in the file.
	DeclaredID bool
	// SourceKey locates the file in the ownership record.
	SourceKey string
	// Root and Path locate it in the corpus.
	Root string
	Path string
	// Digest is the content digest, and PriorDigest is what was ingested last
	// time. Equal digests mean unchanged; a plan that reported a change with
	// equal digests would be reporting a filesystem's opinion as a fact.
	Digest      string
	PriorDigest string
	// Size and ModTime are the file's own.
	Size    int64
	ModTime time.Time
	// Title is for the plan's human reader.
	Title string
	// Tags is what the file would be or was ingested with.
	Tags []string
	// Body is the markdown a person wrote, below the frontmatter. It is present
	// only when Read is true: a file the cheap filter skipped has a digest but no
	// body, and the applier reads it then rather than the planner reading every
	// file of an unchanged corpus on every run.
	Body string
	// EventTime is the value to ingest the document with — an RFC 3339 time the
	// author declared, or TimelessTimestamp when they declared none.
	EventTime string
	// Binary reports that the file goes through the backend's binary path.
	Binary bool
	// Read reports whether this plan has the file's bytes. A file the cheap
	// filter skipped has a digest but no body, and cannot be ingested until it
	// is read; a plan must not pretend otherwise.
	Read bool
	// Reason explains a deletion, because a deletion is the outcome a person
	// most needs explained.
	Reason string
}

// PriorDigest is what the ownership record had for this file, and it is the whole of how an apply
// tells a create from a replace: a create has none, a replace has one that differs. The two are
// kept in one field rather than as a boolean because a boolean would have to be set by whoever
// built the entry, and a plan that disagrees with itself about which files changed is a plan whose
// ingest counts are wrong in a way nothing else would notice.

// PlanMove is a file whose identity was declared and therefore survived a change
// of path.
//
// It is reported rather than executed as a delete and a create because those two
// things are not the same: re-ingesting under the same identifier preserves the
// facts extracted from the file, and every fact that referenced the old path
// stays valid. A corpus that gets reorganised without declared identities
// silently re-extracts everything and orphans every fact, which is why the
// reconciler says something when it sees a path-derived identity disappear.
type PlanMove struct {
	From PlanEntry
	To   PlanEntry
}

// Drift is a piece of derived knowledge the corpus appears to contradict.
//
// It is reported and never resolved. The corpus is the record of what people
// decided, and a model's belief about the same thing is a hypothesis about a
// document it has read; picking between them is a decision this subsystem is not
// positioned to make, and making it silently is how a base ends up confidently
// wrong.
type Drift struct {
	// ClaimID is the derived item's identifier.
	ClaimID string
	// ClaimText is what it says.
	ClaimText string
	// BackingContent lists the content identifiers the claim was derived from.
	BackingContent []string
	// ConflictContent lists the content the corpus now says, whose text does not
	// contain the claim.
	ConflictContent []string
	// Detection names how the conflict was spotted, so that a reader knows how
	// much weight to put on it. It is a heuristic and says so.
	Detection string
}

// driftDetection names the check that produced a Drift.
//
// It is a containment check, not a contradiction detector, and the difference
// matters: a claim whose wording differs from the file that produced it is
// ordinary consolidation, not a conflict. This reports candidates for a person to
// look at and explicitly does not claim the base is wrong.
const driftDetection = "claim text is not contained in the authored content that produced it"

// PlanOptions configures a plan.
type PlanOptions struct {
	// BaseID is the base being reconciled.
	BaseID string
	// Commit is the revision the corpus is at.
	Commit string
	// Owner is written into the ownership marker on ingested content.
	Owner string
	// Namespace is the identifier namespace, and must match the one the ownership
	// record was bound to.
	Namespace string
	// Prune enables the deletion half. A caller that only wants to know what is
	// new sets it false, and a plan with no deletions is what a first reconcile
	// over an existing base should produce so that a mistake cannot remove
	// anything.
	Prune bool
	// Derived is the set of derived claims to check the corpus against for drift.
	// It is optional: a reconcile that was given none reports no drift, rather
	// than reporting that there is none.
	Derived []DerivedClaim
	// FollowBinary includes attachments in the plan. It is off by default, and
	// the default is a decision rather than an omission: a corpus of a thousand
	// documents may hold a few thousand images, and none of them is prose. An
	// attachment is ingested as a byte string beside the chunk it belongs to
	// rather than extracted from, so including them all means paying for a large
	// number of documents nobody referenced.
	//
	// It is here because the walk already classified every file either way —
	// `File.Binary` is set whether or not the file is returned — so the only
	// question is whether the plan carries it, and putting the question at the
	// walk alone would have made every caller that wanted an attachment rewrite
	// the corpus's own options.
	FollowBinary bool
}

// DerivedClaim is one piece of derived knowledge, as far as drift detection is
// concerned.
type DerivedClaim struct {
	ID string
	// Text is what the claim says.
	Text string
	// SourceContentIDs are the content items the claim was consolidated from.
	SourceContentIDs []string
	// Origin is what produced it. Only derived claims are drift-checked; a claim
	// that came from a person contradicting a person's file is the corpus talking
	// to itself, which the frontmatter's own supersedes chain is for.
	Origin Origin
}

// Planner computes plans. It holds no state between calls, so one reconcile
// cannot influence the next.
type Planner struct {
	corpus *Corpus
}

// NewPlanner returns a planner for a corpus.
func NewPlanner(c *Corpus) *Planner { return &Planner{corpus: c} }

// Plan reads the corpus, compares it with the ownership record, and returns what
// a reconcile would do.
//
// It reads and never writes: not the base, and not the ownership record. The
// record is an input here and an output of ApplyReconcile, which is what makes a
// plan safe to compute over a directory that a person is actively editing.
func (p *Planner) Plan(ctx context.Context, rec *OwnershipRecord, opts PlanOptions) (Plan, error) {
	walk, err := p.corpus.Walk(ctx, WalkOptions{
		Prior:        rec,
		TrustModTime: true,
		FollowBinary: opts.FollowBinary,
	})
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{
		BaseID:      opts.BaseID,
		Commit:      opts.Commit,
		Warnings:    append([]string(nil), walk.Warnings...),
		UnreadFiles: walk.Unchanged,
	}

	seenKeys := make(map[string]struct{}, len(walk.Files))
	seenIDs := make(map[string]struct{}, len(walk.Files))
	// The record is keyed by source location, so a file whose identity is
	// declared and whose location changed appears in neither the record's key
	// space nor the walk's. The index by identifier is what finds it: same
	// identity, different location, which is a move — and the whole reason the
	// declared form exists.
	priorByID := make(map[string]OwnershipEntry, len(rec.Files))
	for _, prior := range rec.Files {
		if prior.ID != "" {
			if _, clash := priorByID[prior.ID]; !clash {
				priorByID[prior.ID] = prior
			}
		}
	}
	// Content digests of files that have gone away, so that a path-derived
	// identity which moves can still be recognised as a move rather than as an
	// unrelated new document.
	vacated := make(map[string][]OwnershipEntry)

	for _, f := range walk.Files {
		seenKeys[f.SourceKey] = struct{}{}
		if f.ID == "" {
			return Plan{}, fmt.Errorf("knowledge: %s has no identifier after parsing", f.SourceKey)
		}
		if _, dup := seenIDs[f.ID]; dup {
			// Two files claiming one identity is a corpus problem a person has to
			// resolve: there is no correct answer, and picking one would leave the
			// other's content permanently unreconciled with nobody noticing.
			return Plan{}, fmt.Errorf(
				"knowledge: two files claim the identifier %q (%s and %s); give them distinct `id` values, or remove one",
				f.ID, f.SourceKey, priorKeyForID(rec, f.ID))
		}
		seenIDs[f.ID] = struct{}{}

		tagOpts := TagOptions{RootName: f.Root, Owner: opts.Owner}
		entry := PlanEntry{
			ID:         f.ID,
			DeclaredID: f.DeclaredID,
			SourceKey:  f.SourceKey,
			Root:       f.Root,
			Path:       f.Path,
			Digest:     f.Digest,
			Size:       f.Size,
			ModTime:    f.ModTime,
			Title:      f.DerivedTitle(),
			Tags:       TagsFor(f, tagOpts),
			EventTime:  IngestTimestamp(f),
			Binary:     f.Binary,
			Read:       f.Read,
		}
		// A file the cheap filter skipped has no body, and that is recorded rather
		// than papered over: the applier reads it, because a plan that cannot be
		// applied is worse than a plan that costs one read to apply.
		if f.Read && !f.Binary {
			entry.Body = f.Body
		}

		prior, atSameKey := rec.EntryFor(f.SourceKey)
		switch {
		case atSameKey && prior.ID != f.ID:
			// The same path, a different declared identity. That is a rename of
			// identity, and the old identifier is now unowned.
			plan.Moved = append(plan.Moved, PlanMove{From: entryForPrior(prior), To: entry})
		case atSameKey:
			entry.PriorDigest = prior.Digest
			if prior.Digest == f.Digest {
				plan.Unchanged = append(plan.Unchanged, entry)
			} else {
				plan.Updated = append(plan.Updated, entry)
			}
		case declaredMove(f, priorByID):
			// The same identity at a new location.
			from := priorByID[f.ID]
			plan.Moved = append(plan.Moved, PlanMove{From: entryForPrior(from), To: entry})
		default:
			plan.Created = append(plan.Created, entry)
		}
	}

	// Whatever the record owns and the corpus no longer has.
	for key, prior := range rec.Files {
		if _, still := seenKeys[key]; still {
			continue
		}
		if _, still := seenIDs[prior.ID]; still {
			// The identity moved to a different file, which the loop above already
			// reported as a move. Deleting it now would undo the move.
			continue
		}
		if opts.Prune {
			plan.Deleted = append(plan.Deleted, entryForPrior(prior))
		}
		if prior.Digest != "" {
			vacated[prior.Digest] = append(vacated[prior.Digest], prior)
		}
	}

	// A move of a file that declares no identity.
	//
	// Without a declared id the identity is the path, so a move is
	// indistinguishable from a delete and a create — and both of those are
	// already reported correctly. What is not visible without this pass is
	// *why* they happened together, and a person who reorganised a tree needs to
	// be told, because the consequence is that every fact extracted from the file
	// has just been orphaned under a new identifier. The check is exact rather
	// than heuristic: identical content digests do not coincide by accident.
	for i := range plan.Created {
		candidates := vacated[plan.Created[i].Digest]
		if len(candidates) == 0 {
			continue
		}
		for _, prior := range candidates {
			to := plan.Created[i]
			plan.Moved = append(plan.Moved, PlanMove{From: entryForPrior(prior), To: to})
			// The new file is now accounted for as a move rather than a creation,
			// and its previous location is not a deletion.
			plan.Created = removeCreated(plan.Created, to.ID)
			plan.Deleted = removeDeleted(plan.Deleted, SourceKey(prior.Root, prior.Path))
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"%s moved from %s to %s and declares no `id` in its frontmatter, so its facts were re-extracted under a new identifier and everything that referenced the old one is now orphaned; add `id: %s` to the frontmatter to make the next move a move",
				prior.ID, prior.Path, to.Path, prior.ID))
			break
		}
	}

	plan.Drift = detectDrift(opts.Derived, walk.Files)

	plan.Created = sortEntries(plan.Created)
	plan.Updated = sortEntries(plan.Updated)
	plan.Unchanged = sortEntries(plan.Unchanged)
	plan.Deleted = sortEntries(plan.Deleted)
	sort.Slice(plan.Moved, func(i, j int) bool { return plan.Moved[i].To.Path < plan.Moved[j].To.Path })
	sort.Strings(plan.Warnings)
	plan.PlanDigest = plan.digest()
	return plan, nil
}

func entryForPrior(prior OwnershipEntry) PlanEntry {
	return PlanEntry{
		ID:          prior.ID,
		DeclaredID:  prior.DeclaredID,
		SourceKey:   SourceKey(prior.Root, prior.Path),
		Root:        prior.Root,
		Path:        prior.Path,
		Digest:      prior.Digest,
		PriorDigest: prior.Digest,
		Size:        prior.Size,
		ModTime:     prior.ModTime,
		Title:       prior.Title,
		Tags:        prior.Tags,
		Binary:      prior.Binary,
		Read:        false,
		Reason:      "recorded by this reconciler and no longer on disk",
	}
}

// declaredMove reports whether a walked file is a file the record already owns
// under the same identity at a different location.
//
// It only applies to a declared identity, and that restriction is the difference
// between a move and a coincidence. A path-derived identity contains its own
// path, so two files with the same path-derived identity are the same file, and
// one with the same digest as a vanished file is handled separately and reported
// as the lossy case it is.
func declaredMove(f File, priorByID map[string]OwnershipEntry) bool {
	if !f.DeclaredID {
		return false
	}
	prior, ok := priorByID[f.ID]
	if !ok {
		return false
	}
	return SourceKey(prior.Root, prior.Path) != f.SourceKey
}

func sortEntries(in []PlanEntry) []PlanEntry {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Root != in[j].Root {
			return in[i].Root < in[j].Root
		}
		return in[i].Path < in[j].Path
	})
	return in
}

// removeDeleted and removeCreated return the slice without the named entry.
//
// They return rather than mutate in place because a slice header is passed by
// value: a function that appended to a copy of the header would shrink the local
// slice and leave the plan holding the entry it was asked to remove, which is
// the kind of bug that makes a plan say one thing and an apply do another.
func removeDeleted(deleted []PlanEntry, sourceKey string) []PlanEntry {
	for i, e := range deleted {
		if e.SourceKey == sourceKey {
			return append(deleted[:i], deleted[i+1:]...)
		}
	}
	return deleted
}

func removeCreated(created []PlanEntry, id string) []PlanEntry {
	for i, e := range created {
		if e.ID == id {
			return append(created[:i], created[i+1:]...)
		}
	}
	return created
}

func priorKeyForID(rec *OwnershipRecord, id string) string {
	if e, ok := rec.EntryForID(id); ok {
		return e.SourceKeyOrPath()
	}
	return "an unrecorded file"
}

// SourceKeyOrPath returns the record key for an entry, recomputing it when the
// entry did not carry one.
func (e OwnershipEntry) SourceKeyOrPath() string { return SourceKey(e.Root, e.Path) }

// detectDrift reports derived claims whose text is not contained in the authored
// content they were built from.
//
// The check is a containment test, and it is weak on purpose. Consolidation
// rewrites a claim in the model's own words, so a claim that differs from its
// source is the normal case rather than a conflict; what this surfaces is the
// narrower thing a reader can act on — a claim whose exact wording is absent
// from every file that produced it, which is what a person editing a runbook
// would want to look at.
//
// It is reported rather than resolved because resolving it means deciding which
// of a person's file and the system's belief is current, and that decision is
// the expensive one. See the specification's discussion of two kinds of
// contradiction.
func detectDrift(derived []DerivedClaim, files []File) []Drift {
	if len(derived) == 0 {
		return nil
	}
	// Only content this reconciler owns can contradict the corpus, so the check
	// is over the files just walked.
	bodyByID := make(map[string]string, len(files))
	for _, f := range files {
		if f.Read && f.ID != "" {
			bodyByID[f.ID] = f.Body
		}
	}

	var out []Drift
	for _, claim := range derived {
		if claim.ID == "" || claim.Origin != OriginDerived {
			continue
		}
		var backing, conflicting []string
		anyAuthored := false
		for _, src := range claim.SourceContentIDs {
			body, isAuthored := bodyByID[src]
			if !isAuthored {
				continue
			}
			anyAuthored = true
			backing = append(backing, src)
			if !containsFold(body, claim.Text) {
				conflicting = append(conflicting, src)
			}
		}
		// A claim backed by nothing this walk read, or backed partly by content
		// the corpus does not own, is not a conflict between a person and the
		// system. Reporting it would be noise.
		if !anyAuthored || len(backing) == 0 || len(conflicting) == 0 {
			continue
		}
		out = append(out, Drift{
			ClaimID:         claim.ID,
			ClaimText:       claim.Text,
			BackingContent:  backing,
			ConflictContent: conflicting,
			Detection:       driftDetection,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClaimID < out[j].ClaimID })
	return out
}

// containsFold reports whether needle appears in haystack, ignoring case and
// collapsing runs of whitespace.
//
// A claim is compared against prose, and prose is reflowed: the same sentence
// can carry a line break, a double space and a different case between the file
// and the consolidated claim, none of which is a difference in meaning.
func containsFold(haystack, needle string) bool {
	return strings.Contains(foldSpace(haystack), foldSpace(needle))
}

func foldSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range strings.ToLower(s) {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// digest fingerprints a plan so that a confirmation can be shown to refer to the
// plan a person read.
//
// It covers what the plan decides, not the timestamps of the run that computed
// it: two plans over an unchanged corpus are the same plan, and a confirmation
// that expired because somebody ran the planner twice would be a rule that fails
// for no reason.
func (p Plan) digest() string {
	var b strings.Builder
	fmt.Fprintf(&b, "base=%s\ncommit=%s\n", p.BaseID, p.Commit)
	writeEntries := func(label string, entries []PlanEntry) {
		fmt.Fprintf(&b, "%s=%d\n", label, len(entries))
		for _, e := range entries {
			fmt.Fprintf(&b, "  %s %s %s %s\n", e.ID, e.SourceKey, e.Digest, e.PriorDigest)
		}
	}
	writeEntries("created", p.Created)
	writeEntries("updated", p.Updated)
	writeEntries("unchanged", p.Unchanged)
	writeEntries("deleted", p.Deleted)
	fmt.Fprintf(&b, "moved=%d\n", len(p.Moved))
	for _, m := range p.Moved {
		fmt.Fprintf(&b, "  %s %s -> %s %s\n", m.From.ID, m.From.SourceKey, m.To.ID, m.To.SourceKey)
	}
	return Digest([]byte(b.String()))
}

// RestrictedTo returns the plan narrowed to one subtree of the corpus, with its digest
// recomputed.
//
// The digest has to be recomputed because a confirmation refers to the plan a person read. If a
// caller previewed one directory and then confirmed, the confirmation has to match the narrowed
// plan they saw — not the whole-corpus plan it was derived from, which is a different document
// and which they did not read.
//
// Drift is dropped rather than filtered. A derived claim is contradicted by the corpus as a whole
// and attributing it to a directory would misattribute it, so a narrowed plan reports no drift
// rather than a partial answer that reads like a complete one.
//
// A subtree that names nothing — empty, whitespace, or a slash — is **not** a narrowing. The
// alternative is the worst of the three answers available: a plan with nothing in it says "this
// directory is current", and for a whitespace-only value that is a statement about the whole corpus
// which happens to be false. The caller gets the plan it would have got without the argument, whose
// digest is therefore the one a confirmation computed over the full corpus still matches.
func (p Plan) RestrictedTo(subtree string) Plan {
	prefix := NormalizePath(strings.TrimSpace(subtree))
	if prefix == "" {
		return p
	}
	keep := func(entries []PlanEntry) []PlanEntry {
		out := make([]PlanEntry, 0, len(entries))
		for _, e := range entries {
			if e.Path == prefix || strings.HasPrefix(e.Path, prefix+"/") {
				out = append(out, e)
			}
		}
		return out
	}
	narrowed := Plan{
		BaseID:      p.BaseID,
		Commit:      p.Commit,
		Created:     keep(p.Created),
		Updated:     keep(p.Updated),
		Unchanged:   keep(p.Unchanged),
		Deleted:     keep(p.Deleted),
		Warnings:    p.Warnings,
		UnreadFiles: p.UnreadFiles,
	}
	for _, m := range p.Moved {
		if m.To.Path == prefix || strings.HasPrefix(m.To.Path, prefix+"/") {
			narrowed.Moved = append(narrowed.Moved, m)
		}
	}
	// The warnings are kept whole: a warning about a file outside the preview is still true
	// and still something the reader needs, and hiding it because it names a path they did
	// not ask for would be the plan being tidy at the expense of being honest.
	narrowed.PlanDigest = narrowed.digest()
	return narrowed
}
