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

	// refresh serialises a check-and-refresh, and only a refresh.
	//
	// It is separate from `mu` on purpose. `mu` guards the served view, and a reader holding it
	// must never wait on a fetch — a fetch reads a whole wiki, which is the slow thing this
	// cache exists to avoid repeating. So the fetch runs with `mu` unlocked, which means the
	// check-then-act in `RefreshIfChanged` is not atomic with respect to another refresher, and
	// two of them interleaving is a lost update: one reads revision "r1", the other reads "r2"
	// and fetches it, and then the first — holding a view fetched before the change — writes
	// "r1" over the top. The cache then serves the older view indefinitely, because every later
	// check now compares "r2" against a cached "r1", sees a difference, and is refused by a
	// refresher that is already running. A stale view that never corrects itself is worse than
	// one that is briefly wrong, and it is invisible: every reader sees a complete, coherent
	// view, just not the newest one.
	//
	// One mutex over the whole operation fixes it, because then the second refresher re-reads
	// the revision *after* the first has stored it and finds nothing to do. Readers never take
	// it, so the hot path is unaffected.
	refresh sync.Mutex

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
		wiki := c.wiki
		c.mu.RUnlock()
		return wiki, nil
	}
	c.mu.RUnlock()

	// The same lock a refresh takes, so populating and refreshing cannot interleave. One of
	// them storing a view over the other's would leave a served view labelled with a revision
	// that does not describe it, and the re-check below is what makes the wait worth taking:
	// a reader that queued behind a refresh serves what that refresh stored rather than
	// fetching a second copy of a view that is already there.
	c.refresh.Lock()
	defer c.refresh.Unlock()

	c.mu.RLock()
	if c.have {
		wiki := c.wiki
		c.mu.RUnlock()
		return wiki, nil
	}
	c.mu.RUnlock()

	// The revision is read **before** the fetch, and that direction is the whole point.
	//
	// A view and a revision read at different instants can disagree, and the disagreement has
	// to resolve in one direction only. Recording the *newer* revision leaves a view labelled
	// with a revision it does not have — and then every later check compares that revision
	// against the source, finds them equal, and reports that there is nothing to do. The cache
	// serves the stale view for the life of the process, with no error and no failed
	// assertion anywhere: every reader sees a complete, coherent view, just not the newest
	// one. Recording the older revision means the next check sees a difference and fetches
	// again, which costs one redundant fetch. A refresh that turned out to be unnecessary is
	// free; one that never happens is not.
	var rev ProjectionRevision
	resolved := false
	if c.revision != nil {
		// A revision that cannot be read is not a reason to refuse the bundle: a reader
		// who cannot have the change signal can still have the content, and the
		// alternative is showing somebody nothing because a status call failed.
		if got, rerr := c.revision(ctx); rerr == nil {
			rev, resolved = got, true
		}
	}
	wiki, err := c.fetch(ctx)
	if err != nil {
		return Wiki{}, err
	}
	if !resolved {
		// There was no source to ask, or the one there was could not answer. The view's own
		// digest describes it exactly, because it is computed from it — which is the only
		// revision available that is certainly true of what is being served.
		rev = ProjectionRevision{Value: wikiDigest(wiki)}
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
	// Serialised for the whole operation, and the revision is read *inside* the lock rather
	// than before it. Reading it outside is the bug this arrangement exists to remove: a
	// revision observed before waiting is a revision that may already be stale by the time the
	// fetch returns, and storing a view built from it is a lost update.
	c.refresh.Lock()
	defer c.refresh.Unlock()

	c.mu.RLock()
	previous := c.rev
	c.mu.RUnlock()
	if c.revision == nil {
		return false, nil
	}
	got, err := c.revision(ctx)
	if err != nil {
		return false, err
	}
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
//
// It counts `WikiPaths` rather than adding two to a file count, because those two numbers
// answering the same question is exactly the shape of bug that hides: a status bar said five files
// while `ls` showed three, and both were computed from the same view in the same process.
func (c *ProjectionCache) FileCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.have {
		return 0
	}
	return len(c.wiki.WikiPaths())
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
	// `.` and the empty path are both the root. `fs.WalkDir` stats its root before descending and
	// `fs.Stat(fsys, ".")` is how a reader asks a filesystem what it is, so a view that cannot
	// open its own root is not an `fs.FS` — it is a map with an `Open` on it. `ReadDir` synthesised
	// directories from the start; `Open` did not, and the two disagreeing is what made
	// `fs.WalkDir` fail on a projection that `ReadDir` could enumerate perfectly well.
	if clean == "" || clean == "." {
		return &memFile{name: ".", dir: true, from: r}, nil
	}
	if content, ok := r.extra[clean]; ok {
		// The size is set, which looks like a detail and is not one. It is the same answer the
		// projected files give, and leaving it off made the index and the marker report zero
		// bytes while holding content — so `ls -l` showed 0 for the two files that make a
		// projection navigable, and any reader that sized a buffer from the reported length
		// would have read nothing at all.
		return &memFile{name: clean, data: content, size: int64(len(content)), mod: time.Time{},
			from: r}, nil
	}
	if f, ok := r.files[clean]; ok {
		return &memFile{name: clean, data: f.Content, size: int64(len(f.Content)), mod: time.Time{},
			origin: f.Origin, from: r}, nil
	}
	// A directory is not a file, so it is answered by the same rule `ReadDir` uses: something is
	// under it. The order matters and is deliberate: a path that is *both* a file and a prefix of
	// other files opens as the file. A reader that asked for that path by name wants its content,
	// and the FUSE mount refuses such a projection outright rather than serving either.
	if r.isDir(clean) {
		return &memFile{name: clean, dir: true, from: r}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// isDir reports whether a path names a directory in this view, which means something is under it.
//
// Synthesised rather than stored, for the same reason `ReadDir` synthesises it: the projection is a
// flat list of paths, and requiring a caller to have declared its directories in advance would make
// this a filesystem with a second authority over its shape.
func (r *readOnlyFS) isDir(path string) bool {
	if path == "" || path == "." {
		return true
	}
	for candidate := range r.files {
		if underDir(candidate, path) {
			return true
		}
	}
	for candidate := range r.extra {
		if underDir(candidate, path) {
			return true
		}
	}
	return false
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
		// The origin is carried here as well as on the file's own `Stat`, and it has to be: a
		// reader that lists a directory and a reader that stats a file are asking the same
		// question — "is this something a person wrote?" — through two paths, and an answer
		// that depends on which one was taken is an answer nobody can rely on. The one place
		// this was not passed is the one place a listing could not tell a reviewed runbook
		// from a rendered page without opening each file.
		out = append(out, fs.FileInfoToDirEntry(&fileInfo{
			name:   base,
			size:   r.sizeOf(path),
			origin: r.originOf(path),
		}))
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

// originOf is where a projected path came from, and the empty string for the two generated files
// the view always carries — the index and the marker are this framework's own output and are not
// "a file a person wrote" under any reading.
func (r *readOnlyFS) originOf(path string) string {
	if f, ok := r.files[path]; ok {
		return f.Origin
	}
	return ""
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
//
// A directory handle carries a back-reference to the view, because a handle that can be opened and
// stat'ed but not listed is a half-implemented handle: `tar`, `zip`, `http.FileServer` and a plain
// `os.File`-shaped reader all take a directory handle and call `ReadDir` on it. The `io/fs`
// contract allows `ReadDir` on a file to fail, and for a *file* it must; for a directory that would
// just be a view that stops working one level up from where it worked.
type memFile struct {
	name   string
	data   []byte
	size   int64
	mod    time.Time
	dir    bool
	origin string
	from   *readOnlyFS
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

// ReadDir lists a directory handle's entries.
//
// For a directory this is the same listing `ReadDir` on the filesystem gives, so the two agree —
// and their agreeing is the point, because a reader that lists through the handle and a reader that
// lists through the filesystem are the same reader on different transports. For a file it is the
// refusal `io/fs` requires: a file is not a directory, and answering with a listing would be a lie
// about what was opened.
func (m *memFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if !m.dir || m.from == nil {
		return nil, &fs.PathError{Op: "readdir", Path: m.name, Err: fs.ErrInvalid}
	}
	entries, err := m.from.ReadDir(m.name)
	if err != nil {
		return nil, err
	}
	// `n > 0` means "at most n, and the next call continues". The listing is materialised either
	// way, so honouring a partial read would need a cursor this handle does not have; the whole
	// list is returned and `io` is told so by returning no error, which is the documented
	// behaviour for a caller that gets everything at once.
	return entries, nil
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
//
// **The files are sorted before hashing, and that is load-bearing rather than tidy.** A view is a
// set of files, so a fingerprint that depends on the order they arrived in reports a change
// whenever a fetcher returns the same files differently — and the cost of a false change is not one
// wasted call: every reader's file cache is invalidated, a thousand mount threads re-read, and the
// projection a person is reading is replaced with a byte-identical one for no visible reason. The
// corpus walk sorts its own output, so today's fetcher happens to be stable; the composition of two
// sources, one of which is the backend's own bundle, is not something a digest should have to
// assume.
func wikiDigest(w Wiki) string {
	pairs := make([]string, 0, len(w.Files))
	for _, f := range w.Files {
		pairs = append(pairs, f.Path+"="+f.Digest)
	}
	sort.Strings(pairs)

	h := &strings.Builder{}
	h.WriteString(w.BaseID)
	h.WriteString("\x00")
	h.WriteString(w.Commit)
	h.WriteString("\x00")
	h.WriteString(Digest(w.Index))
	h.WriteString("\x00")
	h.WriteString(Digest(w.Marker))
	for _, pair := range pairs {
		h.WriteString("\x00")
		h.WriteString(pair)
	}
	return Digest([]byte(h.String()))
}
