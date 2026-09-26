package skills

import (
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
)

// Client is who a skill is being served to.
//
// It is what the client reported about itself on the request, and never something a
// deployment configured. A distillation that guessed wrong hands a client content it cannot
// use, and nothing in the result would reveal the guess: the skill arrives, it is simply
// wrong for the reader, and the reader has no way to say so.
type Client struct {
	// Name is what the client calls itself. The four this framework knows are named by
	// their own convention, lowercased.
	Name string
	// Version is what it reported, for a message that needs to say which build.
	Version string
}

// String renders the client for a message.
func (c Client) String() string {
	name := strings.TrimSpace(c.Name)
	switch {
	case name == "":
		return "a client that did not say what it is"
	case strings.TrimSpace(c.Version) == "":
		return name
	default:
		return name + " " + c.Version
	}
}

// ModelSpelling is how a client states whether the model may load a skill by itself.
//
// The two spellings are the same fact stated two ways, which is why the framework reads both
// into one field: `disable-model-invocation` is the negation, and `allow_implicit_invocation`
// is the assertion. A framework holding both spellings as separate fields would have two
// opinions about one fact, and a template using both correctly — the negated one false, the
// positive one true — would look like a contradiction.
type ModelSpelling int

const (
	// ModelSpellingNone means the client states this fact in no spelling, so nothing is
	// added to what the author wrote. This is the common denominator, given to a client
	// this framework has never heard of.
	ModelSpellingNone ModelSpelling = iota
	// ModelSpellingNegated is `disable-model-invocation`, as Claude Code and Copilot read it.
	ModelSpellingNegated
	// ModelSpellingAsserted is `policy.allow_implicit_invocation`, as Codex reads it.
	ModelSpellingAsserted
)

// RequirementsSpelling is where a client states the tools a skill needs.
type RequirementsSpelling int

const (
	// RequirementsNowhere means the client states this fact in no dialect, so nothing is
	// added to what the author wrote.
	RequirementsNowhere RequirementsSpelling = iota
	// RequirementsInCodexBlock is `dependencies.tools`, as Codex reads it.
	RequirementsInCodexBlock
)

// Capabilities is what a client can do with a skill.
//
// The table is small on purpose. Because distillation adapts content and hides nothing, a
// field the client does not recognise is left in place for it to ignore — which is what
// every one of these clients documents, and three of the four say they do it without
// reporting an error. So the only capabilities a projection has to reason about are the ones
// where the *spelling* differs and the one where the client cannot do something at all.
type Capabilities struct {
	// Known is whether this framework has a projection for this client.
	Known bool
	// ModelSpelling is how the client states whether the model may load a skill.
	ModelSpelling ModelSpelling
	// RequirementsSpelling is where the client states the tools a skill needs.
	RequirementsSpelling RequirementsSpelling
	// RunsScripts is whether the client can execute the scripts a skill ships.
	//
	// All four current targets can, so this is true for every one of them. It is in the
	// table because the passage appended when it is false is derivable — a script's content
	// is in the manifest, so a model can be told to follow it by hand — and because a table
	// with a column nobody has ever set false is a table nobody has checked.
	RunsScripts bool
}

// The clients this framework has a projection for, by the name each reports.
//
// The names are the clients' own, and the fact that all four support skills is what removes
// the need for a fallback: there is no client that needs a different shape, only clients
// that need different spellings.
var clientProfiles = map[string]Capabilities{
	// opencode controls a skill's availability from its own configuration rather than from
	// the skill, and states neither the model-invocation fact nor a requirements block.
	"opencode": {
		Known:                true,
		ModelSpelling:        ModelSpellingNone,
		RequirementsSpelling: RequirementsNowhere,
		RunsScripts:          true,
	},
	// Claude Code states the model-invocation fact as its negation, and has no
	// requirements block: a skill tells it which tools it needs with allowed-tools, which is
	// the one field this framework never carries.
	"claude-code": {
		Known:                true,
		ModelSpelling:        ModelSpellingNegated,
		RequirementsSpelling: RequirementsNowhere,
		RunsScripts:          true,
	},
	// Codex states both facts in blocks, and is the only one of the four that states a
	// skill's requirements at all.
	"codex": {
		Known:                true,
		ModelSpelling:        ModelSpellingAsserted,
		RequirementsSpelling: RequirementsInCodexBlock,
		RunsScripts:          true,
	},
	"copilot": {
		Known:                true,
		ModelSpelling:        ModelSpellingNegated,
		RequirementsSpelling: RequirementsNowhere,
		RunsScripts:          true,
	},
}

// CapabilitiesFor reports what a client can do with a skill.
//
// A client this framework has never heard of gets the common denominator, and the common
// denominator is stated rather than assumed: the author wrote it in a portable format, every
// target client ignores what it does not recognise, and a client that reads none of the
// spellings this framework knows is served exactly what its author wrote.
func CapabilitiesFor(client Client) Capabilities {
	profile, known := clientProfiles[normalizeClientName(client.Name)]
	if !known {
		return Capabilities{Known: false, ModelSpelling: ModelSpellingNone}
	}
	return profile
}

// KnownClients is every client this framework has a projection for, in a stable order, so a
// message can list them.
func KnownClients() []string {
	names := make([]string, 0, len(clientProfiles))
	for name := range clientProfiles {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

// normalizeClientName reduces a reported name to the form the table is keyed by.
//
// Clients differ in how they spell themselves — `claude-code`, `Claude Code`,
// `claude_code`, and whatever a proxy in front of one decides to call it — and a projection
// that fell to the common denominator for a Claude Code client would be wrong in a way no
// client could report. So the name is normalised rather than matched exactly, and a name
// that normalises to nothing known is still the common denominator.
func normalizeClientName(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.NewReplacer(" ", "-", "_", "-", "/", "-", ".", "-").Replace(normalized)
	// A version suffix and a vendor prefix are both decoration around the name a person
	// would recognise: "copilot@1.2.3" and "github.copilot" are the same client.
	for _, separator := range []string{"@", "-v", ":v"} {
		if cut, _, found := strings.Cut(normalized, separator); found && len(cut) > 2 {
			normalized = cut
			break
		}
	}
	normalized = strings.Trim(normalized, "-")
	// GitHub's Copilot is reached through its editor, and reports a name with the host in
	// front of it. The part that identifies the client is the last segment.
	if _, tail, found := strings.Cut(normalized, "copilot"); found && tail == "" {
		normalized = "copilot"
	}
	return normalized
}

// RequireKnownClient refuses a client name this framework will not guess about.
//
// It is for a caller that wants a mis-spelled name to be an error rather than a silent
// common-denominator projection — which is the difference between a client's own field
// being served in the wrong spelling and being served in the only spelling every client
// understands. Asking is the caller's decision, so asking is a function.
func RequireKnownClient(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return api.Errorf(api.KindInvalid,
			"the client did not say what it is, so this framework does not know which "+
				"spelling of a skill's frontmatter it reads. The clients it knows are: %s",
			strings.Join(KnownClients(), ", "))
	}
	if _, known := clientProfiles[normalizeClientName(trimmed)]; !known {
		return api.Errorf(api.KindInvalid,
			"%q is not a client this framework has a projection for. The clients it knows are: "+
				"%s. An unknown client is served the common denominator rather than refused, "+
				"so this is only an error where a caller asked to be told",
			trimmed, strings.Join(KnownClients(), ", "))
	}
	return nil
}
