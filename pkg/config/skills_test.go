package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// project lays out a project with a configuration file and returns its path.
func project(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, ".toolbox")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	path := filepath.Join(directory, config.FileName)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func load(t *testing.T, path string) config.Config {
	t.Helper()
	resolved, err := config.New(config.Options{ConfigFile: path, WorkDir: filepath.Dir(filepath.Dir(path))}).
		Resolve(path)
	require.NoError(t, err)
	return resolved
}

// A project's `skills:` list is read out of its own file, and a project with none has an
// empty list rather than an error.
func TestTheSkillsListIsReadFromTheProjectFile(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		resolved := load(t, project(t, "skills:\n  - skills.sh.pull-request-review\n  - some-team.coding-standards\n"))
		assert.Equal(t, []string{"skills.sh.pull-request-review", "some-team.coding-standards"},
			resolved.Skills)
		assert.Equal(t, config.SourceFile, resolved.SourceOf(config.KeySkills))
	})
	t.Run("absent", func(t *testing.T) {
		resolved := load(t, project(t, "daemon:\n  host: 127.0.0.1\n"))
		assert.Empty(t, resolved.Skills, "a project naming no remote skills is a normal project")
	})
	t.Run("empty", func(t *testing.T) {
		resolved := load(t, project(t, "skills: []\n"))
		assert.Empty(t, resolved.Skills)
	})
}

// The section is located here and interpreted by the reader that owns skills, so a
// misspelled top-level key is still caught with the list of keys this package accepts.
func TestAMisspelledKeyIsStillRefused(t *testing.T) {
	path := project(t, "skill:\n  - some-team.review\n")
	_, err := config.New(config.Options{WorkDir: filepath.Dir(filepath.Dir(path))}).Resolve(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skill")
	assert.Contains(t, err.Error(), config.KeySkills, "the message names the key that was meant")
}

// Adding a skill to a project means naming it in the project's configuration, and never
// copying files into it — which is what keeps every catalog read-only and true.
func TestAddingASkillProposesAChangeAndDoesNotMakeIt(t *testing.T) {
	path := project(t, "# This project uses the team's review standards.\ndaemon:\n  host: 127.0.0.1\n")
	resolved := load(t, path)

	change, err := resolved.ProposeSkillAddition(skills.Reference{Catalog: "some-team", Name: "review"})
	require.NoError(t, err)
	assert.Equal(t, path, change.File())
	assert.Equal(t, "some-team.review", change.Reference().String())
	assert.Contains(t, change.Summary(), path, "the summary names the file")
	assert.Contains(t, change.Summary(), "some-team.review", "and the change")
	assert.Contains(t, change.Summary(), "would then include",
		"and states the outcome rather than the action, because that is what is being agreed to")
	assert.True(t, change.Changed())

	// Nothing has been written: proposing and making are two calls so a caller cannot do the
	// second without having the first to show.
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(before), "some-team.review")

	require.NoError(t, change.Apply())
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(after), "- some-team.review")
	assert.Contains(t, string(after), "# This project uses the team's review standards.",
		"the rest of the file is left alone, comments included: a diff that says more than "+
			"the change does is a diff nobody reviews")
	assert.Contains(t, string(after), "host: 127.0.0.1")
}

// A local skill is already part of the project, so naming one is refused with the reason
// rather than accepted and quietly ignored.
func TestNamingALocalSkillIsRefusedWithTheReason(t *testing.T) {
	resolved := load(t, project(t, "daemon:\n  host: 127.0.0.1\n"))

	_, err := resolved.ProposeSkillAddition(skills.Reference{Catalog: skills.LocalCatalog, Name: "review"})
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "already part of the project")
}

// Adding what the project already has is not a change, and reporting it as one would send a
// person to confirm a diff that is empty.
func TestAddingWhatTheProjectAlreadyHasIsNotAChange(t *testing.T) {
	resolved := load(t, project(t, "skills:\n  - some-team.review\n"))

	change, err := resolved.ProposeSkillAddition(skills.Reference{Catalog: "some-team", Name: "review"})
	require.NoError(t, err)
	assert.False(t, change.Changed())
	require.NoError(t, change.Apply(), "applying a change that changes nothing is not a write")

	after, err := os.ReadFile(resolved.Path)
	require.NoError(t, err)
	assert.Contains(t, string(after), "- some-team.review")
}

// Removing a reference the project does not name is reported as nothing to remove, because
// "disabled" and "was never enabled" are different facts and a caller reporting the second as
// the first would be telling a person a change happened that did not.
func TestRemovingWhatTheProjectDoesNotNameIsReported(t *testing.T) {
	resolved := load(t, project(t, "skills:\n  - some-team.review\n"))

	_, err := resolved.ProposeSkillRemoval(skills.Reference{Catalog: "other", Name: "review"})
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	assert.Contains(t, err.Error(), "nothing to remove")
}

// Disabling is a real change and the file says so.
func TestRemovingASkillProposesTheRemoval(t *testing.T) {
	path := project(t, "skills:\n  - some-team.review\n  - other.standards\n")
	resolved := load(t, path)

	change, err := resolved.ProposeSkillRemoval(skills.Reference{Catalog: "some-team", Name: "review"})
	require.NoError(t, err)
	assert.Contains(t, change.Summary(), "would then no longer include")
	require.NoError(t, change.Apply())

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(after), "some-team.review")
	assert.Contains(t, string(after), "other.standards", "the rest of the list is left alone")
}

// A deployment's own configuration file is not a project saying something about itself, so it
// is refused rather than written: the file that records what a project depends on is the
// project's.
func TestADeploymentConfigurationIsNotAProjectFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, config.FileName)
	require.NoError(t, os.WriteFile(path, []byte("daemon:\n  host: 127.0.0.1\n"), 0o600))

	resolved := load(t, path)
	_, err := resolved.ProposeSkillAddition(skills.Reference{Catalog: "some-team", Name: "review"})
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), config.ProjectDir)
}

// A section that is not a list is refused by name, because a `skills:` key holding a block is
// a person who meant to write something else, and serving them no skills would report a
// working system.
func TestASectionThatIsNotAListIsRefused(t *testing.T) {
	for _, content := range []string{
		"skills:\n  catalog: some-team\n",
		"skills: some-team.review\n",
		"skills:\n  - name: some-team.review\n",
	} {
		t.Run(content, func(t *testing.T) {
			path := project(t, content)
			_, err := config.New(config.Options{WorkDir: filepath.Dir(filepath.Dir(path))}).Resolve(path)
			if err == nil {
				// Reading the list happens when a skill is added or removed, so that is
				// where a section of the wrong shape is caught.
				change, proposeErr := load(t, path).ProposeSkillAddition(
					skills.Reference{Catalog: "other", Name: "thing"})
				if proposeErr == nil {
					proposeErr = change.Apply()
				}
				err = proposeErr
			}
			require.Error(t, err)
			assert.Equal(t, api.KindInvalid, api.KindOf(err))
		})
	}
}

// A project with no configuration file has nothing to record a skill in, and saying so is more
// useful than creating one.
func TestAProjectWithNoConfigurationFileIsReported(t *testing.T) {
	directory := t.TempDir()
	resolved, err := config.New(config.Options{WorkDir: directory}).Resolve("")
	require.NoError(t, err)
	assert.Empty(t, resolved.Path)

	_, err = resolved.ProposeSkillAddition(skills.Reference{Catalog: "some-team", Name: "review"})
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	assert.Contains(t, err.Error(), config.FileName)
}

// A change to a project's configuration is proposed, not made, because that file is the one
// file in a project a person writes and reviews. This asserts the shape of the interface that
// enforces it: the summary is reachable without applying, and applying is a separate call.
func TestAChangeIsProposedBeforeItIsMade(t *testing.T) {
	resolved := load(t, project(t, "daemon:\n  host: 127.0.0.1\n"))

	change, err := resolved.ProposeSkillAddition(skills.Reference{Catalog: "some-team", Name: "review"})
	require.NoError(t, err)

	// Everything a person needs to answer is available on the change itself.
	assert.NotEmpty(t, change.Summary())
	assert.NotEmpty(t, change.File())
	assert.Equal(t, []string{"some-team.review"}, change.After())
}
