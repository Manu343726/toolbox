package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	"go.yaml.in/yaml/v3"
)

// A change to a project's configuration file is proposed before it is made, and it names the
// file and the change when it is asked about.
//
// The reason is not that a configuration file is fragile — it is text — but that it is the one
// file in a project that states what the project *is*, and a person writes and reviews it. An
// agent that changes it changes what that person's next review will contain, in a way they
// did not type. So proposing is the interface, not a wrapper: a caller cannot apply a change
// without first having something to show.
//
// This is AGENTS.md rule 17, stated as a general rule rather than a skills one, and it is
// implemented as a value the agent has to relay and the user has to answer rather than as a
// prompt. That is deliberate: a rule that only held where the transport can ask would not be a
// framework rule. See docs/mcp.md.

// SkillChange is a proposed change to a project's `skills:` list.
//
// It carries the file and the line that would change, so a caller can hand both to a person
// and get an answer. It is not a diff and not a patch: the change is one reference added or
// removed, and the rest of the file is not this package's business.
type SkillChange struct {
	// file is the configuration file the change would be made in.
	file string
	// before and after are the list as it is and as it would be.
	before []string
	after  []string
	// action is what the change does, for a message.
	action string
	// reference is the reference the change is about.
	reference skills.Reference
}

// File is the configuration file the change would be made in.
func (c SkillChange) File() string { return c.file }

// Reference is the reference the change is about.
func (c SkillChange) Reference() skills.Reference { return c.reference }

// Summary describes the change in one line, naming the file and the change.
//
// It is the sentence a person reads before answering, so it states what would be different
// afterwards rather than what would be done: "add" is an action somebody can decline, and
// "the project would then include" is the fact they are agreeing to.
func (c SkillChange) Summary() string {
	switch c.action {
	case actionAdd:
		return fmt.Sprintf(
			"%s: add %q to the project's skills, so the project would then include the "+
				"%s skill from the %s catalog",
			c.file, c.reference.String(), c.reference.Name, c.reference.Catalog)
	default:
		return fmt.Sprintf(
			"%s: remove %q from the project's skills, so the project would then no longer "+
				"include the %s skill from the %s catalog",
			c.file, c.reference.String(), c.reference.Name, c.reference.Catalog)
	}
}

// After is the list as it would be, for a caller that wants to show the whole thing.
func (c SkillChange) After() []string { return append([]string(nil), c.after...) }

const (
	actionAdd    = "add"
	actionRemove = "remove"
)

// ProposeSkillAddition proposes adding a reference to the project's `skills:` list.
//
// "Adding a skill to the project" means adding a reference to the project's configuration
// file, not copying files into it. The catalogs are never written: a remote skill is fetched,
// read and verified where the catalog keeps it, and a project gains access to it by naming
// it. That is what keeps the read-only claim about every catalog true, and why a read-only
// catalog is a limitation here rather than a missing feature.
func (c Config) ProposeSkillAddition(reference skills.Reference) (SkillChange, error) {
	if reference.Catalog == "" || reference.Name == "" {
		return SkillChange{}, api.Errorf(api.KindInvalid,
			"%q is not a qualified skill reference; a reference is <catalog>.<name>",
			reference.String())
	}
	if reference.IsLocal() {
		// A local skill is included implicitly, so naming one is not merely redundant — it
		// records a dependency the project does not have, and a project whose configuration
		// says otherwise from its own directory is a project nobody can reason about.
		return SkillChange{}, api.Errorf(api.KindInvalid,
			"%q is a skill in the %s catalog, and every skill in %s is already part of the "+
				"project without being named. Name it only if it is from another catalog",
			reference.String(), skills.LocalCatalog, skills.LocalCatalog)
	}
	return c.propose(reference, actionAdd)
}

// ProposeSkillRemoval proposes removing a reference from the project's `skills:` list.
func (c Config) ProposeSkillRemoval(reference skills.Reference) (SkillChange, error) {
	return c.propose(reference, actionRemove)
}

func (c Config) propose(reference skills.Reference, action string) (SkillChange, error) {
	file, err := c.projectFile()
	if err != nil {
		return SkillChange{}, err
	}
	before, err := readSkillList(file)
	if err != nil {
		return SkillChange{}, err
	}
	written := reference.String()
	present := false
	for _, entry := range before {
		if strings.EqualFold(entry, written) {
			present = true
			break
		}
	}

	after := make([]string, 0, len(before)+1)
	switch {
	case action == actionAdd && present:
		// Adding something the project already has is not an error, because the outcome the
		// caller wanted is the one already in place. Reporting it as a change would send a
		// person to confirm a diff that is empty.
		return SkillChange{file: file, before: before, after: before, action: actionAdd,
			reference: reference}, nil
	case action == actionAdd:
		after = append(after, written)
		after = append(after, before...)
	default:
		if !present {
			return SkillChange{}, api.Errorf(api.KindNotFound,
				"%s does not list %q, so there is nothing to remove", file, written)
		}
		for _, entry := range before {
			if !strings.EqualFold(entry, written) {
				after = append(after, entry)
			}
		}
	}
	return SkillChange{file: file, before: before, after: after, action: action, reference: reference}, nil
}

// Changed reports whether applying the change would alter the file.
func (c SkillChange) Changed() bool {
	if len(c.before) != len(c.after) {
		return true
	}
	for index := range c.before {
		if c.before[index] != c.after[index] {
			return true
		}
	}
	return false
}

// Apply makes the change.
//
// It is a separate call from the one that proposed it so that the two cannot be confused: a
// caller that has not asked for a summary has not asked a person, and this method is where
// that would show. Nothing here asks anything — the framework has no way to reach the user
// from every transport it serves, and a rule that depended on that would hold on some of them
// and not others.
func (c SkillChange) Apply() error {
	if !c.Changed() {
		return nil
	}
	return writeSkillList(c.file, c.after)
}

// projectFile is the project's own configuration file, which is the only one a project says
// things about itself in.
func (c Config) projectFile() (string, error) {
	if strings.TrimSpace(c.Path) == "" {
		return "", api.Errorf(api.KindNotFound,
			"no configuration file was read, so there is no project to record a skill in. "+
				"A project's skills are named in its own .toolbox/%s", FileName)
	}
	if filepath.Base(filepath.Dir(c.Path)) != ProjectDir {
		return "", api.Errorf(api.KindInvalid,
			"%s is not a project's own configuration file — a project's lives in a %s "+
				"directory — and a project says things about itself only in its own file",
			c.Path, ProjectDir)
	}
	return c.Path, nil
}

// readSkillList reads a file's `skills:` list, treating an absent section as empty.
//
// An empty list is a project that names no remote skills, which is a normal project rather
// than a broken one.
func readSkillList(file string) ([]string, error) {
	document, err := loadDocument(file)
	if err != nil {
		return nil, err
	}
	section, found := mapValue(document, KeySkills)
	if !found {
		return nil, nil
	}
	if section == nil || section.Kind != yaml.SequenceNode {
		return nil, api.Errorf(api.KindInvalid,
			"%s states %s as %s, and it is a list of qualified references. The references "+
				"are <catalog>.<name>, one per entry", file, KeySkills, describeNode(section))
	}
	entries := make([]string, 0, len(section.Content))
	for _, item := range section.Content {
		if item.Kind != yaml.ScalarNode {
			return nil, api.Errorf(api.KindInvalid,
				"%s has an entry in %s that is %s, and every entry is one qualified reference",
				file, KeySkills, describeNode(item))
		}
		entries = append(entries, item.Value)
	}
	return entries, nil
}

// writeSkillList rewrites a file's `skills:` list, leaving the rest of it alone.
//
// The document is read as a YAML node rather than as a value, because a node keeps the
// comments, the key order and the formatting that a value would flatten. A person's
// configuration file is the one file in a project they read and review, and a framework that
// rewrote it into its own formatting would make the diff say more than the change does.
func writeSkillList(file string, entries []string) error {
	document, err := loadDocument(file)
	if err != nil {
		return err
	}
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, entry := range entries {
		sequence.Content = append(sequence.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: entry,
		})
	}
	if value, found := mapValue(document, KeySkills); found {
		// The whole value node is replaced, so a section somebody wrote as a block becomes a
		// list rather than gaining a sequence alongside a mapping — which would be a file
		// this package could no longer read.
		*value = *sequence
	} else {
		document.Content = append(document.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: KeySkills},
			sequence,
		)
	}
	rendered, err := marshalDocument(document)
	if err != nil {
		return err
	}
	// Written through a temporary file and renamed, so an interrupted write cannot leave a
	// project with a configuration file this framework cannot read.
	temporary := file + ".toolbox-tmp"
	if err := os.WriteFile(temporary, rendered, 0o600); err != nil {
		return api.WrapError(api.KindInternal, err, "writing the project's configuration to %s", temporary)
	}
	if err := os.Rename(temporary, file); err != nil {
		_ = os.Remove(temporary)
		return api.WrapError(api.KindInternal, err, "replacing the project's configuration at %s", file)
	}
	return nil
}

func loadDocument(file string) (*yaml.Node, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, api.WrapError(api.KindInternal, err, "reading the project's configuration at %s", file)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return nil, api.WrapError(api.KindInvalid, err, "the project's configuration at %s", file)
	}
	// A document parsed into a node is a document node wrapping the mapping, and it is
	// unwrapped here so everything below works on the mapping a person's file actually
	// states settings in.
	if document.Kind == yaml.DocumentNode {
		if len(document.Content) == 0 {
			// An empty file is an empty document, which is a project that has configured
			// nothing. It is not refused: a file this framework cannot parse is refused, and
			// an empty one is parseable.
			return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
		}
		document = *document.Content[0]
	}
	if document.Kind != yaml.MappingNode {
		return nil, api.Errorf(api.KindInvalid,
			"%s is %s, so it does not state any settings as a mapping of keys",
			file, describeNode(&document))
	}
	return &document, nil
}

func marshalDocument(document *yaml.Node) ([]byte, error) {
	rendered, err := yaml.Marshal(document)
	if err != nil {
		return nil, api.WrapError(api.KindInternal, err, "rendering the project's configuration")
	}
	return rendered, nil
}

func mapValue(document *yaml.Node, key string) (*yaml.Node, bool) {
	index, found := mapIndex(document, key)
	if !found {
		return nil, false
	}
	return document.Content[index+1], true
}

func mapIndex(document *yaml.Node, key string) (int, bool) {
	if document.Kind != yaml.MappingNode {
		return 0, false
	}
	for index := 0; index+1 < len(document.Content); index += 2 {
		if document.Content[index].Value == key {
			return index, true
		}
	}
	return 0, false
}

func describeNode(node *yaml.Node) string {
	if node == nil {
		return "empty"
	}
	switch node.Kind {
	case yaml.MappingNode:
		return "a block"
	case yaml.SequenceNode:
		return "a nested list"
	case yaml.ScalarNode:
		if node.Tag == "!!int" || node.Tag == "!!float" || node.Tag == "!!bool" {
			return "the value " + node.Value
		}
		return "the text " + node.Value
	default:
		return "not text"
	}
}
