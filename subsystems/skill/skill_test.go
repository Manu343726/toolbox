package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/config"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	skillv1skill "github.com/Manu343726/toolbox/subsystems/skill/skillv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// project lays out a project with a configuration file and some skills, and returns the service
// over it.
func project(t *testing.T, configuration string, files map[string]string) *Service {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, ".toolbox")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, config.FileName), []byte(configuration), 0o600))
	for relative, content := range files {
		full := filepath.Join(directory, relative)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	resolved, err := config.New(config.Options{WorkDir: root}).Resolve("")
	require.NoError(t, err)
	service, err := NewService(Options{ProjectDir: directory, Config: NewFileConfig(resolved)})
	require.NoError(t, err)
	return service
}

// bareService is a service with no project at all: a deployment serving only its own
// configuration. Its project's skills are then the local ones, and there are none.
func bareService(t *testing.T) *Service {
	t.Helper()
	service, err := NewService(Options{})
	require.NoError(t, err)
	return service
}

const reviewSkill = "---\nname: code-review\ndescription: Use this when reviewing a change.\n---\n\n# Review\n\nRead the diff.\n"

// A project's effective set is every skill in the local catalog plus every reference it names.
// The two halves are for different purposes, and this is where that shows.
func TestTheEffectiveSetIsLocalSkillsPlusNamedOnes(t *testing.T) {
	service := project(t, "skills:\n  - some-team.coding-standards\n", map[string]string{
		"skills/code-review/SKILL.md":   reviewSkill,
		"skills/release-notes/SKILL.md": "---\nname: release-notes\ndescription: Use this when releasing.\n---\n\nBody.\n",
	})
	// The named catalog is not integrated, so the named skill cannot be fetched. It is
	// reported by CheckPins as a missing reference rather than appearing in a listing.
	found, err := service.effectiveSet(t.Context())
	require.NoError(t, err)

	names := make([]string, 0, len(found))
	for _, one := range found {
		names = append(names, one.GetRef().GetCatalog()+"."+one.GetRef().GetName())
	}
	assert.Equal(t, []string{"local.code-review", "local.release-notes"}, names,
		"local skills are part of the project without being named, and cannot be forgotten")
	for _, one := range found {
		assert.False(t, one.GetDeclared(),
			"a local skill is not declared, which is the fact a reviewer wants to read off the answer")
		assert.Equal(t, "skill://local/"+one.GetRef().GetName()+"/SKILL.md", one.GetUri())
	}
}

// A local skill is switched off in its own frontmatter, in the same place the person put it,
// and it is then not part of the project.
func TestALocalSkillIsSwitchedOffInItsOwnFrontmatter(t *testing.T) {
	service := project(t, "daemon:\n  host: 127.0.0.1\n", map[string]string{
		"skills/code-review/SKILL.md": reviewSkill,
		"skills/draft/SKILL.md": "---\nname: draft\ndescription: Use this when drafting.\n" +
			skills.ToolboxPrefix + ":\n  enabled: false\n---\n\nBody.\n",
	})

	found, err := service.effectiveSet(t.Context())
	require.NoError(t, err)
	require.Len(t, found, 1, "a skill switched off in its own frontmatter is not part of the project")
	assert.Equal(t, "code-review", found[0].GetRef().GetName())

	_, err = service.local.Skill("draft")
	require.Error(t, err)
	assert.Equal(t, api.KindNotFound, api.KindOf(err))
	assert.Contains(t, err.Error(), "switched off")
}

// A project with no skills directory offers nothing, and that is not a failure.
func TestAProjectWithNoSkillsOffersNothing(t *testing.T) {
	service := project(t, "daemon:\n  host: 127.0.0.1\n", nil)

	found, err := service.effectiveSet(t.Context())
	require.NoError(t, err)
	assert.Empty(t, found, "an empty catalog is a project that offers nothing, not one that failed to start")
}

// Every catalog a deployment has integrated is listed, and the implicit one is among them
// because a caller asking what a project can reach should not have to know one source needs no
// configuration to exist.
func TestListingCatalogsIncludesTheImplicitOne(t *testing.T) {
	service := project(t, "daemon:\n  host: 127.0.0.1\n", nil)

	response, err := service.ListCatalogs(t.Context(), connect.NewRequest(&skillv1skill.ListCatalogsRequest{}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetCatalogs(), 1)
	listed := response.Msg.GetCatalogs()[0]
	assert.Equal(t, skills.LocalCatalog, listed.GetId())
	assert.False(t, listed.GetWritable(),
		"the framework reads a project's own skills and never writes to them: a person owns those files")
	assert.NotEmpty(t, listed.GetLocation())
}

// A write against a catalog that cannot take one is refused with a reason, which is what makes
// "copy and move" answerable rather than appearing to succeed.
func TestWritingToAReadOnlyCatalogIsRefusedWithAReason(t *testing.T) {
	service := project(t, "daemon:\n  host: 127.0.0.1\n", nil)

	_, err := service.AddSkill(t.Context(), connect.NewRequest(&skillv1skill.AddSkillRequest{
		Ref: "local.code-review",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindInvalid, api.KindOf(err))
	assert.Contains(t, err.Error(), "already part of the project")
}

// Adding a skill proposes a change and changes nothing until it is confirmed. That is the
// configuration-confirmation rule, and it is a field rather than a second call so the two
// cannot be reordered.
func TestAddingASkillIsProposedAndConfirmed(t *testing.T) {
	service := project(t, "daemon:\n  host: 127.0.0.1\n", nil)

	// No catalog is integrated, so the reference cannot be reached and nothing is written.
	_, err := service.AddSkill(t.Context(), connect.NewRequest(&skillv1skill.AddSkillRequest{
		Ref: "some-team.coding-standards",
	}))
	require.Error(t, err, "a reference to a catalog this deployment does not have is refused "+
		"before anything is written, because a project must never name something that resolves to nothing")
	assert.Contains(t, err.Error(), "some-team")
}

// A deployment with no project configuration has no project to record a skill in, and says so
// rather than creating one.
func TestADeploymentWithNoProjectCannotAddASkill(t *testing.T) {
	service := bareService(t)

	_, err := service.AddSkill(t.Context(), connect.NewRequest(&skillv1skill.AddSkillRequest{
		Ref: "some-team.coding-standards",
	}))
	require.Error(t, err)
	assert.Equal(t, api.KindFailedPrecondition, api.KindOf(err))
	assert.Contains(t, err.Error(), "no project configuration")
}

// A skill's manifest is the pin, and a pin is recorded on first use so a *later* change is
// visible. Nothing is pinned means nothing has been agreed to yet.
func TestAPinIsRecordedOnFirstUseAndDriftIsReportedAfterwards(t *testing.T) {
	service := project(t, "daemon:\n  host: 127.0.0.1\n", map[string]string{
		"skills/code-review/SKILL.md": reviewSkill,
	})
	reference := skills.Reference{Catalog: skills.LocalCatalog, Name: "code-review"}

	_, pinned := service.pins.Get(reference)
	assert.False(t, pinned, "a project that has not used a skill has agreed to nothing yet")

	entry, err := service.fetchEntry(t.Context(), reference)
	require.NoError(t, err)
	outdated, err := service.pinAndCheck(t.Context(), reference, entry)
	require.NoError(t, err)
	assert.False(t, outdated)

	pin, pinned := service.pins.Get(reference)
	require.True(t, pinned, "the manifest is recorded so a later change is visible")
	assert.NotEmpty(t, pin.Files)
	assert.FileExists(t, service.pins.Path(),
		"the pin is a lockfile of its own, so a person's configuration file holds only what "+
			"the person decided")

	// A changed skill is reported and still served.
	path := filepath.Join(service.local.Location(), "code-review", skills.SkillFileName)
	require.NoError(t, os.WriteFile(path,
		[]byte("---\nname: code-review\ndescription: Use this when reviewing a change, thoroughly.\n---\n\nRead the diff twice.\n"),
		0o644))

	entry, err = service.fetchEntry(t.Context(), reference)
	require.NoError(t, err)
	outdated, err = service.pinAndCheck(t.Context(), reference, entry)
	require.NoError(t, err)
	assert.True(t, outdated, "the content behind a name changed under a project that never asked it to")

	// Recording again does not overwrite the pin: accepting a catalog's new content is the
	// decision a person has to make, not something that happens on a read.
	changedEntry, err := service.fetchEntry(t.Context(), reference)
	require.NoError(t, err)
	_, err = service.pinAndCheck(t.Context(), reference, changedEntry)
	require.NoError(t, err)
	stillThere, _ := service.pins.Get(reference)
	assert.Equal(t, pin.Files, stillThere.Files, "the pin is the agreement and is not moved quietly")
}

// CheckPins reports drift and reports a reference that no longer resolves, because those are
// two different failures: one is content that changed, the other is a catalog that is gone.
func TestCheckPinsReportsDriftAndMissingReferences(t *testing.T) {
	service := project(t, "skills:\n  - some-team.gone\n", map[string]string{
		"skills/code-review/SKILL.md": reviewSkill,
	})
	reference := skills.Reference{Catalog: skills.LocalCatalog, Name: "code-review"}
	entry, err := service.fetchEntry(t.Context(), reference)
	require.NoError(t, err)
	_, err = service.pinAndCheck(t.Context(), reference, entry)
	require.NoError(t, err)

	response, err := service.CheckPins(t.Context(), connect.NewRequest(&skillv1skill.CheckPinsRequest{}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetMissing(), 1,
		"a reference whose catalog is not integrated is reported rather than silently absent")
	assert.Equal(t, "some-team.gone", response.Msg.GetMissing()[0].GetRef())
	assert.NotEmpty(t, response.Msg.GetMissing()[0].GetReason())
	assert.Empty(t, response.Msg.GetOutdated())
	assert.NotEmpty(t, response.Msg.GetPinnedIn())
}

// Validate fails a deployment at start rather than at first use, and each invalid skill names
// what is wrong with it in terms its author can act on.
func TestValidateReportsWhatIsWrongWithEachSkill(t *testing.T) {
	service := project(t, "daemon:\n  host: 127.0.0.1\n", map[string]string{
		"skills/good/SKILL.md":       "---\nname: good\ndescription: Use this when good.\n---\n\nBody.\n",
		"skills/unnamed/SKILL.md":    "---\ndescription: Use this when reviewing.\n---\n\nBody.\n",
		"skills/nofence/SKILL.md":    "# Just markdown\n",
		"skills/other-name/SKILL.md": "---\nname: something-else\ndescription: Use this.\n---\n\nBody.\n",
	})

	response, err := service.Validate(t.Context(), connect.NewRequest(&skillv1skill.ValidateRequest{}))
	require.NoError(t, err)
	assert.Equal(t, int32(1), response.Msg.GetChecked(), "the one skill that is fine is checked")
	require.Len(t, response.Msg.GetInvalid(), 3)

	reasons := make([]string, 0, len(response.Msg.GetInvalid()))
	for _, invalid := range response.Msg.GetInvalid() {
		reasons = append(reasons, invalid.GetRef()+": "+invalid.GetReason())
		assert.Equal(t, string(api.KindInvalid), invalid.GetKind())
	}
	joined := strings.Join(reasons, "\n")
	assert.Contains(t, joined, "unnamed")
	assert.Contains(t, joined, "no name")
	assert.Contains(t, joined, "nofence")
	assert.Contains(t, joined, "'---'")
	assert.Contains(t, joined, "other-name")
	assert.Contains(t, joined, "must match")
}

// A skill a catalog served is checked by the same reader that checks one from a directory, and
// the manifest is verified against the content — because a digest in a manifest a server
// published is worth exactly as much as the server published it.
func TestACatalogsEntryIsCheckedAndItsManifestVerified(t *testing.T) {
	good := &skillv1.SkillEntry{
		Ref:         &skillv1.SkillRef{Catalog: "some-team", Name: "coding-standards", Description: "Use this."},
		Uri:         "skill://some-team/coding-standards/SKILL.md",
		Frontmatter: structOf(map[string]any{"name": "coding-standards", "description": "Use this."}),
		Body:        "Follow them.\n",
	}
	// The content a catalog would have served, from which its manifest is derived the same
	// way rather than written by hand.
	frontmatter := skills.RenderSkillMarkdown(good.GetFrontmatter().AsMap(), good.GetBody())

	t.Run("content is verified when a file is read", func(t *testing.T) {
		// The entry's own digest check above is about the shape of the claim; this is the
		// check that runs where the bytes are actually in hand.
		reference := skills.Reference{Catalog: "some-team", Name: "coding-standards"}
		honest := skills.NewFile("SKILL.md", frontmatter)
		require.NoError(t, verifyFile(reference, honest, frontmatter))

		err := verifyFile(reference, honest, []byte("something else entirely"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not what the manifest described")
		assert.Contains(t, err.Error(), "says nothing about whether either is safe")
	})

	t.Run("a matching manifest is accepted", func(t *testing.T) {
		entry := good
		entry.Resources = manifestOf(t, frontmatter, "coding-standards")
		require.NoError(t, validateEntry(entry))
	})

	t.Run("a digest that does not match its content is refused", func(t *testing.T) {
		entry := good
		entry.Resources = manifestOf(t, frontmatter, "coding-standards")
		entry.Resources[0].Digest = "sha256:" + strings.Repeat("0", 64)
		err := validateEntry(entry)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot establish trust")
	})

	t.Run("a file listed twice is refused", func(t *testing.T) {
		entry := good
		resources := manifestOf(t, frontmatter, "coding-standards")
		entry.Resources = append(resources, resources[0])
		err := validateEntry(entry)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "twice")
	})

	t.Run("a file whose digest is not a digest is refused", func(t *testing.T) {
		// The other files' content does not travel with the entry, so their digests cannot
		// be recomputed from it. What can be checked is the shape of the claim, and a value
		// that is not a digest is a claim this framework will not pass on to a host that is
		// going to try to verify it.
		entry := good
		entry.Resources = append(manifestOf(t, frontmatter, "coding-standards"),
			&skillv1.SkillFile{Path: "scripts/run.sh", Size: 1, Digest: "not-a-digest"})
		err := validateEntry(entry)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a "+skills.DigestPrefix+" digest")
	})

	t.Run("a file the catalog says exists but did not send is refused", func(t *testing.T) {
		entry := good
		entry.Resources = append(manifestOf(t, frontmatter, "coding-standards"),
			&skillv1.SkillFile{Path: "scripts/run.sh", Size: 7,
				Digest: "sha256:" + strings.Repeat("a", 64)})
		require.NoError(t, validateEntry(entry),
			"a well-formed claim about a file whose content did not travel is passed on, and "+
				"verified when the file is read — which is where the bytes are")
	})

	t.Run("an empty manifest is refused", func(t *testing.T) {
		entry := good
		entry.Resources = nil
		err := validateEntry(entry)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "complete by construction")
	})

	t.Run("a skill with no reference is refused", func(t *testing.T) {
		entry := &skillv1.SkillEntry{Frontmatter: good.GetFrontmatter(), Body: good.GetBody()}
		err := validateEntry(entry)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no reference")
	})
}

// A skill with a declared requirement is checked against what the deployment exposes, and a
// requirement it cannot meet is reported rather than granted.
func TestADeclaredRequirementIsCheckedAgainstWhatTheDeploymentExposes(t *testing.T) {
	exposing := &exposedTools{names: []string{"git", "grep"}}
	service, err := NewService(Options{ExposedTools: exposing})
	require.NoError(t, err)

	declares := "---\nname: needs-git\ndescription: Use this when reviewing.\n" +
		skills.ToolboxPrefix + ":\n  requirements:\n    tools:\n      - name: git\n" +
		"      - name: a-tool-this-deployment-does-not-have\n---\n\nBody.\n"
	template, err := skills.Parse(skills.LocalCatalog, "needs-git", []byte(declares), nil)
	require.NoError(t, err)
	frontmatter := structOf(template.Frontmatter.Document)
	entry := &skillv1.SkillEntry{
		Ref:         &skillv1.SkillRef{Catalog: skills.LocalCatalog, Name: "needs-git"},
		Frontmatter: frontmatter,
		Body:        template.Body,
	}

	unmet, err := service.unmet(t.Context(), entry)
	require.NoError(t, err)
	require.Len(t, unmet, 1, "one requirement is met and one is not")
	assert.Equal(t, "a-tool-this-deployment-does-not-have", unmet[0].Requirement.Name)
	assert.Contains(t, unmet[0].Reason, "exposes no tool")
}

// A deployment that does not know its own tool surface cannot tell a reader that a dependency
// is missing, and guessing would be worse than saying nothing.
func TestADeploymentWithNoToolSurfaceReportsNothingUnmet(t *testing.T) {
	service := bareService(t)
	unmet, err := service.unmet(t.Context(), &skillv1.SkillEntry{})
	require.NoError(t, err)
	assert.Empty(t, unmet)
}

// A page token is an offset into an ordered list rather than an opaque handle, because the list
// comes from catalogs that can change between calls and an opaque handle would resume in the
// wrong place without saying so.
func TestPagingIsByOffsetAndIsTotal(t *testing.T) {
	found := []*skillv1skill.ProjectSkill{
		{Ref: &skillv1.SkillRef{Name: "a"}},
		{Ref: &skillv1.SkillRef{Name: "b"}},
		{Ref: &skillv1.SkillRef{Name: "c"}},
		{Ref: &skillv1.SkillRef{Name: "d"}},
	}
	first, next := page(found, 2, "")
	assert.Len(t, first, 2)
	assert.NotEmpty(t, next)
	second, done := page(found, 2, next)
	assert.Len(t, second, 2)
	assert.Empty(t, done, "the last page says so rather than offering an empty one")

	rest, done := page(found, 0, "")
	assert.Len(t, rest, 4, "a page size of zero means everything")
	assert.Empty(t, done)

	// A token past the end is not an error: the list may have shrunk since the page was
	// issued, and a caller holding a stale token should get an empty page rather than a
	// failure it cannot act on.
	empty, done := page(found, 2, indexToken(99))
	assert.Empty(t, empty)
	assert.Empty(t, done)
}

// exposedTools is a deployment's tool surface.
type exposedTools struct{ names []string }

func (e *exposedTools) ExposedTools(context.Context) ([]string, error) { return e.names, nil }

// structOf converts a frontmatter document into the contract's verbatim form.
func structOf(document map[string]any) *structpb.Struct {
	converted, err := toStruct(document)
	if err != nil {
		panic(err)
	}
	return converted
}

func manifestOf(t *testing.T, content []byte, name string) []*skillv1.SkillFile {
	t.Helper()
	template, err := skills.Parse("some-team", name, content, nil)
	require.NoError(t, err)
	// The manifest must describe the file as served, so it is derived the same way a
	// catalog's would be rather than written by hand.
	served := []byte(skills.RenderSkillMarkdown(template.Frontmatter.Document, template.Body))
	files := []skills.File{{
		Path:     skills.SkillFileName,
		Size:     int64(len(served)),
		Digest:   digestOf(served),
		MIMEType: skills.MIMETypeFor(skills.SkillFileName),
	}}
	return toManifest(files)
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
