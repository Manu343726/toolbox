package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The client side of the extension, declared here as well as on the server. A custom method is
// something both ends register, and a test that reached the server by any other route would not
// be testing what a real client does.
type (
	clientListParams struct{ sdkmcp.ParamsBase }
	clientListResult struct {
		sdkmcp.ResultBase
		Skills []skillsEntry `json:"skills"`
	}
	clientGetParams struct {
		sdkmcp.ParamsBase
		URI string `json:"uri"`
	}
	clientGetResult struct {
		sdkmcp.ResultBase
		Skill skillsEntry `json:"skill"`
	}
)

// fakeSkills is a deployment's skill surface as the gateway sees it: entries already projected
// for the client that asked.
//
// It stands in for the skills subsystem so that this file tests the *protocol* — the two
// methods, the entry shape, the declaration and the URI handling — rather than the
// aggregation, which is tested where it lives.
type fakeSkills struct {
	entries  []skills.Entry
	files    map[string][]byte
	failWith error
	// clients records the identity each call arrived with, so a test can assert that it came
	// from the request rather than from configuration.
	clients []skills.Client
}

func (f *fakeSkills) ListSkills(_ context.Context, client skills.Client) ([]skills.Entry, error) {
	f.clients = append(f.clients, client)
	if f.failWith != nil {
		return nil, f.failWith
	}
	return f.entries, nil
}

func (f *fakeSkills) GetSkill(
	_ context.Context, client skills.Client, catalog, name string,
) (skills.Entry, error) {
	f.clients = append(f.clients, client)
	if f.failWith != nil {
		return skills.Entry{}, f.failWith
	}
	for _, candidate := range f.entries {
		if candidate.Catalog == catalog && candidate.Name == name {
			return candidate, nil
		}
	}
	return skills.Entry{}, api.Errorf(api.KindNotFound, "the catalog has no skill %q", name)
}

func (f *fakeSkills) ReadSkillFile(
	_ context.Context, _ skills.Client, _, _, path string,
) ([]byte, skills.File, error) {
	content, found := f.files[path]
	if !found {
		return nil, skills.File{}, api.Errorf(api.KindNotFound, "no file at %q", path)
	}
	return content, skills.NewFile(path, content), nil
}

// lyingSkills reports a manifest that does not describe the bytes it holds, which is what a
// broken or compromised catalog looks like from here.
type lyingSkills struct{ *fakeSkills }

func (l *lyingSkills) ReadSkillFile(
	ctx context.Context, client skills.Client, catalog, name, path string,
) ([]byte, skills.File, error) {
	content, _, err := l.fakeSkills.ReadSkillFile(ctx, client, catalog, name, path)
	if err != nil {
		return nil, skills.File{}, err
	}
	return content, skills.File{
		Path: path, Size: 1, MIMEType: "text/markdown",
		Digest: "sha256:" + strings.Repeat("0", 64),
	}, nil
}

// sampleEntry is a distilled skill, as a catalog would hand one over.
func sampleEntry(name string, unmet ...skills.Unmet) skills.Entry {
	template, err := skills.Parse(skills.LocalCatalog, name, []byte(
		"---\nname: "+name+"\ndescription: Use this when reviewing a change.\n"+
			"license: Apache-2.0\n---\n\n# "+name+"\n\nDo the thing.\n"), nil)
	if err != nil {
		panic(err)
	}
	entry, err := skills.Distill(template, skills.Client{Name: "claude-code"},
		skills.DistillOptions{Unmet: unmet})
	if err != nil {
		panic(err)
	}
	return entry
}

// skillsGateway connects a client that reports `name` to a gateway serving `source`.
//
// The name matters: a client identity is what selects a projection, so a test that wanted the
// common denominator and a test that wanted Claude Code's spelling need different clients, and
// the gateway has no configuration that would decide it for them.
func skillsGateway(t *testing.T, name string, source SkillsSource) (*Server, *sdkmcp.ClientSession) {
	t.Helper()
	bridge, err := New(t.Context(), newFakeSource(t), Options{
		Name:   "toolbox",
		Policy: APIPolicy(),
		Skills: source,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() { serverErr <- bridge.Run(ctx, serverTransport) }()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: name, Version: "1.2.3"}, nil)
	require.NoError(t, sdkmcp.AddSendingCustomMethod[*clientListParams, *clientListResult](
		client, "skills/list"))
	require.NoError(t, sdkmcp.AddSendingCustomMethod[*clientGetParams, *clientGetResult](
		client, "skills/get"))
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
	})
	return bridge, session
}

func listSkills(t *testing.T, session *sdkmcp.ClientSession) []skillsEntry {
	t.Helper()
	result, err := sdkmcp.CallCustomMethod[*clientListParams, *clientListResult](
		t.Context(), session, "skills/list", nil)
	require.NoError(t, err)
	return result.Skills
}

func getSkill(t *testing.T, session *sdkmcp.ClientSession, uri string) (skillsEntry, error) {
	result, err := sdkmcp.CallCustomMethod[*clientGetParams, *clientGetResult](
		t.Context(), session, "skills/get", &clientGetParams{URI: uri})
	if err != nil {
		return skillsEntry{}, err
	}
	return result.Skill, nil
}

// The extension is declared where the specification says to declare it, and a deployment that
// serves no skills declares nothing — a server declaring an extension it did not implement would
// send a client looking for methods that answer "not found".
func TestTheExtensionIsDeclaredOnlyWhenSkillsAreServed(t *testing.T) {
	t.Run("declared when skills are served", func(t *testing.T) {
		_, session := skillsGateway(t, "claude-code", &fakeSkills{
			entries: []skills.Entry{sampleEntry("code-review")},
		})
		// The SDK performs `server/discover` itself when it connects to a stateless server,
		// and the capabilities it comes back with are the ones a client reads. There is no
		// client method for it, so the declaration is asserted where a client actually sees
		// it: the result the handshake carries.
		initialized := session.InitializeResult()
		require.NotNil(t, initialized, "the server answered the discover handshake")
		settings, found := initialized.Capabilities.Extensions[SkillsExtension]
		require.True(t, found, "declared in the extensions field of the discover response")
		assert.Equal(t, map[string]any{"directoryRead": DirectoryRead}, settings,
			"the extension's own setting, false because the manifest is already complete")
	})

	t.Run("not declared when no skills are served", func(t *testing.T) {
		_, session := skillsGateway(t, "claude-code", nil)
		initialized := session.InitializeResult()
		require.NotNil(t, initialized)
		_, found := initialized.Capabilities.Extensions[SkillsExtension]
		assert.False(t, found)
	})
}

// `skills/list` returns every skill the deployment serves, each with a complete manifest, and
// an empty list is how a deployment with no skills says so.
func TestSkillsListReturnsEverySkillWithACompleteManifest(t *testing.T) {
	t.Run("with skills", func(t *testing.T) {
		_, session := skillsGateway(t, "claude-code", &fakeSkills{entries: []skills.Entry{
			sampleEntry("code-review"), sampleEntry("release-notes"),
		}})
		listed := listSkills(t, session)
		require.Len(t, listed, 2)
		for _, one := range listed {
			assert.NotEmpty(t, one.URI, "an entry is addressed by the URI of its SKILL.md")
			assert.NotEmpty(t, one.Frontmatter["name"])
			assert.Equal(t, "Apache-2.0", one.Frontmatter["license"],
				"every field the author wrote reaches the client, since a host builds its "+
					"registry from these entries alone")
			require.NotEmpty(t, one.Resources, "the manifest is complete by construction")
			hasDocument := false
			for _, resource := range one.Resources {
				assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, resource.Digest)
				assert.NotEmpty(t, resource.URI)
				if strings.HasSuffix(resource.URI, "/SKILL.md") {
					hasDocument = true
				}
			}
			assert.True(t, hasDocument, "the skill's own document is among its resources")
		}
	})

	t.Run("without skills", func(t *testing.T) {
		_, session := skillsGateway(t, "claude-code", &fakeSkills{})
		assert.Empty(t, listSkills(t, session),
			"an empty list is how a server says it serves none, and a client cannot tell "+
				"that from a failure it would have to retry")
	})
}

// `skills/get` returns one entry by the URI of its SKILL.md, and a URI identifying no skill is
// refused rather than guessed at.
func TestSkillsGetIsByURIAndRefusesOneNamingNoSkill(t *testing.T) {
	_, session := skillsGateway(t, "claude-code", &fakeSkills{
		entries: []skills.Entry{sampleEntry("code-review")},
	})

	found, err := getSkill(t, session, "skill://local/code-review/SKILL.md")
	require.NoError(t, err)
	assert.Equal(t, "skill://local/code-review/SKILL.md", found.URI)
	assert.Equal(t, "code-review", found.Frontmatter["name"])

	for _, uri := range []string{
		"skill://local/absent/SKILL.md",
		"skill://local/SKILL.md",
		"https://example.com/SKILL.md",
		"skill://local/code-review/other.md",
	} {
		t.Run("refused: "+uri, func(t *testing.T) {
			_, err := getSkill(t, session, uri)
			require.Error(t, err, "a server that resolved a URI naming no skill to *some* "+
				"skill would serve instructions its reader did not ask for")
		})
	}
}

// A skill's files are ordinary MCP resources under a `skill://` URI, read with the standard
// `resources/read` — which is what makes the extension a way to publish instructions alongside
// the tools a server already serves rather than a second content channel.
func TestASkillsFilesAreOrdinaryResources(t *testing.T) {
	document := []byte("# Review\n\nRead the diff.\n")
	_, session := skillsGateway(t, "claude-code", &fakeSkills{
		entries: []skills.Entry{sampleEntry("code-review")},
		files:   map[string][]byte{"SKILL.md": document},
	})

	t.Run("reachable through a URI template", func(t *testing.T) {
		templates, err := session.ListResourceTemplates(t.Context(), &sdkmcp.ListResourceTemplatesParams{})
		require.NoError(t, err)
		found := false
		for _, one := range templates.ResourceTemplates {
			if strings.HasPrefix(one.URITemplate, "skill://") {
				found = true
			}
		}
		assert.True(t, found)
	})

	t.Run("read with resources/read", func(t *testing.T) {
		result, err := session.ReadResource(t.Context(), &sdkmcp.ReadResourceParams{
			URI: "skill://local/code-review/SKILL.md",
		})
		require.NoError(t, err)
		require.Len(t, result.Contents, 1)
		assert.Equal(t, "text/markdown", result.Contents[0].MIMEType)
		assert.Equal(t, string(document), result.Contents[0].Text)
	})

	t.Run("refused for a file the skill does not have", func(t *testing.T) {
		_, err := session.ReadResource(t.Context(), &sdkmcp.ReadResourceParams{
			URI: "skill://local/code-review/scripts/absent.sh",
		})
		require.Error(t, err)
	})

	t.Run("refused for a URI that is not a skill's", func(t *testing.T) {
		_, err := session.ReadResource(t.Context(), &sdkmcp.ReadResourceParams{
			URI: "https://example.com/anything",
		})
		require.Error(t, err)
	})
}

// The content a file is served with is verified against the manifest entry it is served under.
// The specification has the *host* verify what it fetched; a server that verifies first can
// refuse to serve at all, which is a better outcome than a host discovering it.
func TestAFileWhoseContentDoesNotMatchItsManifestIsNotServed(t *testing.T) {
	document := []byte("# Review\n")
	honest := &fakeSkills{
		entries: []skills.Entry{sampleEntry("code-review")},
		files:   map[string][]byte{"SKILL.md": document},
	}
	_, session := skillsGateway(t, "claude-code", honest)
	_, err := session.ReadResource(t.Context(), &sdkmcp.ReadResourceParams{
		URI: "skill://local/code-review/SKILL.md",
	})
	require.NoError(t, err, "content that matches its manifest is served")

	_, session = skillsGateway(t, "claude-code", &lyingSkills{fakeSkills: honest})
	_, err = session.ReadResource(t.Context(), &sdkmcp.ReadResourceParams{
		URI: "skill://local/code-review/SKILL.md",
	})
	require.Error(t, err, "a digest a host could never verify is worse than no file at all")
	assert.Contains(t, err.Error(), "not what the manifest described")
}

// The client identity comes from the request. Under revision 2026-07-28 there is no `initialize`
// handshake, so the client states itself in every request's metadata and the SDK leaves the
// session's initialization parameters nil — which is why this is read from the request and not
// from anywhere a deployment could have configured it.
func TestClientIdentityComesFromTheRequest(t *testing.T) {
	source := &fakeSkills{entries: []skills.Entry{sampleEntry("code-review")}}
	_, session := skillsGateway(t, "claude-code", source)

	_ = listSkills(t, session)
	require.Len(t, source.clients, 1, "the handler saw a client")
	assert.Equal(t, "claude-code", source.clients[0].Name,
		"the identity is what the client reported, which is the only place it comes from")
	assert.Equal(t, "1.2.3", source.clients[0].Version)

	t.Run("a client that says nothing is served rather than refused", func(t *testing.T) {
		unknown := &fakeSkills{entries: []skills.Entry{sampleEntry("code-review")}}
		_, anonymous := skillsGateway(t, "", unknown)
		assert.Len(t, listSkills(t, anonymous), 1,
			"a client this framework has never heard of is still served, in the form every "+
				"client understands")
	})

	t.Run("the common denominator is what an unknown client gets", func(t *testing.T) {
		assert.Equal(t, skills.Client{}, clientOf(nil),
			"no request in hand is not an error; it is a client that said nothing")
	})
}

// A requirement the deployment cannot meet is reported in the entry rather than granted.
func TestAnEntryCarriesWhatIsUnmet(t *testing.T) {
	_, session := skillsGateway(t, "code-review", &fakeSkills{entries: []skills.Entry{
		sampleEntry("code-review", skills.Unmet{Requirement: skills.ToolRequirement{Name: "git"}}),
	}})

	found, err := getSkill(t, session, "skill://local/code-review/SKILL.md")
	require.NoError(t, err)
	assert.Equal(t, []string{"git"}, found.Unmet,
		"a declared requirement this deployment cannot meet is reported, not granted")
}

// A failure from the catalog reaches the client rather than being swallowed, because a
// deployment that cannot list its skills and one that has none look identical otherwise.
func TestAFailureReachesTheClient(t *testing.T) {
	_, session := skillsGateway(t, "claude-code", &fakeSkills{
		failWith: api.Errorf(api.KindUnavailable, "the catalog is unreachable"),
	})

	_, err := sdkmcp.CallCustomMethod[*clientListParams, *clientListResult](
		t.Context(), session, "skills/list", nil)
	require.Error(t, err)
}
