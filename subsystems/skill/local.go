package skill

import (
	"path/filepath"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	"google.golang.org/protobuf/types/known/structpb"
)

// LocalCatalog is the catalog every project has without declaring it: the `skills/` directory
// under the project's configuration directory.
//
// It holds three things and delegates the reading. A project's own skills are a
// *directory* of skills like any other source, so `skills.Directory` reads them and a git
// checkout is read by the same code — which is what stops a manifest from meaning one thing for
// a project's own skill and another for the same skill in a checkout, and a pin from being two
// kinds of thing depending on where the skill came from.
//
// What is left here is what makes a local skill *different*, and none of it is about reading:
//
//   - it is implicit, so a project never names it and `local.x` in `skills:` is refused;
//   - it belongs to a person, so it is read-only and the framework never writes to it;
//   - a skill is switched off in its own frontmatter rather than by removing a line from a
//     list somewhere else.
type LocalCatalog struct {
	*skills.Directory
}

// NewLocalCatalog returns the local catalog for a project's configuration directory.
//
// A project directory is the `.toolbox` directory itself; a configuration file elsewhere
// describes a deployment rather than a project, and a deployment has no skills of its own. A
// project with no `skills/` directory has an empty local catalog, and an empty catalog is a
// project that offers nothing rather than a project that failed to start.
func NewLocalCatalog(projectDir string) *LocalCatalog {
	root := ""
	if trimmed := strings.TrimSpace(projectDir); trimmed != "" {
		root = filepath.Join(trimmed, "skills")
	}
	// The error is impossible — the identifier is a constant — and a value built by a
	// constructor that cannot fail is better than one whose constructor can.
	directory, _ := skills.NewDirectory(skills.DirectoryOptions{ID: skills.LocalCatalog, Root: root})
	return &LocalCatalog{Directory: directory}
}

// Skill reads one skill as the project has it.
//
// The only thing decided here rather than by the reader is the switched-off case, and it is
// here because it is a fact about a *project* rather than about a document: a local skill is
// part of the project without being named, so there is no entry in the configuration to remove,
// and the person who put the skill in their project is the person who switches it off — in the
// same place they put it.
func (c *LocalCatalog) Skill(name string) (skills.Skill, error) {
	skill, err := c.typedSkill(name)
	if err != nil {
		return skills.Skill{}, err
	}
	if !skill.IsEnabled() {
		return skills.Skill{}, api.Errorf(api.KindNotFound,
			"the skill %s.%s is switched off in its own frontmatter, with %senabled: false, "+
				"so it is not part of this project",
			skills.LocalCatalog, name, skills.ToolboxPrefix)
	}
	return skill, nil
}

// TemplateFor reads a skill, re-reading it only when its files have changed.
func (c *LocalCatalog) TemplateFor(name string) (skills.Template, error) {
	if _, err := c.Skill(name); err != nil {
		return skills.Template{}, err
	}
	return c.Cached(name)
}

// typedSkill reads the typed form without the switched-off check, so a caller that is asking
// *about* a skill can see why it is not served.
func (c *LocalCatalog) typedSkill(name string) (skills.Skill, error) {
	template, err := c.Template(name)
	if err != nil {
		return skills.Skill{}, err
	}
	return template.Skill()
}

// entry reads one local skill into the contract's shape.
func (c *LocalCatalog) entry(reference skills.Reference) (*skillv1.SkillEntry, error) {
	if _, err := c.Skill(reference.Name); err != nil {
		return nil, err
	}
	template, err := c.Cached(reference.Name)
	if err != nil {
		return nil, err
	}
	frontmatter, err := toStruct(template.Frontmatter.Document)
	if err != nil {
		return nil, err
	}
	return &skillv1.SkillEntry{
		Ref: &skillv1.SkillRef{
			Catalog: skills.LocalCatalog, Name: reference.Name,
			Description: template.Frontmatter.Skill.Description,
		},
		Uri:         reference.URI(),
		Frontmatter: frontmatter,
		Body:        template.Body,
		Resources:   toManifest(template.Files),
		MaxFiles:    skills.MaxFiles,
		MaxBytes:    skills.MaxBytes,
	}, nil
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
