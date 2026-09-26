package skilldirectory_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Manu343726/toolbox/pkg/host"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/skilldirectory"
	"github.com/Manu343726/toolbox/subsystems/skillgit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A repository stands in for one the public directory indexes. A local path is a remote, so
// nothing here touches the network and nothing depends on the machine's privileges.
func repository(t *testing.T, skillName, description string) string {
	t.Helper()
	// Under a named directory, so the contributor derives a catalog name a person would
	// recognise and does not resolve the path as an `owner/repo` shorthand to GitHub.
	directory := filepath.Join(t.TempDir(), "some-team", "skills")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	skill := filepath.Join(directory, ".claude", "skills", skillName)
	require.NoError(t, os.MkdirAll(skill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skill, "SKILL.md"),
		[]byte("---\nname: "+skillName+"\ndescription: "+description+"\n---\n\n# "+skillName+"\n\nBody.\n"),
		0o644))
	git, err := skillgit.NewGit("")
	require.NoError(t, err)
	require.NoError(t, git.Init(context.Background(), directory, skillgit.DefaultIdentity()))
	return directory
}

// The contributor fetches what it was told to and the deployment serves it, which is the whole
// claim: a skill from the public directory reaches a project the way a git-backed catalog's
// skill does, and the only thing that made it a *directory* rather than a URL is a
// contribution.
func TestTheDirectoryIsFetchedAndServed(t *testing.T) {
	remote := repository(t, "coding-standards", "Use this when writing code here.")
	dataDir := t.TempDir()

	h := host.New()
	require.NoError(t, h.Register("skilldirectory", func() (*subsystem.Server, error) {
		return skilldirectory.New(skilldirectory.Options{
			Repositories: []string{remote},
			DataDir:      dataDir,
			CheckoutDir:  t.TempDir(),
		})
	}))
	require.NoError(t, h.Select("skilldirectory"))
	require.NoError(t, h.Start(t.Context()))
	defer func() { _ = h.Shutdown(t.Context()) }()

	require.Contains(t, h.Servers(), skilldirectory.ProviderName,
		"the contributed provider is running, under the name the composition gave it")
	assert.NotContains(t, h.Servers(), "skilldirectory",
		"and the contributor itself is not: it decided the wiring and is finished")
	assert.NotContains(t, h.StartOrder(), "skilldirectory",
		"so it is not even in the start order, which is what makes it invisible in a deployment")

	t.Run("and it holds no credential anywhere, because it needed none", func(t *testing.T) {
		// The public directory is browsed, not written to, so this contributor holds
		// nothing secret. What it does is the same three lines a user's own contributor
		// with a credential does, which is why the shape is worth shipping.
		recorded, err := os.ReadFile(filepath.Join(dataDir, skillgit.RegistrationsFile))
		require.NoError(t, err)
		assert.Contains(t, string(recorded), "id: skills",
			"the catalog is named after the repository's own directory, which is the word a "+
				"project writes and is one segment of a skill's URI")
		assert.NotContains(t, string(recorded), "remote:",
			"a catalog handed a checkout has no remote to record, and a path is the one thing "+
				"that cannot carry a credential")
		assert.NotContains(t, string(recorded), "auth:",
			"and nothing to authenticate, which for the public directory is the whole reason "+
				"it needs nothing held back")
	})
}

// A deployment that has not said which repositories it wants is a deployment serving only its
// project's own skills, which is a working deployment. A contributor that refused to start
// would make an optional thing a requirement.
func TestContributingNothingIsNotAFailure(t *testing.T) {
	for name, options := range map[string]skilldirectory.Options{
		"unset":                              {DataDir: t.TempDir()},
		"empty":                              {DataDir: t.TempDir(), Repositories: []string{}},
		"blanks a machine's list would have": {DataDir: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			// A host of its own, because a registration is refused after a start and the
			// point of the table is four arrangements rather than one.
			h := host.New()
			require.NoError(t, h.Register("skilldirectory", func() (*subsystem.Server, error) {
				return skilldirectory.New(options)
			}))
			require.NoError(t, h.Start(t.Context()))
			defer func() { _ = h.Shutdown(t.Context()) }()
			assert.Empty(t, h.Servers(), "and it composed nothing")
		})
	}
}

// A blank is a typo in a list a person wrote and a trailing comma in a machine's, and the two
// are treated differently on purpose: a list a person wrote is refused with the reason, so
// they hear that their repository did not register, while a comma-separated variable has
// nothing a person wrote in it and is filtered.
func TestABlankIsRefusedInAListAndFilteredInAVariable(t *testing.T) {
	t.Run("refused in a list", func(t *testing.T) {
		recorder := &recordingCompositor{}
		err := contributorsConfigure(t, recorder, skilldirectory.Options{
			Repositories: []string{"  "},
			DataDir:      t.TempDir(),
		})
		require.Error(t, err, "a person who wrote a blank entry is told, rather than served a "+
			"deployment with no catalog and no reason")
		assert.Contains(t, err.Error(), "empty")
		assert.Empty(t, recorder.composed)
	})

	t.Run("filtered in a variable", func(t *testing.T) {
		// Which is what a trailing comma produces, and a trailing comma is not a mistake.
		t.Setenv(skilldirectory.RepositoriesVariable, "no-slash, ,")
		recorder := &recordingCompositor{}
		err := contributorsConfigure(t, recorder, skilldirectory.Options{DataDir: t.TempDir()})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no-slash",
			"and the entry that was there is still contributed")
	})
}

// A repository whose name could not be a catalog's identifier is refused, because a catalog is
// one segment of a skill's URI and one directory the provider creates — and the rule is the
// provider's, applied here rather than restated, so a name is not let through that the
// provider would then refuse.
func TestARepositoryNamedUnusablyIsRefused(t *testing.T) {
	for _, malformed := range []string{"/", "..", ".", ".hidden", "a name", "a\tb"} {
		t.Run("refused: "+malformed, func(t *testing.T) {
			recorder := &recordingCompositor{}
			_, err := skilldirectory.New(skilldirectory.Options{
				Repositories: []string{malformed},
				DataDir:      t.TempDir(),
			})
			require.NoError(t, err)
			err = contributorsConfigure(t, recorder, skilldirectory.Options{
				Repositories: []string{malformed},
				DataDir:      t.TempDir(),
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "catalog's name",
				"the message names the rule that fired, so a person fixing their list knows "+
					"what to write instead")
		})
	}
}

// A repository whose own name is usable is served under that name, whether it was named as a
// shorthand or as a path — a deployment mirroring a repository should not have to pretend to be
// the public directory to use it.
func TestTheNameIsTheLastSegmentOfWhateverWasNamed(t *testing.T) {
	for _, named := range []string{
		"vercel-labs/skills",
		"vercel-labs/skills/",
		"owner/",
		"a/b/",
		"/srv/mirrors/coding-standards",
		"./relative/standards",
	} {
		t.Run(named, func(t *testing.T) {
			recorder := &recordingCompositor{}
			// The fetch is what stops this test before the assertion, so what is asserted is
			// that the name was accepted and the failure is the remote rather than the name.
			err := contributorsConfigure(t, recorder, skilldirectory.Options{
				Repositories: []string{named},
				DataDir:      t.TempDir(),
				CheckoutDir:  t.TempDir(),
			})
			if err != nil {
				assert.NotContains(t, err.Error(), "could not be served under",
					"%q has a usable name, so the failure is the fetch", named)
			}
		})
	}
}

// The repositories are read from the environment rather than the project's configuration,
// because what a deployment draws skills from is the deployment's answer: two projects on one
// machine should not have to state the same list twice, and neither is where a person looks
// to change it.
func TestTheRepositoriesComeFromTheEnvironment(t *testing.T) {
	t.Setenv(skilldirectory.RepositoriesVariable, "no-slash")
	recorder := &recordingCompositor{}
	err := contributorsConfigure(t, recorder, skilldirectory.Options{DataDir: t.TempDir()})
	// The name in the variable is a usable one, so the contribution gets as far as the
	// fetch — which is what proves the environment was read at all, and that a person
	// correcting their variable is looking at the right place.
	require.Error(t, err, "the repository it names does not exist")
	assert.Contains(t, err.Error(), "no-slash",
		"and the failure is about the repository from the variable rather than about the "+
			"variable itself")
}

// The contributor exposes no contract of its own, so a deployment listing never mentions it.
func TestTheContributorExposesNothing(t *testing.T) {
	server, err := skilldirectory.New(skilldirectory.Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	assert.Empty(t, server.Descriptor().ServiceNames)
	assert.False(t, server.Serves(), "so it is not started")
	assert.True(t, server.IsContributor(), "while still contributing")
}

// A contributor is a subsystem, so it is named, versioned and described like one, and it
// builds and tests as a module of its own.
func TestTheContributorIsASubsystem(t *testing.T) {
	server, err := skilldirectory.New(skilldirectory.Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	descriptor := server.Descriptor()
	assert.Equal(t, skilldirectory.Name, descriptor.SubsystemName)
	assert.NotEmpty(t, descriptor.ImplementationVersion)
	assert.NotEmpty(t, descriptor.Description)
}

// recordingCompositor collects what a contribution adds, so a test can read the decision
// without starting a deployment for it.
type recordingCompositor struct{ composed []string }

func (r *recordingCompositor) Compose(name string, _ subsystem.Factory) error {
	r.composed = append(r.composed, name)
	return nil
}

func contributorsConfigure(
	t *testing.T, into subsystem.Compositor, options skilldirectory.Options,
) error {
	t.Helper()
	server, err := skilldirectory.New(options)
	require.NoError(t, err)
	return server.Configure(t.Context(), into)
}
