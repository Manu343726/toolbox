package skills_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSkill lays out a skill directory from a map of relative path to content.
func writeSkill(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for relative, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

const codeReviewSkill = `---
name: code-review
description: Use this when reviewing a change for correctness, not style.
license: Apache-2.0
---

# Code review

Read the diff. Then say what is wrong with it.
`

// A skill in the standard format is read as a template with its frontmatter, its body and a
// complete manifest.
func TestAStandardSkillIsReadAsATemplate(t *testing.T) {
	dir := writeSkill(t, map[string]string{
		"code-review/SKILL.md":                codeReviewSkill,
		"code-review/references/checklist.md": "# Checklist\n\n- correctness\n- tests\n",
	})
	template, err := skills.ReadDirectory("local", "code-review", filepath.Join(dir, "code-review"))
	require.NoError(t, err)

	assert.Equal(t, "local", template.Catalog)
	assert.Equal(t, "code-review", template.Name)
	skill, err := template.Skill()
	require.NoError(t, err)
	assert.Equal(t, "code-review", skill.Name)
	assert.Equal(t, "Apache-2.0", skill.License)
	assert.Contains(t, template.Body, "Read the diff.")
	assert.Equal(t, "local.code-review", template.Reference())

	// The manifest is complete: every file, each exactly once, SKILL.md among them.
	paths := make([]string, 0, len(template.Files))
	for _, file := range template.Files {
		paths = append(paths, file.Path)
	}
	assert.ElementsMatch(t,
		[]string{skills.SkillFileName, "references/checklist.md"}, paths)

	require.NoError(t, template.Validate())
}

// A skill directory with no SKILL.md has no frontmatter and no instructions, and saying so
// is more useful than reporting a missing name.
func TestASkillWithNoSkillFileIsReportedAsMissingIt(t *testing.T) {
	dir := writeSkill(t, map[string]string{"notes/notes.md": "# Notes\n"})

	_, err := skills.ReadDirectory("local", "notes", filepath.Join(dir, "notes"))
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	assert.Contains(t, err.Error(), skills.SkillFileName)
}

// A catalog asked for a skill it does not hold hears that, rather than a message about a
// filesystem that happens to be how this one keeps its skills.
func TestAMissingSkillDirectoryIsNotFound(t *testing.T) {
	_, err := skills.ReadDirectory("local", "absent", filepath.Join(t.TempDir(), "absent"))
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
}

// A skill whose frontmatter is not a fence is a file that is not a skill document, and the
// message says so rather than reporting an absent name.
func TestASkillWithNoFrontmatterFenceIsRefused(t *testing.T) {
	dir := writeSkill(t, map[string]string{
		"plain/SKILL.md": "# Just markdown\n\nname: code-review\n",
	})

	_, err := skills.ReadDirectory("local", "plain", filepath.Join(dir, "plain"))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "'---'")
}

// Both required fields are required, each naming itself.
func TestAValidatesIdentity(t *testing.T) {
	cases := []struct {
		name        string
		frontmatter string
		directory   string
		mentions    string
	}{
		{
			name:        "no name",
			frontmatter: "---\ndescription: Something.\n---\n\nBody.\n",
			directory:   "unnamed",
			mentions:    "no name",
		},
		{
			name:        "no description",
			frontmatter: "---\nname: unnamed\n---\n\nBody.\n",
			directory:   "unnamed",
			mentions:    "no description",
		},
		{
			name:        "a name that is not a path segment",
			frontmatter: "---\nname: Code Review\ndescription: Something.\n---\n\nBody.\n",
			directory:   "unnamed",
			mentions:    "directory name",
		},
		{
			name:        "a name with two hyphens in a row",
			frontmatter: "---\nname: code--review\ndescription: Something.\n---\n\nBody.\n",
			directory:   "unnamed",
			mentions:    "single hyphens",
		},
		{
			name:        "a name longer than the limit",
			frontmatter: "---\nname: " + strings.Repeat("a", 65) + "\ndescription: x\n---\n\nBody.\n",
			directory:   "unnamed",
			mentions:    "64",
		},
		{
			name:        "a description longer than the limit",
			frontmatter: "---\nname: ok\ndescription: " + strings.Repeat("d", 1025) + "\n---\n\nBody.\n",
			directory:   "unnamed",
			mentions:    "1024",
		},
		{
			name:        "a name that disagrees with the directory",
			frontmatter: "---\nname: other\ndescription: Something.\n---\n\nBody.\n",
			directory:   "mismatched",
			mentions:    "must match",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := writeSkill(t, map[string]string{testCase.directory + "/SKILL.md": testCase.frontmatter})
			template, err := skills.ReadDirectory("local", testCase.directory,
				filepath.Join(dir, testCase.directory))
			if err == nil {
				err = template.Validate()
			}
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
			assert.Contains(t, err.Error(), testCase.mentions)
		})
	}
}

// The manifest is the pin, so its digests and sizes are recomputed and compared rather than
// copied from anywhere: a digest in a manifest that was not computed from the bytes would
// make the pin worth nothing.
func TestTheManifestDigestsTheFilesItDescribes(t *testing.T) {
	dir := writeSkill(t, map[string]string{
		"pinned/SKILL.md":       codeReviewSkill,
		"pinned/scripts/run.sh": "#!/bin/sh\necho hello\n",
	})
	template, err := skills.ReadDirectory("local", "pinned", filepath.Join(dir, "pinned"))
	require.NoError(t, err)

	for _, file := range template.Files {
		content, err := os.ReadFile(filepath.Join(dir, "pinned", filepath.FromSlash(file.Path)))
		require.NoError(t, err)
		assert.Equal(t, int64(len(content)), file.Size, file.Path)
		assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, file.Digest, file.Path)
	}

	// The same directory read twice is the same manifest, byte for byte: a pin is only a
	// pin if it does not depend on when it was taken.
	again, err := skills.ReadDirectory("local", "pinned", filepath.Join(dir, "pinned"))
	require.NoError(t, err)
	assert.Equal(t, template.Files, again.Files)
}

// Both limits are enforced, at the boundary and over it, because a skill over either is one
// no conforming host is obliged to load.
func TestTheManifestLimitsAreEnforced(t *testing.T) {
	t.Run("files at the limit are accepted", func(t *testing.T) {
		files := manifestOfCount(skills.MaxFiles)
		require.NoError(t, validateManifestFor(files))
	})
	t.Run("files over the limit are refused", func(t *testing.T) {
		files := manifestOfCount(skills.MaxFiles + 1)
		err := validateManifestFor(files)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "513")
		assert.Contains(t, err.Error(), "512")
	})
	t.Run("bytes at the limit are accepted", func(t *testing.T) {
		files := []skills.File{{Path: "SKILL.md", Size: skills.MaxBytes}}
		require.NoError(t, validateManifestFor(files))
	})
	t.Run("bytes over the limit are refused", func(t *testing.T) {
		files := []skills.File{{Path: "SKILL.md", Size: skills.MaxBytes + 1}}
		err := validateManifestFor(files)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "limit")
	})
}

func manifestOfCount(count int) []skills.File {
	files := make([]skills.File, 0, count)
	for index := range count {
		files = append(files, skills.File{Path: "file-" + itoa(index), Size: 1})
	}
	return files
}

// validateManifestFor goes through the public validation rather than the private one, so
// the test asserts what a caller would see.
func validateManifestFor(files []skills.File) error {
	template, err := skills.Parse("local", "pinned", []byte(frontmatterOnly), files)
	if err != nil {
		return err
	}
	return template.Validate()
}

const frontmatterOnly = "---\nname: pinned\ndescription: Something.\n---\n\nBody.\n"

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// A skill's features may be written in Toolbox's typed block or in a client's own spelling,
// and both reach the one typed form. This is the property that makes "write once and it
// works in the client you are in" true rather than aspirational.
func TestTheTwoDialectsReachOneTypedForm(t *testing.T) {
	cases := []struct {
		name        string
		frontmatter string
		assert      func(t *testing.T, skill skills.Skill)
	}{
		{
			name: "the Toolbox block declares a client-native field",
			frontmatter: `com.github.manu343726.toolbox/:
  invocation:
    when_to_use: Whenever a diff needs reading.
    model_invocation: false
  execution:
    effort: high
  presentation:
    display_name: Code Review
`,
			assert: func(t *testing.T, skill skills.Skill) {
				assert.Equal(t, "Whenever a diff needs reading.", skill.WhenToUse)
				require.NotNil(t, skill.ModelInvocation)
				assert.False(t, *skill.ModelInvocation)
				assert.Equal(t, skills.EffortHigh, skill.Effort)
				assert.Equal(t, "Code Review", skill.DisplayName)
			},
		},
		{
			name: "a client-native field declares the same fact in the other spelling",
			frontmatter: `disable-model-invocation: true
when_to_use: Whenever a diff needs reading.
`,
			assert: func(t *testing.T, skill skills.Skill) {
				require.NotNil(t, skill.ModelInvocation)
				assert.False(t, *skill.ModelInvocation, "negated in the dialect, one typed field")
			},
		},
		{
			name: "Codex asserts the same fact positively",
			frontmatter: `policy:
  allow_implicit_invocation: false
`,
			assert: func(t *testing.T, skill skills.Skill) {
				require.NotNil(t, skill.ModelInvocation)
				assert.False(t, *skill.ModelInvocation)
			},
		},
		{
			name: "the Toolbox block without a trailing slash is the same block",
			frontmatter: `com.github.manu343726.toolbox:
  enabled: true
`,
			assert: func(t *testing.T, skill skills.Skill) {
				require.NotNil(t, skill.Enabled)
				assert.True(t, *skill.Enabled)
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			template, err := skills.Parse("local", "dialect", []byte(
				"---\nname: dialect\ndescription: Something.\n"+testCase.frontmatter+"---\n\nBody.\n"), nil)
			require.NoError(t, err)
			skill, err := template.Skill()
			require.NoError(t, err)
			testCase.assert(t, skill)
		})
	}
}

// Two sources for one fact is the disagreement this framework refuses everywhere else, so a
// template stating one feature twice with two values is refused rather than resolved — and
// the message names both places.
func TestAFeatureStatedTwiceWithDifferentValuesIsRefused(t *testing.T) {
	cases := []struct {
		name        string
		frontmatter string
		mentions    []string
	}{
		{
			name: "the two dialects of model invocation",
			frontmatter: `disable-model-invocation: true
com.github.manu343726.toolbox/:
  invocation:
    model_invocation: true
`,
			mentions: []string{"model_invocation", "disable-model-invocation"},
		},
		{
			name: "a native field and the Toolbox block",
			frontmatter: `when_to_use: From the dialect.
com.github.manu343726.toolbox/:
  invocation:
    when_to_use: From the block.
`,
			// The one field stated twice as two different strings is a disagreement this
			// framework has no way to resolve either, so it is refused as well.
			mentions: []string{"when_to_use"},
		},
		{
			name: "the two clients' spellings disagreeing",
			frontmatter: `disable-model-invocation: true
policy:
  allow_implicit_invocation: true
`,
			mentions: []string{"model_invocation"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := skills.Parse("local", "dialect", []byte(
				"---\nname: dialect\ndescription: Something.\n"+testCase.frontmatter+"---\n\nBody.\n"), nil)
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
			for _, mention := range testCase.mentions {
				assert.Contains(t, err.Error(), mention)
			}
		})
	}
}

// A property under this framework's own namespace that it does not define is an error, not an
// ignored key: the author wrote it expecting it to be read.
func TestAnUnknownToolboxPropertyIsRefusedByName(t *testing.T) {
	_, err := skills.Parse("local", "typo", []byte(
		"---\nname: typo\ndescription: Something.\n"+
			"com.github.manu343726.toolbox/:\n  invocation:\n    when_to_us: Whenever.\n"+
			"---\n\nBody.\n"), nil)
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "when_to_us")
	// The message lists what is accepted, so a reader can fix it without leaving the message.
	assert.Contains(t, err.Error(), "invocation.when_to_use")
}

// Claude Code accepts yes, no, on, off, 1 and 0 as well as true and false, so a reader that
// took YAML's word for a boolean would refuse a skill that client is perfectly happy with.
func TestBooleansAreReadInEverySpellingItsTargetsAccept(t *testing.T) {
	accepted := map[string]bool{
		"true": true, "TRUE": true, "True": true,
		"yes": true, "YES": true, "Yes": true,
		"on": true, "ON": true, "On": true,
		"1":     true,
		"false": false, "FALSE": false, "False": false,
		"no": false, "NO": false, "No": false,
		"off": false, "OFF": false, "Off": false,
		"0": false,
	}
	for spelling, expected := range accepted {
		t.Run(spelling, func(t *testing.T) {
			template, err := skills.Parse("local", "bools", []byte(
				"---\nname: bools\ndescription: Something.\n"+
					"com.github.manu343726.toolbox/:\n  enabled: "+spelling+"\n---\n\nBody.\n"), nil)
			require.NoError(t, err)
			skill, err := template.Skill()
			require.NoError(t, err)
			require.NotNil(t, skill.Enabled, "%q is a boolean", spelling)
			assert.Equal(t, expected, *skill.Enabled)
		})
	}
}

// A value that is none of the accepted spellings is reported rather than coerced: a value
// coerced to false is a skill silently switched off.
func TestABooleanThatIsNotOneIsReportedNotCoerced(t *testing.T) {
	for _, spelling := range []string{"maybe", "2", "enabled", ""} {
		t.Run(spelling, func(t *testing.T) {
			_, err := skills.Parse("local", "bools", []byte(
				"---\nname: bools\ndescription: Something.\n"+
					"com.github.manu343726.toolbox/:\n  enabled: \""+spelling+"\"\n---\n\nBody.\n"), nil)
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
			assert.Contains(t, err.Error(), "not a boolean")
		})
	}
}

// The closed sets are refused by name. A value outside them is not a lesser instruction, it
// is none: a client that does not recognise an effort level ignores the whole instruction.
func TestTheClosedSetsAreRefusedByName(t *testing.T) {
	cases := []struct{ name, block, mentions string }{
		{"effort", "  execution:\n    effort: enormous\n", "effort"},
		{"context", "  invocation:\n    context: sideways\n", "context"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := skills.Parse("local", "sets", []byte(
				"---\nname: sets\ndescription: Something.\n"+
					"com.github.manu343726.toolbox/:\n"+testCase.block+"---\n\nBody.\n"), nil)
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
			assert.Contains(t, err.Error(), testCase.mentions)
		})
	}
}

// `allowed-tools` is read and kept, and is the one field never served. The decision is
// asserted here as well as documented, because a field that quietly started being carried
// would widen a host's permissions with nothing in the diff to say so.
func TestAllowedToolsIsReadAndKeptAndNeverActedOn(t *testing.T) {
	dir := writeSkill(t, map[string]string{
		"asking/SKILL.md": "---\nname: asking\ndescription: Something.\n" +
			"allowed-tools:\n  - Bash(git:*)\n  - Read\n---\n\nBody.\n",
	})
	template, err := skills.ReadDirectory("local", "asking", filepath.Join(dir, "asking"))
	require.NoError(t, err)
	skill, err := template.Skill()
	require.NoError(t, err)
	assert.Equal(t, []string{"Bash(git:*)", "Read"}, skill.AllowedTools,
		"the template keeps what the author asked for")

	assert.False(t, skills.AllowedToolsAreServed,
		"a request for elevated access on the host is never carried: one client of four "+
			"grants it and three ignore it, so carrying it gives one field two meanings")
}

// A skill that states nothing is enabled and model-invocable: a template states the
// exceptions, and a client treats an absent instruction as no instruction.
func TestAnUnstatedFactDefaultsTheWayAClientDefaultsIt(t *testing.T) {
	template, err := skills.Parse("local", "plain", []byte(
		"---\nname: plain\ndescription: Something.\n---\n\nBody.\n"), nil)
	require.NoError(t, err)
	skill, err := template.Skill()
	require.NoError(t, err)
	assert.True(t, skill.IsEnabled())
	assert.True(t, skill.ModelMayInvoke())
	assert.True(t, skill.UserMayInvoke())
}

// A local skill is switched off through its own frontmatter, in the same place the person
// put it, rather than in a second list in the configuration file.
func TestALocalSkillIsSwitchedOffInItsOwnFrontmatter(t *testing.T) {
	template, err := skills.Parse("local", "off", []byte(
		"---\nname: off\ndescription: Something.\n"+
			"com.github.manu343726.toolbox/:\n  enabled: false\n---\n\nBody.\n"), nil)
	require.NoError(t, err)
	skill, err := template.Skill()
	require.NoError(t, err)
	assert.False(t, skill.IsEnabled())
}

// The Codex blocks live in a file rather than in the frontmatter, and a skill states its
// requirements in one place or the other rather than in both.
func TestTheCodexFileIsFoldedInAndCannotDisagree(t *testing.T) {
	t.Run("folded in from the file", func(t *testing.T) {
		dir := writeSkill(t, map[string]string{
			"codex/SKILL.md": "---\nname: codex\ndescription: Something.\n---\n\nBody.\n",
			"codex/agents/openai.yaml": "interface:\n  display_name: Codex Skill\n" +
				"  short_description: A short one.\npolicy:\n  allow_implicit_invocation: false\n" +
				"dependencies:\n  tools:\n    - type: mcp\n      name: git\n      description: To read history.\n",
		})
		template, err := skills.ReadDirectory("local", "codex", filepath.Join(dir, "codex"))
		require.NoError(t, err)
		skill, err := template.Skill()
		require.NoError(t, err)
		assert.Equal(t, "Codex Skill", skill.DisplayName)
		assert.Equal(t, "A short one.", skill.ShortDescription)
		require.NotNil(t, skill.ModelInvocation)
		assert.False(t, *skill.ModelInvocation)
		require.Len(t, skill.Tools, 1)
		assert.Equal(t, "git", skill.Tools[0].Name)

		// The file is part of the manifest, because a file a client cannot read is not a
		// file of the skill.
		paths := make([]string, 0, len(template.Files))
		for _, file := range template.Files {
			paths = append(paths, file.Path)
		}
		assert.Contains(t, paths, skills.CodexFileName)
	})

	t.Run("refused when the frontmatter also states them", func(t *testing.T) {
		dir := writeSkill(t, map[string]string{
			"both/SKILL.md": "---\nname: both\ndescription: Something.\n" +
				"com.github.manu343726.toolbox/:\n  requirements:\n    tools:\n      - name: git\n" +
				"---\n\nBody.\n",
			"both/agents/openai.yaml": "dependencies:\n  tools:\n    - name: other\n",
		})
		template, err := skills.ReadDirectory("local", "both", filepath.Join(dir, "both"))
		require.NoError(t, err)
		_, err = template.Skill()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agents/openai.yaml")
	})
}
