package knowledge

import (
	"fmt"
	"time"
)

// ContentOptions configures how a `Content` is built.
type ContentOptions struct {
	// BaseID is the base this content belongs to. It is required: a `Content` with no base is
	// not addressable, and `Valid` says so rather than letting it through.
	BaseID string
	// Owner is the identity written into the ownership marker, and it becomes part of
	// nothing here — it is a reconcile concern. It is carried because a caller building a
	// `Content` from a record usually has it and a tag list derived from it.
	Owner string
	// IncludeBody says whether the body should be read and carried. A listing that does not
	// want three thousand bodies says false, and the result then has `HasBody` false — which
	// is the same state as a deployment that does not retain text, and both are honest.
	IncludeBody bool
	// ReconciledAt and SourceCommit are the reconcile's record of this content, and are
	// carried when the caller has them so "the index matches the merge" is checkable.
	ReconciledAt time.Time
	SourceCommit string
	// SkipValidation is for the one caller that builds a `Content` to have `Valid` say so.
	// It exists because the invariant is worth having, not because it is worth bypassing, and
	// a `Content` returned with an error is more useful than one returned broken.
	SkipValidation bool
}

// ContentFromFile builds the domain content for a corpus file.
//
// This is where a file becomes a `Content`, and it is here rather than in a provider because the
// mapping is the framework's: the walk, the projection, the reconcile and every contract that
// reports content all need the same one, and a second provider writing its own would be a second
// thing to keep in step — and the one that drifts is the one that decides whether a runbook is
// reported as editable.
func ContentFromFile(f File, tags []string, opts ContentOptions) (Content, error) {
	// A file the cheap filter skipped has no body in memory, and the record that stands in
	// for it carries everything else. So the body is carried only when the caller asked and
	// the walk actually read one, and `HasBody` says which happened.
	hasBody := opts.IncludeBody && f.Read
	body := f.Body
	if !hasBody {
		body = ""
	}
	c := Content{
		ID:         f.ID,
		BaseID:     opts.BaseID,
		Origin:     OriginAuthored,
		Title:      f.Title,
		Body:       body,
		HasBody:    hasBody,
		Tags:       tags,
		Location:   Location{Kind: LocationKindCorpusPath, Path: f.Path},
		Provenance: Provenance{OriginTag: OriginTag(OriginAuthored), Source: f.Path},
		Revision: Revision{
			Digest:       f.Digest,
			ModTime:      f.ModTime,
			Size:         f.Size,
			ReconciledAt: opts.ReconciledAt,
			SourceCommit: opts.SourceCommit,
		},
		Mutability: MutabilityFor(OriginAuthored),
	}
	return finishContent(c, opts)
}

// ContentFromRecord builds the domain content for a file from the ownership record that stands in
// for one the walk skipped.
//
// It is a separate constructor rather than a flag on `ContentFromFile` because the two genuinely
// differ: a record knows a file's declared metadata, tags and digest but not its body, and building
// it through the file constructor would produce a `Content` claiming a body that was never read.
func ContentFromRecord(root, path, id string, e OwnershipEntry, tags []string, opts ContentOptions) (Content, error) {
	// An ownership record never carries a body — it is a record of what was ingested, not the
	// text — so a `Content` built from one has no body even when the caller asked for one. That
	// is the honest answer for a file the cheap filter skipped, and it is why this is a
	// separate constructor rather than a flag: a `Content` claiming a body that was never read
	// is a document that says nothing and looks like an empty one.
	hasBody := false
	body := ""
	title := e.Title
	if title == "" {
		title = e.Front.Title
	}
	c := Content{
		ID:         id,
		BaseID:     opts.BaseID,
		Origin:     OriginAuthored,
		Title:      title,
		Body:       body,
		HasBody:    hasBody,
		Tags:       tags,
		Location:   Location{Kind: LocationKindCorpusPath, Path: path},
		Provenance: Provenance{OriginTag: OriginTag(OriginAuthored), Source: path},
		Revision: Revision{
			Digest:       e.Digest,
			ModTime:      e.ModTime,
			Size:         e.Size,
			ReconciledAt: opts.ReconciledAt,
			SourceCommit: opts.SourceCommit,
		},
		Mutability: MutabilityFor(OriginAuthored),
	}
	// The declared date travels on the frontmatter, which the contract's `corpus_meta` reports
	// separately. It is not on `Provenance` because provenance is about what *produced* the
	// content and a date is something a person wrote about it.
	_ = e.Front
	return finishContent(c, opts)
}

// ContentFromRetained builds the domain content for a document a backend holds.
//
// It takes the pieces rather than a backend type, so a second provider can call it with its own
// document and get the same invariant. The mutability is `Curated` because retained content is the
// one origin that is edited through the surface — an assistant told `None` would go and edit a file
// that does not exist, and one told `Curated` about a page would write prose the next refresh
// discards.
func ContentFromRetained(baseID, id, title, body, documentID, sourceRevision string, tags []string, opts ContentOptions) (Content, error) {
	hasBody := opts.IncludeBody && body != ""
	if !hasBody {
		body = ""
	}
	c := Content{
		ID:         id,
		BaseID:     baseID,
		Origin:     OriginRetained,
		Title:      title,
		Body:       body,
		HasBody:    hasBody,
		Tags:       tags,
		Location:   Location{Kind: LocationKindDocument, Path: documentID},
		Provenance: Provenance{OriginTag: OriginTag(OriginRetained), Source: sourceRevision},
		Revision:   Revision{ReconciledAt: opts.ReconciledAt, SourceCommit: opts.SourceCommit},
		Mutability: MutabilityFor(OriginRetained),
	}
	return finishContent(c, opts)
}

// ContentFromPage builds the domain content for a generated page.
//
// The mutability is `Regenerated` and the body is whatever the system last wrote, so an edit here is
// discarded rather than applied. That is the whole distinction between a page and a document, and
// it is the reason a caller is told rather than left to infer it from the content.
func ContentFromPage(baseID, id, name, body, pageID string, tags []string, opts ContentOptions) (Content, error) {
	hasBody := body != ""
	if !hasBody {
		body = ""
	}
	c := Content{
		ID:         id,
		BaseID:     baseID,
		Origin:     OriginDerived,
		Title:      name,
		Body:       body,
		HasBody:    hasBody,
		Tags:       tags,
		Location:   Location{Kind: LocationKindPage, PageID: pageID},
		Provenance: Provenance{OriginTag: OriginTag(OriginDerived), Source: pageID},
		Revision:   Revision{ReconciledAt: opts.ReconciledAt, SourceCommit: opts.SourceCommit},
		Mutability: MutabilityFor(OriginDerived),
	}
	return finishContent(c, opts)
}

// finishContent applies the fields a caller cannot get wrong and checks the invariant.
//
// The mutability is *not* left to the caller: it is a function of the origin and a contract that
// could disagree with it would be a contract whose central promise — that a caller can tell whether
// a file is editable — has two answers.
func finishContent(c Content, opts ContentOptions) (Content, error) {
	if c.Mutability == "" {
		c.Mutability = MutabilityFor(c.Origin)
	}
	if opts.SkipValidation {
		return c, nil
	}
	if err := validateContent(c); err != nil {
		return Content{}, fmt.Errorf("building content %q: %w", c.ID, err)
	}
	return c, nil
}

// ValidateContent is the invariant, exported so a provider that builds a `Content` some other way
// can check its own.
func ValidateContent(c Content) error { return validateContent(c) }
