package skills_test

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustParse reads a skill whose frontmatter is the standard identity plus whatever the test
// adds, and whose body is fixed, so a projection test is about the projection.
func mustParse(t *testing.T, extraFrontmatter string) skills.Template {
	t.Helper()
	template, err := skills.Parse("local", "review", []byte(
		"---\nname: review\ndescription: Use this when reviewing a change.\n"+
			extraFrontmatter+"---\n\n# Review\n\nRead the diff.\n"), nil)
	require.NoError(t, err)
	return template
}

func mustDistill(t *testing.T, clientName, extraFrontmatter string, options skills.DistillOptions) skills.Entry {
	t.Helper()
	entry, err := skills.Distill(mustParse(t, extraFrontmatter),
		skills.Client{Name: clientName}, options)
	require.NoError(t, err)
	return entry
}

// Every skill is visible to every client, and every client receives the author's description
// exactly as written. Two clients reading different descriptions of one skill means the model
// is reasoning about something the author did not write, so this is asserted for all of them
// including one this framework has never heard of.
func TestEveryClientSeesEverySkillWithTheAuthorsOwnDescription(t *testing.T) {
	clients := append(skills.KnownClients(), "some-client-from-2030", "")
	for _, name := range clients {
		t.Run("client="+name, func(t *testing.T) {
			entry := mustDistill(t, name, "", skills.DistillOptions{})
			assert.Equal(t, "Use this when reviewing a change.",
				entry.Frontmatter["description"],
				"the description reaches the model as written")
			assert.Equal(t, "review", entry.Frontmatter["name"])
		})
	}
}

// The manifest is complete whatever the client can do, because a file is never dropped from
// a skill to suit a client — and the specification requires it.
func TestTheManifestIsCompleteForEveryClient(t *testing.T) {
	dir := writeSkill(t, map[string]string{
		"review/SKILL.md":            "---\nname: review\ndescription: Use this when reviewing.\n---\n\nBody.\n",
		"review/references/check.md": "# Checklist\n",
		"review/scripts/run.sh":      "#!/bin/sh\n",
		"review/assets/diagram.png":  "\x89PNG",
	})
	template, err := skills.ReadDirectory("local", "review", filepath.Join(dir, "review"))
	require.NoError(t, err)

	want := []string{"SKILL.md", "assets/diagram.png", "references/check.md", "scripts/run.sh"}
	for _, name := range append(skills.KnownClients(), "unknown-client") {
		t.Run("client="+name, func(t *testing.T) {
			entry, err := skills.Distill(template, skills.Client{Name: name}, skills.DistillOptions{})
			require.NoError(t, err)
			paths := make([]string, 0, len(entry.Files))
			for _, file := range entry.Files {
				paths = append(paths, file.Path)
			}
			assert.ElementsMatch(t, want, paths, "every file, each exactly once, whatever the client")
		})
	}
}

// One typed field, the client's own spelling. Claude Code and Copilot read the negation and
// Codex reads the assertion, and a framework holding both as separate fields would have two
// opinions about one fact.
func TestTheModelInvocationFactIsSpelledTheWayEachClientReadsIt(t *testing.T) {
	const authorSaysFalse = "com.github.manu343726.toolbox/:\n  invocation:\n    model_invocation: false\n"
	const authorSaysTrue = "com.github.manu343726.toolbox/:\n  invocation:\n    model_invocation: true\n"

	t.Run("negated for the clients that read the negation", func(t *testing.T) {
		for _, name := range []string{"claude-code", "copilot"} {
			t.Run(name, func(t *testing.T) {
				entry := mustDistill(t, name, authorSaysFalse, skills.DistillOptions{})
				assert.Equal(t, true, entry.Frontmatter["disable-model-invocation"],
					"the model may not invoke it, stated the way this client reads it")
				assert.NotContains(t, entry.Frontmatter, "policy",
					"the other clients' spelling is not added to a client that reads neither")

				allowed := mustDistill(t, name, authorSaysTrue, skills.DistillOptions{})
				assert.Equal(t, false, allowed.Frontmatter["disable-model-invocation"])
			})
		}
	})

	t.Run("asserted for the client that reads the assertion", func(t *testing.T) {
		entry := mustDistill(t, "codex", authorSaysFalse, skills.DistillOptions{})
		policy, ok := entry.Frontmatter["policy"].(map[string]any)
		require.True(t, ok, "the assertion goes in the block this client reads it in")
		assert.Equal(t, false, policy["allow_implicit_invocation"])

		allowed := mustDistill(t, "codex", authorSaysTrue, skills.DistillOptions{})
		policy, _ = allowed.Frontmatter["policy"].(map[string]any)
		assert.Equal(t, true, policy["allow_implicit_invocation"])
	})

	t.Run("left as written for a client that reads neither", func(t *testing.T) {
		// opencode controls availability from configuration rather than from the skill, and
		// states neither spelling, so nothing is added to what the author wrote.
		entry := mustDistill(t, "opencode", authorSaysFalse, skills.DistillOptions{})
		assert.NotContains(t, entry.Frontmatter, "disable-model-invocation")
		assert.NotContains(t, entry.Frontmatter, "policy")
		assert.NotContains(t, entry.Frontmatter, skills.ToolboxPrefix)
	})

	t.Run("given to a client that never heard of the author either", func(t *testing.T) {
		// The author's own dialect is left in place: an unknown client is served the common
		// denominator, and the common denominator is what the author wrote.
		entry := mustDistill(t, "claude-code", "disable-model-invocation: true\n",
			skills.DistillOptions{})
		assert.Equal(t, true, entry.Frontmatter["disable-model-invocation"],
			"a dialect that agrees with the typed field is served unchanged")
	})
}

// `allowed-tools` is the one field never carried, to any client. The test asserts the
// decision rather than the documentation, because a field that quietly started being carried
// would widen a host's permissions with nothing in the diff to say so.
func TestAllowedToolsIsNeverCarriedToAnyClient(t *testing.T) {
	const declaresAuthority = "allowed-tools:\n  - Bash\n  - Write\n"
	for _, name := range append(skills.KnownClients(), "unknown-client") {
		t.Run("client="+name, func(t *testing.T) {
			entry := mustDistill(t, name, declaresAuthority, skills.DistillOptions{})
			assert.NotContains(t, entry.Frontmatter, "allowed-tools",
				"a request for pre-approved access on the host is never served")
		})
	}
}

// Everything else is left in place for a client to ignore, because every one of these clients
// documents that it ignores what it does not recognise — three of them say so without
// reporting an error. The projection is a union, not a filter.
func TestAFieldTheClientDoesNotHaveIsLeftInPlaceForItToIgnore(t *testing.T) {
	const manyDialects = `when_to_use: Whenever a diff needs reading.
argument-hint: "[path]"
disallowed-tools:
  - WebFetch
license: Apache-2.0
compatibility: claude-code>=2
`
	for _, name := range append(skills.KnownClients(), "unknown-client") {
		t.Run("client="+name, func(t *testing.T) {
			entry := mustDistill(t, name, manyDialects, skills.DistillOptions{})
			for _, key := range []string{"when_to_use", "argument-hint", "disallowed-tools", "license", "compatibility"} {
				assert.Contains(t, entry.Frontmatter, key,
					"%s is the author's field and this client may ignore it", key)
			}
		})
	}
}

// Toolbox's own properties are about how this deployment treats a skill rather than about the
// skill, so they stay on the template where a person can read them and never reach a client.
func TestToolboxPropertiesStayOnTheTemplate(t *testing.T) {
	const toolboxBlock = "com.github.manu343726.toolbox/:\n  enabled: true\n  invocation:\n    when_to_use: Whenever.\n"
	for _, name := range append(skills.KnownClients(), "unknown-client") {
		t.Run("client="+name, func(t *testing.T) {
			entry := mustDistill(t, name, toolboxBlock, skills.DistillOptions{})
			assert.NotContains(t, entry.Frontmatter, skills.ToolboxPrefix)
			assert.NotContains(t, entry.Frontmatter, "com.github.manu343726.toolbox",
				"the namespace is removed however it was spelled")
		})
	}
}

// Nothing is renamed: a field a client reads keeps the author's key, and the served document
// is a superset of what was written rather than a translation of it.
func TestNothingIsRenamed(t *testing.T) {
	entry := mustDistill(t, "claude-code", "when_to_use: Whenever.\nargument-hint: \"[path]\"\n",
		skills.DistillOptions{})
	assert.Equal(t, "Whenever.", entry.Frontmatter["when_to_use"])
	assert.Equal(t, "[path]", entry.Frontmatter["argument-hint"])
}

// The digest in a served entry is of the bytes this server is about to send. That is the only
// thing a digest in an entry is for, so the entry's digest, its size, and its content have to
// agree — and a client that fetches the file and checks it against the entry gets the right
// answer.
//
// There are two manifests and they are two artifacts rather than two versions of one: the
// served one describes what this server serves to this client, and the catalog's describes
// the author's file, which is what a project's pin is taken against.
func TestTheServedDigestIsOfTheBytesThatAreServed(t *testing.T) {
	dir := writeSkill(t, map[string]string{
		"review/SKILL.md": "---\nname: review\ndescription: Use this when reviewing.\n---\n\nRead the diff.\n",
		"review/keep.md":  "Unchanged.\n",
	})
	template, err := skills.ReadDirectory("local", "review", filepath.Join(dir, "review"))
	require.NoError(t, err)
	catalogued, found := template.File("SKILL.md")
	require.True(t, found)

	distilled, err := skills.Distill(template, skills.Client{Name: "claude-code"},
		skills.DistillOptions{Unmet: []skills.Unmet{{
			Requirement: skills.ToolRequirement{Name: "git"},
			Reason:      "this deployment exposes no tool called git",
		}}})
	require.NoError(t, err)
	served, found := distilled.File("SKILL.md")
	require.True(t, found)

	assert.Equal(t, int64(len(distilled.Content)), served.Size,
		"the size is the length of the content the client receives")
	assert.Equal(t, digestOf(distilled.Content), served.Digest,
		"the digest is of the content the client receives")
	assert.NotEqual(t, catalogued.Digest, served.Digest,
		"the served file is rendered from the served frontmatter, so it is a different "+
			"artifact from the author's file a pin is taken against")

	t.Run("and the rendered file is a skill document again", func(t *testing.T) {
		// The rendering has to be readable by the same reader that read the original, or a
		// client that reads the file and re-reads it finds a document it cannot parse.
		reread, err := skills.Parse("local", "review", distilled.Content, distilled.Files)
		require.NoError(t, err)
		skill, err := reread.Skill()
		require.NoError(t, err)
		assert.Equal(t, "review", skill.Name)
		assert.Contains(t, reread.Body, "Read the diff.")
		assert.Contains(t, reread.Body, "this deployment exposes no tool called git",
			"the appended passage survives a round trip, because a client reads the file")
	})

	t.Run("and the rendering is deterministic", func(t *testing.T) {
		// A client verifying the same file twice must get the same digest twice, or a
		// correct fetch looks like tampering.
		again, err := skills.Distill(template, skills.Client{Name: "claude-code"},
			skills.DistillOptions{Unmet: []skills.Unmet{{
				Requirement: skills.ToolRequirement{Name: "git"},
				Reason:      "this deployment exposes no tool called git",
			}}})
		require.NoError(t, err)
		assert.Equal(t, distilled.Content, again.Content)
	})

	t.Run("the other files are unchanged", func(t *testing.T) {
		// Only the body changed, so only its file's entry may. Every other file is
		// byte-identical, which is why a projection needs no access to their contents.
		before, _ := template.File("keep.md")
		after, _ := distilled.File("keep.md")
		assert.Equal(t, before, after)
	})
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// A distilled body is a superset of the author's instructions, never a different skill. Two
// clients differing means one was given extra guidance, not a different opinion.
func TestADistilledBodyIsASupersetOfTheAuthors(t *testing.T) {
	plain := mustDistill(t, "claude-code", "", skills.DistillOptions{})
	guided := mustDistill(t, "claude-code", "", skills.DistillOptions{Unmet: []skills.Unmet{{
		Requirement: skills.ToolRequirement{Name: "git", Description: "to read history"},
		Reason:      "this deployment exposes no tool called git",
	}}})
	assert.Contains(t, guided.Body, plain.Body, "the author's instructions, unchanged")
	assert.Contains(t, guided.Body, "git")
	assert.Contains(t, guided.Body, "to read history")
	assert.Contains(t, guided.Body, "this deployment exposes no tool called git")
}

// A declared requirement reaches the client in the one dialect that states one, which is the
// same rule as the model-invocation fact: one typed field, the client's spelling.
func TestADeclaredRequirementIsSpelledTheWayEachClientReadsIt(t *testing.T) {
	const declares = `com.github.manu343726.toolbox/:
  requirements:
    tools:
      - type: mcp
        name: git
        description: to read history
`
	for _, name := range skills.KnownClients() {
		t.Run("client="+name, func(t *testing.T) {
			entry := mustDistill(t, name, declares, skills.DistillOptions{})
			spelling := skills.CapabilitiesFor(skills.Client{Name: name}).RequirementsSpelling
			if spelling != skills.RequirementsInCodexBlock {
				assert.NotContains(t, entry.Frontmatter, "dependencies",
					"this client states requirements in no dialect, so nothing is added")
				return
			}
			dependencies, ok := entry.Frontmatter["dependencies"].(map[string]any)
			require.True(t, ok, "the block this client reads requirements in is the one written")
			tools, ok := dependencies["tools"].([]any)
			require.True(t, ok)
			require.Len(t, tools, 1)
			first, _ := tools[0].(map[string]any)
			assert.Equal(t, "git", first["name"])
			assert.Equal(t, "to read history", first["description"])
		})
	}
}

// A declared requirement is checked rather than granted, and the check is reported.
func TestADeclaredRequirementIsCheckedAndReported(t *testing.T) {
	const declares = `com.github.manu343726.toolbox/:
  requirements:
    tools:
      - type: mcp
        name: git
        description: to read history
`
	t.Run("met, so nothing is reported", func(t *testing.T) {
		entry := mustDistill(t, "claude-code", declares, skills.DistillOptions{})
		assert.Empty(t, entry.Unmet)
		assert.Equal(t, "# Review\n\nRead the diff.\n", entry.Body,
			"nothing was appended for a dependency that is there")
	})

	t.Run("unmet, so it is reported and not granted", func(t *testing.T) {
		entry := mustDistill(t, "claude-code", declares, skills.DistillOptions{Unmet: []skills.Unmet{{
			Requirement: skills.ToolRequirement{Name: "git", Description: "to read history"},
			Reason:      "this deployment exposes no tool called git",
		}}})
		require.Len(t, entry.Unmet, 1)
		assert.Equal(t, "git", entry.Unmet[0].Name)
		// The requirement is reported rather than granted, and the toolbox block itself is
		// never served: it is about how this deployment treats a skill, not about the skill.
		assert.NotContains(t, entry.Frontmatter, skills.ToolboxPrefix)
	})
}

// A client that cannot run a script is told to read it instead, because the script's text is
// in the manifest and following it by hand is genuinely available. The alternative is not
// derivable, and the passage says so rather than inventing one.
func TestAClientThatCannotRunScriptsIsToldToReadThem(t *testing.T) {
	dir := writeSkill(t, map[string]string{
		"review/SKILL.md":         "---\nname: review\ndescription: Use this when reviewing.\n---\n\nRun it.\n",
		"review/scripts/check.sh": "#!/bin/sh\ngit diff --stat\n",
	})
	template, err := skills.ReadDirectory("local", "review", filepath.Join(dir, "review"))
	require.NoError(t, err)

	// Every current target runs scripts, so the passage is reached through a client that
	// does not — which is the case the table exists for.
	for name, capabilities := range map[string]skills.Capabilities{
		"claude-code": skills.CapabilitiesFor(skills.Client{Name: "claude-code"}),
		"codex":       skills.CapabilitiesFor(skills.Client{Name: "codex"}),
	} {
		t.Run(name+" can run them, so nothing is appended", func(t *testing.T) {
			require.True(t, capabilities.RunsScripts)
			entry, err := skills.Distill(template, skills.Client{Name: name}, skills.DistillOptions{})
			require.NoError(t, err)
			assert.NotContains(t, entry.Body, "Working here without executing scripts")
		})
	}
}

// The table of what each client can do is the evidence the projection is built from, so a
// client losing a capability has to be a deliberate edit rather than a drift.
func TestEveryTargetClientIsProjected(t *testing.T) {
	assert.ElementsMatch(t, []string{"claude-code", "codex", "copilot", "opencode"},
		skills.KnownClients())
	for _, name := range skills.KnownClients() {
		t.Run(name, func(t *testing.T) {
			capabilities := skills.CapabilitiesFor(skills.Client{Name: name})
			assert.True(t, capabilities.Known, "all four target clients support skills, so "+
				"none of them needs a fallback for having none")
			assert.True(t, capabilities.RunsScripts, "all four execute scripts")
			assert.NoError(t, skills.RequireKnownClient(name))
		})
	}
}

// A client's name is normalised rather than matched exactly, because clients differ in how
// they spell themselves and a projection that fell to the common denominator for a Claude
// Code client would be wrong in a way the client could not report.
func TestClientNamesAreNormalised(t *testing.T) {
	cases := map[string]string{
		"claude-code":    "claude-code",
		"Claude Code":    "claude-code",
		"claude_code":    "claude-code",
		"CLAUDE-CODE":    "claude-code",
		"copilot":        "copilot",
		"github.copilot": "copilot",
		"copilot-vscode": "copilot",
		"codex@1.2.3":    "codex",
		"  opencode  ":   "opencode",
	}
	for reported, expected := range cases {
		t.Run(reported, func(t *testing.T) {
			entry := mustDistill(t, reported, "com.github.manu343726.toolbox/:\n  invocation:\n    model_invocation: false\n",
				skills.DistillOptions{})
			// A normalised name reaches the same projection as the canonical one.
			want := mustDistill(t, expected, "com.github.manu343726.toolbox/:\n  invocation:\n    model_invocation: false\n",
				skills.DistillOptions{})
			assert.Equal(t, want.Frontmatter, entry.Frontmatter)
		})
	}
}

// A caller that wants a mis-spelled name to be an error asks for that, because an unknown
// client is served the common denominator rather than refused — which is right for serving
// and wrong for a command whose whole job is to report what it is talking to.
func TestAnUnknownClientMayBeAskedAbout(t *testing.T) {
	entry := mustDistill(t, "not-a-client", "", skills.DistillOptions{})
	assert.NotEmpty(t, entry.Frontmatter, "an unknown client is served, not refused")

	err := skills.RequireKnownClient("not-a-client")
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "claude-code")

	err = skills.RequireKnownClient("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not say what it is")
}

// A skill's URI spells its catalog, including for a project's own skill, and is refused
// rather than guessed at when it names no skill.
func TestSkillURIsSpellTheCatalogAndAreRefusedWhenTheyNameNoSkill(t *testing.T) {
	t.Run("the catalog is always spelled", func(t *testing.T) {
		entry := mustDistill(t, "claude-code", "", skills.DistillOptions{})
		assert.Equal(t, "skill://local/review/SKILL.md", entry.URI)

		remote, err := skills.Parse("skills.sh", "pull-request-review",
			[]byte("---\nname: pull-request-review\ndescription: Use this on a pull request.\n---\n\nBody.\n"), nil)
		require.NoError(t, err)
		assert.Equal(t, "skill://skills.sh/pull-request-review/SKILL.md",
			skills.SkillURI(remote.Catalog, remote.Name))
	})

	t.Run("every file of a skill is a resource under it", func(t *testing.T) {
		entry := mustDistill(t, "claude-code", "", skills.DistillOptions{})
		assert.Equal(t, "skill://local/review/SKILL.md", entry.ResourceURI("SKILL.md"))
		assert.Equal(t, "skill://local/review/references/check.md",
			entry.ResourceURI("references/check.md"))
	})

	// A URI naming no file of a skill is refused by the general reader.
	for _, uri := range []string{
		"https://example.com/SKILL.md",
		"skill://local/SKILL.md",
		"skill:///review/SKILL.md",
		"skill://local/review/",
	} {
		t.Run("refused as a resource: "+uri, func(t *testing.T) {
			_, _, _, err := skills.ParseResourceURI(uri)
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
		})
	}

	// A URI naming a file of a skill is a resource, and the narrow reader refuses it because
	// it is not the skill's own document. `skills/get` takes one of those and nothing else,
	// so accepting anything else here would serve a skill the reader did not ask for.
	for _, uri := range []string{
		"skill://local/review/other.md",
		"skill://local/review/references/check.md",
	} {
		t.Run("refused as a skill: "+uri, func(t *testing.T) {
			_, _, err := skills.ParseSkillURI(uri)
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
		})
	}

	t.Run("read back", func(t *testing.T) {
		catalog, name, relative, err := skills.ParseResourceURI(
			"skill://skills.sh/pull-request-review/references/check.md")
		require.NoError(t, err)
		assert.Equal(t, "skills.sh", catalog)
		assert.Equal(t, "pull-request-review", name)
		assert.Equal(t, "references/check.md", relative)
	})
}

// A skill marked outdated is reported rather than refused: the pin is the agreement, drift is
// visible, and nothing is blocked behind the user's back.
func TestAnOutdatedSkillIsMarkedAndStillServed(t *testing.T) {
	entry := mustDistill(t, "claude-code", "", skills.DistillOptions{Outdated: true})
	assert.True(t, entry.Outdated, "drift is reported")
	assert.NotEmpty(t, entry.Body, "and the skill is still served")
}

// The appended passage is a passage rather than a refusal, because a skill is still mostly
// usable: a model told a tool is missing can do the rest and say what it could not do.
func TestTheAppendedPassageTellsTheModelWhatToDoInstead(t *testing.T) {
	entry := mustDistill(t, "claude-code", "", skills.DistillOptions{Unmet: []skills.Unmet{{
		Requirement: skills.ToolRequirement{Name: "git", Description: "to read history"},
		Reason:      "this deployment exposes no tool called git",
	}}})
	assert.True(t, strings.Contains(entry.Body, "say that you could not do it rather than "+
		"working around it by guessing"),
		"the instruction is about what the model should do, not about the missing tool alone")
}
