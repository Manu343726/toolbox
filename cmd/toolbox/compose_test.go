package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/config"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	"github.com/Manu343726/toolbox/pkg/skills/skillv1/skillv1connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/skillgit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The credential is the whole reason a contributor exists, so the guarantee it makes is the
// only thing here worth asserting: **a value a contributor holds does not appear in anything
// the deployment produces or serves.**
//
// The assertions are over the artefacts a deployment actually leaves behind and a client
// actually receives, rather than over a list of fields somebody meant to exclude. A list is
// the shape this guarantee takes when it is not tested, and it is wrong the first time a
// subsystem adds a field.

// A contributor that holds a credential, fetches with it, and hands the provider a path.
//
// This is the case the shipped `skilldirectory` is a public version of: the difference is the
// credential, which is why it belongs here rather than there.
func credentialedContributor(t *testing.T, secret, remote, dataDir string) subsystem.Factory {
	t.Helper()
	return func() (*subsystem.Server, error) {
		return subsystem.NewServer(subsystem.Config{
			Name: "company", Version: "0.1.0",
			Configure: func(ctx context.Context, into subsystem.Compositor) error {
				git, err := skillgit.NewGit("")
				if err != nil {
					return err
				}
				// The credential is used here, once, to fetch. It is a Go value in a
				// closure and it is not passed to anything.
				fetched := filepath.Join(dataDir, "company-skills")
				if err := cloneWithCredential(t, ctx, git, remote, fetched, secret); err != nil {
					return err
				}
				return into.Compose("company-provider", func() (*subsystem.Server, error) {
					return skillgit.New(skillgit.Options{
						// The name the composition gave it, which the provider must report
						// for the deployment to be able to say which one answered.
						Name:    "company-provider",
						DataDir: dataDir,
						Git:     git,
						Registrations: []skillgit.Registration{{
							ID: "company", Directory: "company-skills", ReadOnly: true,
						}},
					})
				})
			},
		})
	}
}

// The deployment a person's project gets, with a contributor supplying its catalog, and every
// artefact the result leaves behind.
func TestAContributorsCredentialReachesNoArtefact(t *testing.T) {
	const secret = "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"
	dataDir := t.TempDir()
	project := t.TempDir()

	// A repository publishing skills the way a repository does, and a project naming it.
	remote := originRepository(t, project)
	require.NoError(t, os.WriteFile(
		filepath.Join(project, "toolbox.yaml"),
		[]byte("skills:\n  - company.coding-standards\n"), 0o600))
	projectDir := filepath.Join(project, ".toolbox")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	require.NoError(t, os.Rename(
		filepath.Join(project, "toolbox.yaml"), filepath.Join(projectDir, config.FileName)))

	resolved, err := config.New(config.Options{
		WorkDir: project, ConfigFile: filepath.Join(projectDir, config.FileName),
	}).Resolve(filepath.Join(projectDir, config.FileName))
	require.NoError(t, err)

	h, _, err := buildHost(hostComposition{config: resolved})
	require.NoError(t, err)
	require.NoError(t, h.Register("company", credentialedContributor(t, secret, remote, dataDir)))
	// The deployment's own `skillgit` is deliberately left out: a contribution adds a
	// subsystem and never replaces one, so the contributor's provider is the one in play.
	require.NoError(t, h.Select("skill", "company", "policy", "registry"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { require.NoError(t, h.Shutdown(context.Background())) }()

	t.Run("the catalog it configured is served", func(t *testing.T) {
		// The point of all of this: it works. A project naming the catalog reaches the
		// skill, which is why the guarantee below is worth anything.
		require.Contains(t, h.Servers(), "company-provider",
			"the contributed provider is running")
		entry := servedEntry(t, h, "company", "coding-standards")
		assert.Equal(t, "skill://company/coding-standards/SKILL.md", entry.GetUri())
		assert.NotEmpty(t, entry.GetFrontmatter().AsMap()["description"],
			"and the skill in it is the one the repository published")
	})

	t.Run("and the credential is in no file the deployment wrote", func(t *testing.T) {
		for _, artefact := range writtenArtefacts(t, projectDir, dataDir) {
			content, err := os.ReadFile(artefact)
			require.NoError(t, err, "%s is readable", artefact)
			assert.NotContains(t, string(content), secret,
				"%s holds a credential, and it was handed to a fetch rather than to a record", artefact)
		}
	})

	t.Run("and in no descriptor, provider record or service the deployment serves", func(t *testing.T) {
		// A credential in a descriptor would be readable by anything that can reach the
		// deployment's registry, and in a provider record by anything that can read the
		// provider directory. Both are things a deployment hands out.
		for name, server := range h.Servers() {
			descriptor := server.Descriptor()
			encoded, err := json.Marshal(descriptor)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), secret,
				"the descriptor of %q carries a credential", name)
		}
		directory := h.ProviderDirectory()
		providers, err := directory.Providers(context.Background())
		require.NoError(t, err)
		for _, provider := range providers {
			encoded, err := json.Marshal(provider)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), secret,
				"the provider record for %q carries a credential", provider.ID)
		}
	})

	t.Run("and the contributor itself is not part of the deployment", func(t *testing.T) {
		// It decided how the rest is wired and is finished. It has no listener, so there
		// is no port for anything to reach it on, and no registry entry naming it.
		assert.NotContains(t, h.Servers(), "company")
		assert.NotContains(t, h.StartOrder(), "company")
	})

	t.Run("and the checkout records a path, not a remote", func(t *testing.T) {
		// What the deployment says about where the content came from is a path on this
		// machine. Which is the answer to "where did this come from" that a person can act
		// on, and it is also the reason a credential cannot ride along.
		recorded, err := os.ReadFile(filepath.Join(dataDir, skillgit.RegistrationsFile))
		require.NoError(t, err)
		assert.Contains(t, string(recorded), "company-skills")
		assert.NotContains(t, string(recorded), "remote:",
			"a catalog contributed by fetching has no remote to record: it was handed a path")
	})
}

// The public directory ships as a contributor in the host's own set, and a deployment that
// has not configured any repositories gets a contributor that contributes nothing and is
// never started.
//
// This is the case that decides whether registering it is safe: a contributor in the default
// set that contributed something, or failed, would make an optional thing a requirement.
func TestThePublicDirectoryIsSafeToHaveInADeploymentByDefault(t *testing.T) {
	t.Setenv("TOOLBOX_SKILL_REPOSITORIES", "")
	resolved, err := config.New(config.Options{WorkDir: t.TempDir()}).Resolve("")
	require.NoError(t, err)

	h, _, err := buildHost(hostComposition{config: resolved})
	require.NoError(t, err)
	// Registered by the host itself, which is the point: nothing had to be added for it to
	// be part of the deployment.
	require.NoError(t, h.Select("skilldirectory", "skill"))
	require.NoError(t, h.Start(context.Background()))
	defer func() { require.NoError(t, h.Shutdown(context.Background())) }()

	assert.NotContains(t, h.Servers(), "skilldirectory",
		"so it is not started, and a deployment listing never mentions it")
	assert.NotContains(t, h.StartOrder(), "skilldirectory")
	assert.Contains(t, h.Servers(), "skill", "and the deployment runs what it otherwise would")
}

// The helpers the leak test needs: a repository that publishes skills the way a repository
// does, a fetch that uses a credential, the artefacts a deployment writes, and the entry a
// client would be served.

// originRepository lays out a repository with skills under a convention its own clients read,
// and returns its path. A local path stands in for a remote, so nothing here touches the
// network and nothing depends on the machine's privileges or credentials.
func originRepository(t *testing.T, root string) string {
	t.Helper()
	directory := filepath.Join(root, "origin")
	skill := filepath.Join(directory, ".claude", "skills", "coding-standards")
	require.NoError(t, os.MkdirAll(skill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skill, "SKILL.md"),
		[]byte("---\nname: coding-standards\ndescription: Use this when writing code here.\n"+
			"---\n\n# Coding standards\n\nFollow them.\n"), 0o644))

	git, err := skillgit.NewGit("")
	require.NoError(t, err)
	require.NoError(t, git.Init(context.Background(), directory, skillgit.DefaultIdentity()))
	return directory
}

// cloneWithCredential fetches a repository the way a contributor holding a credential does:
// the credential is given to the fetch and to nothing else.
//
// The remote here is a local path, so there is nothing for the credential to authenticate
// with, and it is passed through git's own environment as a variable rather than in a URL —
// which is the point of the exercise. A credential in a URL is written to the checkout's own
// config by git, and a contributor that has the choice should not make it.
func cloneWithCredential(
	t *testing.T, ctx context.Context, git *skillgit.Git, remote, directory, secret string,
) error {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "!f() { test \"$1\" = get && echo \"password="+secret+"\"; }; f")
	return git.Clone(ctx, remote, directory)
}

// writtenArtefacts is every file the deployment wrote that a person could read: the project's
// own configuration, the lockfile beside it, and the provider's record of its catalogs.
func writtenArtefacts(t *testing.T, projectDir, dataDir string) []string {
	t.Helper()
	candidates := []string{
		filepath.Join(projectDir, config.FileName),
		filepath.Join(projectDir, "skills.lock.yaml"),
		filepath.Join(dataDir, skillgit.RegistrationsFile),
	}
	var found []string
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			found = append(found, candidate)
		}
	}
	require.NotEmpty(t, found, "the deployment wrote at least its configuration")
	return found
}

// servedEntry is what a client would be served for a reference, reached over ConnectRPC the
// way the Model Context Protocol endpoint reaches it.
func servedEntry(t *testing.T, h interface {
	Servers() map[string]*subsystem.Server
}, catalog, name string) *skillv1.SkillEntry {
	t.Helper()
	host, ok := h.(interface {
		Servers() map[string]*subsystem.Server
	})
	require.True(t, ok)
	server, running := host.Servers()["company-provider"]
	require.True(t, running, "the contributed provider is running")

	client := skillv1connect.NewSkillCatalogServiceClient(http.DefaultClient, server.Endpoint())
	response, err := client.GetSkill(context.Background(), connect.NewRequest(&skillv1.GetSkillRequest{
		Catalog: catalog, Name: name,
	}))
	require.NoError(t, err, "the catalog the contributor configured serves the skill in it")
	require.NotNil(t, response.Msg.GetEntry())
	return response.Msg.GetEntry()
}
