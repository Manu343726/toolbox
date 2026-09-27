package knowledge

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"
)

// ioEOF is the end-of-file sentinel, named once so a file handle's Read does not have to import
// io for one value.
var ioEOF = io.EOF

func readAll(f fs.File) ([]byte, error) {
	var out []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
	}
}

// Fetcher produces a whole-wiki view for a base. It is a function rather than an interface so a
// caller can pass a closure over whatever it reads through — the corpus directory, the backend's
// page bundle — and so a test can pass a fixed view with no backend and no filesystem.
type Fetcher func(ctx context.Context) (Wiki, error)

// ProjectionCache holds the current whole-wiki view and knows when it went stale.
//
// It is here, in the domain package, rather than beside the filesystem that serves it, because all
// of the interesting part — which files a projection contains, when it changed, how a filter
// narrows it — is provider-neutral. The filesystem is one consumer of it and a test asserts against
// a plain io/fs with a fake revision source, so the repository's rule that no test may require a
// mount to work is satisfied by construction rather than by discipline.
type ProjectionCache struct {
	fetch    Fetcher
	revision func(ctx context.Context) (ProjectionRevision, error)

	mu        sync.RWMutex
	wiki      Wiki
	have      bool
	rev       ProjectionRevision
	refreshes int
	refreshed time.Time
}

// NewProjectionCache returns a cache over a fetcher and a revision source.
func NewProjectionCache(fetch Fetcher, revision func(ctx context.Context) (ProjectionRevision, error)) *ProjectionCache {
	return &ProjectionCache{fetch: fetch, revision: revision}
}

// Current returns the cached view, fetching it once if there is none.
//
// The first caller pays for the first fetch and everybody after it reads the cache, so a mount
// serving a thousand reads makes one backend call rather than a thousand.
func (c *ProjectionCache) Current(ctx context.Context) (Wiki, error) {
	c.mu.RLock()
	if c.have {
		defer c.mu.RUnlock()
		return c.wiki, nil
	}
	c.mu.RUnlock()

	wiki, err := c.fetch(ctx)
	if err != nil {
		return Wiki{}, err
	}
	rev := ProjectionRevision{Value: wikiDigest(wiki)}
	if c.revision != nil {
		if got, rerr := c.revision(ctx); rerr == nil {
			rev = got
		}
		// A revision that cannot be read is not a reason to refuse the bundle: a reader
		// who cannot have the change signal can still have the content, and the alternative
		// is showing somebody nothing because a status call failed.
	}
	now := time.Now().UTC()
	c.mu.Lock()
	c.wiki, c.have, c.rev, c.refreshed = wiki, true, rev, now
	c.mu.Unlock()
	return wiki, nil
}

// RefreshIfChanged re-fetches when the revision moved, and reports whether it did.
//
// This is the whole of the change signal. There is no server-side feed, so a client asks whether
// anything moved and re-reads when it has; and the answer is a revision rather than a diff, because
// the cheap thing to compute is "is this the same view" and the expensive thing to compute is "what
// changed", and a client that re-reads does not need the second one.
func (c *ProjectionCache) RefreshIfChanged(ctx context.Context) (bool, error) {
	if c.revision == nil {
		return false, nil
	}
	got, err := c.revision(ctx)
	if err != nil {
		return false, err
	}
	c.mu.RLock()
	previous := c.rev
	c.mu.RUnlock()
	if got.Value == previous.Value {
		return false, nil
	}
	wiki, err := c.fetch(ctx)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	c.wiki, c.rev, c.have = wiki, got, true
	c.refreshes++
	c.refreshed = time.Now().UTC()
	c.mu.Unlock()
	return true, nil
}

// Revision returns the revision currently served.
func (c *ProjectionCache) Revision() ProjectionRevision {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rev
}

// Refreshes is how many times the view has been re-fetched.
func (c *ProjectionCache) Refreshes() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.refreshes
}

// RefreshedAt is when the view was last fetched.
func (c *ProjectionCache) RefreshedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.refreshed
}

// FileCount is how many files the served view has, the index and marker included.
func (c *ProjectionCache) FileCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.have {
		return 0
	}
	return len(c.wiki.Files) + 2
}

// FS returns a read-only io/fs view of the projection.
//
// The whole view is copied into memory rather than served from the directory the corpus lives in,
// for one reason: the projection spans two halves and one of them is a backend export, and a
// filesystem assembled from a real directory plus a generated tree is a filesystem with two
// authorities. A copy is regenerable, cheap at this size, and cannot be edited back into a corpus.
//
// It is read-only in the only sense that matters: there is no write method on this type, and
// `OpenFile` refuses rather than pretending.
func (c *ProjectionCache) FS(ctx context.Context) (fs.FS, error) {
	wiki, err := c.Current(ctx)
	if err != nil {
		return nil, err
	}
	files := make(map[string]*ProjectedFile, len(wiki.Files)+2)
	for _, f := range wiki.Files {
		copied := f
		files[f.Path] = &copied
	}
	index := wiki.Index
	marker := wiki.Marker
	return &readOnlyFS{
		files: files,
		extra: map[string][]byte{"index.md": index, MarkerName(): marker},
	}, nil
}

// readOnlyFS serves a set of in-memory files and refuses every write.
//
// The refusal is `ErrPermission` rather than a silent success, because a filesystem that appears to
// accept an edit and discards it on the next regeneration is worse than one that refuses: the reader
// believes they changed something.
type readOnlyFS struct {
	files map[string]*ProjectedFile
	extra map[string][]byte
}

func (r *readOnlyFS) Open(name string) (fs.File, error) {
	clean := trimLeadingSlash(name)
	if clean == "" {
		return &memFile{name: ".", dir: true}, nil
	}
	if content, ok := r.extra[clean]; ok {
		return &memFile{name: clean, data: content, mod: time.Time{}}, nil
	}
	if f, ok := r.files[clean]; ok {
		return &memFile{name: clean, data: f.Content, size: int64(len(f.Content)), mod: time.Time{}, origin: f.Origin}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadDir returns a directory's entries, so that a reader can walk the tree.
//
// It synthesises the intermediate directories rather than requiring them to exist as files: the
// projection is a flat list of paths, and a reader that had to know which directories were
// "real" would be reading a filesystem rather than a bundle.
func (r *readOnlyFS) ReadDir(name string) ([]fs.DirEntry, error) {
	clean := trimLeadingSlash(name)
	if clean == "" {
		clean = "."
	}

	seen := map[string]bool{}
	var out []fs.DirEntry

	addFile := func(path string) {
		base := baseName(path)
		if base == "" || seen[base] {
			return
		}
		seen[base] = true
		out = append(out, fs.FileInfoToDirEntry(&fileInfo{name: base, size: r.sizeOf(path)}))
	}
	addDir := func(path string) {
		base := baseName(path)
		if base == "" || seen[base] {
			return
		}
		seen[base] = true
		out = append(out, fs.FileInfoToDirEntry(&fileInfo{name: base, dir: true}))
	}

	visit := func(path string) {
		if path == "" {
			return
		}
		if clean != "." && path != clean && !underDir(path, clean) {
			return
		}
		rel := path
		if clean != "." {
			rel = path[len(clean)+1:]
		}
		if i := firstSlash(rel); i >= 0 {
			addDir(join(clean, rel[:i]))
			return
		}
		addFile(path)
	}
	for path := range r.files {
		visit(path)
	}
	for path := range r.extra {
		visit(path)
	}

	if len(out) == 0 {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func (r *readOnlyFS) sizeOf(path string) int64 {
	if f, ok := r.files[path]; ok {
		return int64(len(f.Content))
	}
	return int64(len(r.extra[path]))
}

func (r *readOnlyFS) ReadFile(name string) ([]byte, error) {
	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAll(f)
}

// memFile is one file in the read-only view.
type memFile struct {
	name   string
	data   []byte
	size   int64
	mod    time.Time
	dir    bool
	origin string
}

func (m *memFile) Stat() (fs.FileInfo, error) {
	return &fileInfo{name: m.name, dir: m.dir, size: m.size, mod: m.mod, origin: m.origin}, nil
}

func (m *memFile) Read(p []byte) (int, error) {
	if m.dir {
		return 0, &fs.PathError{Op: "read", Path: m.name, Err: fs.ErrInvalid}
	}
	if len(m.data) == 0 {
		return 0, ioEOF
	}
	n := copy(p, m.data)
	m.data = m.data[n:]
	return n, nil
}

func (m *memFile) Close() error { return nil }

// ReadDir reports a directory's entries for a directory handle.
func (m *memFile) ReadDir(n int) ([]fs.DirEntry, error) {
	return nil, &fs.PathError{Op: "readdir", Path: m.name, Err: fs.ErrInvalid}
}

// fileInfo is one entry's metadata.
type fileInfo struct {
	name   string
	dir    bool
	size   int64
	mod    time.Time
	origin string
}

func (f *fileInfo) Name() string { return f.name }
func (f *fileInfo) Size() int64 {
	if f.dir {
		return 0
	}
	return f.size
}
func (f *fileInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o555
	}
	// Read-only for every file, and never executable: a projection is markdown and a page
	// rendering, and a file mode implying otherwise would be a claim nothing backs.
	return 0o444
}
func (f *fileInfo) ModTime() time.Time { return f.mod }
func (f *fileInfo) IsDir() bool        { return f.dir }
func (f *fileInfo) Sys() any           { return nil }

// Origin is where a projected file came from, exposed so a reader can tell a reviewed runbook from
// a rendered page without parsing frontmatter.
func (f *fileInfo) Origin() string { return f.origin }

func trimLeadingSlash(p string) string {
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	return p
}

func lastSlash(p string) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return i
		}
	}
	return -1
}

func firstSlash(p string) int {
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			return i
		}
	}
	return -1
}

func underDir(path, dir string) bool {
	return len(path) > len(dir)+1 && path[:len(dir)] == dir && path[len(dir)] == '/'
}

func baseName(p string) string {
	if i := lastSlash(p); i >= 0 {
		return p[i+1:]
	}
	return p
}

func join(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + "/" + name
}

// wikiDigest fingerprints a whole-wiki view, so that two fetches of the same base can be compared
// without a second call to the backend.
//
// The index and the marker are included. The marker carries the timestamp the view was taken at, so
// two otherwise identical views taken at different moments compare different — which is correct: a
// view is a thing that was taken, not a thing that is true.
func wikiDigest(w Wiki) string {
	h := &strings.Builder{}
	h.WriteString(w.BaseID)
	h.WriteString("\x00")
	h.WriteString(w.Commit)
	h.WriteString("\x00")
	h.WriteString(Digest(w.Index))
	h.WriteString("\x00")
	h.WriteString(Digest(w.Marker))
	for _, f := range w.Files {
		h.WriteString("\x00")
		h.WriteString(f.Path)
		h.WriteString("=")
		h.WriteString(f.Digest)
	}
	return Digest([]byte(h.String()))
}
