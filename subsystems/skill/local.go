package skill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	"google.golang.org/protobuf/types/known/structpb"
)

// LocalCatalog is the catalog every project has without declaring it: the `skills/` directory
// under the project's configuration directory.
//
// It is read-only. The framework reads this directory and never writes to it, because a person
// owns these files and edits them as they would any other file in their project — and because
// "adding a skill to the project" means naming it, not copying it in. See docs/skills.md.
//
// A project with no `skills/` directory has an empty local catalog, and an empty catalog is a
// project that offers nothing rather than a project that failed to start.
type LocalCatalog struct {
	// dir is the directory skills are read from, or empty when the project has none.
	dir string

	// mu guards cache, which holds templates read from disk. A skill is re-read when its
	// SKILL.md changes, because a person editing a skill in their own project must see the
	// edit, and a long-running process would otherwise serve the version it started with.
	mu    sync.RWMutex
	cache map[string]skills.Template
}

// NewLocalCatalog returns the local catalog for a project's configuration directory.
//
// A project directory is the `.toolbox` directory itself; a configuration file elsewhere
// describes a deployment rather than a project, and a deployment has no skills of its own.
func NewLocalCatalog(projectDir string) *LocalCatalog {
	catalog := &LocalCatalog{cache: map[string]skills.Template{}}
	trimmed := strings.TrimSpace(projectDir)
	if trimmed == "" {
		return catalog
	}
	catalog.dir = filepath.Join(trimmed, "skills")
	return catalog
}

// ID is the catalog's identifier, which is the first segment of every local skill's URI.
func (c *LocalCatalog) ID() string { return skills.LocalCatalog }

// Writable reports whether this catalog accepts a write. It does not: a person owns these
// files.
func (c *LocalCatalog) Writable() bool { return false }

// Location is where the catalog keeps its skills, when it has any.
func (c *LocalCatalog) Location() string { return c.dir }

// Names returns every skill in the catalog, ordered, including one that is switched off.
//
// An invalid skill is not reported here. A directory holding a skill this framework cannot read
// still *has* a skill, and a listing that hid it would leave a person with a skill in their
// project that nothing will ever serve and nothing will ever explain. It is reported by
// Validate, which is the method whose job is to say what is wrong.
func (c *LocalCatalog) Names() ([]string, error) {
	if c.dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, api.WrapError(api.KindInternal, err, "reading the local skills directory %s", c.dir)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			// A hidden directory is not part of the catalog: a person hiding one is keeping
			// it out of the way, and serving it anyway would ignore the only signal they
			// gave. A file rather than a directory is not a skill, and a skill is a directory.
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Has reports whether the catalog holds a skill by that name.
func (c *LocalCatalog) Has(name string) bool {
	if c.dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(c.dir, filepath.FromSlash(name)))
	return err == nil && info.IsDir()
}

// Skill reads one skill, as this deployment would serve it to nobody in particular: the
// template, distilled for a client that reads nothing this framework knows about.
//
// The client identity is the local catalog's own, because there is no request in hand — this is
// the in-process path a deployment uses to read its own skills, and a projection for an unknown
// client is exactly the common denominator it should get.
func (c *LocalCatalog) Skill(name string) (skills.Skill, error) {
	template, err := c.TemplateFor(name)
	if err != nil {
		return skills.Skill{}, err
	}
	skill, err := template.Skill()
	if err != nil {
		return skills.Skill{}, err
	}
	if !skill.IsEnabled() {
		// A local skill that is switched off is not part of the project. It is not an error
		// and not a warning: the person who put the file there also put the switch there, and
		// the two are in the same place on purpose.
		return skills.Skill{}, api.Errorf(api.KindNotFound,
			"the skill %s is switched off in its own frontmatter, with "+
				"%senabled: false, so it is not part of this project",
			template.Describe(), skills.ToolboxPrefix)
	}
	return skill, nil
}

// ReadFile returns one file of a skill, with the digest and size its manifest gave for it.
func (c *LocalCatalog) ReadFile(name, relative string) ([]byte, skills.File, error) {
	template, err := c.TemplateFor(name)
	if err != nil {
		return nil, skills.File{}, err
	}
	entry, found := template.File(relative)
	if !found {
		return nil, skills.File{}, api.Errorf(api.KindNotFound,
			"the skill %s has no file at %q; it holds: %s",
			template.Describe(), relative, manifestPaths(template.Files))
	}
	content, err := os.ReadFile(filepath.Join(c.dir, name, filepath.FromSlash(relative)))
	if err != nil {
		return nil, skills.File{}, api.WrapError(api.KindInternal, err,
			"reading %s of %s", relative, template.Describe())
	}
	return content, entry, nil
}

// TemplateFor reads a skill, re-reading it when its SKILL.md has changed.
//
// The change is detected by comparing the file's own bytes with what produced the cached
// manifest, rather than by a modification time: a checkout, a `git pull` and a `touch` all
// change the time without changing the content, and a modification time is a fact about the
// machine rather than about the skill.
func (c *LocalCatalog) TemplateFor(name string) (skills.Template, error) {
	if c.dir == "" {
		return skills.Template{}, api.Errorf(api.KindNotFound,
			"this deployment has no project directory, so it has no local skills")
	}
	directory := filepath.Join(c.dir, filepath.FromSlash(name))
	fresh, err := skills.ReadDirectory(skills.LocalCatalog, name, directory)
	if err != nil {
		c.mu.Lock()
		delete(c.cache, name)
		c.mu.Unlock()
		return skills.Template{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.cache[name]; ok && sameManifest(cached.Files, fresh.Files) {
		return cached, nil
	}
	c.cache[name] = fresh
	return fresh, nil
}

// sameManifest reports whether two manifests describe the same bytes, which is what "the skill
// has not changed" means.
func sameManifest(a, b []skills.File) bool {
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

// manifestPaths lists a manifest's paths for a message about a file that is not in it. A
// caller who asked for a file this skill does not have needs to be told what it does have.
func manifestPaths(files []skills.File) string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return strings.Join(paths, ", ")
}

// toManifest converts a template's file list into the contract's manifest.
func toManifest(files []skills.File) []*skillv1.SkillFile {
	manifest := make([]*skillv1.SkillFile, 0, len(files))
	for _, file := range files {
		manifest = append(manifest, &skillv1.SkillFile{
			Path:     file.Path,
			Size:     file.Size,
			Digest:   file.Digest,
			MimeType: file.MIMEType,
		})
	}
	return manifest
}

// toStruct converts a frontmatter document into the contract's verbatim form.
//
// It is a conversion rather than a filter, so a field this framework has no opinion about
// reaches the client: a host builds its registry from these entries alone, and a field dropped
// here is a field no client could ever see.
func toStruct(document map[string]any) (*structpb.Struct, error) {
	if len(document) == 0 {
		return nil, nil
	}
	converted, err := structpb.NewStruct(document)
	if err != nil {
		return nil, api.WrapError(api.KindInternal, err,
			"converting a skill's frontmatter for a client")
	}
	return converted, nil
}
