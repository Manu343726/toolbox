package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// binarySniffBytes is how much of a file is examined for a NUL byte when
// classifying it. It is the same window git uses for the same purpose, and a
// markdown file has no legitimate reason to contain one in its first few
// kilobytes.
const binarySniffBytes = 8000

// defaultMaxFileBytes bounds what one corpus file may cost. A base is rebuilt
// from a repository, so a file that is accidentally a build artefact would
// otherwise be read into memory and handed to an extraction step.
const defaultMaxFileBytes = 4 << 20

// CorpusRoot is one directory of markdown that a base reconciles from.
type CorpusRoot struct {
	// Path is the directory on disk.
	Path string
	// Name namespaces identifiers from this root.
	//
	// It exists because two roots reconciled into one base can each hold
	// "Notes/todo.md", and a base that cannot tell those two documents apart has
	// silently lost one of them. It is part of identity and therefore part of
	// the ownership binding, so changing it is a deliberate act that
	// re-extracts the corpus rather than a quiet renumbering.
	Name string
}

// CorpusOptions configures what a walk considers part of the corpus.
type CorpusOptions struct {
	// Include restricts the walk to these folder prefixes. Empty means
	// everything. A path is included when it starts with one of them.
	Include []string
	// Exclude removes these folder prefixes. It is applied after Include, so
	// Exclude wins, and narrowing scope is what legitimately prunes what the
	// previous scope owned.
	Exclude []string
	// MaxFileBytes bounds one file. Zero uses the default.
	MaxFileBytes int64
}

// Corpus is a set of roots and the rules for walking them.
//
// It knows nothing about any backend. Everything it produces is a fact about a
// directory, which is what lets the reconcile be tested against a temporary
// directory and no server.
type Corpus struct {
	roots  []CorpusRoot
	opts   CorpusOptions
	maxLen int64
}

// NewCorpus validates the roots and returns a walker.
//
// A root with no name is refused rather than defaulted from its directory,
// because the default would depend on where somebody cloned the repository —
// which is exactly the property the identifier namespace exists to avoid.
func NewCorpus(roots []CorpusRoot, opts CorpusOptions) (*Corpus, error) {
	if len(roots) == 0 {
		return nil, errors.New("knowledge: a corpus needs at least one root")
	}
	seenRoot := make(map[string]struct{}, len(roots))
	seenName := make(map[string]struct{}, len(roots))
	for i, r := range roots {
		switch {
		case strings.TrimSpace(r.Path) == "":
			return nil, fmt.Errorf("knowledge: corpus root %d has no path", i)
		case strings.TrimSpace(r.Name) == "":
			return nil, fmt.Errorf("knowledge: corpus root %q has no name; the name namespaces identifiers and cannot be inferred from a path that depends on where the repository was cloned", r.Path)
		}
		abs, err := filepath.Abs(r.Path)
		if err != nil {
			return nil, fmt.Errorf("knowledge: corpus root %q: %w", r.Path, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("knowledge: corpus root %q: %w", r.Path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("knowledge: corpus root %q is not a directory", r.Path)
		}
		// The one artifact in this system that looks exactly like a corpus and is
		// not one is a projection, and a projection is regenerable. Reading one as
		// a corpus would make a generation of the system reason partly from the
		// previous generation's own output, which is the one thing the derived
		// layer must never become. The check is here rather than in a doc comment
		// because an enforcement that depends on a caller not pointing a reconcile
		// at the wrong directory is not an enforcement.
		if IsProjectionRoot(abs) {
			return nil, fmt.Errorf(
				"knowledge: corpus root %q is a generated projection, marked by %s; reconciling it would ingest the system's own output as authored documentation. Point the corpus at the reviewed markdown in your repository instead",
				r.Path, projectionMarkerName)
		}
		roots[i].Path = abs
		if _, dup := seenRoot[abs]; dup {
			return nil, fmt.Errorf("knowledge: corpus root %q is listed twice", abs)
		}
		seenRoot[abs] = struct{}{}
		if _, dup := seenName[r.Name]; dup {
			return nil, fmt.Errorf("knowledge: two corpus roots are both named %q; their identifiers would collide", r.Name)
		}
		seenName[r.Name] = struct{}{}
	}
	max := opts.MaxFileBytes
	if max <= 0 {
		max = defaultMaxFileBytes
	}
	return &Corpus{roots: roots, opts: opts, maxLen: max}, nil
}

// Roots returns the configured roots, with their paths made absolute.
func (c *Corpus) Roots() []CorpusRoot { return append([]CorpusRoot(nil), c.roots...) }

// File is one file in the corpus, after it has been read.
type File struct {
	// ID is the document identifier: the declared frontmatter id when there is
	// one, and the namespaced path otherwise.
	ID string
	// DeclaredID reports whether ID came from the file's frontmatter rather than
	// from its location. It is the difference between a move and a delete.
	DeclaredID bool
	// SourceKey is this file's stable key in the ownership record. It is the
	// root name and the relative path, and it does not change when the file
	// declares a different id — which is what lets a changed id be detected as a
	// rename of identity rather than as a deletion.
	SourceKey string
	// Root is the name of the root this file came from.
	Root string
	// Path is slash-separated and relative to its root.
	Path string
	// FullPath is the file on disk, for the caller to show or open.
	FullPath string
	// Front is the file's declared metadata.
	Front Frontmatter
	// Title is the declared title, or the first markdown heading, or the
	// filename.
	Title string
	// Body is the markdown below the frontmatter.
	Body string
	// Raw is the file's bytes exactly as read, which is what a projection copies
	// and what the digest is taken over.
	Raw []byte
	// Digest is the digest of Raw.
	Digest string
	// ModTime and Size are the file's own.
	ModTime time.Time
	Size    int64
	// Binary reports that the file is not text. A binary file has no
	// frontmatter, no body and no declared identity, and is ingested through the
	// backend's binary path rather than the text one.
	Binary bool
	// Read reports whether this walk actually read the file.
	//
	// It is false when the modification-time pre-filter skipped it, and Raw,
	// Body and everything derived from them are then empty. That emptiness is
	// deliberate and marked, because the alternative — a File whose Body is
	// empty for two indistinguishable reasons — is how a reconcile ingests an
	// empty document or a projection writes a blank page.
	Read bool
}

// Location returns where this file is addressable as authored content.
func (f File) Location() Location {
	return Location{Kind: LocationKindCorpusPath, Path: f.Path}
}

// Revision returns the file's revision as this package records it.
func (f File) Revision() Revision {
	return Revision{
		Digest:  f.Digest,
		ModTime: f.ModTime,
		Size:    f.Size,
	}
}

// SourceKey builds the ownership record key for a root name and relative path.
func SourceKey(rootName, relPath string) string {
	return rootName + ":" + NormalizePath(relPath)
}

// WalkOptions configures one walk.
type WalkOptions struct {
	// Prior is the ownership record from the last successful reconcile. It
	// enables the modification-time pre-filter and nothing else: the digest in
	// the record is trusted only to the extent the pre-filter below explains.
	Prior *OwnershipRecord
	// TrustModTime enables the pre-filter. It is on by default because an
	// unchanged file is the common case and reading is the expensive part.
	//
	// The residual risk is stated rather than hidden: a file whose contents
	// changed while both its modification time and its size were left exactly as
	// they were will be reported unchanged. Restoring an old revision of a file
	// with `touch -r` does precisely that. Callers that must not accept that set
	// this false, and pay a read per file.
	TrustModTime bool
	// FollowBinary includes binary files in the result instead of skipping them.
	// They are classified either way; this decides whether they are returned.
	FollowBinary bool
}

// WalkResult is what one walk produced.
type WalkResult struct {
	// Files is every file in scope, in a stable order: by root name, then path.
	// A stable order is not cosmetic — a plan and a projection both have to be
	// byte-identical between two runs over an unchanged directory.
	Files []File
	// Warnings are conditions a person should know about that are not failures:
	// a file too large to read, a file the walk could not stat. A walk that
	// cannot read a file says so rather than treating the file as absent,
	// because "absent" is a deletion and a deletion is acted on.
	Warnings []string
	// Unchanged counts files the pre-filter skipped, which is reported so a
	// caller can tell a cheap run from a thorough one.
	Unchanged int
}

// Walk reads the corpus.
//
// The two stages are the ones the shipped Hindsight client uses and the reason
// they are worth copying: a file whose modification time and size match the
// record is skipped without being read at all, and the digest in the record is
// reused. A file whose modification time moved is read, and its digest decides
// whether anything actually changed. The timestamp avoids work; the digest
// decides.
func (c *Corpus) Walk(ctx context.Context, opts WalkOptions) (WalkResult, error) {
	var out WalkResult

	for _, root := range c.roots {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		files, warnings, skipped, err := c.walkRoot(ctx, root, opts)
		if err != nil {
			return out, err
		}
		out.Files = append(out.Files, files...)
		out.Warnings = append(out.Warnings, warnings...)
		out.Unchanged += skipped
	}

	sort.Slice(out.Files, func(i, j int) bool {
		if out.Files[i].Root != out.Files[j].Root {
			return out.Files[i].Root < out.Files[j].Root
		}
		return out.Files[i].Path < out.Files[j].Path
	})
	sort.Strings(out.Warnings)
	return out, nil
}

func (c *Corpus) walkRoot(ctx context.Context, root CorpusRoot, opts WalkOptions) ([]File, []string, int, error) {
	var files []File
	var warnings []string
	skipped := 0

	walkErr := filepath.WalkDir(root.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is a warning, not a silent absence.
			warnings = append(warnings, fmt.Sprintf("cannot read %s: %v", p, err))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		// "." on the root itself.
		if p == root.Path {
			return nil
		}
		relPath := NormalizePath(p[len(root.Path):])
		if relPath == "" {
			return nil
		}
		name := d.Name()

		if d.IsDir() {
			// Dot-directories hold configuration and history — ".git", editor
			// state, a trash directory. They are never corpus content, and a
			// repository's object store is emphatically not documentation.
			if strings.HasPrefix(name, ".") && name != "." {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Symlinks, sockets and devices are not corpus content. Following a
			// symlink out of the tree would let a reconcile ingest whatever it
			// points at, from outside the reviewed repository.
			return nil
		}
		if !isMarkdownName(name) {
			return nil
		}
		if !c.inScope(relPath) {
			return nil
		}

		info, ierr := d.Info()
		if ierr != nil {
			warnings = append(warnings, fmt.Sprintf("cannot stat %s: %v", relPath, ierr))
			return nil
		}
		if info.Size() > c.maxLen {
			warnings = append(warnings, fmt.Sprintf(
				"%s is %d bytes, over the %d byte limit; it was not read and is not in this reconcile",
				relPath, info.Size(), c.maxLen))
			return nil
		}

		key := SourceKey(root.Name, relPath)

		// Stage one: the cheap filter. It does not read the file, and it does not
		// verify the digest — that is the trade being made, and WalkOptions states
		// what it costs.
		if opts.TrustModTime && opts.Prior != nil {
			if prior, ok := opts.Prior.Files[key]; ok &&
				prior.Size == info.Size() && prior.ModTime.Equal(info.ModTime().UTC()) {
				skipped++
				if prior.Binary && !opts.FollowBinary {
					return nil
				}
				files = append(files, fileFromRecord(root, relPath, key, info, prior))
				return nil
			}
		}

		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			warnings = append(warnings, fmt.Sprintf("cannot read %s: %v", relPath, rerr))
			return nil
		}
		f := File{
			SourceKey: key,
			Root:      root.Name,
			Path:      relPath,
			FullPath:  p,
			Raw:       raw,
			Digest:    Digest(raw),
			ModTime:   info.ModTime().UTC(),
			Size:      info.Size(),
			Binary:    looksBinary(raw),
			Read:      true,
		}
		if !f.Binary {
			parsed, perr := ParseFile(raw)
			if perr != nil {
				// A file with an unusable declared value is a failure, not a
				// warning: ingesting it would put a filter in the base that
				// matches nothing, and skipping it silently would delete it.
				return fmt.Errorf("%s/%s: %w", root.Name, relPath, perr)
			}
			f.Front = parsed.Front
			f.Body = parsed.Body
		}
		// Identity is read from the file, never looked up. A declared id is the
		// identity; without one it is the namespaced path, which is what makes a
		// move of a file that declares no id a delete and a create rather than a
		// move — and it is the reason the record has to carry the id forward for
		// a file the cheap filter skipped.
		f.ID, f.DeclaredID = IdentityFor(f.Front.ID, root.Name, relPath)
		if f.Binary && !opts.FollowBinary {
			skipped++
			return nil
		}
		f.Title = f.DerivedTitle()
		files = append(files, f)
		return nil
	})
	if walkErr != nil {
		return nil, warnings, skipped, walkErr
	}
	return files, warnings, skipped, nil
}

// fileFromRecord rebuilds a File without reading it, from the recorded revision
// of a file whose modification time and size did not move.
//
// The identity is the one the file declared when it was last read, which is
// exactly why the record carries it: a declared id is not knowable without the
// file, so a walk that skipped the read has to take the identity from somewhere.
//
// Raw and Body are therefore empty and Read is false. A caller that needs the
// bytes of an unchanged file must read them itself; the walk has declined to,
// and says so rather than leaving an empty body to be mistaken for an empty
// document.
func fileFromRecord(root CorpusRoot, relPath, key string, info fs.FileInfo, prior OwnershipEntry) File {
	return File{
		ID:         prior.ID,
		DeclaredID: prior.DeclaredID,
		SourceKey:  key,
		Root:       root.Name,
		Path:       relPath,
		FullPath:   filepath.Join(root.Path, filepath.FromSlash(relPath)),
		Digest:     prior.Digest,
		ModTime:    info.ModTime().UTC(),
		Size:       info.Size(),
		Binary:     prior.Binary,
		Read:       false,
		// Title is carried from the record rather than re-derived, because
		// deriving it means reading the file, which is what was just avoided.
		Title: prior.Title,
		Front: prior.Front,
	}
}

// inScope applies include and exclude folder prefixes, with exclude winning.
func (c *Corpus) inScope(relPath string) bool {
	prefixed := underAny(relPath, c.opts.Exclude)
	if prefixed {
		return false
	}
	if len(c.opts.Include) == 0 {
		return true
	}
	return underAny(relPath, c.opts.Include)
}

func underAny(relPath string, prefixes []string) bool {
	for _, p := range prefixes {
		p = NormalizePath(p)
		if p == "" {
			continue
		}
		if relPath == p || strings.HasPrefix(relPath, p+"/") {
			return true
		}
	}
	return false
}

// isMarkdownName reports whether a filename is corpus content.
//
// The extension is compared case-insensitively because a repository written on
// one operating system gets checked out on another, and ".MD" is the same
// document as ".md". A repository whose identifiers changed with the filesystem
// would be a repository nobody could reason about.
func isMarkdownName(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".md")
}

// looksBinary reports whether raw is not text, by looking for a NUL byte in the
// first few kilobytes. The whole file is not scanned: a file can be a megabyte
// and the answer is decided in the first window or not at all.
func looksBinary(raw []byte) bool {
	window := raw
	if len(window) > binarySniffBytes {
		window = window[:binarySniffBytes]
	}
	return strings.IndexByte(string(window), 0) >= 0
}

// Title derives a display title for a file: the declared title if there is one,
// then the first markdown heading, then a readable form of the filename.
func (f File) DerivedTitle() string {
	if f.Front.Title != "" {
		return f.Front.Title
	}
	if h := firstHeading(f.Body); h != "" {
		return h
	}
	base := filepath.Base(f.Path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// firstHeading returns the text of the first level-one ATX heading in body, or
// the empty string. It is a display convenience and deliberately shallow: a file
// that starts with a paragraph and mentions a heading later has no title, and
// inventing one by scanning for any heading would produce a title from an
// example section.
func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "# "))
	}
	return ""
}
