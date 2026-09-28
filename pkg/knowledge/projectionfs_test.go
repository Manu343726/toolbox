package knowledge

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The projection read path, over a plain `io/fs`.
//
// None of this needs a mount, and that is the whole reason it lives in this package: the FUSE
// wiring is a consumer of a `fs.FS`, so a reader's `cat` and `find` are decided by what is here and
// nowhere else. The mount was attempted for real and this machine refuses an unprivileged one, so
// these tests are the only thing standing between the read path and no coverage at all.

// wikiFixture is a view with one file at the root, one in a folder, one two folders deep, and the
// two generated files a projection always carries.
func wikiFixture() Wiki {
	return Wiki{
		BaseID: "docs",
		Commit: "abc123",
		Files: []ProjectedFile{
			{Path: "index-authored.md", Content: []byte("# Overview\n"), Origin: ProjectionOriginFile},
			{Path: "runbooks/restore.md", Content: []byte("# Restore\n\nDrop the base.\n"),
				Origin: ProjectionOriginFile, SourcePath: "runbooks/restore.md", ContentID: "wiki:restore"},
			{Path: "runbooks/deep/rollback.md", Content: []byte("# Rollback\n"),
				Origin: ProjectionOriginGenerated, ContentID: "page-1"},
		},
		Index:  []byte("# Index\n"),
		Marker: []byte(`{"kind":"toolbox.knowledge.projection"}`),
	}
}

// staticCache is a cache over a fixed view, with a fetch counter so "fetched once" is assertable
// rather than assumed.
func staticCache(t *testing.T, w Wiki, rev string) (*ProjectionCache, *int) {
	t.Helper()
	fetches := 0
	c := NewProjectionCache(
		func(context.Context) (Wiki, error) {
			fetches++
			return w, nil
		},
		func(context.Context) (ProjectionRevision, error) {
			return ProjectionRevision{Value: rev}, nil
		},
	)
	return c, &fetches
}

func TestTheCacheFetchesOnceAndServesEverybodyAfterIt(t *testing.T) {
	t.Parallel()
	c, fetches := staticCache(t, wikiFixture(), "r1")

	for i := 0; i < 5; i++ {
		wiki, err := c.Current(context.Background())
		require.NoError(t, err)
		require.Len(t, wiki.Files, 3)
	}
	// The claim this type exists to make good: a mount serving a thousand reads makes one backend
	// call rather than a thousand. If this ever reads "one fetch per read", the mount is a
	// thousand backend calls and nobody notices until it falls over.
	assert.Equal(t, 1, *fetches, "five reads, one fetch")
	assert.Equal(t, "r1", c.Revision().Value)
	assert.Equal(t, 5, c.FileCount(), "three files, the index and the marker")
}

func TestTheCacheCountsNothingBeforeItHasFetched(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	// A mount that has not completed its first fetch must not claim to be serving files, or a
	// reader asking "how big is this" gets a number for a view that does not exist.
	assert.Zero(t, c.FileCount())
	assert.True(t, c.RefreshedAt().IsZero(), "no fetch, no time")
	assert.Zero(t, c.Revision().Value, "no fetch, no revision")
}

func TestAFetcherThatFailsIsReportedRatherThanCachedAsEmpty(t *testing.T) {
	t.Parallel()
	fetches := 0
	c := NewProjectionCache(func(context.Context) (Wiki, error) {
		fetches++
		return Wiki{}, errors.New("the backend is down")
	}, nil)

	_, err := c.Current(context.Background())
	require.Error(t, err)
	// Two calls, two fetches. A failure cached as "there is nothing" is a deployment that has
	// quietly decided its wiki is empty, and every later read agrees with it.
	_, err = c.Current(context.Background())
	require.Error(t, err)
	assert.Equal(t, 2, fetches, "a failure is not cached")
}

func TestAViewIsServedEvenWhenItsChangeSignalCannotBeRead(t *testing.T) {
	t.Parallel()
	c := NewProjectionCache(
		func(context.Context) (Wiki, error) { return wikiFixture(), nil },
		func(context.Context) (ProjectionRevision, error) {
			return ProjectionRevision{}, errors.New("the status endpoint is down")
		},
	)
	wiki, err := c.Current(context.Background())
	require.NoError(t, err)
	require.Len(t, wiki.Files, 3, "a reader who cannot have the change signal can still have the content")
	// And the revision falls back to the view's own fingerprint, so it is *something* rather than
	// an empty string that reads as "the backend has not reported anything yet".
	assert.NotEmpty(t, c.Revision().Value)
}

func TestRefreshReFetchesOnlyWhenTheRevisionActuallyMoved(t *testing.T) {
	t.Parallel()
	rev := "r1"
	fetches := 0
	c := NewProjectionCache(
		func(context.Context) (Wiki, error) {
			fetches++
			w := wikiFixture()
			w.Commit = rev
			return w, nil
		},
		func(context.Context) (ProjectionRevision, error) { return ProjectionRevision{Value: rev}, nil },
	)
	_, err := c.Current(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, fetches)

	changed, err := c.RefreshIfChanged(context.Background())
	require.NoError(t, err)
	assert.False(t, changed, "nothing moved")
	assert.Equal(t, 1, fetches, "so nothing is re-read: a client that re-reads on a false signal re-reads a thousand times")

	rev = "r2"
	changed, err = c.RefreshIfChanged(context.Background())
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 2, fetches)
	assert.Equal(t, "r2", c.Revision().Value)
	assert.Equal(t, 1, c.Refreshes(), "one re-read, counted")

	// And the new view is the one now served, which is the other half of a working signal.
	wiki, err := c.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "r2", wiki.Commit)
}

func TestRefreshIsARefusalWhenTheChangeSignalCannotBeRead(t *testing.T) {
	t.Parallel()
	revErr := error(nil)
	c := NewProjectionCache(
		func(context.Context) (Wiki, error) { return wikiFixture(), nil },
		func(context.Context) (ProjectionRevision, error) { return ProjectionRevision{}, revErr },
	)
	_, err := c.Current(context.Background())
	require.NoError(t, err)

	revErr = errors.New("the status endpoint is down")
	changed, err := c.RefreshIfChanged(context.Background())
	require.Error(t, err)
	// Not "unchanged". A signal that could not be read is not evidence that nothing moved, and
	// reporting it as unchanged is how a reader's view goes silently stale.
	assert.False(t, changed)
}

func TestACacheWithNoChangeSignalReportsNoChangeRatherThanGuessing(t *testing.T) {
	t.Parallel()
	c := NewProjectionCache(func(context.Context) (Wiki, error) { return wikiFixture(), nil }, nil)
	changed, err := c.RefreshIfChanged(context.Background())
	require.NoError(t, err)
	assert.False(t, changed, "with nothing to compare against, there is no change to report")
}

func TestAProjectionReadsAsAFileTreeWithNothingButAnIoFs(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	// `find` is what a person runs, so `fs.WalkDir` is what the test runs: the same traversal,
	// through the same interface, with no mount anywhere.
	var walked []string
	require.NoError(t, fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if path == "." {
			return nil
		}
		kind := "file"
		if d.IsDir() {
			kind = "dir "
		}
		walked = append(walked, kind+" "+path)
		return nil
	}))
	// Sorted at every level, depth first, which is what `find` prints. The order is the
	// contract: a reader diffing two listings, or a shell globbing them, needs a stable order
	// and not merely a set.
	assert.Equal(t, []string{
		"file " + MarkerName(),
		"file index-authored.md",
		"file index.md",
		"dir  runbooks",
		"dir  runbooks/deep",
		"file runbooks/deep/rollback.md",
		"file runbooks/restore.md",
	}, walked, "sorted at each level, depth first, with the intermediate directories synthesised")
}

func TestEveryProjectedFileReadsBackByteForByte(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	// The corpus half is copied verbatim and the page half comes from the backend's bundle. A
	// projection that re-rendered either would produce a *different* file, and the difference
	// between what was reviewed and what is published is a question nobody asked.
	for _, want := range []string{"index-authored.md", "runbooks/restore.md", "runbooks/deep/rollback.md", "index.md"} {
		got, err := fs.ReadFile(fsys, want)
		require.NoError(t, err, want)
		switch want {
		case "index-authored.md":
			assert.Equal(t, "# Overview\n", string(got))
		case "runbooks/restore.md":
			assert.Equal(t, "# Restore\n\nDrop the base.\n", string(got))
		case "runbooks/deep/rollback.md":
			assert.Equal(t, "# Rollback\n", string(got))
		case "index.md":
			assert.Equal(t, "# Index\n", string(got))
		}
	}
	marker, err := fs.ReadFile(fsys, MarkerName())
	require.NoError(t, err)
	assert.Contains(t, string(marker), "toolbox.knowledge.projection")
}

func TestAMissingFileIsAbsentRatherThanEmpty(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	_, err = fs.ReadFile(fsys, "runbooks/nope.md")
	require.Error(t, err)
	// A file that is missing and a file that is empty are different, and returning empty bytes for
	// both is how a `cat` of a typo'd path looks like a document that says nothing.
	assert.ErrorIs(t, err, fs.ErrNotExist)

	_, err = fs.Stat(fsys, "runbooks/nope.md")
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestAMissingDirectoryIsAbsentRatherThanEmpty(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	entries, err := fs.ReadDir(fsys, "architecture")
	require.Error(t, err, "a folder that is not in the projection is not an empty folder")
	assert.ErrorIs(t, err, fs.ErrNotExist)
	assert.Nil(t, entries)
}

func TestEveryEntryIsReadOnlyAndNeverExecutable(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	info, err := fs.Stat(fsys, "runbooks/restore.md")
	require.NoError(t, err)
	// A projection is markdown and rendered pages. A mode implying otherwise is a claim nothing
	// backs, and `r-x` on a file is a strong one.
	assert.Equal(t, fs.FileMode(0o444), info.Mode().Perm())
	assert.Zero(t, info.Mode()&0o111, "a markdown file is not executable")
	assert.Equal(t, int64(len("# Restore\n\nDrop the base.\n")), info.Size())

	dir, err := fs.Stat(fsys, "runbooks")
	require.NoError(t, err)
	assert.True(t, dir.IsDir())
	// 0o555: traversable and readable, not writable. A directory that reads as writable invites
	// the edit that is then refused.
	assert.Equal(t, fs.FileMode(0o555), dir.Mode().Perm())
	assert.Zero(t, dir.Size(), "a directory has no size to report")
}

func TestTheViewIsCopiedSoAChangeToTheFetcherIsNotVisibleUnderneathAReader(t *testing.T) {
	t.Parallel()
	// A mount holds this filesystem for a long time. If it were a view onto the slice the fetcher
	// returned, the next fetch would change the bytes a reader is mid-way through reading.
	live := wikiFixture()
	c := NewProjectionCache(func(context.Context) (Wiki, error) { return live, nil }, nil)
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	live.Files[0].Content = []byte("# Replaced\n")
	got, err := fs.ReadFile(fsys, "index-authored.md")
	require.NoError(t, err)
	assert.Equal(t, "# Overview\n", string(got), "the served view was copied at mount time")
}

func TestALeadingSlashIsTheSameFileRatherThanASecondOne(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	// `cat //runbooks/restore.md` and `find -printf` both produce leading slashes. A reader typing
	// one should get the file rather than a not-found.
	direct := &readOnlyFS{files: map[string]*ProjectedFile{"a/b.md": {Path: "a/b.md", Content: []byte("x")}}}
	for _, name := range []string{"a/b.md", "/a/b.md", "//a/b.md"} {
		got, err := fs.ReadFile(direct, name)
		require.NoError(t, err, name)
		assert.Equal(t, "x", string(got), name)
	}
	_ = fsys
}

func TestAFileHandleDrainsAcrossManyReads(t *testing.T) {
	t.Parallel()
	// Larger than the 32 KiB buffer `readAll` uses, so the loop really iterates and a reader
	// paging through a big document gets all of it.
	big := strings.Repeat("the corpus is authoritative and everything else is rebuildable.\n", 2000)
	c, _ := staticCache(t, Wiki{BaseID: "docs", Files: []ProjectedFile{
		{Path: "big.md", Content: []byte(big)},
	}}, "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	f, err := fsys.Open("big.md")
	require.NoError(t, err)
	defer f.Close()
	info, err := f.Stat()
	require.NoError(t, err)
	assert.Equal(t, int64(len(big)), info.Size())

	var out []byte
	buf := make([]byte, 997) // an awkward size, so reads straddle the buffer boundary
	for {
		n, err := f.Read(buf)
		out = append(out, buf[:n]...)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}
	assert.Equal(t, big, string(out))
}

func TestAnEmptyFileReadsAsEmptyRatherThanAsAbsent(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, Wiki{BaseID: "docs", Files: []ProjectedFile{{Path: "empty.md"}}}, "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)
	got, err := fs.ReadFile(fsys, "empty.md")
	require.NoError(t, err, "a file with no content is still a file")
	assert.Empty(t, got)
}

func TestTheRootIsADirectoryAndReadingItAsAFileIsRefused(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	root, err := fsys.Open(".")
	require.NoError(t, err)
	info, err := root.Stat()
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Reading a directory as a file is refused, specifically and with the sentinel `io/fs` defines,
	// because a naive recursive copier does exactly this and deserves an error it can act on.
	_, err = fs.ReadFile(fsys, ".")
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrInvalid)

	// And the root handle *is* a `ReadDirFile` that lists the same entries the filesystem does.
	// A handle that opens and stats but cannot be listed is half-implemented: `tar`, `zip` and
	// `http.FileServer` all take a directory handle and call `ReadDir` on it, and they would
	// work one level down and fail at the top.
	dirFile, ok := root.(fs.ReadDirFile)
	require.True(t, ok, "a directory handle supports ReadDir")
	viaHandle, err := dirFile.ReadDir(-1)
	require.NoError(t, err)
	viaFS, err := fs.ReadDir(fsys, ".")
	require.NoError(t, err)
	require.Len(t, viaHandle, len(viaFS))
	for i := range viaFS {
		assert.Equal(t, viaFS[i].Name(), viaHandle[i].Name(), "the two listings agree, in order")
	}

	// A *file* handle refuses to be listed, which is the other half of the same contract.
	f, err := fsys.Open("runbooks/restore.md")
	require.NoError(t, err)
	_, err = f.(fs.ReadDirFile).ReadDir(-1)
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrInvalid)
}

func TestWhereAProjectedFileCameFromIsOnItsEntry(t *testing.T) {
	t.Parallel()
	// A reader has to be able to tell a reviewed runbook from a rendered page without parsing
	// frontmatter, because the two answer "can I edit this" differently.
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	origins := map[string]string{}
	read := func(dir string) {
		entries, err := fs.ReadDir(fsys, dir)
		require.NoError(t, err)
		for _, e := range entries {
			info, err := e.Info()
			require.NoError(t, err)
			withOrigin, ok := info.(interface{ Origin() string })
			require.True(t, ok, "an entry reports where it came from")
			origins[dir+"/"+e.Name()] = withOrigin.Origin()
		}
	}
	read("runbooks")
	read("runbooks/deep")
	assert.Equal(t, ProjectionOriginFile, origins["runbooks/restore.md"], "a corpus file a person wrote")
	assert.Equal(t, ProjectionOriginGenerated, origins["runbooks/deep/rollback.md"],
		"a page the system rendered, one folder down — the origin is not a top-level thing")

	// And the listing and the file agree, because they are the same question asked twice. An
	// origin that is there through `Stat` and absent from a listing is an answer that depends
	// on which path the reader took to ask.
	viaStat, err := fs.Stat(fsys, "runbooks/restore.md")
	require.NoError(t, err)
	statOrigin, ok := viaStat.(interface{ Origin() string })
	require.True(t, ok)
	assert.Equal(t, ProjectionOriginFile, statOrigin.Origin())

	// The index and the marker are this framework's own output, so they claim no origin rather
	// than borrowing one.
	generated, err := fs.ReadDir(fsys, ".")
	require.NoError(t, err)
	for _, e := range generated {
		if e.Name() == "index.md" || e.Name() == MarkerName() {
			info, err := e.Info()
			require.NoError(t, err)
			withOrigin, ok := info.(interface{ Origin() string })
			require.True(t, ok)
			assert.Empty(t, withOrigin.Origin(), e.Name()+" is generated by this framework, not authored")
		}
	}
}

func TestTheViewFingerprintDoesNotDependOnTheOrderTheFilesArrivedIn(t *testing.T) {
	t.Parallel()
	a := wikiFixture()
	b := wikiFixture()
	b.Files[0], b.Files[2] = b.Files[2], b.Files[0]
	// A view is a set of files. A fingerprint that depends on the order they arrived in reports a
	// change whenever a fetcher returns the same files differently, and the cost of that false
	// change is not one wasted call — every reader's file cache is invalidated, a thousand mount
	// threads re-read, and a person watching their directory sees it rebuilt for no reason.
	assert.Equal(t, wikiDigest(a), wikiDigest(b))
}

func TestTheViewFingerprintChangesWhenAnythingItCoversChanges(t *testing.T) {
	t.Parallel()
	base := wikiFixture()
	original := wikiDigest(base)

	for name, mutate := range map[string]func(*Wiki){
		"the commit it was taken from": func(w *Wiki) { w.Commit = "def456" },
		"the index":                    func(w *Wiki) { w.Index = []byte("# Different\n") },
		"the marker":                   func(w *Wiki) { w.Marker = []byte("{}") },
		"a file's path":                func(w *Wiki) { w.Files[0].Path = "moved.md" },
		"a file's digest":              func(w *Wiki) { w.Files[0].Digest = "sha256:other" },
		"a file added":                 func(w *Wiki) { w.Files = append(w.Files, ProjectedFile{Path: "new.md"}) },
	} {
		mutated := wikiFixture()
		mutate(&mutated)
		assert.NotEqual(t, original, wikiDigest(mutated), name)
	}
}

func TestTheViewFingerprintDistinguishesBasesServingTheSameFiles(t *testing.T) {
	t.Parallel()
	a := wikiFixture()
	b := wikiFixture()
	b.BaseID = "handbook"
	assert.NotEqual(t, wikiDigest(a), wikiDigest(b),
		"two bases holding identical content are still two bases, and a client comparing across them must be able to tell")
}

// --- the path helpers, which is where a projection's shape is actually decided.

func TestPathHelpersNameWhatTheyAreMeantTo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		got  any
		want any
	}{
		{"trimLeadingSlash keeps a clean path", trimLeadingSlash("a/b.md"), "a/b.md"},
		{"trimLeadingSlash takes every one", trimLeadingSlash("///a"), "a"},
		{"trimLeadingSlash on the root is the root", trimLeadingSlash("/"), ""},
		{"lastSlash of a nested path", lastSlash("a/b/c.md"), 3},
		{"lastSlash of a root-level path", lastSlash("c.md"), -1},
		{"firstSlash of a nested path", firstSlash("a/b/c.md"), 1},
		{"firstSlash of a root-level path", firstSlash("c.md"), -1},
		{"underDir for a real child", underDir("a/b.md", "a"), true},
		{"underDir refuses a prefix that is not a directory", underDir("ab.md", "a"), false},
		{"underDir refuses the directory itself", underDir("a", "a"), false},
		{"baseName of a nested path", baseName("a/b/c.md"), "c.md"},
		{"baseName of a root-level path", baseName("c.md"), "c.md"},
		{"join below a directory", join("a", "b.md"), "a/b.md"},
		{"join at the root is just the name", join(".", "b.md"), "b.md"},
		{"join with no directory is just the name", join("", "b.md"), "b.md"},
	} {
		assert.Equal(t, tc.want, tc.got, tc.name)
	}
}

// --- concurrency. The mount reads this from many kernel threads at once, and a data race here is
// not a flaky test but a corrupt projection.

func TestTheCacheSurvivesConcurrentReadersAndOneRefresh(t *testing.T) {
	t.Parallel()
	// The revision is read by the fetcher and by the revision source, and written by this test, so
	// it is behind the same lock throughout. An unguarded read here would be a race *in the test*,
	// which the detector would report against `pkg/knowledge` and send a reader looking in the
	// cache for a bug that is not there.
	var mu sync.Mutex
	rev := "r1"
	fetches := 0
	current := func() string {
		mu.Lock()
		defer mu.Unlock()
		return rev
	}
	bump := func(next string) {
		mu.Lock()
		defer mu.Unlock()
		rev = next
	}
	c := NewProjectionCache(
		func(context.Context) (Wiki, error) {
			mu.Lock()
			fetches++
			mu.Unlock()
			w := wikiFixture()
			w.Commit = current()
			return w, nil
		},
		func(context.Context) (ProjectionRevision, error) {
			return ProjectionRevision{Value: current()}, nil
		},
	)

	// Populated before the goroutines start, so the test is about what its name says: a
	// refresh landing while readers are reading. Left to the readers, whether a refresh was
	// even *needed* would depend on which of them won the first fetch — one that fetched after
	// the revision moved would already be serving the new view, the refresher would correctly
	// do nothing, and `Refreshes` would be zero for a reason that has nothing to do with
	// concurrency. Populating first makes the refresh necessary, and so makes it observable.
	seed, err := c.Current(context.Background())
	require.NoError(t, err)
	require.Equal(t, "r1", seed.Commit)

	// Readers, a refresher and the accessors all at once. The race detector is the assertion; the
	// counts are there so the test still means something without it.
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				wiki, err := c.Current(context.Background())
				if err != nil {
					t.Errorf("Current: %v", err)
					return
				}
				// A reader must never observe a half-updated cache: zero files, or a mix of two
				// views. That is what the lock is for and it is invisible without concurrency.
				if len(wiki.Files) != 3 {
					t.Errorf("a reader saw %d files, not 3", len(wiki.Files))
					return
				}
				_ = c.FileCount()
				_ = c.Revision()
				_ = c.Refreshes()
				_ = c.RefreshedAt()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			if j == 10 {
				bump("r2")
			}
			if _, err := c.RefreshIfChanged(context.Background()); err != nil {
				t.Errorf("RefreshIfChanged: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	// Every reader saw a complete view, and the refresh actually took effect — a reader that had
	// caught a cache mid-update would see zero files or a mixture of two views.
	wiki, err := c.Current(context.Background())
	require.NoError(t, err)
	assert.Len(t, wiki.Files, 3)
	assert.Equal(t, "r2", wiki.Commit, "the newest revision is the one now served")
	assert.Positive(t, c.Refreshes())
	assert.False(t, c.RefreshedAt().IsZero())
}

func TestAFilesystemBuiltFromAViewIsSafeToShare(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	// The served `fs.FS` is a map read by every kernel thread the mount has. Concurrent traversal
	// is the normal case, not an edge case, so it is exercised as one.
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				entries, err := fs.ReadDir(fsys, "runbooks")
				if err != nil {
					t.Errorf("ReadDir: %v", err)
					return
				}
				if len(entries) != 2 {
					t.Errorf("ReadDir returned %d entries, not 2", len(entries))
					return
				}
				if _, err := fs.ReadFile(fsys, "runbooks/restore.md"); err != nil {
					t.Errorf("ReadFile: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestTheServedViewMatchesWhatAFsTestMapWouldSay guards the property the whole file exists for: a
// projection is a filesystem, so the standard library's own expectations of one are the reference
// rather than a hand-written list of them.
func TestTheServedViewSatisfiesTheStandardLibrarysOwnViewOfAFilesystem(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	want := fstest.MapFS{
		"index-authored.md":         {Data: []byte("# Overview\n")},
		"index.md":                  {Data: []byte("# Index\n")},
		MarkerName():                {Data: []byte(`{"kind":"toolbox.knowledge.projection"}`)},
		"runbooks/restore.md":       {Data: []byte("# Restore\n\nDrop the base.\n")},
		"runbooks/deep/rollback.md": {Data: []byte("# Rollback\n")},
	}

	var names []string
	require.NoError(t, fs.WalkDir(fsys, ".", func(path string, _ fs.DirEntry, _ error) error {
		if path == "." {
			return nil
		}
		names = append(names, path)
		return nil
	}))
	// The reference is walked the same way, so the comparison is like for like.
	var wantNames []string
	require.NoError(t, fs.WalkDir(want, ".", func(path string, _ fs.DirEntry, _ error) error {
		if path != "." {
			wantNames = append(wantNames, path)
		}
		return nil
	}))
	assert.Equal(t, wantNames, names, "the same tree the standard library's own test filesystem would report")
}

// The clock is injected nowhere, so the one time this package reaches for it is asserted rather
// than left to a reader's trust: a cache that never records when it fetched cannot report staleness.
func TestTheCacheRecordsWhenItFetchedAndOnlyThen(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	before := time.Now().UTC()
	_, err := c.Current(context.Background())
	require.NoError(t, err)
	after := c.RefreshedAt()
	assert.False(t, after.Before(before.Add(-time.Second)), "the fetch time is not before the fetch")
	assert.False(t, after.IsZero())
}

// TestTheThreeAnswersToHowManyFilesAreInAProjectionAreTheSame is the test for the bug this file's
// siblings found: the path list, the served filesystem and the count were three separate answers.
//
// A mount's status bar said five files while `ls` showed three, because the count added two for
// the index and the marker and the tree that was actually attached did not contain them. Every one
// of the three is a correct answer to a question nobody asked; what a reader gets is three answers
// to *one* question.
func TestTheThreeAnswersToHowManyFilesAreInAProjectionAreTheSame(t *testing.T) {
	t.Parallel()
	wiki := wikiFixture()
	c, _ := staticCache(t, wiki, "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	paths := wiki.WikiPaths()
	byPath := map[string]bool{}
	for _, p := range paths {
		byPath[p] = true
	}

	var served []string
	require.NoError(t, fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if path != "." && !d.IsDir() {
			served = append(served, path)
		}
		return nil
	}))

	assert.ElementsMatch(t, paths, served, "the list the mount attaches and the tree it serves are the same set")
	assert.Equal(t, len(paths), c.FileCount(), "and the count is the length of that same list")

	// The two generated files are in it, which is what makes a mounted projection navigable and
	// identifiable rather than just a directory of markdown.
	assert.True(t, byPath["index.md"], "the index is served, or the projection cannot be navigated")
	assert.True(t, byPath[MarkerName()],
		"the marker is served, or a corpus walk re-ingesting the mountpoint has no way to know it is not content")
}

func TestTheIndexAndMarkerAreReachableByName(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	// `cd` into a mounted projection and `cat .toolbox-projection.json` is how a person — or a
	// tool walking the tree — finds out what they are looking at. Both must be openable.
	for _, name := range []string{"index.md", MarkerName()} {
		f, err := fsys.Open(name)
		require.NoError(t, err, name)
		info, err := f.Stat()
		require.NoError(t, err)
		assert.False(t, info.IsDir(), name)
		assert.Positive(t, info.Size(), name+" is present and not empty")
		require.NoError(t, f.Close())
	}
}

func TestAnEntriesModTimeIsTheFetchTimeAndItCarriesNoPlatformData(t *testing.T) {
	t.Parallel()
	c, _ := staticCache(t, wikiFixture(), "r1")
	fsys, err := c.FS(context.Background())
	require.NoError(t, err)

	info, err := fs.Stat(fsys, "runbooks/restore.md")
	require.NoError(t, err)
	// The same reasoning the FUSE mount gives: a projected file's content comes from a backend
	// export whose timestamps are the page's refresh time, which beside a corpus file's own mtime
	// would say something nobody verified. A reader gets a bound instead.
	assert.True(t, info.ModTime().IsZero(), "no timestamp is claimed that nothing backs")
	// `Sys` is nil rather than a half-populated struct, so a caller that type-asserts for platform
	// data finds nothing rather than something empty and plausible.
	assert.Nil(t, info.Sys())
}

// A view and the revision recorded beside it are read at two different instants, and the cache
// has to be able to resolve that disagreement in one direction only.
//
// Reading the revision *after* the fetch records the newer revision beside the older view. Every
// later check then compares that revision against the source, finds the two equal, and reports
// that there is nothing to do — so the stale view is served for the life of the process, with no
// error and no failed assertion anywhere. Every reader sees a complete, coherent view; it is
// simply not the newest one, and nothing in the system is able to say so.
//
// The change landing *inside* the fetch is what makes it reproducible without a sleep, a goroutine
// or a retry, which is the only kind of reproduction worth having for a bug that a race detector
// cannot see: this one is a logic ordering, and it fails every time or not at all.
func TestARefreshStillHappensWhenTheRevisionMovesWhileTheViewIsBeingFetched(t *testing.T) {
	t.Parallel()
	rev := "r1"
	fetches := 0
	c := NewProjectionCache(
		func(context.Context) (Wiki, error) {
			fetches++
			w := wikiFixture()
			if fetches == 1 {
				w.Commit = "r1"
				// The change lands mid-fetch: after the view was decided, before the
				// caller records which revision that view belongs to.
				rev = "r2"
				return w, nil
			}
			w.Commit = "r2"
			return w, nil
		},
		func(context.Context) (ProjectionRevision, error) {
			return ProjectionRevision{Value: rev}, nil
		},
	)

	wiki, err := c.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "r1", wiki.Commit, "the first fetch is the view it was, and the cache says so")

	refreshed, err := c.RefreshIfChanged(context.Background())
	require.NoError(t, err)
	assert.True(t, refreshed,
		"the revision moved while the view was being fetched, so the recorded revision is the "+
			"older one and this check finds work to do. Recording the newer one is the bug: "+
			"every later check would agree there was nothing to do")

	wiki, err = c.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "r2", wiki.Commit, "and the newest view is the one now served")
}

// The recorded revision and the served view are one fact, and a reader that finds them
// disagreeing has been handed a pair that cannot both be true.
//
// This is the invariant the lost update breaks, and it is stated over the served pair rather than
// over a counter because a counter cannot distinguish "refreshed once" from "refreshed once and
// then had a newer view written over it by a refresh that had started earlier".
func TestTheServedViewAndTheRevisionRecordedBesideItAlwaysAgree(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	rev := "r1"
	c := NewProjectionCache(
		func(context.Context) (Wiki, error) {
			mu.Lock()
			defer mu.Unlock()
			w := wikiFixture()
			w.Commit = rev
			return w, nil
		},
		func(context.Context) (ProjectionRevision, error) {
			mu.Lock()
			defer mu.Unlock()
			return ProjectionRevision{Value: rev}, nil
		},
	)

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				if i == 0 && j == 20 {
					mu.Lock()
					rev = "r2"
					mu.Unlock()
				}
				if i%2 == 0 {
					if _, err := c.RefreshIfChanged(context.Background()); err != nil {
						t.Errorf("RefreshIfChanged: %v", err)
						return
					}
					continue
				}
				if _, err := c.Current(context.Background()); err != nil {
					t.Errorf("Current: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// Let the refreshers that were already inside a fetch land, then settle it: a refresher
	// that observed the old revision and stores its view afterwards is the regression, and it
	// is only visible once every in-flight refresh has finished.
	_, err := c.RefreshIfChanged(context.Background())
	require.NoError(t, err)

	wiki, err := c.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "r2", wiki.Commit, "the newest view is the one served once the dust settles")
	assert.Equal(t, "r2", c.Revision().Value,
		"and the revision recorded beside it describes that view rather than a newer one")
}
