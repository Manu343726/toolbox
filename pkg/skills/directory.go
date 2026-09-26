package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/api"
)

// Directory is a read-only catalog backed by a directory of skills.
//
// It is the reusable half of every directory-shaped source of skills: a project's own
// `.toolbox/skills/`, a git checkout, anything a deployment unpacked. One type serves all
// three, because the reading is the same for all three and three copies of it would be three
// things to keep in step — and a manifest that disagreed between a project's own skill and the
// same skill in a checkout would make a pin mean two things.
//
// It is read-only and that is not a limitation. A person owns the files in a project's own
// directory; a git checkout is written through git so that a change is a commit; and neither is
// the place a framework writes. Adding a skill to a project means naming it, not writing here.
type Directory struct {
	// id is the catalog identifier, the first segment of every reference and URI.
	id string
	// root is the directory skills are read from, or empty when the source has none.
	root string
	// subdirectories are the paths under root that hold skills, relative to it. Empty means
	// root's immediate children, which is a project's own layout.
	subdirectories []string

	// mu guards cache. A skill is re-read when its files change, because a person editing a
	// skill must see the edit and a long-running process would otherwise serve the version it
	// started with.
	mu    sync.RWMutex
	cache map[string]Template
}

// DirectoryOptions configures a directory-backed catalog.
type DirectoryOptions struct {
	// ID is the catalog identifier: the first segment of every reference and URI. Required.
	ID string
	// Root is the directory holding the skills. An empty Root, or one that does not exist, is
	// an empty catalog rather than a failure — a project that has added no skills has none.
	Root string
	// Subdirectories are the paths under Root that hold skills, each with a `SKILL.md`
	// somewhere inside it.
	//
	// A repository publishes its skills under whichever directory its own clients read —
	// `.claude/skills`, `.agents/skills`, `.opencode/skills` and twenty-eight others — and a
	// source that only looked at Root's immediate children would find none of them. A project
	// names none, because `.toolbox/skills/<name>/` is already one level down.
	Subdirectories []string
}

// NewDirectory returns a read-only catalog over a directory of skills.
func NewDirectory(options DirectoryOptions) (*Directory, error) {
	id := strings.TrimSpace(options.ID)
	if id == "" {
		return nil, api.Errorf(api.KindInvalid,
			"a directory-backed catalog needs an identifier: it is the first segment of every "+
				"reference and URI for a skill in it, because two catalogs may hold a skill of "+
				"the same name")
	}
	return &Directory{
		id:             id,
		root:           strings.TrimSpace(options.Root),
		subdirectories: options.Subdirectories,
		cache:          map[string]Template{},
	}, nil
}

// ID is the catalog's identifier.
func (d *Directory) ID() string { return d.id }

// Writable reports whether this catalog accepts a write. It does not: the files belong to
// whoever put them there, and a framework that wrote into them would be writing into somebody
// else's repository.
func (d *Directory) Writable() bool { return false }

// Location is where the catalog keeps its skills, when it has any.
//
// It is reported rather than acted on, and it is the one fact a person deciding whether to
// depend on a remote skill's content needs: where the bytes they would be trusting came from.
func (d *Directory) Location() string { return d.root }

// Names returns every skill the directory holds, ordered, whether or not this framework can
// read it.
//
// A directory holding a skill that cannot be read still *has* a skill, and a listing that hid
// it would leave a person with content in their tree that nothing will serve and nothing will
// explain. It is reported by Validate, whose whole job is to say what is wrong.
func (d *Directory) Names() ([]string, error) {
	if d.root == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var names []string
	for _, subdirectory := range d.searchPaths() {
		entries, err := os.ReadDir(filepath.Join(d.root, filepath.FromSlash(subdirectory)))
		if err != nil {
			if os.IsNotExist(err) {
				// A source that publishes skills under a convention this repository does not
				// use simply has none there, and a deployment that carries thirty of those
				// conventions must not report an error for the twenty-nine it skipped.
				continue
			}
			return nil, api.WrapError(api.KindInternal, err,
				"reading %s of the %s catalog", subdirectory, d.id)
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				// A hidden directory is not part of the catalog: a person hiding one is
				// keeping it out of the way, and serving it anyway would ignore the only
				// signal they gave.
				continue
			}
			directory := filepath.Join(subdirectory, entry.Name())
			if !hasSkillDocument(filepath.Join(d.root, filepath.FromSlash(directory))) {
				continue
			}
			if seen[entry.Name()] {
				// Two conventions holding a skill of the same name is a repository that
				// publishes it twice. The first is served and the collision is reported by
				// Validate; serving both would give one name two bodies.
				continue
			}
			seen[entry.Name()] = true
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// searchPaths are the directories to look in, defaulting to the root's own children.
func (d *Directory) searchPaths() []string {
	if len(d.subdirectories) == 0 {
		return []string{""}
	}
	return d.subdirectories
}

func hasSkillDocument(directory string) bool {
	info, err := os.Stat(filepath.Join(directory, SkillFileName))
	return err == nil && !info.IsDir()
}

// Has reports whether the catalog holds a skill by that name.
func (d *Directory) Has(name string) bool {
	if d.root == "" {
		return false
	}
	names, err := d.Names()
	if err != nil {
		return false
	}
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

// SkillPath is where a skill's directory is, for a catalog that needs to look.
func (d *Directory) SkillPath(name string) (string, error) {
	if d.root == "" {
		return "", api.Errorf(api.KindNotFound,
			"the %s catalog has no directory, so it holds no skills", d.id)
	}
	if strings.ContainsAny(name, "/\\") || name == "" || name == "." || name == ".." {
		// A name is a directory's name, and a name carrying a separator is a path someone
		// would like this to read. A skill's name is validated elsewhere; this is the
		// boundary that keeps a name from being one.
		return "", api.Errorf(api.KindInvalid,
			"%q is not a skill's name; a name is one directory's name, with no separator in it", name)
	}
	for _, subdirectory := range d.searchPaths() {
		candidate := filepath.Join(d.root, filepath.FromSlash(subdirectory), filepath.FromSlash(name))
		if hasSkillDocument(candidate) {
			return candidate, nil
		}
	}
	return "", api.Errorf(api.KindNotFound,
		"the %s catalog holds no skill named %q", d.id, name)
}

// Template reads one skill, as a template: the portable artefact, and the thing a pin is taken
// against.
func (d *Directory) Template(name string) (Template, error) {
	path, err := d.SkillPath(name)
	if err != nil {
		return Template{}, err
	}
	return ReadDirectory(d.id, name, path)
}

// File reads one file of a skill, with the digest and size its manifest gave for it.
//
// The digest comes from the manifest and the bytes come from the disk, and the two are
// returned side by side rather than one derived from the other: a caller verifying what it
// fetched needs the manifest's claim and the actual content, and a source that recomputed the
// digest here would be checking the content against itself.
func (d *Directory) File(name, relative string) ([]byte, File, error) {
	template, err := d.Template(name)
	if err != nil {
		return nil, File{}, err
	}
	entry, found := template.File(relative)
	if !found {
		return nil, File{}, api.Errorf(api.KindNotFound,
			"the skill %s has no file at %q; it holds: %s",
			template.Describe(), relative, ManifestPaths(template.Files))
	}
	path, err := d.SkillPath(name)
	if err != nil {
		return nil, File{}, err
	}
	content, err := os.ReadFile(filepath.Join(path, filepath.FromSlash(relative)))
	if err != nil {
		return nil, File{}, api.WrapError(api.KindInternal, err,
			"reading %s of %s", relative, template.Describe())
	}
	return content, entry, nil
}

// cached returns a template, re-reading it when its files have changed.
//
// The change is detected by comparing the manifest a fresh read produces with the cached one,
// rather than by a modification time: a checkout, a `git pull` and a `touch` all change the
// time without changing the content, and a modification time is a fact about the machine
// rather than about the skill.
func (d *Directory) cached(name string) (Template, error) {
	fresh, err := d.Template(name)
	if err != nil {
		d.mu.Lock()
		delete(d.cache, name)
		d.mu.Unlock()
		return Template{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if previous, cached := d.cache[name]; cached && sameManifest(previous.Files, fresh.Files) {
		return previous, nil
	}
	d.cache[name] = fresh
	return fresh, nil
}

// Cached returns a template, re-reading it only when its files have changed.
//
// It is exported for a caller that reads a skill often and wants the read to be cheap. The
// manifest is the cache key because it is computed from the bytes, so a skill that has not
// changed is not re-read even if every file's timestamp moved.
func (d *Directory) Cached(name string) (Template, error) { return d.cached(name) }

func sameManifest(a, b []File) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index].Path != b[index].Path || a[index].Digest != b[index].Digest {
			return false
		}
	}
	return true
}

// ManifestPaths lists a manifest's paths, for a message about a file that is not in it.
//
// A caller who asked for a file this skill does not have needs to be told what it does have,
// and a message listing nothing is not a message.
func ManifestPaths(files []File) string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return strings.Join(paths, ", ")
}

// ConventionalSkillDirectories are the paths a repository publishes its skills under, one per
// client that has its own convention.
//
// The list is the one the ecosystem's own installer searches, and it is stated here rather
// than derived because it is other people's convention rather than this framework's: a
// repository that publishes under `.claude/skills` is doing what its author intended, and a
// framework that only looked in its own `.toolbox/skills` would find nothing. It is *read*
// from the installer rather than invented, because an invented list would find the
// repositories nobody actually uses.
//
// A directory under one of these is searched for a `SKILL.md` one level down, which is where
// every one of these conventions puts a skill.
func ConventionalSkillDirectories() []string {
	return []string{
		".agents/skills",
		".claude/skills",
		".cline/skills",
		".codebuddy/skills",
		".codex/skills",
		".commandcode/skills",
		".continue/skills",
		".factory/skills",
		".github/skills",
		".goose/skills",
		".grok/skills",
		".iflow/skills",
		".junie/skills",
		".kilo/skills",
		".kilocode/skills",
		".kimchi/skills",
		".kiro/skills",
		".minimax/skills",
		".mux/skills",
		".neovate/skills",
		".opencode/skills",
		".openhands/skills",
		".pi/skills",
		".posit/assistant/skills",
		".qoder/skills",
		".roo/skills",
		".trae/skills",
		".windsurf/skills",
		".zcode/skills",
		".zencoder/skills",
	}
}
