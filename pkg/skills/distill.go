package skills

import (
	"bytes"
	"sort"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"go.yaml.in/yaml/v3"
)

// Entry is a skill as it is served to one client: the distilled form of a template.
//
// Two clients differing means one of them was given extra guidance, not a different opinion
// about what the skill says. Everything the author wrote is here, in their own words, and
// the only thing this package adds is a passage at the end of the body — so an entry's body
// is a superset of the template's, never a different skill.
type Entry struct {
	// Catalog is the catalog holding the skill, which is the first segment of its URI.
	Catalog string
	// Name is the skill's name.
	Name string
	// URI is the resource URI of the SKILL.md, "skill://<catalog>/<name>/SKILL.md".
	//
	// The catalog is always spelled, including for a project's own skill. The specification
	// requires the final path segment to equal the skill name and allows leading segments as
	// a server-chosen prefix, so this satisfies it — and one spelling per skill means the
	// string in a configuration file and the string in a URI are the same fact, with no rule
	// to learn about when a prefix disappears.
	URI string
	// Frontmatter is what the client is served: the author's document, minus the fields
	// this framework never carries, plus the client's own spelling of the model-invocation
	// fact.
	//
	// Every other field the author wrote is present, including ones this client ignores.
	// A host builds its registry from these entries alone, without fetching each SKILL.md,
	// so a field dropped here is a field no client could ever see.
	Frontmatter map[string]any
	// Body is the author's instructions, with any appended passage at the end.
	Body string
	// Content is the SKILL.md as it is served: the served frontmatter and this body, as the
	// bytes a client fetching the file will receive.
	//
	// It is a rendering rather than the author's own file, because the served frontmatter
	// carries a spelling of the model-invocation fact that the author's may not. That makes
	// two manifests, and they are two different artifacts rather than two versions of one:
	// this is the manifest of what this server serves to this client, and the catalog's
	// manifest — which is what a project's pin is taken against — is the author's file. A
	// client that fetches a file and verifies it against this entry gets the right answer,
	// which is the only thing the digests in an entry are for.
	Content []byte
	// Files is the manifest: every file of the skill, each exactly once, SKILL.md included.
	//
	// Complete by construction, and recomputed for the body as served — so the digest for
	// SKILL.md describes the bytes this server is about to send, which is what makes a
	// client's verification of what it fetched come out right.
	Files []File
	// Unmet lists the skill's declared requirements this deployment does not provide.
	//
	// It is reported rather than satisfied: a dependency is a statement of need, and
	// pretending to have met one costs the author the knowledge that it is missing.
	Unmet []ToolRequirement
	// Outdated is whether the manifest this was distilled from differs from the one the
	// project pinned.
	//
	// A changed skill is marked and reported rather than refused: the pin is the agreement,
	// drift is visible, and nothing is blocked behind the user's back.
	Outdated bool
}

// Reference is the entry's fully-qualified name, "<catalog>.<name>".
func (e Entry) Reference() string {
	if e.Catalog == "" {
		return e.Name
	}
	return e.Catalog + "." + e.Name
}

// ResourceURI is the URI of one of the skill's files, relative to the skill's own directory.
//
// Every file of a skill is an ordinary MCP resource under the skill's URI, and read with
// the standard `resources/read`. The skills extension is a way to publish instructions and
// their supporting files alongside the tools a server already serves — not a second content
// channel.
func (e Entry) ResourceURI(relative string) string {
	base := strings.TrimSuffix(e.URI, "/"+SkillFileName)
	return base + "/" + strings.TrimPrefix(relative, "/")
}

// File returns one file of the manifest by its path.
func (e Entry) File(relative string) (File, bool) { return findFile(e.Files, relative) }

// DistillOptions is what a projection needs beyond the template and the client.
type DistillOptions struct {
	// Unmet lists the skill's declared requirements that this deployment does not provide,
	// each with the reason. A caller computes it because only the caller knows what the
	// deployment exposes.
	Unmet []Unmet
	// Outdated marks the entry when the catalog's manifest for a pinned skill no longer
	// matches the pin.
	Outdated bool
}

// Unmet is a requirement the deployment cannot satisfy, and why.
type Unmet struct {
	// Requirement is what the skill declared.
	Requirement ToolRequirement
	// Reason says why it is not available, in terms a reader can act on.
	Reason string
}

// Distill projects a template for one client.
//
// It changes content and never the file set. Every file stays in the manifest, the author's
// description is never rewritten, and the only change to the body is a passage appended for
// something the client or the deployment cannot do. That is what makes aggregation the
// right shape here: a client never fails to see a skill because of what it can do, it
// receives the skill in the form it can act on.
func Distill(template Template, client Client, options DistillOptions) (Entry, error) {
	skill, err := template.Skill()
	if err != nil {
		return Entry{}, err
	}
	capabilities := CapabilitiesFor(client)

	body := template.Body
	appended := appendedPassage(template, skill, capabilities, options.Unmet)
	if appended != "" {
		body = joinBody(body, appended)
	}

	document := servedFrontmatter(template.Frontmatter.Document, skill, capabilities)
	content := RenderSkillMarkdown(document, body)
	files := serveManifest(template.Files, content)

	return Entry{
		Catalog:     template.Catalog,
		Name:        template.Name,
		URI:         SkillURI(template.Catalog, template.Name),
		Frontmatter: document,
		Body:        body,
		Content:     content,
		Files:       files,
		Unmet:       unmetRequirements(options.Unmet),
		Outdated:    options.Outdated,
	}, nil
}

// RenderSkillMarkdown renders a served frontmatter document and body as a SKILL.md.
//
// One function, so the file a client fetches and the entry it fetched it under cannot
// disagree — and they have to agree, because the entry's digest for SKILL.md is this file's
// digest. The rendering is deterministic: the same document and body always produce the same
// bytes, so a client verifying what it fetched twice gets the same answer twice.
func RenderSkillMarkdown(document map[string]any, body string) []byte {
	var rendered bytes.Buffer
	rendered.WriteString("---\n")
	encoder := yaml.NewEncoder(&rendered)
	// Two spaces, because the document is nested and a mapping nested under an indented
	// key is what a reader of this format expects to see.
	encoder.SetIndent(2)
	// A document this package built cannot fail to encode: every value in it came from a
	// YAML document or from this package's own types. A failure would mean a value nobody
	// anticipated reached the served frontmatter, and dropping it silently would serve a
	// skill that is missing a field the author wrote — so it is rendered without the
	// encoder's error and reported by the digest not matching, which is the honest signal.
	_ = encoder.Encode(document)
	_ = encoder.Close()
	rendered.WriteString("---\n\n")
	if body != "" {
		rendered.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			rendered.WriteString("\n")
		}
	}
	return rendered.Bytes()
}

// SkillURI is the resource URI of a skill's SKILL.md.
//
// The catalog is always the first segment, for every catalog including `local`. See the
// note on Entry.URI for why there is exactly one spelling.
func SkillURI(catalog, name string) string {
	return "skill://" + catalog + "/" + name + "/" + SkillFileName
}

// ParseSkillURI reads a skill's catalog and name from a URI of its SKILL.md.
//
// A URI that identifies no skill is refused rather than guessed at: a server that resolved
// `skill://local/SKILL.md` to some skill would be serving a skill its reader did not ask
// for, and the mistake would be invisible until the model read the wrong instructions.
func ParseSkillURI(uri string) (catalog, name string, err error) {
	catalog, name, relative, err := ParseResourceURI(uri)
	if err != nil {
		return "", "", err
	}
	if relative != SkillFileName {
		return "", "", api.Errorf(api.KindInvalid,
			"%q does not identify a skill's %s; a skill URI is %s<catalog>/<name>/%s",
			uri, SkillFileName, scheme, SkillFileName)
	}
	return catalog, name, nil
}

// scheme is what a skill's resources are served under.
const scheme = "skill://"

// ParseResourceURI reads which skill and which file a resource URI names.
//
// A resource URI is a skill's URI with a file path appended, and it is the one place a caller
// can be given a URI naming a file rather than a skill: `resources/read` takes whatever URI
// the client holds. So this is the general reader and ParseSkillURI is the narrow one.
func ParseResourceURI(uri string) (catalog, name, relative string, err error) {
	trimmed := strings.TrimSpace(uri)
	rest, found := strings.CutPrefix(trimmed, scheme)
	if !found {
		return "", "", "", api.Errorf(api.KindInvalid,
			"%q is not a skill URI; a skill's files are read as %s<catalog>/<name>/<path>",
			uri, scheme)
	}
	segments := strings.Split(rest, "/")
	if len(segments) < 3 {
		return "", "", "", api.Errorf(api.KindInvalid,
			"%q does not identify a skill's file; a skill URI is %s<catalog>/<name>/%s",
			uri, scheme, SkillFileName)
	}
	catalog, name = strings.TrimSpace(segments[0]), strings.TrimSpace(segments[1])
	relative = strings.Join(segments[2:], "/")
	if catalog == "" || name == "" {
		return "", "", "", api.Errorf(api.KindInvalid,
			"%q does not identify a skill; both the catalog and the skill's name are part of "+
				"its identity, because two catalogs may hold a skill of the same name",
			uri)
	}
	if strings.TrimSpace(relative) == "" {
		return "", "", "", api.Errorf(api.KindInvalid, "%q names no file of the skill", uri)
	}
	return catalog, name, relative, nil
}

// servedFrontmatter is what a client is served: the author's document with the two things
// this framework never carries removed, and the client's own spelling of one fact added.
//
// The removal is the whole of it. `allowed-tools` is a request for pre-approved access to
// the host, which one client of four grants and three ignore, and Toolbox's own properties
// are about how *this* deployment treats a skill rather than about the skill — so both stay
// on the template, where a person can read them, and neither reaches a client.
func servedFrontmatter(document map[string]any, skill Skill, capabilities Capabilities) map[string]any {
	served := deepCopyMap(document)

	delete(served, keyAllowedTools)
	delete(served, ToolboxPrefix)
	delete(served, strings.TrimSuffix(ToolboxPrefix, "/"))

	// One typed field, the client's spelling. Writing it whichever dialect the author used
	// is what makes the served document self-consistent: a skill that said
	// `disable-model-invocation: false` in the negating dialect is served the same fact as
	// `allow_implicit_invocation: true` to a client that asserts it, and to a client that
	// does not read either, the author's own field is left exactly as written.
	switch capabilities.ModelSpelling {
	case ModelSpellingNegated:
		if skill.ModelInvocation != nil {
			served[keyDisableModelInvoc] = !*skill.ModelInvocation
		}
	case ModelSpellingAsserted:
		if skill.ModelInvocation != nil {
			served = withBlockValue(served, keyCodexPolicy, keyCodexImplied, *skill.ModelInvocation)
		}
	case ModelSpellingNone:
		// The common denominator: what the author wrote, and nothing this framework
		// would have had to guess at.
	}

	// The same rule for the requirements a skill states. A skill that declared them in
	// Toolbox's typed block gets them in the block this client reads them in, and a client
	// that states them in no dialect keeps whatever the author wrote.
	if len(skill.Tools) > 0 && capabilities.RequirementsSpelling == RequirementsInCodexBlock {
		served = withBlockValue(served, keyCodexDependencies, "tools", renderedRequirements(skill.Tools))
	}
	return served
}

// withBlockValue sets one key inside a block, creating the block if the author did not write
// it and leaving everything else in it alone.
func withBlockValue(served map[string]any, block, key string, value any) map[string]any {
	nested, _ := asMap(served[block])
	if nested == nil {
		nested = map[string]any{}
	}
	nested[key] = value
	served[block] = nested
	return served
}

// renderedRequirements puts a typed tool list into the shape a client reads it in.
func renderedRequirements(requirements []ToolRequirement) []any {
	rendered := make([]any, 0, len(requirements))
	for _, requirement := range requirements {
		item := map[string]any{"name": requirement.Name}
		for key, value := range map[string]string{
			"type": requirement.Type, "description": requirement.Description,
			"transport": requirement.Transport, "url": requirement.URL,
		} {
			if value != "" {
				item[key] = value
			}
		}
		rendered = append(rendered, item)
	}
	return rendered
}

// serveManifest recomputes the manifest for the SKILL.md as served.
//
// Only SKILL.md changes, and it is recomputed rather than carried: a client that verifies
// what it fetched against the entry it was given has to be given a digest of the bytes this
// server is about to send. Every other file is byte-identical to the catalog's, so its
// digest is still the digest — which is why a projection needs no access to the other files'
// contents to be correct.
func serveManifest(files []File, content []byte) []File {
	served := copyManifest(files)
	for index := range served {
		if served[index].Path == SkillFileName {
			served[index].Size = int64(len(content))
			served[index].Digest = digest(content)
			served[index].MIMEType = MIMETypeFor(SkillFileName)
			return served
		}
	}
	return served
}

// appendedPassage is the one thing distillation adds to a body: how to work here when
// something the skill depends on is not available.
//
// It is a passage rather than a refusal because the skill is still mostly usable: a model
// told that a tool is missing can do the rest and say what it could not do, which is more
// useful than a model that was handed nothing. And it is honest about its own limits —
// where the alternative is derivable, as it is for a script whose text is in the manifest,
// the passage says so; where it is not, the passage says that rather than inventing one.
func appendedPassage(template Template, skill Skill, capabilities Capabilities, unmet []Unmet) string {
	var notes []string

	// A client that cannot run a script gets told to read it instead. Its text is in the
	// manifest, so following it by hand is genuinely available and the instruction can say
	// so rather than apologising.
	if !capabilities.RunsScripts {
		if scripts := filesUnder(template.Files, "scripts/"); len(scripts) > 0 {
			notes = append(notes, strings.Join([]string{
				"## Working here without executing scripts",
				"",
				"This skill ships scripts, and the client reading it cannot execute them. Each " +
					"one is readable as a resource of this skill, so read the script and carry out " +
					"what it does yourself, step by step, showing the work as you go.",
				"",
				"The scripts are: " + strings.Join(quotedList(scripts), ", ") + ".",
			}, "\n"))
		}
	}

	// A declared requirement the deployment does not provide. The framework cannot write the
	// alternative — only the author knows what the tool was for — so it names the
	// requirement, says what the author said it was for, and says what to do about it.
	if len(unmet) > 0 {
		lines := []string{
			"## Working here without everything this skill declared",
			"",
			"This skill declared tools that this deployment does not provide. Carry on without " +
				"them where you can, and where an instruction below depends on one, say that you " +
				"could not do it rather than working around it by guessing.",
			"",
		}
		for _, requirement := range unmet {
			lines = append(lines, "- **"+requirement.Requirement.Name+"**"+
				describeRequirement(requirement))
		}
		notes = append(notes, strings.Join(lines, "\n"))
	}

	return strings.Join(notes, "\n\n")
}

func describeRequirement(unmet Unmet) string {
	var parts []string
	if purpose := strings.TrimSpace(unmet.Requirement.Description); purpose != "" {
		parts = append(parts, " — the skill says it is for "+purpose)
	}
	if reason := strings.TrimSpace(unmet.Reason); reason != "" {
		parts = append(parts, " — unavailable because "+reason)
	}
	return strings.Join(parts, "")
}

// joinBody appends a passage to a body, keeping the author's text exactly as it was.
func joinBody(body, passage string) string {
	trimmed := strings.TrimRight(body, "\n")
	if trimmed == "" {
		return passage
	}
	return trimmed + "\n\n" + passage
}

func unmetRequirements(unmet []Unmet) []ToolRequirement {
	if len(unmet) == 0 {
		return nil
	}
	requirements := make([]ToolRequirement, 0, len(unmet))
	for _, one := range unmet {
		requirements = append(requirements, one.Requirement)
	}
	return requirements
}

func filesUnder(files []File, prefix string) []string {
	var matched []string
	for _, file := range files {
		if strings.HasPrefix(file.Path, prefix) {
			matched = append(matched, file.Path)
		}
	}
	sort.Strings(matched)
	return matched
}

func quotedList(values []string) []string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "`"+value+"`")
	}
	return quoted
}

func deepCopyMap(source map[string]any) map[string]any {
	copied := make(map[string]any, len(source))
	for key, value := range source {
		copied[key] = deepCopyValue(value)
	}
	return copied
}

func deepCopyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return deepCopyMap(typed)
	case map[any]any:
		if converted, ok := asMap(typed); ok {
			return deepCopyMap(converted)
		}
		return typed
	case []any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, deepCopyValue(item))
		}
		return items
	default:
		return value
	}
}

func sortStrings(values []string) { sort.Strings(values) }
