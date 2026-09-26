package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"go.yaml.in/yaml/v3"
)

// SkillFileName is the file a skill's frontmatter and instructions live in.
const SkillFileName = "SKILL.md"

// CodexFileName is the file Codex keeps its own blocks in, rather than in the frontmatter.
//
// Its being a file rather than a field is what makes manifest completeness and per-client
// behaviour stop competing: a skill that includes one carries it for every client, and the
// three that do not read it ignore it while it is listed either way.
const CodexFileName = "agents/openai.yaml"

// The MCP Skills extension's limits. A host must accept a skill up to these, so a skill
// over either is one no conforming host is obliged to load — which makes exceeding them a
// reason to refuse rather than a reason to truncate: a truncated skill is a skill whose
// instructions end mid-sentence, and a model acting on half a skill is worse than a model
// that was told the skill is not servable.
const (
	// MaxFiles is the most resources one skill may hold.
	MaxFiles = 512
	// MaxBytes is the most bytes one skill's files may total.
	MaxBytes = 16 << 20
)

// DigestPrefix is how a digest is written, so a value that is not one is recognisable.
const DigestPrefix = "sha256:"

// File is one file in a skill's manifest.
type File struct {
	// Path is within the skill, slash-separated, relative to the skill directory.
	Path string
	// Size is the file's byte length.
	Size int64
	// Digest is the SHA-256 of the raw bytes, as "sha256:" followed by 64 lowercase hex
	// characters.
	Digest string
	// MIMEType is what to serve the file as.
	MIMEType string
}

// Template is a skill as its author wrote it: the document, the typed reading of it, the
// instructions, and the file set.
//
// It is a template rather than a served skill because what a client receives is a
// distillation of it. The template itself is the portable artefact, and keeping the two
// apart is what lets the served form differ per client while the authored form does not
// change at all.
type Template struct {
	// Catalog is the identifier of the catalog holding the skill, which is the first
	// segment of every reference and URI for it.
	Catalog string
	// Name is the skill's name, which is also its directory's.
	Name string
	// Frontmatter is the document as written, alongside the typed reading of it.
	Frontmatter Frontmatter
	// Body is the SKILL.md content after the frontmatter, exactly as the author wrote it.
	Body string
	// Files is the manifest: every file of the skill, each exactly once, SKILL.md included.
	//
	// Complete by construction. A catalog that cannot enumerate a skill's files does not
	// serve it, because a manifest that is not complete cannot be the pin that drift is
	// detected against.
	Files []File
	// OpenAI holds the Codex blocks, read from agents/openai.yaml when the skill has one.
	//
	// It is a file rather than a frontmatter field, which is why manifest completeness is
	// not in tension with per-client behaviour: a template that includes one carries it for
	// every client, and the three that do not read it ignore it.
	OpenAI *CodexDocument
}

// CodexDocument is what an agents/openai.yaml file holds: the blocks Codex reads that do
// not belong in a frontmatter.
//
// It is a *separate file* in Codex's own layout, so it is read as a document of its own
// rather than as frontmatter, and a skill is not required to have one.
type CodexDocument struct {
	// Interface is how a client presents the skill.
	Interface CodexInterface
	// Policy states how the skill may be invoked.
	Policy CodexPolicy
	// Dependencies states what the skill needs.
	Dependencies CodexDependencies
}

// CodexInterface is the presentation block.
type CodexInterface struct {
	DisplayName      string `yaml:"display_name"`
	ShortDescription string `yaml:"short_description"`
	IconSmall        string `yaml:"icon_small"`
	IconLarge        string `yaml:"icon_large"`
	BrandColor       string `yaml:"brand_color"`
	DefaultPrompt    string `yaml:"default_prompt"`
}

// CodexPolicy is the invocation block.
type CodexPolicy struct {
	AllowImplicitInvocation *bool `yaml:"allow_implicit_invocation"`
}

// CodexDependencies is the requirements block.
type CodexDependencies struct {
	Tools []CodexTool `yaml:"tools"`
}

// CodexTool is one required tool.
type CodexTool struct {
	Type        string `yaml:"type"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Transport   string `yaml:"transport"`
	URL         string `yaml:"url"`
}

// File returns one file of the manifest by its path, as read from a catalog.
func (t Template) File(relative string) (File, bool) { return findFile(t.Files, relative) }

// Skill returns the typed skill, with the Codex file's blocks folded in.
//
// The file is applied after the frontmatter and the same disagreement rule applies: a skill
// stating a fact in both places with two values is refused, because two sources for one
// fact is what this framework keeps refusing.
func (t Template) Skill() (Skill, error) {
	skill := t.Frontmatter.Skill.clone()
	if t.OpenAI == nil {
		return skill, nil
	}
	claims := newClaimSet()

	if value := t.OpenAI.Policy.AllowImplicitInvocation; value != nil {
		if err := claims.agreeBool("model_invocation", CodexFileName, *value, skill.ModelInvocation); err != nil {
			return Skill{}, err
		}
		if skill.ModelInvocation == nil {
			skill.ModelInvocation = value
		}
	}
	// The file's blocks are a source like any other, so each is checked against whatever the
	// frontmatter already claimed and assigned only where nothing has. A skill stating one
	// fact in the frontmatter and in this file is refused, whichever way the two disagree.
	iface := t.OpenAI.Interface
	for _, field := range []struct {
		origin string
		parsed string
		target *string
	}{
		{"display_name", iface.DisplayName, &skill.DisplayName},
		{"short_description", iface.ShortDescription, &skill.ShortDescription},
		{"icon_small", iface.IconSmall, &skill.IconSmall},
		{"icon_large", iface.IconLarge, &skill.IconLarge},
		{"brand_color", iface.BrandColor, &skill.BrandColor},
		{"default_prompt", iface.DefaultPrompt, &skill.DefaultPrompt},
	} {
		from := CodexFileName + "." + field.origin
		if err := claims.agreeString(field.origin, from, field.parsed, *field.target); err != nil {
			return Skill{}, err
		}
		if strings.TrimSpace(*field.target) == "" {
			*field.target = field.parsed
		}
	}

	if len(t.OpenAI.Dependencies.Tools) > 0 {
		if len(skill.Tools) > 0 {
			return Skill{}, api.Errorf(api.KindInvalid,
				"this skill states its required tools in both the frontmatter and "+
					"agents/openai.yaml. One fact, one source — keep them in one of them")
		}
		skill.Tools = make([]ToolRequirement, 0, len(t.OpenAI.Dependencies.Tools))
		for _, tool := range t.OpenAI.Dependencies.Tools {
			skill.Tools = append(skill.Tools, ToolRequirement{
				Type:        tool.Type,
				Name:        tool.Name,
				Description: tool.Description,
				Transport:   tool.Transport,
				URL:         tool.URL,
			})
		}
	}
	return skill, nil
}

func readString(current *string, value, field, origin string) {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(*current) != "" {
		return
	}
	*current = value
	_ = field
	_ = origin
}

// Validate refuses a template that cannot be served, naming what is wrong.
//
// Validation happens on the template rather than on a projection, so a skill that is wrong
// is wrong for every client and is reported once rather than per client.
func (t Template) Validate() error {
	skill, err := t.Skill()
	if err != nil {
		return err
	}
	if name := strings.TrimSpace(skill.Name); name == "" {
		// A client that defaults the name from the directory would work, and one that
		// requires it would not. The standard requires it, so it is required here: a skill
		// with two names in two clients is a skill whose identity depends on who read it.
		return api.Errorf(api.KindInvalid, "%s has no name; a skill's name is required",
			t.Describe())
	}
	if err := validateSkillName(skill.Name, t.Describe()); err != nil {
		return err
	}
	if description := strings.TrimSpace(skill.Description); description == "" {
		return api.Errorf(api.KindInvalid, "%s has no description; a skill's description is "+
			"required, and it is the field a model selects the skill on", t.Describe())
	} else if len(description) > 1024 {
		return api.Errorf(api.KindInvalid,
			"%s has a description of %d characters, and the limit is 1024. A description is "+
				"what a model selects on, so it has to be short enough to compare",
			t.Describe(), len(description))
	}
	if t.Name != "" && skill.Name != t.Name {
		return api.Errorf(api.KindInvalid,
			"%s says it is named %q, but the skill's name is %q and they must match: a client "+
				"that trusts the frontmatter and a client that trusts the directory would "+
				"register it under two different names",
			t.Describe(), skill.Name, t.Name)
	}
	return validateManifest(t.Files, t.Describe())
}

// Describe names the skill in a message, by its qualified reference where one is known and
// by its file otherwise.
func (t Template) Describe() string {
	if t.Catalog == "" {
		return "the skill " + t.Name
	}
	return "the skill " + t.Reference()
}

// Reference is the skill's fully-qualified name, "<catalog>.<name>".
func (t Template) Reference() string {
	if t.Catalog == "" {
		return t.Name
	}
	return t.Catalog + "." + t.Name
}

// validateSkillName enforces the standard's name rule.
//
// Lowercase letters, digits and single hyphens, 1–64 characters. The rule exists because a
// name is a directory name and a URI segment and a slash command: a name that is not a valid
// path segment cannot be all three, and a skill whose identity depends on which client reads
// it is not a skill anybody can refer to.
func validateSkillName(name, where string) error {
	if len(name) > 64 {
		return api.Errorf(api.KindInvalid,
			"%s is named %q, which is %d characters, and the limit is 64",
			where, name, len(name))
	}
	previousHyphen := false
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			previousHyphen = false
		case character == '-':
			if previousHyphen {
				return api.Errorf(api.KindInvalid,
					"%s is named %q, which has two hyphens in a row; a name uses single hyphens "+
						"between its words", where, name)
			}
			previousHyphen = true
		default:
			return api.Errorf(api.KindInvalid,
				"%s is named %q, which contains %q. A name is lowercase letters, digits and "+
					"single hyphens, because it is also a directory name and a slash command",
				where, name, string(character))
		}
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return api.Errorf(api.KindInvalid,
			"%s is named %q, which starts or ends with a hyphen", where, name)
	}
	return nil
}

// validateManifest refuses a skill whose file set is over a limit, naming both what it holds
// and what the limit is.
func validateManifest(files []File, where string) error {
	if len(files) > MaxFiles {
		return api.Errorf(api.KindInvalid,
			"%s holds %d files, and the limit is %d. A host must accept a skill up to the "+
				"limit, so a skill over it is one no host is obliged to load",
			where, len(files), MaxFiles)
	}
	var total int64
	for _, file := range files {
		total += file.Size
	}
	if total > MaxBytes {
		return api.Errorf(api.KindInvalid,
			"%s holds %d bytes of files, and the limit is %d", where, total, MaxBytes)
	}
	return nil
}

// digest returns the content digest in the form the manifest and the specification write it.
func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// NewFile returns a manifest entry for some content, digesting it.
//
// It is exported because a manifest entry is the one thing two places must agree on: the reader
// that walks a directory, and the check that verifies what a catalog published. A caller that
// wrote its own digest computation would be a second statement of the same fact, and a
// mismatch between them would look exactly like tampering.
func NewFile(relative string, content []byte) File {
	return File{
		Path:     relative,
		Size:     int64(len(content)),
		Digest:   digest(content),
		MIMEType: MIMETypeFor(relative),
	}
}

// mimeTypeFor names what a file is served as.
//
// The table is small and stated rather than detected, because a content sniffer would serve
// a model's own output as whatever it looks like: the purpose of the manifest's media type
// is to say what a reader should treat the bytes as, and that is a fact about the skill's
// author, not about its content.
var mimeTypes = map[string]string{
	".md":    "text/markdown",
	".txt":   "text/plain",
	".json":  "application/json",
	".yaml":  "application/yaml",
	".yml":   "application/yaml",
	".csv":   "text/csv",
	".html":  "text/html",
	".js":    "text/javascript",
	".mjs":   "text/javascript",
	".ts":    "text/typescript",
	".py":    "text/x-python",
	".sh":    "text/x-shellscript",
	".go":    "text/x-go",
	".rs":    "text/x-rust",
	".tsv":   "text/tab-separated-values",
	".toml":  "application/toml",
	".xml":   "application/xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".svg":   "image/svg+xml",
	".webp":  "image/webp",
	".pdf":   "application/pdf",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".zip":   "application/zip",
}

// MIMETypeFor names what a skill file with this path is served as.
func MIMETypeFor(filePath string) string {
	extension := strings.ToLower(path.Ext(filePath))
	if known, ok := mimeTypes[extension]; ok {
		return known
	}
	// An extension nobody has an opinion about is served as bytes rather than guessed at,
	// so a reader is told the truth about it: this is a file of unknown kind.
	return "application/octet-stream"
}

// ReadDirectory reads a skill from a directory on disk.
//
// It is the whole of what a directory-backed catalog does with a skill, and it is a
// function rather than a type so that a catalog is free to be something else: a git
// checkout is a directory, a downloaded archive is not, and neither has to be this.
//
// The manifest is complete by construction, which is the property the whole design rests
// on. It is also deterministic — ordered by path, and carrying nothing about the machine it
// was produced on — so the same directory yields the same manifest on another machine and a
// digest in it means something.
func ReadDirectory(catalog, name, dir string) (Template, error) {
	manifest, err := manifestFromDir(dir)
	if err != nil {
		return Template{}, err
	}
	entry, found := findFile(manifest, SkillFileName)
	if !found {
		template := Template{Catalog: catalog, Name: name, Files: manifest}
		return Template{}, api.Errorf(api.KindNotFound,
			"the skill %s has no %s, and a skill without one has no frontmatter to read and "+
				"no instructions to give", template.Describe(), SkillFileName)
	}
	content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(entry.Path)))
	if err != nil {
		template := Template{Catalog: catalog, Name: name, Files: manifest}
		return Template{}, api.WrapError(api.KindInternal, err,
			"reading %s of %s", SkillFileName, template.Describe())
	}
	template, err := Parse(catalog, name, content, manifest)
	if err != nil {
		return Template{}, err
	}
	// The Codex blocks are a separate file, so the catalog reads it: a YAML document is
	// not frontmatter, and reading it here rather than in the frontmatter parser is what
	// keeps the two dialects distinguishable.
	if entry, found := findFile(manifest, CodexFileName); found {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(entry.Path)))
		if err != nil {
			return Template{}, api.WrapError(api.KindInternal, err, "reading %s of %s",
				CodexFileName, template.Describe())
		}
		document, err := ParseCodex(raw)
		if err != nil {
			return Template{}, err
		}
		template.OpenAI = document
	}
	return template, nil
}

// Parse reads a skill from its SKILL.md and a manifest the caller already computed.
//
// A catalog that holds files it did not read from a directory — one that downloaded them,
// or that read them out of an archive — uses this and supplies the manifest itself. The
// split is deliberate: reading a document is this package's business, and deciding what
// files exist is the catalog's, because only the catalog knows what its source contains.
func Parse(catalog, name string, content []byte, manifest []File) (Template, error) {
	document, body, err := splitFrontmatter(content)
	if err != nil {
		return Template{}, err
	}
	front, err := parseFrontmatter(document)
	if err != nil {
		return Template{}, err
	}
	return Template{
		Catalog:     catalog,
		Name:        name,
		Frontmatter: front,
		Body:        body,
		Files:       copyManifest(manifest),
	}, nil
}

// ParseCodex reads an agents/openai.yaml: the blocks Codex keeps out of the frontmatter.
//
// It is exported because a catalog reading files from somewhere other than a directory is
// exactly the case that needs it, and a reader only one catalog can reach is a reader two
// catalogs cannot share.
func ParseCodex(content []byte) (*CodexDocument, error) {
	var document CodexDocument
	if err := yaml.Unmarshal(content, &document); err != nil {
		return nil, api.WrapError(api.KindInvalid, err,
			"the %s of this skill is not valid YAML", CodexFileName)
	}
	return &document, nil
}

// manifestFromDir builds a manifest from a directory, deterministically.
//
// A skill directory is a filesystem walk, and the three things that make the result worth
// being a pin are that it is complete, that it is ordered, and that it says nothing about
// the machine it was produced on. A file's modification time and its owner are not part of
// the manifest, and no absolute path is recorded.
func manifestFromDir(dir string) ([]File, error) {
	// A directory that is not there is a skill that is not there, which is a different
	// failure from one that cannot be read. A catalog asked for a skill it does not hold
	// should hear that, and not a message about a filesystem it happens to use.
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, api.Errorf(api.KindNotFound, "there is no skill directory at %s", dir)
		}
		return nil, api.WrapError(api.KindInternal, err, "reading the skill directory %s", dir)
	}
	var files []File
	err := filepath.WalkDir(dir, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			// A symlink, a socket or a device is not a file of the skill. Following one
			// would let a manifest name content from outside the skill's own directory,
			// which is a hole in the pin rather than a feature of it.
			return nil
		}
		relative, relErr := filepath.Rel(dir, current)
		if relErr != nil {
			return relErr
		}
		content, readErr := os.ReadFile(current)
		if readErr != nil {
			return readErr
		}
		files = append(files, NewFile(filepath.ToSlash(relative), content))
		return nil
	})
	if err != nil {
		return nil, api.WrapError(api.KindInternal, err, "reading the skill directory %s", dir)
	}
	sortManifest(files)
	return files, nil
}

func findFile(files []File, want string) (File, bool) {
	for _, file := range files {
		if file.Path == want {
			return file, true
		}
	}
	return File{}, false
}

func copyManifest(files []File) []File {
	if len(files) == 0 {
		return nil
	}
	return append([]File(nil), files...)
}

// sortManifest orders a manifest by path, so the same directory always produces the same
// order and a diff between two manifests is readable.
func sortManifest(files []File) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}

// splitFrontmatter separates a SKILL.md into its frontmatter document and its body.
//
// The format is a YAML document between a pair of `---` lines, then the body. A file that
// does not open with that fence has no frontmatter at all, which is reported rather than
// treated as an empty one: a skill with no frontmatter has no name, and a reader that
// reported that as "no name" would send a person looking for a name that was never there.
func splitFrontmatter(content []byte) (map[string]any, string, error) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") && strings.TrimSpace(text) != "---" {
		return nil, "", api.Errorf(api.KindInvalid,
			"a skill's %s must open with a '---' line holding its frontmatter; this one opens "+
				"with %s", SkillFileName, firstLine(text))
	}
	rest := strings.TrimPrefix(text, "---\n")
	closing := strings.Index(rest, "\n---")
	if closing < 0 {
		return nil, "", api.Errorf(api.KindInvalid,
			"a skill's %s opens a frontmatter block with '---' and never closes it", SkillFileName)
	}
	document := rest[:closing]
	body := strings.TrimLeft(rest[closing+len("\n---"):], "\n")
	parsed, err := unmarshalYAML([]byte(document))
	if err != nil {
		return nil, "", err
	}
	return parsed, body, nil
}

func firstLine(text string) string {
	if line, _, found := strings.Cut(text, "\n"); found {
		return fmt.Sprintf("%q", line)
	}
	return fmt.Sprintf("%q", text)
}
