package skills

import (
	"sort"
	"strconv"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"go.yaml.in/yaml/v3"
)

// The frontmatter keys a client of each dialect reads. They are stated as the target
// clients' own documentation states them, because a reader that guesses at a key is a
// reader that silently drops a field its author wrote.
const (
	keyName              = "name"
	keyDescription       = "description"
	keyLicense           = "license"
	keyCompatibility     = "compatibility"
	keyMetadata          = "metadata"
	keyAllowedTools      = "allowed-tools"
	keyUserInvocable     = "user-invocable"
	keyDisableModelInvoc = "disable-model-invocation"
	keyWhenToUse         = "when_to_use"
	keyArgumentHint      = "argument-hint"
	keyArguments         = "arguments"
	keyDefaultPrompt     = "default_prompt"
	keyContext           = "context"
	keyModel             = "model"
	keyEffort            = "effort"
	keyDisallowedTools   = "disallowed-tools"
)

// The Codex dialect splits its features across blocks rather than putting them at the top
// level, and one of those blocks lives in a separate file rather than in the frontmatter at
// all. Both are read here, and the file is read by the reader that opens a skill directory
// rather than by a frontmatter parser, because a YAML file is not frontmatter.
const (
	keyCodexPolicy       = "policy"
	keyCodexInterface    = "interface"
	keyCodexDependencies = "dependencies"
	keyCodexImplied      = "allow_implicit_invocation"
)

// toolboxBlock is the shape of the properties this framework defines under its own
// namespace, as a tree rather than as a list of dotted paths.
//
// It is a tree because the block is nested, and because the list of accepted properties has
// to be derivable from what is actually read: a message that lists the accepted names must
// not be able to drift from the reader that accepts them, or a person fixing a typo is told
// a set of options this package does not have. The tree is the single statement of both.
var toolboxBlock = &toolboxNode{children: []*toolboxNode{
	{
		name: "enabled",
		read: func(out *Skill, value any, claims *claimSet, path string) error {
			return claimBool(value, "enabled", fullToolboxPath(path), &out.Enabled, claims)
		},
	},
	{name: "invocation", children: []*toolboxNode{
		{
			name: "user_invocable",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				return claimBool(value, "user_invocable", fullToolboxPath(path), &out.UserInvocable, claims)
			},
		},
		{
			// The one field two clients spell differently. It is one field here, and the two
			// spellings are two readings of it, so a skill stating it twice in both
			// spellings correctly is not a contradiction.
			name: "model_invocation",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				return claimBool(value, "model_invocation", fullToolboxPath(path), &out.ModelInvocation, claims)
			},
		},
		{
			name: "when_to_use",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				return claimText(value, "when_to_use", fullToolboxPath(path), &out.WhenToUse, claims)
			},
		},
		{
			name: "argument_hint",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				return claimText(value, "argument_hint", fullToolboxPath(path), &out.ArgumentHint, claims)
			},
		},
		{
			name: "arguments",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				return claimList(value, "Arguments", fullToolboxPath(path), &out.Arguments, claims)
			},
		},
		{
			name: "default_prompt",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				return claimText(value, "default_prompt", fullToolboxPath(path), &out.DefaultPrompt, claims)
			},
		},
		{
			name: "context",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				parsed, err := ParseContext(asString(value))
				if err != nil {
					return err
				}
				return claimText(string(parsed), "context", fullToolboxPath(path), (*string)(&out.Context), claims)
			},
		},
	}},
	{name: "execution", children: []*toolboxNode{
		{
			name: "model",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				return claimText(value, "model", fullToolboxPath(path), &out.Model, claims)
			},
		},
		{
			name: "effort",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				parsed, err := ParseEffort(asString(value))
				if err != nil {
					return err
				}
				return claimText(string(parsed), "effort", fullToolboxPath(path), (*string)(&out.Effort), claims)
			},
		},
	}},
	{name: "permissions", children: []*toolboxNode{
		{
			// Read, kept, and never served. Reading it is not honouring it, and
			// AllowedToolsAreServed is where the second decision is written down.
			name: "allowed_tools",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				return claimList(value, "AllowedTools", fullToolboxPath(path), &out.AllowedTools, claims)
			},
		},
		{
			name: "disallowed_tools",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				return claimList(value, "DisallowedTools", fullToolboxPath(path), &out.DisallowedTools, claims)
			},
		},
	}},
	{name: "requirements", children: []*toolboxNode{
		{
			name: "tools",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				parsed, err := asToolRequirements(value)
				if err != nil {
					return err
				}
				origin := fullToolboxPath(path)
				if err := claims.agree("tools", origin, renderRequirements(parsed),
					renderRequirements(out.Tools), len(out.Tools) > 0); err != nil {
					return err
				}
				if len(out.Tools) == 0 {
					out.Tools = parsed
				}
				return nil
			},
		},
	}},
	{name: "presentation", children: []*toolboxNode{
		stringNode("display_name", func(out *Skill) *string { return &out.DisplayName }),
		stringNode("short_description", func(out *Skill) *string { return &out.ShortDescription }),
		stringNode("icon_small", func(out *Skill) *string { return &out.IconSmall }),
		stringNode("icon_large", func(out *Skill) *string { return &out.IconLarge }),
		stringNode("brand_color", func(out *Skill) *string { return &out.BrandColor }),
	}},
	{name: "classification", children: []*toolboxNode{
		stringNode("license", func(out *Skill) *string { return &out.License }),
		stringNode("compatibility", func(out *Skill) *string { return &out.Compatibility }),
		{
			name: "metadata",
			read: func(out *Skill, value any, claims *claimSet, path string) error {
				parsed := asStringMap(value)
				if err := claims.agree("metadata", fullToolboxPath(path), renderMetadata(parsed),
					renderMetadata(out.Metadata), len(out.Metadata) > 0); err != nil {
					return err
				}
				if len(out.Metadata) == 0 {
					out.Metadata = parsed
				}
				return nil
			},
		},
	}},
}}

// stringNode is a leaf holding text, which is most of the block.
func stringNode(name string, field func(*Skill) *string) *toolboxNode {
	return &toolboxNode{name: name, read: func(out *Skill, value any, claims *claimSet, path string) error {
		return claimText(value, name, fullToolboxPath(path), field(out), claims)
	}}
}

// toolboxNode is one property, or one block of properties, under the namespace.
type toolboxNode struct {
	// name is the property's own name, without the namespace.
	name string
	// read assigns a leaf's value. A node with children has none.
	read func(out *Skill, value any, claims *claimSet, path string) error
	// children are the properties of a block.
	children []*toolboxNode
}

func (n *toolboxNode) child(name string) *toolboxNode {
	for _, candidate := range n.children {
		if candidate.name == name {
			return candidate
		}
	}
	return nil
}

// paths returns every property under this node, as a namespace-prefixed path, in a stable
// order. It is what an error lists, and it is derived from the tree rather than stated beside
// it so the two cannot disagree.
func (n *toolboxNode) paths() []string {
	names := make([]string, 0, len(n.children))
	for _, child := range n.children {
		if len(child.children) == 0 {
			names = append(names, child.name)
			continue
		}
		for _, nested := range child.paths() {
			names = append(names, child.name+"."+nested)
		}
	}
	sort.Strings(names)
	return names
}

// Frontmatter is one skill's parsed frontmatter.
//
// The document is kept as it was written — Raw — alongside the typed reading of it,
// because the MCP Skills extension requires the served frontmatter to carry every field
// the author wrote rather than a curated subset: a host builds its registry from these
// entries alone, without fetching each SKILL.md, so a field dropped here is a field no
// client could ever see.
type Frontmatter struct {
	// Document is the frontmatter as it was written, with every key the author used.
	Document map[string]any
	// Skill is the typed reading of that document, with the Toolbox block folded in.
	Skill Skill
}

// parseFrontmatter reads a skill's frontmatter document into both forms.
//
// The two forms exist because the format is portable and the union is wider than any one
// client: the document is what is served, and the typed reading is what the framework
// reasons with. Neither is derived from the other after the fact, because the document is
// authoritative for anything the framework has no opinion about.
func parseFrontmatter(document map[string]any) (Frontmatter, error) {
	if document == nil {
		document = map[string]any{}
	}
	front := Frontmatter{Document: document}

	// A claim is a statement about one typed field, from one place. Two claims about the
	// same field that disagree are a template stating one fact twice with two values, and
	// that is refused rather than resolved: the framework has no way to choose, and a
	// choice it made silently would be one the author never wrote.
	claims := newClaimSet()

	readIdentity(document, &front.Skill)
	readClassification(document, &front.Skill)

	// The Toolbox block first, so a client-native field written after it is the one
	// reported as disagreeing. The order is a reporting choice and nothing more: both
	// sources are read either way, and only a disagreement is refused.
	if err := readToolboxBlock(document, &front.Skill, claims); err != nil {
		return Frontmatter{}, err
	}
	if err := readClientDialect(document, &front.Skill, claims); err != nil {
		return Frontmatter{}, err
	}
	return front, nil
}

func readIdentity(document map[string]any, out *Skill) {
	out.Name = stringField(document, keyName)
	out.Description = stringField(document, keyDescription)
}

func readClassification(document map[string]any, out *Skill) {
	out.License = stringField(document, keyLicense)
	out.Compatibility = stringField(document, keyCompatibility)
	out.Metadata = stringMapField(document, keyMetadata)
}

// readToolboxBlock reads the typed block, refusing a property it does not define.
//
// A misspelled property under the framework's own namespace is an error rather than an
// ignored key, because the author wrote it expecting it to be read: a silently dropped
// `invocation.when_to_us` is a skill that behaves differently from the one its author
// described, and nothing in the result would say so.
func readToolboxBlock(document map[string]any, out *Skill, claims *claimSet) error {
	// The block is one key whose value is a mapping. Both spellings of the namespace are
	// read — with and without the trailing slash — because a YAML key containing slashes is
	// legal and an author writing the namespace out by hand may or may not close it, and
	// refusing one of them would be refusing a spelling of a key this framework published.
	block, found := asMap(document[ToolboxPrefix])
	if !found {
		block, found = asMap(document[strings.TrimSuffix(ToolboxPrefix, "/")])
	}
	if !found {
		return nil
	}
	return readToolboxNode(block, toolboxBlock, "", out, claims)
}

// readToolboxNode reads one level of the block, at the path walked to reach it.
func readToolboxNode(
	block map[string]any, node *toolboxNode, prefix string, out *Skill, claims *claimSet,
) error {
	// Sorted so that a block with two problems is refused on the same one every time. A
	// reader fixing it should meet the same message twice rather than a different one each
	// run, which is the same determinism a manifest needs.
	for _, key := range sortedKeysOf(block) {
		path := prefix + key
		child := node.child(key)
		if child == nil {
			return api.Errorf(api.KindInvalid,
				"%s is not a property this framework defines. The properties under %s are: %s",
				fullToolboxPath(path), ToolboxPrefix, strings.Join(toolboxPropertyNames(), ", "))
		}
		value := block[key]
		if len(child.children) > 0 {
			nested, ok := asMap(value)
			if !ok {
				return api.Errorf(api.KindInvalid,
					"%s is %s, and it is a block: its properties are %s",
					fullToolboxPath(path), describeValue(value), strings.Join(child.names(), ", "))
			}
			if err := readToolboxNode(nested, child, path+".", out, claims); err != nil {
				return err
			}
			continue
		}
		if err := child.read(out, value, claims, path); err != nil {
			return err
		}
	}
	return nil
}

func (n *toolboxNode) names() []string {
	names := make([]string, 0, len(n.children))
	for _, child := range n.children {
		names = append(names, child.name)
	}
	sort.Strings(names)
	return names
}

func fullToolboxPath(relative string) string { return ToolboxPrefix + relative }

// toolboxPropertyNames is every property under the Toolbox namespace, in a stable order, so
// an error can list what is accepted. It is read off the tree, so it cannot list a property
// this package does not read or omit one it does.
func toolboxPropertyNames() []string {
	paths := toolboxBlock.paths()
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, ToolboxPrefix+path)
	}
	return names
}

// readClientDialect reads the fields a client of this framework's targets reads in its own
// spelling.
//
// Every field is left in the document and merely *read*: a client that does not recognise a
// field ignores it, and three of the four say so in their documentation. So nothing is
// dropped or renamed on the way out, and this function's only job is to reach an agreement
// with the Toolbox block where the two speak about the same fact.
func readClientDialect(document map[string]any, out *Skill, claims *claimSet) error {
	// Each field is read, checked against whatever the typed block already said about the
	// same fact, and assigned only if nothing has claimed it yet. The order is a reporting
	// choice and nothing more: every source is read either way, and only a disagreement is
	// refused.
	if err := readBoolField(document, keyUserInvocable, "user_invocable",
		out.UserInvocable, &out.UserInvocable, claims); err != nil {
		return err
	}
	// The one field two clients spell differently. Claude Code and Copilot read the
	// negation; Codex reads the assertion. Both are read here, and both land on the one typed
	// field, so the framework holds one opinion about whether the model may load a skill
	// however many of them asked — and a skill stating the same fact in both spellings
	// correctly is not a contradiction.
	if value, present := document[keyDisableModelInvoc]; present {
		negated, err := parseBool(value, keyDisableModelInvoc)
		if err != nil {
			return err
		}
		allowed := !negated
		if err := claims.agreeBool("model_invocation", keyDisableModelInvoc, allowed, out.ModelInvocation); err != nil {
			return err
		}
		if out.ModelInvocation == nil {
			out.ModelInvocation = &allowed
		}
	}

	if err := readTextField(document, keyWhenToUse, "when_to_use", &out.WhenToUse, claims); err != nil {
		return err
	}
	if err := readTextField(document, keyArgumentHint, "argument_hint", &out.ArgumentHint, claims); err != nil {
		return err
	}
	if err := readListField(document, keyArguments, "arguments", &out.Arguments, claims); err != nil {
		return err
	}
	if err := readTextField(document, keyDefaultPrompt, "default_prompt", &out.DefaultPrompt, claims); err != nil {
		return err
	}
	if value, present := document[keyContext]; present {
		parsed, err := ParseContext(asString(value))
		if err != nil {
			return err
		}
		if err := claims.agreeString("context", keyContext, string(parsed), string(out.Context)); err != nil {
			return err
		}
		if out.Context == "" {
			out.Context = parsed
		}
	}
	if err := readTextField(document, keyModel, "model", &out.Model, claims); err != nil {
		return err
	}
	if value, present := document[keyEffort]; present {
		parsed, err := ParseEffort(asString(value))
		if err != nil {
			return err
		}
		if err := claims.agreeString("effort", keyEffort, string(parsed), string(out.Effort)); err != nil {
			return err
		}
		if out.Effort == "" {
			out.Effort = parsed
		}
	}
	if err := readListField(document, keyDisallowedTools, "disallowed_tools", &out.DisallowedTools, claims); err != nil {
		return err
	}

	// The Codex blocks, when an author put them in the frontmatter. Codex's own layout keeps
	// them in a separate file, and a file is read by the directory reader; accepting them
	// here as well means a template that states them inline and one that states them in
	// agents/openai.yaml are read the same way.
	if policy, found := asMap(document[keyCodexPolicy]); found {
		origin := keyCodexPolicy + "." + keyCodexImplied
		if value, present := policy[keyCodexImplied]; present {
			parsed, err := parseBool(value, origin)
			if err != nil {
				return err
			}
			if err := claims.agreeBool("model_invocation", origin, parsed, out.ModelInvocation); err != nil {
				return err
			}
			if out.ModelInvocation == nil {
				out.ModelInvocation = &parsed
			}
		}
	}
	if block, found := asMap(document[keyCodexInterface]); found {
		for _, field := range []struct {
			key, name string
			target    *string
		}{
			{"display_name", "display_name", &out.DisplayName},
			{"short_description", "short_description", &out.ShortDescription},
			{"icon_small", "icon_small", &out.IconSmall},
			{"icon_large", "icon_large", &out.IconLarge},
			{"brand_color", "brand_color", &out.BrandColor},
		} {
			if err := readTextField(block, field.key, field.name, field.target, claims); err != nil {
				return err
			}
		}
	}
	if dependencies, found := asMap(document[keyCodexDependencies]); found {
		parsed, err := asToolRequirements(dependencies["tools"])
		if err != nil {
			return err
		}
		origin := keyCodexDependencies + ".tools"
		if err := claims.agree("tools", origin, renderRequirements(parsed), renderRequirements(out.Tools), len(out.Tools) > 0); err != nil {
			return err
		}
		if len(out.Tools) == 0 {
			out.Tools = parsed
		}
	}

	// allowed-tools is read from the document, kept on the template, and never served.
	// Reading it is not the same as honouring it, and AllowedToolsAreServed is where the
	// second decision is written down.
	if value, present := document[keyAllowedTools]; present {
		parsed := asStringList(value)
		if err := claims.agreeList("allowed_tools", keyAllowedTools, parsed, out.AllowedTools); err != nil {
			return err
		}
		if len(out.AllowedTools) == 0 {
			out.AllowedTools = parsed
		}
	}
	return nil
}

// readBoolField reads a client's own boolean field, if the document has it.
func readBoolField(
	document map[string]any, key, field string, current *bool, target **bool, claims *claimSet,
) error {
	value, present := document[key]
	if !present {
		return nil
	}
	parsed, err := parseBool(value, key)
	if err != nil {
		return err
	}
	if err := claims.agreeBool(field, key, parsed, current); err != nil {
		return err
	}
	if *target == nil {
		*target = &parsed
	}
	return nil
}

// readTextField reads a client's own text field, if the document has it.
func readTextField(
	document map[string]any, key, field string, target *string, claims *claimSet,
) error {
	value, present := document[key]
	if !present {
		return nil
	}
	return claimText(value, field, key, target, claims)
}

// readListField reads a client's own list field, if the document has it.
func readListField(
	document map[string]any, key, field string, target *[]string, claims *claimSet,
) error {
	value, present := document[key]
	if !present {
		return nil
	}
	return claimList(value, field, key, target, claims)
}

// claimText is the one place a text feature is read, whichever dialect it came from, so two
// dialects cannot disagree about how a disagreement is detected.
func claimText(value any, field, origin string, target *string, claims *claimSet) error {
	parsed := asString(value)
	if err := claims.agreeString(field, origin, parsed, *target); err != nil {
		return err
	}
	if strings.TrimSpace(*target) == "" {
		*target = parsed
	}
	return nil
}

// claimList is the one place a list feature is read, for the same reason as claimText.
func claimList(value any, field, origin string, target *[]string, claims *claimSet) error {
	parsed := asStringList(value)
	if err := claims.agreeList(field, origin, parsed, *target); err != nil {
		return err
	}
	if len(*target) == 0 {
		*target = parsed
	}
	return nil
}

// claimBool is the one place a boolean feature is read, for the same reason as claimText.
func claimBool(value any, field, origin string, target **bool, claims *claimSet) error {
	parsed, err := parseBool(value, origin)
	if err != nil {
		return err
	}
	if err := claims.agreeBool(field, origin, parsed, *target); err != nil {
		return err
	}
	if *target == nil {
		*target = &parsed
	}
	return nil
}

// renderMetadata puts a metadata map in a form two of them can be compared in. The keys are
// sorted, because the same two maps written in a different order are the same two maps.
func renderMetadata(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rendered := make([]string, 0, len(keys))
	for _, key := range keys {
		rendered = append(rendered, key+"="+values[key])
	}
	return strings.Join(rendered, "\x1e")
}

// renderRequirements puts a tool list in a form two of them can be compared in.
func renderRequirements(requirements []ToolRequirement) string {
	rendered := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		rendered = append(rendered, strings.Join([]string{
			requirement.Type, requirement.Name, requirement.Description,
			requirement.Transport, requirement.URL,
		}, "\x1f"))
	}
	return strings.Join(rendered, "\x1e")
}

// claimSet refuses a template that states one fact twice with two values.
//
// It is the disagreement this framework refuses everywhere else, applied to a document: two
// sources for one fact mean there is no correct answer to give a client, and a choice made
// silently would be one the author never wrote. So a second claim about a field has to agree
// with the first, whichever dialect it came from — the typed block, a client's own field, or
// the Codex file.
type claimSet struct {
	stated map[string]claim
}

// claim is one statement about one field: where it came from, and what it said.
type claim struct {
	origin string
	// value is the statement rendered for comparison. It is a rendering rather than the
	// parsed value because the comparison is "do these two say the same thing", and two
	// spellings of the same text are the same thing.
	value string
}

func newClaimSet() *claimSet { return &claimSet{stated: map[string]claim{}} }

// agree records a statement about a field, and refuses one that contradicts an earlier one.
//
// current is what the field already holds, which is how two sources are compared without the
// claim set having to know anything about any field's type: a field that is unset cannot
// disagree, and one that is set can only disagree with the value it holds.
func (c *claimSet) agree(field, origin, value, current string, isSet bool) error {
	previous, seen := c.stated[field]
	if !seen {
		c.stated[field] = claim{origin: origin, value: value}
		return nil
	}
	if isSet && current != value {
		return api.Errorf(api.KindInvalid,
			"this skill states %s twice with different values: %s says %q and %s says %q. "+
				"One fact, one source — remove one of them, or make them agree",
			field, previous.origin, previous.value, origin, value)
	}
	return nil
}

// agreeBool records a statement about a boolean field.
func (c *claimSet) agreeBool(field, origin string, parsed bool, current *bool) error {
	isSet := current != nil
	rendered := ""
	if isSet {
		rendered = strconv.FormatBool(*current)
	}
	return c.agree(field, origin, strconv.FormatBool(parsed), rendered, isSet)
}

// agreeString records a statement about a text field.
func (c *claimSet) agreeString(field, origin, parsed, current string) error {
	return c.agree(field, origin, parsed, current, strings.TrimSpace(current) != "")
}

// agreeList records a statement about a list field.
//
// The comparison is over the rendered list, so a skill stating the same tools in a different
// order is a disagreement — and it should be, because there is no reason for a second source
// to have reordered them, and a list is a set of permissions as far as a client is concerned.
func (c *claimSet) agreeList(field, origin string, parsed, current []string) error {
	rendered := strings.Join(parsed, ", ")
	return c.agree(field, origin, rendered, strings.Join(current, ", "), len(current) > 0)
}

// unmarshalYAML reads a YAML document into the generic shape the typed reading works on.
//
// A value is rejected rather than coerced in one place only: a frontmatter that is not a
// mapping is not a frontmatter, and a reader that accepted a sequence here would fail later
// with a message about a field the author never wrote.
func unmarshalYAML(data []byte) (map[string]any, error) {
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, api.WrapError(api.KindInvalid, err, "the frontmatter is not valid YAML")
	}
	if document == nil {
		return map[string]any{}, nil
	}
	return document, nil
}
