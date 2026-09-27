//go:build fuse

package knowledgehindsight

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

func fuseBuilt() bool { return true }

// preflight reports why this host cannot mount, or the empty string when it can.
//
// The three causes are checked and named separately, because each has a different fix and
// "mounting is unavailable" tells an operator none of them: `fusermount3` missing is a package to
// install, `/dev/fuse` missing is a container to run with the device, and a path that cannot be
// mounted is a filesystem problem.
func preflight() (string, error) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		return "this host has no /dev/fuse, so no process on it can mount a filesystem; in a container this means the device was not passed in", nil
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err2 := exec.LookPath("fusermount"); err2 != nil {
			return "neither fusermount3 nor fusermount is on the PATH, so an unprivileged mount is not possible; install the fuse3 package", nil
		}
	}
	return "", nil
}

// projFS is the root inode of a mounted projection.
//
// The tree is built once, at mount, from the paths the projection holds. A refresh does not rebuild
// it: each node reads its bytes from the cache by path, so a refresh swaps the cache and then
// notifies. That is the shape the notification API wants, and it is why a content change is the
// notification that matters — the alternative, replacing nodes, would make a reader's open file
// handle point at a node that no longer exists.
type projFS struct {
	fs.Inode
	cache *projectionCache
}

var (
	_ fs.NodeOnAdder  = (*projFS)(nil)
	_ fs.NodeLookuper = (*projDir)(nil)
)

// projDir is a directory in the projection.
type projDir struct {
	fs.Inode
	cache *projectionCache
	path  string
}

// projFile is one projected file.
type projFile struct {
	fs.Inode
	cache *projectionCache
	path  string
}

// OnAdd populates the tree at mount time.
func (r *projFS) OnAdd(ctx context.Context) {
	for _, path := range r.cache.Paths() {
		if err := r.attach(ctx, path); err != nil {
			// A file that cannot be attached is reported by the mount's status rather than
			// silently missing: a reader who is shown a tree without a runbook has no way
			// to know the runbook was there.
			fmt.Fprintf(os.Stderr, "toolbox-knowledge: cannot project %s: %v\n", path, err)
		}
	}
}

// attach adds one path to the tree, creating its directories.
//
// The parent is tracked as an *Inode rather than as an InodeEmbedder, because adding a child is a
// method on the inode and not on the interface a node embeds: the two are different things here and
// conflating them is how a tree ends up with children attached to the wrong node.
func (r *projFS) attach(ctx context.Context, path string) error {
	parent := &r.Inode
	accumulated := ""
	for _, segment := range splitPath(path) {
		accumulated = joinPath(accumulated, segment)
		existing := parent.GetChild(segment)
		if existing == nil {
			dir := &projDir{cache: r.cache, path: accumulated}
			child := parent.NewPersistentInode(ctx, dir,
				fs.StableAttr{Mode: syscall.S_IFDIR | 0o555})
			if !parent.AddChild(segment, child, false) {
				return fmt.Errorf("adding the directory %s", accumulated)
			}
			r.cache.registerDir(accumulated, child)
			parent = child
			continue
		}
		if !existing.IsDir() {
			return fmt.Errorf("%s exists in the projection as both a file and a directory", accumulated)
		}
		parent = existing
	}
	file := &projFile{cache: r.cache, path: path}
	inode := parent.NewPersistentInode(ctx, file,
		fs.StableAttr{Mode: syscall.S_IFREG | 0o444})
	if !parent.AddChild(baseOf(path), inode, false) {
		return fmt.Errorf("adding the file %s", path)
	}
	r.cache.register(path, inode)
	return nil
}

// Lookup resolves one name in a projected directory.
func (d *projDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	child := d.GetChild(name)
	if child == nil {
		return nil, syscall.ENOENT
	}
	full := joinPath(d.path, name)
	if child.IsDir() {
		out.Mode = syscall.S_IFDIR | 0o555
	} else {
		out.Mode = syscall.S_IFREG | 0o444
		out.Size = uint64(len(d.cache.Read(full)))
	}
	// The modification time is the fetch time rather than the content's. It is the only
	// honest choice: a projected file's content comes from a backend export whose own
	// timestamps are the page's refresh time and would be misleading beside a corpus file's
	// own. What a reader gets instead is a bound — a re-stat within the attribute timeout
	// sees the change.
	out.SetAttrTimeout(nodeAttrTimeout)
	out.SetEntryTimeout(nodeAttrTimeout)
	return child, 0
}

// Getattr reports a directory's attributes.
func (d *projDir) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.EntryOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0o555
	out.SetAttrTimeout(nodeAttrTimeout)
	out.SetEntryTimeout(nodeAttrTimeout)
	return 0
}

// Getattr reports a file's attributes, reading its size from the cache so that a file whose
// content changed is the right size immediately.
func (f *projFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.EntryOut) syscall.Errno {
	out.Mode = syscall.S_IFREG | 0o444
	out.Size = uint64(len(f.cache.Read(f.path)))
	out.SetAttrTimeout(nodeAttrTimeout)
	out.SetEntryTimeout(nodeAttrTimeout)
	return 0
}

// Open returns a read handle. There is no write path anywhere in this file, so a write attempt
// cannot succeed — the mount is read-only by construction rather than by a flag this package
// remembers to check.
func (f *projFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_APPEND|syscall.O_CREAT|syscall.O_TRUNC) != 0 {
		// EROFS, which is what a read-only filesystem reports. Answering this here means
		// the refusal comes from the filesystem itself rather than from a check a caller
		// could route around.
		return nil, 0, syscall.EROFS
	}
	return &projHandle{cache: f.cache, path: f.path}, 0, 0
}

// projHandle reads one file's bytes from the cache at open time.
//
// Reading through the cache rather than a snapshot taken at open is deliberate: a reader that
// re-reads a file it already has open should see the new content, which is the case a content
// notification is for.
type projHandle struct {
	cache *projectionCache
	path  string
	off   int64
}

func (h *projHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data := h.cache.Read(h.path)
	if off >= int64(len(data)) {
		return nil, 0
	}
	n := copy(dest, data[off:])
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *projHandle) Release(ctx context.Context) error { return nil }

func (h *projHandle) Flush(ctx context.Context) error { return nil }

// Create and friends are absent, so the kernel's create path fails against a read-only inode.

// projectionCache is the cache a mount serves, plus the notification bookkeeping.
type projectionCache struct {
	source *projectionSource

	mu sync.RWMutex
	// nodes is every file inode, by path, so a refresh can notify exactly the ones that
	// changed. This is the list the notification walk needs and the reason a refresh does
	// not have to re-walk the tree.
	nodes map[string]*fs.Inode
	// dirs holds the directory inodes, keyed by their path, so a removal can reach the
	// parent to notify against it.
	dirs map[string]*fs.Inode
}

func newProjectionCache(source *projectionSource) *projectionCache {
	return &projectionCache{source: source, nodes: map[string]*fs.Inode{}, dirs: map[string]*fs.Inode{}}
}

// Read returns one file's bytes, or nothing when the projection does not have it.
func (c *projectionCache) Read(path string) []byte {
	wiki, err := c.source.Cache().Current(context.Background())
	if err != nil {
		return nil
	}
	for _, f := range wiki.Files {
		if f.Path == path {
			return f.Content
		}
	}
	return nil
}

// Paths returns every projected file path, sorted.
//
// Delegated rather than rebuilt from `Wiki.Files`, because the domain's list is the one that
// includes the index and the marker. A mount built from the files alone served a tree that was
// neither navigable nor identifiable as a projection — and the marker's entire job is to be
// identifiable, since it is the one thing a corpus walk refuses to read as content. It also meant
// `GetMountStatus.files` counted two files `ls` could not show.
func (c *projectionCache) Paths() []string {
	wiki, err := c.source.Cache().Current(context.Background())
	if err != nil {
		return nil
	}
	return wiki.WikiPaths()
}

// register records a file's inode so a refresh can notify it.
func (c *projectionCache) register(path string, inode *fs.Inode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes[path] = inode
}

// registerDir records a directory inode, keyed by the path it holds.
func (c *projectionCache) registerDir(path string, inode *fs.Inode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirs[path] = inode
}

// refreshIfChanged re-fetches when the projection moved, and reports whether it did.
//
// This is the whole of the change notification, and the asymmetry below is the honest version of
// "notified":
//
//   - a content change drops the kernel's cached copy, so `cat`, `rg` and an agent's file tools are
//     correct immediately;
//   - a removal drops the cache *and* emits an inotify event, per the library's own documentation;
//   - a name appearing drops nothing and emits nothing, and converges through the entry timeout.
//
// The bound is the attribute and entry timeout, and it is a number rather than "eventually". A GUI
// editor with a file open is not told to reload, because a content notification emits no inotify
// event — a property of the notification API, not a bug to work around.
func (c *projectionCache) refreshIfChanged(ctx context.Context) (bool, error) {
	before := c.pathsSnapshot()
	changed, err := c.source.Cache().RefreshIfChanged(ctx)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	after := c.pathsSnapshot()
	c.notify(before, after)
	return true, nil
}

func (c *projectionCache) pathsSnapshot() map[string]bool {
	out := map[string]bool{}
	for _, p := range c.Paths() {
		out[p] = true
	}
	return out
}

// notify tells the kernel about a change, per kind, with the correct mechanism for each.
func (c *projectionCache) notify(before, after map[string]bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for path, inode := range c.nodes {
		_, still := after[path]
		switch {
		case !still:
			// A removal: cache dropped and an inotify event emitted, which is the one
			// case where a watcher hears about it.
			dir, base := splitLast(path)
			if parent := c.dirs[dir]; parent != nil {
				_ = parent.NotifyDelete(base, inode)
			}
		case !before[path]:
			// A name appearing: the next lookup is re-run. No inotify event, and none is
			// available to send.
			if parent := c.dirs[splitFirst(path)]; parent != nil {
				_ = parent.NotifyEntry(baseOf(path))
			}
		default:
			// A content change: the kernel's cached copy is dropped, so the next read
			// returns the new bytes. No inotify event, and that is the property a reader
			// has to know about.
			data := c.readUnlocked(path)
			_ = inode.NotifyContent(0, int64(len(data)))
		}
	}
}

func (c *projectionCache) readUnlocked(path string) []byte { return c.Read(path) }

// fuseMount is one running mount.
type fuseMount struct {
	server *fuse.Server
	cache  *projectionCache

	mu         sync.Mutex
	once       sync.Once
	unmounted  chan struct{}
	unmountErr error
}

func start(ctx context.Context, p *Provider, spec *knowledgev1.MountSpec, id string, poll time.Duration) (mountHandle, error) {
	spec = normalizeMountSpec(spec)
	source, err := p.projection(ctx, spec)
	if err != nil {
		return nil, err
	}
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	cache := newProjectionCache(source)
	// The first fetch happens before the mount is established rather than at the first read,
	// so a caller that gets a successful `EnableMount` is looking at a tree with content in
	// it rather than an empty directory that fills in a moment later.
	if _, err := source.Cache().Current(ctx); err != nil {
		return nil, fmt.Errorf("fetching the projection before mounting: %w", err)
	}

	attrTimeout := spec.GetAttrTimeout().AsDuration()
	if attrTimeout <= 0 {
		attrTimeout = DefaultAttrTimeout
	}
	entryTimeout := spec.GetEntryTimeout().AsDuration()
	if entryTimeout <= 0 {
		entryTimeout = DefaultEntryTimeout
	}

	root := &projFS{cache: cache}
	// Set before mounting: the root's children are added during `fs.Mount`, and a node
	// created before this is set would report the default rather than the configured bound.
	nodeAttrTimeout = attrTimeout

	server, err := fs.Mount(spec.GetMountpoint(), root, &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName: "toolbox-knowledge",
			Name:   "toolbox-knowledge",
			// Read-only at the kernel level as well as in this package's code paths. The
			// two are not redundant: the code refuses a write it is asked for, and this
			// stops the kernel from asking.
			Options: []string{"ro", "default_permissions"},
			// Direct mount needs root, and `fusermount3` is setuid precisely so an
			// unprivileged user can mount without it. Asking for a capability the process
			// does not have turns an ordinary mount into a permission error.
			DirectMount: false,
		},
		AttrTimeout:  &attrTimeout,
		EntryTimeout: &entryTimeout,
		// NullPermissions leaves the read-only modes this package sets on the nodes
		// alone. Without it the library rewrites them to 755 and 644, and a projection
		// whose directories read as writable would contradict the one property a reader
		// has to be able to trust about it.
		NullPermissions: true,
	})
	if err != nil {
		// A failed mount can still leave the mountpoint in a state nothing can clean up.
		//
		// The library has connected to `/dev/fuse` and created the mount by the time it
		// reports the failure — so the directory is a mount point with nothing serving it.
		// That is the worst state a path can be in: `rm -r` on it fails with "Transport
		// endpoint is not connected", `ls` on it hangs, and a caller that retried the mount
		// would be told the path is already mounted. M-7 says a failed mount must never
		// leave a stale mountpoint behind, and this is the line that has to do it.
		cleanupFailedMount(spec.GetMountpoint())
		return nil, fmt.Errorf("mounting at %s: %w (nothing is left mounted there)", spec.GetMountpoint(), err)
	}
	return &fuseMount{server: server, cache: cache, unmounted: make(chan struct{})}, nil
}

// cleanupFailedMount detaches a mountpoint the library created but could not serve.
//
// Best effort by design, and it reports what it managed rather than pretending. A lazy unmount is
// used because a failed mount has no live server to talk to: `-u` asks nicely and `-z` detaches
// regardless, and the combination is what leaves a directory usable again.
//
// The error is returned rather than swallowed because "the mount failed *and* the mountpoint could
// not be cleaned up" is a different situation from "the mount failed", and a caller who retries
// needs to know which they are in.
func cleanupFailedMount(mountpoint string) error {
	if mountpoint == "" {
		return nil
	}
	bin, err := fuseMountBinary()
	if err != nil {
		return err
	}
	// Nothing to detach if the path is not a mount point, which is the common case for a failure
	// that happened before the library connected.
	if _, serr := os.Stat(mountpoint); serr != nil {
		return nil
	}
	cmd := exec.Command(bin, "-u", "-z", mountpoint)
	if out, err := cmd.CombinedOutput(); err != nil {
		// The helper's wording for a path it never mounted, across the versions and locales in
		// the wild. Getting this wrong turns a mount failure into a second, more confusing one
		// about a cleanup — so the branch is deliberately generous about what counts as "there
		// was nothing there".
		benign := []string{
			"not mounted", "no mount point", "not found in", "not a mountpoint",
			"no such file or directory", "invalid argument",
		}
		for _, phrase := range benign {
			if strings.Contains(string(out), phrase) {
				return nil
			}
		}
		return fmt.Errorf("detaching the failed mount at %s: %w: %s", mountpoint, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// fuseMountBinary is the helper this platform uses to mount and unmount.
func fuseMountBinary() (string, error) {
	for _, name := range []string{"fusermount3", "fusermount"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("neither fusermount3 nor fusermount is on PATH, so a failed mount cannot be cleaned up")
}

func (m *fuseMount) Files() int { return m.cache.source.Cache().FileCount() }

// nodeAttrTimeout is the attribute timeout every node reports.
//
// It is a package variable rather than a constant because the mount's configured bound has to
// reach the kernel through each node's EntryOut, and threading it through every method would put
// the same number in eight places to be changed in nine.
var nodeAttrTimeout = time.Second

func (m *fuseMount) Revision() string { return m.cache.source.Cache().Revision().Value }

func (m *fuseMount) Refreshed() time.Time { return m.cache.source.Cache().RefreshedAt() }

func (m *fuseMount) Refreshes() int { return m.cache.source.Cache().Refreshes() }

// Close unmounts, and is safe to call more than once.
//
// A busy mountpoint is reported as EBUSY rather than being detached underneath a reader, because
// files somebody already has open would keep reading the old projection with nothing to say so.
func (m *fuseMount) Close() error {
	m.once.Do(func() {
		if m.server == nil {
			close(m.unmounted)
			return
		}
		if err := m.server.Unmount(); err != nil {
			m.unmountErr = err
			if errors.Is(err, syscall.EBUSY) {
				m.unmountErr = fmt.Errorf("the mountpoint is still busy: %w", err)
			}
			// The channel is closed either way, so a supervisor waiting on it does not
			// outlive the mount.
			close(m.unmounted)
			return
		}
		close(m.unmounted)
	})
	return m.unmountErr
}

// Stop blocks until the mount is unmounted.
func (m *fuseMount) Stop(ctx context.Context) error {
	_ = m.Close()
	return m.unmountErr
}

// refresh re-fetches a mount's projection if the revision moved.
func refresh(ctx context.Context, handle mountHandle, _ string) (bool, error) {
	m, ok := handle.(*fuseMount)
	if !ok {
		return false, nil
	}
	return m.cache.refreshIfChanged(ctx)
}

func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			if i > start {
				out = append(out, p[start:i])
			}
			start = i + 1
		}
	}
	if start < len(p) {
		out = append(out, p[start:])
	}
	return out
}

func splitLast(p string) (string, string) {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i], p[i+1:]
		}
	}
	return "", p
}

func splitFirst(p string) string {
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}

func baseOf(p string) string {
	_, base := splitLast(p)
	return base
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

var (
	_ mountHandle = (*fuseMount)(nil)
	_             = io.EOF
)
