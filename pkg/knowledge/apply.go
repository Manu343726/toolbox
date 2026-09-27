package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Engine is the part of a knowledge backend this package needs.
//
// It is deliberately small. The reconcile is pure computation over a directory
// and a set of digests, and everything in this interface is something the
// backend genuinely has to do rather than something this package could have
// decided for it. A provider implements it against the backend's own client and
// converts failures into classified errors; nothing above this interface knows
// which backend it is talking to.
type Engine interface {
	// Retain ingests a batch of content and returns the identifiers of the
	// asynchronous operations doing the work.
	//
	// The request carries an idempotency key, so a caller that loses the
	// acknowledgement of a batch may re-send the same key and get the original
	// operation back instead of a second copy of the work.
	Retain(ctx context.Context, req RetainRequest) (RetainResult, error)
	// DeleteContent removes a content item. It is only ever called for an
	// identifier an ownership record holds.
	DeleteContent(ctx context.Context, baseID, id string) error
	// RetainBinary ingests a non-text file through the backend's binary path.
	RetainBinary(ctx context.Context, req BinaryRetainRequest) (RetainResult, error)
}

// RetainRequest is one batch of text content to ingest.
type RetainRequest struct {
	// BaseID is the base to ingest into.
	BaseID string
	// Items are the content, and are ingested in the order given. A batch is
	// bounded by the caller because the extraction step is the expensive part and
	// one enormous request is one enormous failure.
	Items []RetainItem
	// IdempotencyKey makes this batch safe to retry. Re-sending a key returns
	// the original operation and does no work; re-using one for a different
	// batch is a conflict, which the engine reports as a classified error rather
	// than by silently doing the wrong thing.
	IdempotencyKey string
	// Async asks the backend to process in the background. A corpus is thousands
	// of extractions and cannot be a synchronous call.
	Async bool
}

// RetainItem is one document to ingest.
type RetainItem struct {
	// ID is the document identifier. Re-ingesting an identifier replaces the
	// document and its facts rather than appending to it, which is what makes an
	// edited runbook stop being retrievable by the sentence it used to contain.
	ID string
	// Content is the text to extract from.
	Content string
	// Tags scope the document.
	Tags []string
	// Metadata is unfiltered key/value detail about the document, and is where
	// the source path goes: a citation needs a field to point at, and a filter
	// does not.
	Metadata map[string]string
	// Context names the subsystem doing the ingest, so that a base holding
	// several writers can tell them apart.
	Context string
	// EventTime is the RFC 3339 time the described event happened, or
	// TimelessTimestamp when nothing declared one.
	EventTime string
	// UpdateMode is always sent explicitly. The backend's schema carries no
	// default for it, so a client that omits it inherits nothing and a
	// generated client cannot tell what omission would mean.
	UpdateMode string
	// ResolveEntities turns entity resolution off. It is false for authored
	// content, and it is the only safe value for it: with resolution on, a name
	// in a person's runbook that is close to a name already in the base may
	// attach itself to that other entity, so the base would attribute a
	// documented statement to something the author never mentioned.
	ResolveEntities bool
}

// BinaryRetainRequest is one batch of non-text content to ingest.
type BinaryRetainRequest struct {
	BaseID string
	Items  []BinaryRetainItem
	// IdempotencyKey makes this batch safe to retry, as for text.
	IdempotencyKey string
	Async          bool
}

// BinaryRetainItem is one non-text file to ingest.
type BinaryRetainItem struct {
	ID      string
	Content []byte
	// Filename is the name the backend stores it under, which a reader sees.
	Filename string
	Tags     []string
	Metadata map[string]string
	Context  string
}

// RetainResult reports what an ingest did.
type RetainResult struct {
	// OperationIDs are the backend's asynchronous operations, in the order the
	// items were sent. Empty when the backend completed the work inline.
	OperationIDs []string
	// Accepted is how many items the backend took.
	Accepted int
	// Duplicate reports that the idempotency key matched an earlier request and
	// no new work was created. An apply that sees this did not fail and did not
	// do anything twice, which is worth distinguishing from both.
	Duplicate bool
}

// ApplyOptions configures an apply.
type ApplyOptions struct {
	// BaseID is the base being written to.
	BaseID string
	// Commit is the revision being reconciled, recorded on every document and on
	// the ownership record, so that "the index matches the merge" is a statement
	// that can be checked rather than believed.
	Commit string
	// Owner is written into the ownership marker on ingested content.
	Owner string
	// Namespace must match the record's binding.
	Namespace string
	// Identity is the destination the ownership record is bound to.
	Identity OwnershipIdentity
	// BatchSize bounds one ingest request. Zero uses a default.
	BatchSize int
	// Concurrency bounds parallel ingest batches. Zero uses a default.
	Concurrency int
	// Async asks the backend to process in the background.
	Async bool
	// Now supplies the clock. It is injected because a reconcile's output has to
	// be reproducible in a test, and a reconciler that reads the wall clock
	// cannot have its output asserted on.
	Now func() time.Time
	// DryRun computes what would be sent without sending it. A dry run is how a
	// caller confirms an apply before committing to it.
	DryRun bool
	// Progress is called after each completed step, for a caller showing
	// progress. It must be safe for concurrent use and must not block for long.
	Progress func(ApplyProgress)
}

const (
	defaultBatchSize   = 32
	defaultConcurrency = 3
)

// ApplyProgress reports how far an apply has got.
type ApplyProgress struct {
	// Phase is one of "ingest", "prune", "record".
	Phase string
	// Done and Total count items within the phase.
	Done, Total int
	// Message describes the current item.
	Message string
}

// ApplyResult reports what an apply did.
type ApplyResult struct {
	// Ingested, Replaced and Pruned count the outcomes.
	Ingested int
	Replaced int
	Pruned   int
	// Failed counts items the engine refused, which do not stop the rest: one
	// unreadable document in a thousand-file corpus is not a reason to leave the
	// other nine hundred and ninety-nine unreconciled.
	Failed int
	// OperationIDs are the backend operations the ingest created, in order.
	OperationIDs []string
	// Warnings describe failures and degradations in enough detail to act on.
	Warnings []string
	// Ownership is the record to persist. It is returned rather than written, so
	// that the caller decides when it lands: a record written for an apply that
	// then failed would claim ownership of documents that were never ingested.
	Ownership *OwnershipRecord
	// Committed is false when DryRun was set.
	Committed bool
}

// Applier executes plans against an engine.
type Applier struct {
	engine Engine
	corpus *Corpus
}

// NewApplier returns an applier. The corpus is needed to read the files a plan
// says to ingest, because a plan built with the cheap filter may have skipped
// exactly those files.
func NewApplier(engine Engine, corpus *Corpus) *Applier {
	return &Applier{engine: engine, corpus: corpus}
}

// Apply executes a plan.
//
// A file the plan could not read is read here rather than skipped, because a
// plan that cannot be applied is worse than a plan that costs a little more to
// apply. A file that is still unreadable at this point is reported as a failure
// and left out of the ownership record, so the next reconcile retries it rather
// than believing it is done.
func (a *Applier) Apply(ctx context.Context, plan Plan, prior *OwnershipRecord, opts ApplyOptions) (ApplyResult, error) {
	// The ownership marker is what authorises a later prune, and it is computed
	// when the plan is built rather than here, because a plan's tags are what a
	// person reads before confirming it. That split has one failure mode worth
	// refusing rather than tolerating: a caller who passes an owner to Apply but
	// not to Plan gets content ingested with no marker on it, and the next
	// reconcile's prune then leaves it behind forever — a silent leak of exactly
	// the kind of ownership record exists to prevent.
	if opts.Owner != "" {
		if err := requireOwnershipMarker(plan, opts.Owner); err != nil {
			return ApplyResult{}, err
		}
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	rec := &OwnershipRecord{
		Version:  ownershipVersion,
		Identity: opts.Identity,
		Files:    make(map[string]OwnershipEntry, len(prior.Files)+len(plan.Created)+len(plan.Updated)),
	}
	// Everything the previous record owned is carried forward, so that a plan
	// that only covers part of the corpus does not disown the rest.
	for k, v := range prior.Files {
		rec.Files[k] = v
	}

	res := ApplyResult{Ownership: rec, Committed: !opts.DryRun}

	// The documents to write: creations, replacements, and moves landing on a new
	// path. A move's destination is an ingest under the same identifier, which is
	// what preserves the facts already extracted from it.
	writes := make([]PlanEntry, 0, len(plan.Created)+len(plan.Updated))
	writes = append(writes, plan.Created...)
	writes = append(writes, plan.Updated...)
	for _, m := range plan.Moved {
		writes = append(writes, m.To)
	}
	sortEntries(writes)

	if len(writes) > 0 {
		if err := a.ingestAll(ctx, writes, plan, opts, rec, now, batchSize, concurrency, &res); err != nil {
			return res, err
		}
	}

	// The documents to remove: what the record owned and the corpus no longer
	// has. Every identifier here came from the record, never from listing the
	// base, so a document this reconciler did not create cannot appear in it.
	prunes := make([]string, 0, len(plan.Deleted))
	for _, e := range plan.Deleted {
		if e.ID == "" {
			continue
		}
		prunes = append(prunes, e.ID)
	}
	// A rename of identity leaves the old identifier orphaned in the base. It is
	// ours — the record holds it — so it is removed, and it is removed because the
	// record says so rather than because anything listed the base.
	for _, m := range plan.Moved {
		if m.From.DeclaredID && m.From.ID != m.To.ID {
			prunes = append(prunes, m.From.ID)
		}
	}
	sort.Strings(prunes)
	prunes = dedupeNonEmpty(prunes)

	if len(prunes) > 0 && !opts.DryRun {
		if ok, why := prior.CanPrune(); !ok {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("nothing was pruned: %s", why))
		} else {
			for i, id := range prunes {
				if err := ctx.Err(); err != nil {
					return res, err
				}
				report(opts.Progress, ApplyProgress{Phase: "prune", Done: i, Total: len(prunes), Message: id})
				if err := a.engine.DeleteContent(ctx, opts.BaseID, id); err != nil {
					// The record keeps its entry, so the next reconcile tries again
					// rather than forgetting a document it still owns.
					res.Failed++
					res.Warnings = append(res.Warnings, fmt.Sprintf("could not remove %s: %v", id, err))
					continue
				}
				res.Pruned++
				deleteBySourceKey(rec, id)
			}
		}
	}
	report(opts.Progress, ApplyProgress{Phase: "prune", Done: len(prunes), Total: len(prunes)})

	rec.LastCommit = opts.Commit
	rec.LastReconciledAt = now().UTC()
	report(opts.Progress, ApplyProgress{Phase: "record", Done: 1, Total: 1, Message: opts.Commit})
	return res, nil
}

// ingestAll writes every document in a plan, in bounded batches, with bounded
// concurrency.
func (a *Applier) ingestAll(
	ctx context.Context,
	writes []PlanEntry,
	plan Plan,
	opts ApplyOptions,
	rec *OwnershipRecord,
	now func() time.Time,
	batchSize, concurrency int,
	res *ApplyResult,
) error {
	// A batch is the unit of idempotence, so the key is derived from what the
	// batch contains. Re-running the same apply over the same corpus produces the
	// same keys, which is what makes a retry safe; a batch whose contents changed
	// produces a different key, which is what stops two different batches from
	// colliding on one.
	type prepared struct {
		entries []PlanEntry
		items   []RetainItem
		binary  []BinaryRetainItem
		key     string
	}

	var batches []prepared
	for start := 0; start < len(writes); start += batchSize {
		end := min(start+batchSize, len(writes))
		chunk := writes[start:end]

		p := prepared{entries: chunk}
		var keyMaterial strings.Builder
		fmt.Fprintf(&keyMaterial, "%s\x00%s\x00", opts.BaseID, opts.Commit)
		for _, e := range chunk {
			fmt.Fprintf(&keyMaterial, "%s\x00%s\x00", e.ID, e.Digest)

			if e.Binary {
				raw, err := a.readBinary(e)
				if err != nil {
					// A file this apply cannot read is not ingested and not
					// recorded, so the next reconcile tries it again rather than
					// believing a corpus it never read.
					res.Failed++
					res.Warnings = append(res.Warnings, fmt.Sprintf("could not read %s: %v", e.SourceKey, err))
					p.entries = removeEntry(p.entries, e.SourceKey)
					continue
				}
				p.binary = append(p.binary, BinaryRetainItem{
					ID:       e.ID,
					Content:  raw,
					Filename: path.Base(e.Path),
					Tags:     e.Tags,
					Metadata: map[string]string{
						"path":   e.Path,
						"root":   e.Root,
						"title":  e.Title,
						"commit": opts.Commit,
					},
					Context: contextValue,
				})
				continue
			}

			if !e.Read {
				// The plan was built with the cheap filter and this file was
				// skipped. Reading it here is the whole point of the filter: not
				// every file is read on every run, and the ones that are about to
				// change are read exactly once.
				raw, err := a.readBody(e)
				if err != nil {
					res.Failed++
					res.Warnings = append(res.Warnings, fmt.Sprintf("could not read %s: %v", e.SourceKey, err))
					p.entries = removeEntry(p.entries, e.SourceKey)
					continue
				}
				e.Body = raw
			}

			p.items = append(p.items, RetainItem{
				ID: e.ID,
				// The body is what a person wrote. A document's frontmatter is
				// metadata this package acts on, and leaving it in the extracted
				// text would teach the extractor that a person wrote
				// "kind: decision".
				Content: e.Body,
				Tags:    e.Tags,
				Metadata: map[string]string{
					"path":   e.Path,
					"root":   e.Root,
					"title":  e.Title,
					"commit": opts.Commit,
				},
				Context:         contextValue,
				EventTime:       e.EventTime,
				UpdateMode:      UpdateModeReplace,
				ResolveEntities: false,
			})
			// The entry carries the body it will actually be sent, so a caller
			// inspecting the result sees what was ingested.
			p.entries = replaceEntry(p.entries, e)
		}
		if len(p.entries) == 0 {
			continue
		}
		p.key = idempotencyKey(keyMaterial.String())
		batches = append(batches, p)
	}

	if opts.DryRun {
		for _, b := range batches {
			res.Ingested += len(b.entries)
		}
		return nil
	}

	// Concurrency is bounded because each batch is a set of extractions competing
	// for the same backend, and an unbounded fan-out is how a reconcile turns
	// into an outage.
	type outcome struct {
		batch prepared
		res   RetainResult
		err   error
	}
	results := make([]outcome, len(batches))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, b := range batches {
		wg.Add(1)
		go func(i int, b prepared) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = outcome{batch: b, err: ctx.Err()}
				return
			}
			report(opts.Progress, ApplyProgress{Phase: "ingest", Done: i, Total: len(batches), Message: b.key})

			if len(b.binary) > 0 {
				res, err := a.engine.RetainBinary(ctx, BinaryRetainRequest{
					BaseID:         opts.BaseID,
					Items:          b.binary,
					IdempotencyKey: b.key,
					Async:          opts.Async,
				})
				results[i] = outcome{batch: b, res: res, err: err}
				return
			}
			res, err := a.engine.Retain(ctx, RetainRequest{
				BaseID:         opts.BaseID,
				Items:          b.items,
				IdempotencyKey: b.key,
				Async:          opts.Async,
			})
			results[i] = outcome{batch: b, res: res, err: err}
		}(i, b)
	}
	wg.Wait()

	// Progress and results are reported in batch order rather than completion
	// order, so that the record a caller persists describes the same sequence a
	// person was shown.
	for i, out := range results {
		if out.err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// A failed batch leaves its documents out of the record, so the next
			// reconcile retries them. Recording them would claim ownership of
			// content the base does not have.
			res.Failed += len(out.batch.entries)
			res.Warnings = append(res.Warnings, fmt.Sprintf("batch %d (%d files, key %s) failed: %v",
				i, len(out.batch.entries), out.batch.key, out.err))
			continue
		}
		if out.res.Duplicate {
			// The work was already done under this key. The documents are in the
			// base; this run simply did not create them twice.
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"batch %d matched an earlier request under the same idempotency key and created no new work; its %d files were already ingested",
				i, len(out.batch.entries)))
		}
		res.OperationIDs = append(res.OperationIDs, out.res.OperationIDs...)

		now := now().UTC()
		for _, e := range out.batch.entries {
			if e.Changed() || !wasKnown(rec, e.SourceKey) {
				if e.PriorDigest == "" {
					res.Ingested++
				} else {
					res.Replaced++
				}
			}
			rec.Files[e.SourceKey] = OwnershipEntry{
				ID:           e.ID,
				DeclaredID:   e.DeclaredID,
				Root:         e.Root,
				Path:         e.Path,
				Digest:       e.Digest,
				ModTime:      e.ModTime,
				Size:         e.Size,
				SourceCommit: opts.Commit,
				ReconciledAt: now,
				Binary:       e.Binary,
				Title:        e.Title,
				Tags:         e.Tags,
			}
		}
	}
	report(opts.Progress, ApplyProgress{Phase: "ingest", Done: len(batches), Total: len(batches)})
	return nil
}

func wasKnown(rec *OwnershipRecord, key string) bool {
	_, ok := rec.Files[key]
	return ok
}

func removeEntry(entries []PlanEntry, sourceKey string) []PlanEntry {
	for i, e := range entries {
		if e.SourceKey == sourceKey {
			return append(entries[:i], entries[i+1:]...)
		}
	}
	return entries
}

func replaceEntry(entries []PlanEntry, updated PlanEntry) []PlanEntry {
	for i, e := range entries {
		if e.SourceKey == updated.SourceKey {
			entries[i] = updated
			return entries
		}
	}
	return append(entries, updated)
}

// readBody reads one file's body at apply time, re-parsing the frontmatter so
// that the text sent is the text below it.
//
// It verifies the digest, and the verification is free here: the plan did not
// read this file, so reading it is work the apply has to do anyway. When the plan
// did read the file, the apply sends the bytes the plan holds without re-reading,
// and a change since the plan is caught by the next reconcile rather than here —
// the record will hold a digest the file no longer has, which is precisely what
// the next plan reports as an update.
func (a *Applier) readBody(e PlanEntry) (string, error) {
	raw, err := os.ReadFile(a.filePath(e))
	if err != nil {
		return "", err
	}
	// The plan's digest was taken over the raw bytes and the file may have
	// changed since. Re-ingesting different bytes under a digest that says
	// otherwise would leave the record claiming a revision the base does not
	// hold, so the mismatch is refused rather than absorbed.
	if got := Digest(raw); got != e.Digest {
		return "", fmt.Errorf("the file changed since the plan was computed: the plan records %s and the file now reads %s; re-run the plan", e.Digest, got)
	}
	if looksBinary(raw) {
		return "", errors.New("the file is binary and must go through the binary ingest path")
	}
	parsed, err := ParseFile(raw)
	if err != nil {
		return "", err
	}
	return parsed.Body, nil
}

func (a *Applier) readBinary(e PlanEntry) ([]byte, error) {
	raw, err := os.ReadFile(a.filePath(e))
	if err != nil {
		return nil, err
	}
	if got := Digest(raw); got != e.Digest {
		return nil, fmt.Errorf("the file changed since the plan was computed: the plan records %s and the file now reads %s; re-run the plan", e.Digest, got)
	}
	return raw, nil
}

// filePath resolves a plan entry to a file on disk, using the corpus the plan
// was computed over. The recorded absolute path is preferred, and the root
// lookup is the fallback for an entry whose root name is known but whose path
// was not recorded — which is the case for a path rebuilt from a record.
func (a *Applier) filePath(e PlanEntry) string {
	for _, root := range a.corpus.Roots() {
		if root.Name == e.Root {
			return filepath.Join(root.Path, filepath.FromSlash(e.Path))
		}
	}
	return filepath.FromSlash(e.Path)
}

func deleteBySourceKey(rec *OwnershipRecord, id string) {
	for k, e := range rec.Files {
		if e.ID == id {
			delete(rec.Files, k)
			return
		}
	}
}

// idempotencyKey derives a stable, opaque key from the content of a batch.
//
// It is a digest rather than a readable string because the key is sent to a
// backend and stored there, and the content it is derived from is a list of file
// identifiers and digests that has no reason to be quoted back. UUID-shaped on
// the wire is the backend's business; what matters is that it is deterministic
// and 32 hex characters of entropy.
func idempotencyKey(material string) string {
	sum := sha256.Sum256([]byte("toolbox.knowledge.reconcile\x00" + material))
	return hex.EncodeToString(sum[:16])
}

// IdempotencyKeyForBatch exposes the key derivation, so that a caller can
// compute the key a batch would use and assert that re-applying a plan is
// idempotent without a backend.
func IdempotencyKeyForBatch(baseID, commit string, entries []PlanEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\x00%s\x00", baseID, commit)
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\x00%s\x00", e.ID, e.Digest)
	}
	return idempotencyKey(b.String())
}

func report(fn func(ApplyProgress), p ApplyProgress) {
	if fn != nil {
		fn(p)
	}
}

// requireOwnershipMarker refuses a plan whose content would be ingested without
// the ownership marker for the identity doing the pruning.
//
// Only files that are actually written are checked. A deletion is authorised by
// the ownership record rather than by a tag: the record already says this
// reconciler ingested it, and the record is what the prune walks, so demanding a
// tag as well would be a second authority for one fact. An unchanged file is not
// written at all.
func requireOwnershipMarker(plan Plan, owner string) error {
	for _, group := range []struct {
		label   string
		entries []PlanEntry
	}{
		{"created", plan.Created},
		{"updated", plan.Updated},
	} {
		for _, e := range group.entries {
			if !OwnedByTag(e.Tags, owner) {
				return fmt.Errorf(
					"knowledge: refusing to apply: %s (%s) would be ingested without the %q ownership marker, so the next reconcile could never prune it; pass the same owner to PlanReconcile and ApplyReconcile",
					e.Path, group.label, owner)
			}
		}
	}
	for _, m := range plan.Moved {
		if !OwnedByTag(m.To.Tags, owner) {
			return fmt.Errorf(
				"knowledge: refusing to apply: %s (moved to) would be ingested without the %q ownership marker, so the next reconcile could never prune it; pass the same owner to PlanReconcile and ApplyReconcile",
				m.To.Path, owner)
		}
	}
	return nil
}

// CountByTag counts how many entries carry a tag. It is a small helper for a
// caller summarising a plan or a result in one line.
func CountByTag(entries []PlanEntry, tag string) int {
	n := 0
	for _, e := range entries {
		for _, t := range e.Tags {
			if t == tag {
				n++
				break
			}
		}
	}
	return n
}
