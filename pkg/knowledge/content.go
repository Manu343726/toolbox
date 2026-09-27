// Package knowledge holds the content model, the engine interfaces, and the
// reconcile that brings an authored corpus and a knowledge base into agreement.
//
// It is a root package rather than a subsystem's implementation because the
// framework needs the behaviour in process: a provider mounts this package and
// converts messages, and nothing else. The reconcile in particular is pure
// computation over a directory and a set of digests with no backend in it, which
// is what lets it be tested offline and deterministically.
//
// The vocabulary is deliberately not the backend's. A base is a base rather than
// a bank, a fact is a fact rather than a memory unit, and the words that would
// import one implementation into a provider-neutral contract are not used. See
// docs/knowledge.md for the specification this implements.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Origin states how a piece of content came to exist.
//
// It is an attribute on one content type rather than a type hierarchy, and it is
// never chosen by a caller: the only way content becomes Authored is a person
// writing it in a corpus file, and the only way it becomes Derived is the system
// rendering it. An assistant cannot launder an inference into documentation.
type Origin string

const (
	// OriginUnspecified is the zero value and is never valid on returned content.
	OriginUnspecified Origin = ""
	// OriginAuthored was written by a person in a corpus, and is edited there.
	OriginAuthored Origin = "authored"
	// OriginRetained was retained from a source: a conversation, an import, an
	// attachment. It is curated in place.
	OriginRetained Origin = "retained"
	// OriginDerived was synthesized and is maintained by the system from other
	// content. It is regenerated, so it is never authority for anything.
	OriginDerived Origin = "derived"
)

// Valid reports whether o is one of the three real origins.
func (o Origin) Valid() bool {
	switch o {
	case OriginAuthored, OriginRetained, OriginDerived:
		return true
	default:
		return false
	}
}

// Mutability states how this origin's content may be changed.
//
// It is declared rather than left to be discovered, because a write that will be
// silently overwritten later is worse than a refused one: an assistant told a
// page is regenerated can tell the user, and one told a runbook is edited at its
// source goes and edits the file.
type Mutability string

const (
	// MutabilityNone cannot be changed through this surface at all. Authored
	// content is edited at its source and reconciled in; a write would be a
	// second source of truth that the next reconcile deletes.
	MutabilityNone Mutability = "none"
	// MutabilityCurated may be edited in place, and the edit persists. Retained
	// content is curated.
	MutabilityCurated Mutability = "curated"
	// MutabilityRegenerated is rewritten by the system whenever its scope
	// changes. An edit is discarded rather than applied.
	MutabilityRegenerated Mutability = "regenerated"
)

// MutabilityFor returns how content of the given origin may be changed. It is a
// total function so that no origin can be added without deciding this.
func MutabilityFor(o Origin) Mutability {
	switch o {
	case OriginAuthored:
		return MutabilityNone
	case OriginRetained:
		return MutabilityCurated
	case OriginDerived:
		return MutabilityRegenerated
	default:
		return MutabilityNone
	}
}

// LocationKind states what shape a Location takes.
type LocationKind string

const (
	// LocationKindCorpusPath is a path relative to a corpus root.
	LocationKindCorpusPath LocationKind = "corpus_path"
	// LocationKindDocument is a retained document identifier.
	LocationKindDocument LocationKind = "document"
	// LocationKindPage is a position in a derived page tree.
	LocationKindPage LocationKind = "page"
)

// Location says where content is addressable. One field, three shapes, because
// "where is this" is one question and a consumer should not have to ask a second
// one to get an answer it can show a person.
type Location struct {
	// Kind is which of the fields below is meaningful.
	Kind LocationKind
	// Path is a corpus-relative slash-separated path, or a document identifier.
	Path string
	// TreePath is the slash-separated folder path of a derived page, and
	// BackingModelID identifies the mental model that maintains its body.
	TreePath       string
	PageID         string
	BackingModelID string
}

// String renders the location the way a person would name it.
func (l Location) String() string {
	switch l.Kind {
	case LocationKindCorpusPath:
		return l.Path
	case LocationKindDocument:
		return l.Path
	case LocationKindPage:
		if l.TreePath == "" {
			return l.PageID
		}
		return l.TreePath + "/" + l.PageID
	default:
		return ""
	}
}

// Provenance records what produced content and what it was derived from. It is
// queryable rather than decorative: ownership is recorded here as a tag, so a
// prune and a provenance filter cannot disagree about what this subsystem owns.
type Provenance struct {
	// OriginTag is this origin as a value in the framework's reserved tag
	// namespace. Callers filter on it, and a deployment cannot collide with it
	// because the prefix belongs to the framework.
	OriginTag string
	// Source is the file a piece of authored content came from, or the origin of
	// a retained one.
	Source string
	// SourceRevision is the commit a piece of authored content was reconciled
	// from, when the reconciler knew it.
	SourceRevision string
	// DerivedFrom names the documents, facts or models this was derived from.
	DerivedFrom []string
}

// Revision carries a content digest and enough timestamps for a consumer to tell
// whether it has seen a version. The digest decides equality; the modification
// time is only ever an optimisation that avoids work.
type Revision struct {
	// Digest is the content digest, in "sha256:<hex>" form.
	Digest string
	// ModTime and Size are the file's own, and are what a cheap staleness probe
	// compares. A reader must not treat either as identity.
	ModTime time.Time
	Size    int64
	// ReconciledAt is when this subsystem last ingested the content.
	ReconciledAt time.Time
	// SourceCommit is the commit the content was reconciled from, so that "the
	// index matches the merge" is a statement that can be checked.
	SourceCommit string
}

// Equal reports whether two revisions describe the same content. The digest
// decides; the timestamps are deliberately not consulted, because they are the
// thing a filesystem is free to get wrong.
func (r Revision) Equal(other Revision) bool { return r.Digest != "" && r.Digest == other.Digest }

// Content is the one addressable, readable unit of knowledge in a base. An
// authored corpus file, a retained document and a derived page are all Content;
// they differ in Origin and in where Location points, and in nothing else a
// reader has to branch on.
type Content struct {
	// ID is stable within the base.
	ID string
	// BaseID is the base this content belongs to.
	BaseID string
	// Origin is how this content came to exist. It is set by the operation that
	// created it and is never chosen by a caller.
	Origin Origin
	// Title is for listings and as a fallback for a missing body.
	Title string
	// Body is the content as markdown, and HasBody says whether the deployment
	// retained it at all, so a caller never has to guess from an empty string.
	Body    string
	HasBody bool
	// Tags are what retrieval scopes by. Their meaning belongs to the filter,
	// not to the content, which is what lets a directory convention become a
	// scope without the retrieval side learning anything about directories.
	Tags []string
	// Location says where this is addressable.
	Location Location
	// Provenance records what produced it.
	Provenance Provenance
	// Revision carries its digest and timestamps.
	Revision Revision
	// Mutability is how this origin's content may be changed.
	Mutability Mutability
}

// IsAuthored reports whether this is corpus content. The corpus walk, the
// projection and the reconcile all branch on it, and it is the one distinction
// here that a wrong answer would be expensive for: authoring is what makes a
// document reviewable.
func (c Content) IsAuthored() bool { return c.Origin == OriginAuthored }

// SortedTags returns the content's tags in a deterministic order, deduplicated.
// A projection and a plan both have to be byte-stable, and map iteration order is
// not.
func SortedTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Digest returns the content digest of b in the form used throughout this
// package and recorded in the ownership record: "sha256:" followed by lowercase
// hex.
//
// The digest is the identity of a byte sequence, and nothing else about the
// filesystem is allowed to be. A file that was touched but not changed has a new
// modification time and the same digest, and a file that was changed and had its
// timestamp restored has a new digest and the same modification time; only the
// digest tells those two apart correctly.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseDigest returns the hex part of a digest, and reports whether the digest
// was well formed. A caller comparing digests must not compare malformed values
// for equality and conclude two different files match.
func ParseDigest(digest string) (hexPart string, ok bool) {
	rest, found := strings.CutPrefix(digest, "sha256:")
	if !found || len(rest) != sha256.Size*2 {
		return "", false
	}
	if _, err := hex.DecodeString(rest); err != nil {
		return "", false
	}
	return rest, true
}

// DocumentID builds the identifier a retained document is stored under.
//
// Namespace exists because two corpora reconciled into one base can each hold
// "Notes/todo.md", and a base that cannot tell those two documents apart has lost
// one of them. It is deliberately part of identity, and therefore part of the
// ownership binding: changing it re-extracts the whole corpus, which is a
// deliberate act rather than a surprise.
func DocumentID(namespace, relPath string) string {
	relPath = NormalizePath(relPath)
	if namespace == "" {
		return relPath
	}
	return strings.TrimSuffix(NormalizePath(namespace), "/") + "/" + relPath
}

// NormalizePath returns p as a slash-separated relative path.
//
// It is applied at every boundary where a path enters the system, because the
// identifier and the folder tags derived from a path are both compared against
// records written on another machine. A backslash on one host and a slash on
// another would otherwise produce two different documents out of one file, and
// the ownership record would cheerfully prune one of them.
func NormalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	return strings.Trim(p, "/")
}

// PathAncestors returns every ancestor folder of a relative path, cumulative and
// in order from shallowest to deepest, excluding the file itself. "a/b/c.md"
// yields "a" and "a/b".
//
// These become one tag each, which is what turns a directory convention into
// something retrieval can already filter on.
func PathAncestors(relPath string) []string {
	parts := strings.Split(NormalizePath(relPath), "/")
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

// PathFolder returns the immediate parent folder of a relative path, or the empty
// string at the root. It is recorded as content metadata, because a citation
// needs a field to point at and a filter does not.
func PathFolder(relPath string) string {
	parts := strings.Split(NormalizePath(relPath), "/")
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], "/")
}

// validateContent applies the invariants every returned Content must satisfy, so
// a handler cannot hand a caller a message this package would not accept back.
func validateContent(c Content) error {
	switch {
	case c.ID == "":
		return fmt.Errorf("knowledge: content has no id")
	case !c.Origin.Valid():
		return fmt.Errorf("knowledge: content %q has origin %q, which is not one of authored, retained, derived", c.ID, c.Origin)
	case c.HasBody && c.Body == "":
		return fmt.Errorf("knowledge: content %q claims to have a body and does not", c.ID)
	case c.Location.Kind == "":
		return fmt.Errorf("knowledge: content %q has no location kind", c.ID)
	case c.Location.Kind == LocationKindCorpusPath && c.Location.Path == "":
		return fmt.Errorf("knowledge: authored content %q has no corpus path", c.ID)
	case c.Location.Kind == LocationKindDocument && c.Location.Path == "":
		return fmt.Errorf("knowledge: retained content %q has no document id", c.ID)
	case c.Location.Kind == LocationKindPage && c.Location.PageID == "":
		return fmt.Errorf("knowledge: derived content %q has no page id", c.ID)
	}
	return nil
}
