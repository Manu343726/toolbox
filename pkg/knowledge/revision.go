package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// half names one side of a projection, for the caller to learn which one moved.
type half string

const (
	halfAuthored half = "authored"
	halfDerived  half = "derived"
	halfBoth     half = "both"
)

// ProjectionRevision is a fingerprint of a projection, and which halves of it moved.
//
// There is no server-side change feed behind this. A change signal is therefore a
// revision rather than a diff: one cheap call reports whether anything moved, and
// a client that cares re-fetches. A stream would be the only streaming contract
// in the framework for a feature a short poll serves, which is a cost paid by
// every deployment to serve the one that needs it.
type ProjectionRevision struct {
	// Value is the digest of the projection's state.
	Value string
	// BaseID is the base this revision describes.
	BaseID string
	// Which names the half that moved since the previous revision, when the
	// caller supplied one to compare against.
	Which string
	// Authored is the fingerprint of the corpus half.
	Authored string
	// Derived is the fingerprint of the engine half.
	Derived string
	// Commit is the corpus revision the authored half reflects.
	Commit string
	// Taken is when the probes ran.
	Taken time.Time
	// StalePages counts derived pages the backend reports as not refreshed.
	StalePages int
	// FileCount and PageCount are what the two probes saw, so a caller can tell
	// an empty base from a failed one.
	FileCount int
	PageCount int
}

// PageNode is what the engine-side probe needs from a page tree, and the whole
// of what it reads.
//
// A page tree already carries each node's staleness, its last refresh and its
// last refresh failure, so a refresh that has not happened yet is visible without
// re-exporting a single page. That is what makes the derived probe one small call
// rather than a full export.
type PageNode struct {
	ID                  string
	TreePath            string
	IsStale             bool
	Timestamp           time.Time
	LastRefreshFailedAt time.Time
}

// RevisionSource produces a projection revision.
type RevisionSource struct {
	corpus *Corpus
	// treeOf fetches the engine's page tree. It is a function rather than an
	// interface so that a caller can pass a closure over whatever generated client
	// it has, and so that a test can pass a function returning a fixed tree with
	// no server at all.
	treeOf func(ctx context.Context, baseID string) ([]PageNode, error)
	now    func() time.Time
}

// NewRevisionSource returns a revision source over a corpus and a page-tree
// fetch.
func NewRevisionSource(c *Corpus, treeOf func(ctx context.Context, baseID string) ([]PageNode, error)) *RevisionSource {
	return &RevisionSource{corpus: c, treeOf: treeOf, now: time.Now}
}

// WithClock injects the clock. The two probes are metadata comparisons and the
// whole point is that they are cheap; a test that asserted on a wall clock would
// be asserting on the machine it ran on.
func (s *RevisionSource) WithClock(now func() time.Time) *RevisionSource {
	s.now = now
	return s
}

// Revision computes the current projection revision for a base.
//
// The two probes are chosen for cost, not for completeness.
//
// The authored half is a directory stat per file: modification time and size,
// never a content digest. At three thousand files a digest per file is a full
// read of the corpus on every poll, and the projection is not changing because
// somebody touched a file without editing it.
//
// The derived half is one call returning the page tree, whose per-node staleness
// and refresh timestamps are what changes when the engine's beliefs change.
func (s *RevisionSource) Revision(ctx context.Context, baseID, commit string) (ProjectionRevision, error) {
	authored, fileCount, err := s.authoredFingerprint(ctx)
	if err != nil {
		return ProjectionRevision{}, err
	}

	nodes, err := s.treeOf(ctx, baseID)
	if err != nil {
		// The engine half failing is not a failure to report a revision: a mount
		// whose corpus changed should still notice. It reports a derived half
		// that says "unknown", which differs from the last known value and so
		// causes a re-fetch — the right outcome, since the page tree may well have
		// changed while the call was failing.
		return ProjectionRevision{
			Value:     digestOf(authored, "unavailable"),
			BaseID:    baseID,
			Authored:  authored,
			Derived:   "unavailable",
			Commit:    commit,
			Taken:     s.now().UTC(),
			FileCount: fileCount,
		}, nil
	}

	derived, stale := derivedFingerprint(nodes)
	return ProjectionRevision{
		Value:      digestOf(authored, derived),
		BaseID:     baseID,
		Authored:   authored,
		Derived:    derived,
		Commit:     commit,
		Taken:      s.now().UTC(),
		StalePages: stale,
		PageCount:  len(nodes),
		FileCount:  fileCount,
	}, nil
}

// Compare reports which half of a revision moved since a previous one.
func (r ProjectionRevision) Compare(previous ProjectionRevision) string {
	switch {
	case previous.Value == "":
		return string(halfBoth)
	case previous.Authored != r.Authored && previous.Derived != r.Derived:
		return string(halfBoth)
	case previous.Authored != r.Authored:
		return string(halfAuthored)
	case previous.Derived != r.Derived:
		return string(halfDerived)
	default:
		return ""
	}
}

// authoredFingerprint hashes every in-scope file's path, size and modification
// time.
//
// It returns the digest and how many files it saw, because "the corpus is
// unchanged" and "the corpus has no files" must be distinguishable by a caller
// debugging a mount that shows nothing.
// probeLine is one file's cheap metadata. It is the whole of what the authored
// probe reads, and the reason it is cheap.
type probeLine struct {
	root, path string
	size       int64
	mod        time.Time
}

func (s *RevisionSource) authoredFingerprint(ctx context.Context) (string, int, error) {
	var all []probeLine

	for _, root := range s.corpus.Roots() {
		probe, err := s.probeRoot(ctx, root)
		if err != nil {
			return "", 0, err
		}
		all = append(all, probe...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].root != all[j].root {
			return all[i].root < all[j].root
		}
		return all[i].path < all[j].path
	})

	var b strings.Builder
	for _, l := range all {
		fmt.Fprintf(&b, "%s\x00%s\x00%d\x00%d\n", l.root, l.path, l.size, l.mod.UTC().UnixNano())
	}
	return digestOf(b.String()), len(all), nil
}

func (s *RevisionSource) probeRoot(ctx context.Context, root CorpusRoot) ([]probeLine, error) {
	var out []probeLine
	// An unreadable directory must not look like an empty one. A projection that
	// silently dropped half a corpus because one directory was unreadable would
	// show a reader a smaller wiki and call it current.
	err := filepath.WalkDir(root.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("probing %s: %w", p, err)
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if p == root.Path {
			return nil
		}
		rel := NormalizePath(p[len(root.Path):])
		if rel == "" {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !isMarkdownName(d.Name()) {
			return nil
		}
		if !s.corpus.inScope(rel) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("probing %s: %w", rel, ierr)
		}
		out = append(out, probeLine{root.Name, rel, info.Size(), info.ModTime().UTC()})
		return nil
	})
	return out, err
}

// derivedFingerprint hashes the page tree's identity and staleness, and counts
// the stale pages.
func derivedFingerprint(nodes []PageNode) (string, int) {
	sorted := append([]PageNode(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var b strings.Builder
	stale := 0
	for _, n := range sorted {
		fmt.Fprintf(&b, "%s\x00%s\x00t=%t\x00%s\x00%s\n",
			n.TreePath, n.ID, n.IsStale,
			n.Timestamp.UTC().Format(time.RFC3339Nano),
			n.LastRefreshFailedAt.UTC().Format(time.RFC3339Nano))
		if n.IsStale {
			stale++
		}
	}
	return digestOf(b.String()), stale
}

func digestOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// statFile reports whether a named entry exists in a directory. It exists so
// that the projection marker check has one definition rather than two, one of
// which would be a test double.
func statFile(dir, name string) (fs.FileInfo, error) {
	return os.Stat(filepath.Join(dir, name))
}

// DescribeRevision renders a revision as one line, for a log or a status bar.
func DescribeRevision(r ProjectionRevision) string {
	parts := []string{
		"base=" + strconv.Quote(r.BaseID),
		"value=" + shortDigest(r.Value),
		"files=" + strconv.Itoa(r.FileCount),
		"pages=" + strconv.Itoa(r.PageCount),
	}
	if r.Commit != "" {
		parts = append(parts, "commit="+shortCommit(r.Commit))
	}
	if r.StalePages > 0 {
		parts = append(parts, "stale="+strconv.Itoa(r.StalePages))
	}
	if r.Which != "" {
		parts = append(parts, "changed="+r.Which)
	}
	return strings.Join(parts, " ")
}

func shortDigest(d string) string {
	hexPart, ok := ParseDigest(d)
	if !ok {
		return d
	}
	return hexPart[:12]
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
