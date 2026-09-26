// Package skills reads agent skills in the standard format, and serves each of them in
// the form a particular client can act on.
//
// A skill is a directory of files with a SKILL.md at its root: YAML frontmatter
// describing the skill, and instructions in the body. The format is defined by the Agent
// Skills standard; four clients extend it with fields of their own, and the MCP Skills
// extension defines how a server publishes skills to a host. This package implements the
// reading and the publishing. It implements no transport and holds no state, because a
// skill is content rather than a resource with a lifecycle, and every source of skills
// needs the same reader.
//
// # One typed form, two input dialects
//
// A skill in a Toolbox catalog is a *template*: it is written in the standard format, and
// it may also carry Toolbox's own properties under the reverse-domain prefix
// com.github.manu343726.toolbox/, following the convention the MCP Skills extension uses
// for the keys it reserves.
//
// So a template may state a feature two ways: in Toolbox's typed block, or in the
// client's own spelling that Claude Code, Codex or Copilot reads. Both are parsed into
// the one typed form in this package, and **a template stating one feature twice with
// different values is refused** — two sources for one fact is the disagreement this
// framework refuses everywhere else, and a skill whose `disable-model-invocation` and
// `invocation.model_invocation` disagree has no correct answer to give a client.
//
// # Aggregation over filtering
//
// What is served is a distillation of the template, produced for the client that is
// asking. Distillation adapts content and hides nothing: every skill is visible to every
// client, the manifest is always complete, the author's `description` is never rewritten,
// and the only content change is a passage appended to the body for a requirement the
// client cannot meet. A client never fails to see a skill because of what it can do; it
// receives the skill in the form it can act on.
//
// # The one field that is never carried
//
// `allowed-tools` is the sole exception, and it is the exception by decision rather than
// by omission. The standard defines that field as a *request for elevated access on the
// host*, not a description of the author's environment: one client of the four grants it
// and three ignore it, so carrying it would give one field two meanings depending on who
// read it. A skill naming it is read, kept on the template, and never served. Its
// neighbours are carried, and for opposite reasons: `disallowed-tools` narrows the tool
// pool, so acting on it can only leave a model less capable, and `dependencies` states a
// need rather than asking for anything.
//
// A direction rule covers the whole surface: a feature that asks for more authority is
// never acted on, a feature that asks for less is carried, and a feature that states a
// need is checked rather than granted.
package skills

import (
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
)

// ToolboxPrefix is the reverse-domain namespace Toolbox's own frontmatter properties live
// under.
//
// A namespaced key is the shape least likely to upset a reader that is strict about its
// own frontmatter. The Agent Skills standard is deliberately silent on unknown keys — it
// defines what each field means and never declares another key invalid — and the MCP
// Skills extension reserves only `io.modelcontextprotocol/`-prefixed keys inside
// `metadata`, so a property about how *this deployment* treats a skill is permitted
// rather than a violation.
const ToolboxPrefix = "com.github.manu343726.toolbox/"

// Context is what a client does with the conversation a skill is invoked in. It is a
// closed set because each value is a different instruction to the host, not a free string.
type Context string

const (
	// ContextInline runs the skill in the current conversation.
	ContextInline Context = "inline"
	// ContextFork runs the skill in a subagent, so its intermediate work does not consume
	// the caller's context.
	ContextFork Context = "fork"
)

// Contexts is every accepted value, in the order they are reported.
var Contexts = []Context{ContextInline, ContextFork}

// ParseContext reads a context mode, refusing anything else by name rather than
// defaulting: a value this package did not expect is a value it would silently mistranslate.
func ParseContext(value string) (Context, error) {
	trimmed := Context(strings.ToLower(strings.TrimSpace(value)))
	for _, mode := range Contexts {
		if trimmed == mode {
			return mode, nil
		}
	}
	names := make([]string, 0, len(Contexts))
	for _, mode := range Contexts {
		names = append(names, string(mode))
	}
	return "", api.Errorf(api.KindInvalid,
		"a skill's context must be one of: %s, not %q", strings.Join(names, ", "), value)
}

// Effort is how much work a skill asks a client to spend on it.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// Efforts is every accepted value, in increasing order.
var Efforts = []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

// ParseEffort reads an effort level, refusing anything else by name.
//
// The closed set is the specification's own, and a value outside it is refused rather than
// passed through: a client that does not recognise an effort level ignores the whole
// instruction, so an unknown one is not a lesser instruction, it is none.
func ParseEffort(value string) (Effort, error) {
	trimmed := Effort(strings.ToLower(strings.TrimSpace(value)))
	for _, level := range Efforts {
		if trimmed == level {
			return level, nil
		}
	}
	names := make([]string, 0, len(Efforts))
	for _, level := range Efforts {
		names = append(names, string(level))
	}
	return "", api.Errorf(api.KindInvalid,
		"a skill's effort must be one of: %s, not %q", strings.Join(names, ", "), value)
}

// ToolRequirement is something a skill states it needs.
//
// It is a statement of need rather than a request: satisfying it grants the model
// nothing, and a deployment that does not have it says so rather than pretending.
type ToolRequirement struct {
	// Type is the requirement's kind as the author wrote it, such as "mcp" or "cli".
	Type string
	// Name is the tool being required.
	Name string
	// Description explains what the skill needs it for.
	Description string
	// Transport is how the skill reaches it, when that is not implied by the type.
	Transport string
	// URL locates it, when it is not part of this deployment.
	URL string
}

// Skill is a skill as this framework understands it: the typed union of every feature any
// supported client has.
//
// The union is the point. A standard skill is written for the union of readers anyway, and
// a person writing one should not have to choose between Claude Code's `when_to_use` and
// Copilot's `argument-hint`. Distillation is what makes "write once, and it works in the
// client you are in" true rather than aspirational: a feature no target client has is not
// a feature of the format.
//
// Optional fields are pointers, because a field that is absent and a field that is false
// are different facts. `disable-model-invocation: false` says the model *may* invoke the
// skill; leaving it out says nothing, and a client treats the two differently.
type Skill struct {
	// Name is the skill's identifier: 1–64 characters, lowercase letters, digits and
	// single hyphens, matching the skill directory's name. Required.
	Name string
	// Description is what the model selects on: 1–1024 characters saying when to use the
	// skill. Required, and never rewritten by this framework.
	Description string

	// License is a licence identifier.
	License string
	// Compatibility states which clients a skill works with.
	Compatibility string
	// Metadata carries free-form string values. The standard's own reserved keys live
	// inside it under the extension's namespace.
	Metadata map[string]string

	// UserInvocable is whether a person may invoke this skill by name, as a slash command.
	UserInvocable *bool
	// ModelInvocation is whether the model may load this skill by itself.
	//
	// One field, and the two clients that read this fact spell it differently:
	// Claude Code and Copilot read `disable-model-invocation`, which is its negation, and
	// Codex reads `policy.allow_implicit_invocation`, which states it positively. Two
	// spellings, one opinion, and no way for this framework to hold two.
	ModelInvocation *bool
	// WhenToUse tells a client when to reach for the skill, in prose.
	WhenToUse string
	// ArgumentHint is a short hint shown for the skill's arguments.
	ArgumentHint string
	// Arguments names the arguments the skill accepts.
	Arguments []string
	// DefaultPrompt is what a client offers to send when the skill is invoked bare.
	DefaultPrompt string
	// Context is whether the skill runs inline or in a subagent.
	Context Context

	// Model is the model a client should run the skill with.
	Model string
	// Effort is how much work a client should spend on it.
	Effort Effort

	// AllowedTools is a request for pre-approved access to tools.
	//
	// It is read, kept on the template, and never served and never acted on. It is the one
	// field in the union that is not carried, and AllowedToolsIsServed is the one place
	// that decision is written down.
	AllowedTools []string
	// DisallowedTools narrows the tool pool while the skill is active. Carried, and
	// honoured: acting on it can only leave a model less capable than it would otherwise
	// be, so there is no direction in which carrying it is unsafe.
	DisallowedTools []string

	// Tools are the tools the skill states it needs. Carried, and checked against what the
	// deployment actually exposes.
	Tools []ToolRequirement

	// DisplayName is the name a client shows.
	DisplayName string
	// ShortDescription is a one-line description for a listing.
	ShortDescription string
	// IconSmall is a small icon, as a path or a URI.
	IconSmall string
	// IconLarge is a large icon, as a path or a URI.
	IconLarge string
	// BrandColor is a colour to present the skill with.
	BrandColor string

	// Enabled is whether the skill is part of the project at all.
	//
	// A local skill is included implicitly, so there is no entry in the project's
	// configuration to remove; a skill a person put in their own project is switched off
	// in the same place they put it, and through this field. It is a different fact from a
	// client's `disable-model-invocation`, which asks whether the *model* may load a skill
	// that is still part of the project.
	Enabled *bool
}

// IsEnabled reports whether the skill is part of the project.
//
// A skill that says nothing is enabled: a template states the exceptions, and a skill that
// is switched off says so.
func (s Skill) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// ModelMayInvoke reports whether the model may load the skill by itself.
//
// A skill that says nothing may be loaded, because that is what a client does with a skill
// carrying no instruction about it.
func (s Skill) ModelMayInvoke() bool { return s.ModelInvocation == nil || *s.ModelInvocation }

// UserMayInvoke reports whether a person may invoke the skill by name.
//
// A skill that says nothing may be, for the same reason.
func (s Skill) UserMayInvoke() bool { return s.UserInvocable == nil || *s.UserInvocable }

// AllowedToolsAreServed is whether `allowed-tools` is ever carried into a served skill.
//
// It is false, and this is the one place the decision is written down, so that the reason
// lives next to the constant rather than in a document somebody has to find:
//
// The standard defines the field as a request for elevated access on the *host*. One
// client of the four grants such a request and three ignore it, so the same bytes would
// mean "pre-approve these tools" to one reader and nothing to another. Carrying it would
// give a single field two meanings, which is worse than not carrying it: the framework
// would have no single opinion about what a skill asked for.
const AllowedToolsAreServed = false

// clone returns a deep copy, so a projection cannot be changed by writing to the template
// it was projected from.
func (s Skill) clone() Skill {
	clone := s
	clone.Metadata = copyStrings(s.Metadata)
	clone.Arguments = append([]string(nil), s.Arguments...)
	clone.AllowedTools = append([]string(nil), s.AllowedTools...)
	clone.DisallowedTools = append([]string(nil), s.DisallowedTools...)
	clone.Tools = append([]ToolRequirement(nil), s.Tools...)
	clone.UserInvocable = copyBool(s.UserInvocable)
	clone.ModelInvocation = copyBool(s.ModelInvocation)
	clone.Enabled = copyBool(s.Enabled)
	return clone
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func copyStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func copyList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
