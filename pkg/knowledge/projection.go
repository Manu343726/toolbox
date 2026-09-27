package knowledge

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// projectionMarkerName is the file that identifies a directory as a projection
// rather than a corpus.
//
// This is the mechanism, not a promise. The whole-wiki view is byte-for-byte
// shaped like a wiki, which makes it the one artifact in this system that a
// well-meaning agent could hand back to a reconcile; and a corpus that ingested
// its own generated index would let the second generation reason partly from the
// first one's output. NewCorpus refuses a root containing this file, so "the
// projection is never a reconcile input" is enforced rather than documented.
const projectionMarkerName = ".toolbox-projection.json"

// ProjectionOriginFile and ProjectionOriginGenerated are the values written into
// a projected file's frontmatter, so that "was this written by a person or
// generated?" is answered by opening the file rather than by knowing which
// directory it came from.
const (
	ProjectionOriginFile      = "file"
	ProjectionOriginGenerated = "generated"
)

// ProjectedFile is one file in the whole-wiki view.
type ProjectedFile struct {
	// Path is slash-separated, relative to the projection root.
	Path string
	// Content is the file's bytes.
	Content []byte
	// Origin is what produced it: ProjectionOriginFile for corpus content a
	// person wrote, ProjectionOriginGenerated for a page the system rendered.
	Origin string
	// SourcePath is where the content came from — a corpus-relative path, or the
	// page identifier in the backend's tree.
	SourcePath string
	// ContentID is the content or page identifier.
	ContentID string
	// Digest is the digest of Content.
	Digest string
}

// PageFile is one file from the backend's own page bundle.
//
// The bundle is reused rather than re-rendered: it already exists, and rendering
// it a second time here would be a second translation of one artifact, which is
// the failure a framework rule exists to prevent. A derived page's canonical form
// is whatever the backend says it is.
type PageFile struct {
	// Path is slash-separated, relative to the bundle root.
	Path string
	// Content is the markdown, already carrying the backend's own frontmatter.
	Content []byte
	// PageID is the page's identifier, when the bundle's frontmatter declares it.
	PageID string
	// IsIndex reports the bundle's own index file, which this package does not
	// use: there is one index for the whole view and it is generated here, so
	// that it lists authored files as well as pages.
	IsIndex bool
	// IsLog reports a refresh-history file, which is part of the page's own record
	// of how it changed.
	IsLog bool
}

// WikiInput is everything the projection composes.
type WikiInput struct {
	// BaseID is the base being projected.
	BaseID string
	// Commit is the corpus revision this view reflects, when one is known.
	Commit string
	// GeneratedAt is when the projection was taken. It is injected rather than
	// read, because a projection that stamps the current time into a file is a
	// projection whose output changes on every run and cannot be diffed.
	GeneratedAt time.Time
	// StalePages counts derived pages the backend reports as not yet refreshed,
	// so a reader can tell a current view from an out-of-date one.
	StalePages int
	// Authored are the corpus files. Their bodies are copied verbatim.
	Authored []ProjectedFile
	// Pages are the backend's page bundle.
	Pages []PageFile
	// OriginFilter, when non-empty, restricts the projection to one origin. An
	// empty value projects everything.
	OriginFilter Origin
	// Subtree, when non-empty, restricts the projection to paths under it.
	Subtree string
}

// Wiki is the whole-wiki view: one tree, one index, both origins.
type Wiki struct {
	// BaseID and Commit are what the view was taken from.
	BaseID  string
	Commit  string
	Stale   int
	Files   []ProjectedFile
	Index   []byte
	Marker  []byte
	Skipped []string
}

// MarkerName is the file whose presence marks a directory as a projection, and
// which a corpus walk refuses to read as one.
func MarkerName() string { return projectionMarkerName }

// MarkerFor returns the marker document for a projection, recording what it was
// taken from.
func MarkerFor(baseID, commit string, generatedAt time.Time) []byte {
	var b strings.Builder
	b.WriteString("{\n")
	fmt.Fprintf(&b, "  \"kind\": \"toolbox.knowledge.projection\",\n")
	fmt.Fprintf(&b, "  \"base_id\": %s,\n", strconv.Quote(baseID))
	fmt.Fprintf(&b, "  \"commit\": %s,\n", strconv.Quote(commit))
	fmt.Fprintf(&b, "  \"generated_at\": %s,\n", strconv.Quote(generatedAt.UTC().Format(time.RFC3339)))
	b.WriteString("  \"authoritative\": false,\n")
	b.WriteString("  \"note\": \"This directory is a generated view of a knowledge base, not a corpus. Do not edit it and do not reconcile it: the corpus is the reviewed markdown in a repository, and ingesting this tree would make one generation of the system reason from another's output.\\n\"\n")
	b.WriteString("}\n")
	return []byte(b.String())
}

// IsProjectionRoot reports whether a directory is a projection rather than a
// corpus. A corpus walk calls it and refuses.
//
// The check is the marker's presence, which is why the marker is written into
// every projection: an enforcement that depends on somebody not pointing a
// reconcile at the wrong directory is not an enforcement.
func IsProjectionRoot(dir string) bool {
	if _, err := statFile(dir, projectionMarkerName); err == nil {
		return true
	}
	return false
}

// ProjectWiki composes the whole-wiki view.
//
// The two halves are handled differently on purpose. An authored file is copied
// verbatim, because re-rendering a reviewed document produces a different
// document and a diff between what was reviewed and what is published is a
// question nobody asked. A derived page comes from the backend's bundle, because
// a page *is* a rendering and its canonical form is whatever the backend says.
func ProjectWiki(in WikiInput) (Wiki, error) {
	out := Wiki{BaseID: in.BaseID, Commit: in.Commit, Stale: in.StalePages}
	subtree := NormalizePath(in.Subtree)

	// The two origins are namespaced into two top-level directories, so that the
	// tree says which half a file came from before anybody opens it, and so that
	// an authored corpus directory and a derived page with the same relative path
	// cannot collide.
	prefix := map[Origin]string{
		OriginAuthored: "file",
		OriginDerived:  "generated",
	}

	add := func(f ProjectedFile, origin Origin) {
		if in.OriginFilter != OriginUnspecified && origin != in.OriginFilter {
			return
		}
		rel := NormalizePath(f.Path)
		if rel == "" || strings.HasPrefix(rel, "..") {
			out.Skipped = append(out.Skipped, f.Path)
			return
		}
		if subtree != "" && rel != subtree && !strings.HasPrefix(rel, subtree+"/") {
			return
		}
		f.Path = path.Join(prefix[origin], rel)
		f.Origin = projectionOriginFor(origin)
		out.Files = append(out.Files, f)
	}

	for _, f := range in.Authored {
		// An authored file is projected with its body verbatim and one added
		// header, for the reason withOriginHeader gives. Its frontmatter already
		// declares the metadata a person wrote, and the origin is the one key
		// this projection owns.
		f.Content = withOriginHeader(f.Content, ProjectionOriginFile, f.SourcePath, f.ContentID)
		f.Digest = Digest(f.Content)
		add(f, OriginAuthored)
	}
	for _, f := range in.Pages {
		// The backend's own index is not used: this view has one index, and it
		// has to list authored files too. Its per-page log files are kept,
		// because a page's history is part of the page.
		if f.IsIndex {
			continue
		}
		content := f.Content
		if !f.IsLog {
			// A page gets its origin stated in its own frontmatter. A log file is
			// already marked as one by the backend, and rewriting a log's
			// frontmatter would make a diff against the backend's bundle noisy.
			content = withOriginHeader(f.Content, ProjectionOriginGenerated, f.Path, f.PageID)
		}
		add(ProjectedFile{
			Path:       f.Path,
			Content:    content,
			SourcePath: f.Path,
			ContentID:  f.PageID,
			Digest:     Digest(f.Content),
		}, OriginDerived)
	}

	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	out.Index = renderIndex(out, in)
	out.Marker = MarkerFor(in.BaseID, in.Commit, in.GeneratedAt)
	return out, nil
}

func projectionOriginFor(o Origin) string {
	if o == OriginAuthored {
		return ProjectionOriginFile
	}
	return ProjectionOriginGenerated
}

// withOriginHeader states a file's origin in its own frontmatter.
//
// This is the one place where a projected file's bytes are not the bytes the
// reconcile ingested, and the difference is deliberate and narrow: the *prose* is
// untouched. A file that already has frontmatter gets one more key; a file with
// none gets a small block prepended. What X-3 forbids is re-rendering a reviewed
// document — reflowing it, reformatting it, running it through a template — and
// a provenance header is none of those. It is also the only way a reader opening
// a file in this tree can tell a reviewed runbook from a rendered page without
// already knowing which base it came from.
func withOriginHeader(content []byte, origin, sourcePath, contentID string) []byte {
	fm := Frontmatter{}
	hasBlock := false
	var rest []byte
	if parsed, err := ParseFile(content); err == nil {
		fm = parsed.Front
		hasBlock = parsed.HadFrontmatter
		rest = []byte(parsed.Body)
	} else {
		// A page whose frontmatter this package cannot parse is still a page. Its
		// text is preserved and the origin is stated by prepending a block, which
		// is visible rather than silent.
		rest = content
	}
	fm.Extra = withOriginKeys(fm.Extra, origin, sourcePath, contentID)
	rendered, err := fm.Render()
	if err != nil {
		return content
	}
	if !hasBlock {
		// Preserve the body exactly, including its leading blank line, because a
		// generated page's own formatting is the backend's business.
		return append(rendered, rest...)
	}
	return append(rendered, rest...)
}

// withOriginKeys adds the projection's own keys to the unrecognised map, so that
// Render writes them alongside the document's declared metadata.
func withOriginKeys(extra map[string]any, origin, sourcePath, contentID string) map[string]any {
	if extra == nil {
		extra = map[string]any{}
	}
	extra["origin"] = origin
	extra["source_path"] = sourcePath
	if contentID != "" {
		extra["content_id"] = contentID
	}
	extra["authoritative"] = false
	return extra
}

// indexHeader is the frontmatter of the generated index. It says what the file
// is, because this is the one file in the tree that describes the whole tree and
// therefore the one that could make a second generation reason from the first.
var indexHeader = []byte(`---
title: Knowledge base
origin: generated
kind: index
authoritative: false
---

`)

// renderIndex writes the one index for the view, in tree order.
//
// It links every projected file, groups them by origin so a reader can tell at a
// glance which half they are looking at, and says what the view was taken from.
func renderIndex(w Wiki, in WikiInput) []byte {
	var b strings.Builder
	b.Write(indexHeader)

	fmt.Fprintf(&b, "# %s\n\n", in.BaseID)
	b.WriteString("This is a generated view of a knowledge base. It is not a corpus and not authority:\n")
	b.WriteString("the files under `file/` are markdown people wrote and reviewed, and the files under\n")
	b.WriteString("`generated/` are the system's current beliefs, which change without review.\n\n")

	fmt.Fprintf(&b, "- base: `%s`\n", in.BaseID)
	if in.Commit != "" {
		fmt.Fprintf(&b, "- corpus commit: `%s`\n", in.Commit)
	}
	if !in.GeneratedAt.IsZero() {
		fmt.Fprintf(&b, "- taken at: %s\n", in.GeneratedAt.UTC().Format(time.RFC3339))
	}
	if w.Stale > 0 {
		fmt.Fprintf(&b, "- **%d derived page(s) the backend reports as not yet refreshed**; their content may lag the facts they were built from\n", w.Stale)
	}
	b.WriteString("\n")

	for _, heading := range []struct {
		dir    string
		origin string
		note   string
	}{
		{"file", ProjectionOriginFile, "Written by people, in files, reviewed in version control. Edit these where they live, not here."},
		{"generated", ProjectionOriginGenerated, "Synthesized and maintained by the system. Regenerated on refresh; an edit here is discarded."},
	} {
		grouped := map[string][]ProjectedFile{}
		for _, f := range w.Files {
			if f.Origin != heading.origin {
				continue
			}
			rel := strings.TrimPrefix(f.Path, heading.dir+"/")
			if rel == f.Path {
				// A file not under this origin's directory, which happens when a
				// caller projected a subtree. It is listed where it is rather than
				// moved, because a projection relocates nothing.
				rel = f.Path
			}
			grouped[path.Dir(rel)] = append(grouped[path.Dir(rel)], f)
		}
		if len(grouped) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", heading.dir, heading.note)
		dirs := make([]string, 0, len(grouped))
		for d := range grouped {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			if d != "." {
				fmt.Fprintf(&b, "### %s\n\n", d)
			}
			entries := grouped[d]
			sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
			for _, f := range entries {
				// The link is the file's full projected path, relative to this
				// index at the projection root. The grouping directory is only a
				// heading, and a link stripped of its origin directory would
				// resolve to a path that does not exist.
				fmt.Fprintf(&b, "- [%s](%s)", displayName(f), f.Path)
				if f.SourcePath != "" && !strings.HasSuffix(f.Path, f.SourcePath) {
					fmt.Fprintf(&b, " — from `%s`", f.SourcePath)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}
	return []byte(b.String())
}

func displayName(f ProjectedFile) string {
	if parsed, err := ParseFile(f.Content); err == nil {
		if parsed.Front.Title != "" {
			return parsed.Front.Title
		}
	}
	base := path.Base(f.Path)
	return strings.TrimSuffix(base, path.Ext(base))
}

// WikiPaths returns the projection's paths in tree order, which is what a
// filesystem needs in order to present the view.
//
// **The index and the marker are included, and that is the whole point of this being one
// function.** A projection is not just the files a person wrote: the index is what makes it
// navigable and the marker is what identifies the directory as a projection at all — a corpus
// walk refuses to read a directory carrying one, which is the only way a projection can be
// re-ingested without being mistaken for content. A caller that assembled the list from
// `Wiki.Files` alone served a tree that is neither navigable nor identifiable, and then counted
// two files it was not serving.
//
// Every count and every listing of a projection goes through here, so "how many files are in it"
// and "which files are in it" cannot be two different questions with two answers.
func (w Wiki) WikiPaths() []string {
	out := make([]string, 0, len(w.Files)+2)
	for _, f := range w.Files {
		out = append(out, f.Path)
	}
	return append(out, "index.md", MarkerName())
}
