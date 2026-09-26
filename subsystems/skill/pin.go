package skill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/skills"
	"go.yaml.in/yaml/v3"
)

// PinFileName is where a project's pinned manifests live.
//
// A pin is a machine-maintained record, and it is kept **out** of the person's configuration
// file for the same reason a lockfile is: the file a person writes and reviews should contain
// what the person decided, and nothing this framework re-derives on every use. Putting the
// manifest inline would mean every content change rewrote the file whose whole value is that a
// person can see at a glance which skills the project depends on — and would put a
// machine-maintained block inside the one file the configuration-confirmation rule protects.
//
// It is a lockfile by every convention that matters: it is written on first use, it is compared
// rather than trusted, and drift is reported from it rather than enforced by it.
const PinFileName = "skills.lock.yaml"

// ConfigSource is a project's configuration as this subsystem reads and writes it.
//
// It is an interface so the subsystem depends on what a project *has* rather than on how the
// configuration was resolved. A deployment running the framework resolves `pkg/config`; a test
// states a project; and a deployment with its own configuration format can serve the same
// contract. What none of them can do is bypass the confirmation rule, because the change is
// proposed before it can be applied.
type ConfigSource interface {
	// ProjectPath is the project's configuration file, or empty when it has none.
	ProjectPath() string
	// References returns the qualified references the project names, in the order written.
	References() ([]skills.Reference, error)
	// ProposeAddition proposes adding a reference, without making the change.
	ProposeAddition(skills.Reference) (SkillChange, error)
	// ProposeRemoval proposes removing a reference, without making the change.
	ProposeRemoval(skills.Reference) (SkillChange, error)
}

// SkillChange is a proposed change to a project's configuration.
type SkillChange interface {
	// Summary names the file and the change, for a person to confirm.
	Summary() string
	// File is the configuration file the change would be made in.
	File() string
	// Changed reports whether applying would alter the file.
	Changed() bool
	// Apply makes the change.
	Apply() error
	// After is the project's skills as they would be.
	After() []string
}

// FileConfig is a ConfigSource over a resolved `pkg/config`.
//
// It is the adapter between the configuration package — which owns finding, parsing and writing
// a project's file, and refusing an unknown key — and this subsystem, which owns what a
// reference means.
type FileConfig struct{ resolved config.Config }

// NewFileConfig adapts a resolved configuration.
func NewFileConfig(resolved config.Config) *FileConfig { return &FileConfig{resolved: resolved} }

// ProjectPath implements ConfigSource.
func (c *FileConfig) ProjectPath() string { return c.resolved.Path }

// References implements ConfigSource.
func (c *FileConfig) References() ([]skills.Reference, error) {
	if len(c.resolved.Skills) == 0 {
		return nil, nil
	}
	return skills.ParseReferences(c.resolved.Skills)
}

// ProposeAddition implements ConfigSource.
func (c *FileConfig) ProposeAddition(reference skills.Reference) (SkillChange, error) {
	return c.resolved.ProposeSkillAddition(reference)
}

// ProposeRemoval implements ConfigSource.
func (c *FileConfig) ProposeRemoval(reference skills.Reference) (SkillChange, error) {
	return c.resolved.ProposeSkillRemoval(reference)
}

// Pin is one skill's recorded manifest: what the project agreed it was serving.
type Pin struct {
	// Reference is the skill this pin is for.
	Reference string `yaml:"ref"`
	// Files is the manifest as it was, each with its digest and size.
	Files []PinnedFile `yaml:"files"`
}

// PinnedFile is one file as a project pinned it.
type PinnedFile struct {
	Path   string `yaml:"path"`
	Size   int64  `yaml:"size"`
	Digest string `yaml:"digest"`
}

// PinStore holds a project's pins.
//
// It is written on first use, so a project that names a skill has a pin for it without having
// asked for one, and it is *read* rather than trusted: a pin that disagrees with the catalog is
// drift, which is reported.
type PinStore struct {
	path string

	mu    sync.RWMutex
	pins  map[string]Pin
	dirty bool
}

// NewPinStore returns the pin store for a project directory.
func NewPinStore(projectDir string) *PinStore {
	store := &PinStore{pins: map[string]Pin{}}
	if trimmed := strings.TrimSpace(projectDir); trimmed != "" {
		store.path = filepath.Join(trimmed, PinFileName)
	}
	return store
}

// Path is where the pins live, or empty when the deployment has no project directory.
func (s *PinStore) Path() string { return s.path }

// Get returns a skill's pin.
func (s *PinStore) Get(reference skills.Reference) (Pin, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pin, found := s.pins[reference.String()]
	return pin, found
}

// Record stores a skill's manifest as the pin, when the skill is not already pinned.
//
// A skill that is already pinned is left alone, because the pin is the agreement and this
// function is not being asked to change it: overwriting it here would silently accept a
// catalog's new content, which is exactly the decision a person has to make.
func (s *PinStore) Record(reference skills.Reference, files []skills.File) error {
	key := reference.String()
	s.mu.Lock()
	if _, pinned := s.pins[key]; pinned {
		s.mu.Unlock()
		return nil
	}
	pin := Pin{Reference: key, Files: make([]PinnedFile, 0, len(files))}
	for _, file := range files {
		pin.Files = append(pin.Files, PinnedFile{Path: file.Path, Size: file.Size, Digest: file.Digest})
	}
	s.pins[key] = pin
	s.dirty = true
	s.mu.Unlock()
	return s.Save()
}

// Forget removes a skill's pin, which is what happens when a project stops naming it.
func (s *PinStore) Forget(reference skills.Reference) error {
	key := reference.String()
	s.mu.Lock()
	_, pinned := s.pins[key]
	if pinned {
		delete(s.pins, key)
		s.dirty = true
	}
	s.mu.Unlock()
	if !pinned {
		return nil
	}
	return s.Save()
}

// All returns every pin, ordered by reference, so a report is reproducible.
func (s *PinStore) All() []Pin {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pins := make([]Pin, 0, len(s.pins))
	for _, pin := range s.pins {
		pins = append(pins, pin)
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Reference < pins[j].Reference })
	return pins
}

// Load reads the pins from disk.
//
// A missing file is an empty set, not an error: a project that has named no skill yet has
// nothing pinned, and that is the normal state of a project before its first use. A file that is
// there and unreadable *is* an error, because it means the deployment cannot tell what the
// project agreed to and would serve changed content as though it had not changed.
func (s *PinStore) Load() error {
	if s.path == "" {
		return nil
	}
	content, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return api.WrapError(api.KindInternal, err, "reading the project's pinned skills at %s", s.path)
	}
	var document struct {
		Skills []Pin `yaml:"skills"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		return api.WrapError(api.KindInvalid, err,
			"the project's pinned skills at %s are not readable; this framework cannot tell "+
				"what the project agreed to, so it will not serve a skill as though it had "+
				"not changed", s.path)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pin := range document.Skills {
		s.pins[pin.Reference] = pin
	}
	return nil
}

// Save writes the pins, if anything changed.
//
// The write goes through a temporary file and a rename, so an interrupted write cannot leave a
// project with a pin file this framework cannot read — which would be worse than no pin file,
// because a missing one means "nothing pinned yet" and a corrupt one means "cannot tell what
// you agreed to".
func (s *PinStore) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.RLock()
	dirty := s.dirty
	pins := make([]Pin, 0, len(s.pins))
	for _, pin := range s.pins {
		pins = append(pins, pin)
	}
	s.mu.RUnlock()
	if !dirty {
		return nil
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Reference < pins[j].Reference })

	rendered, err := yaml.Marshal(struct {
		Skills []Pin `yaml:"skills"`
	}{Skills: pins})
	if err != nil {
		return api.WrapError(api.KindInternal, err, "rendering the project's pinned skills")
	}
	header := "# Pinned skill manifests, maintained by Toolbox.\n" +
		"#\n" +
		"# A pin records the manifest of every skill this project names, so a change to a\n" +
		"# catalog's content is visible rather than silent. A changed skill is reported and\n" +
		"# still served: the pin is the agreement, and nothing here decides on your behalf.\n"
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, append([]byte(header), rendered...), 0o600); err != nil {
		return api.WrapError(api.KindInternal, err, "writing the project's pinned skills to %s", temporary)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		_ = os.Remove(temporary)
		return api.WrapError(api.KindInternal, err, "replacing the project's pinned skills at %s", s.path)
	}
	s.mu.Lock()
	s.dirty = false
	s.mu.Unlock()
	return nil
}

// Drift reports whether a pin still matches what a catalog holds.
//
// A pin with no files, which is what a skill recorded before it had a manifest would be, is not
// drift: there is nothing to disagree with.
func Drift(pin Pin, current []skills.File) (outdated bool, added, removed, changed []string) {
	if len(pin.Files) == 0 {
		return false, nil, nil, nil
	}
	pinned := make(map[string]PinnedFile, len(pin.Files))
	for _, file := range pin.Files {
		pinned[file.Path] = file
	}
	present := make(map[string]bool, len(current))
	for _, file := range current {
		present[file.Path] = true
		was, existed := pinned[file.Path]
		switch {
		case !existed:
			added = append(added, file.Path)
		case was.Digest != file.Digest:
			changed = append(changed, file.Path)
		}
	}
	for _, file := range pin.Files {
		if !present[file.Path] {
			removed = append(removed, file.Path)
		}
	}
	sort.Strings(added)
	sort.Strings(changed)
	sort.Strings(removed)
	return len(added)+len(changed)+len(removed) > 0, added, removed, changed
}
