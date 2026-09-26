package skill

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
	skillv1skill "github.com/Manu343726/toolbox/subsystems/skill/skillv1"
	skillclient "github.com/Manu343726/toolbox/subsystems/skill/skillv1/skillv1connect"
)

// MCPService is the contract the gateway's skills source is written against.
//
// It is the same contract this subsystem serves, restated as the three things the gateway asks
// for. The gateway (`pkg/mcp`) cannot import this subsystem — a shared foundation package that
// imported a feature would invert the dependency — so the source is written against a shape it
// declares, and this type satisfies it structurally.
type MCPService = skillclient.SkillServiceClient

// NewMCPClient constructs the typed client, so the composition root binds it with core.Bind
// after resolving the endpoint rather than reaching into the generated package itself.
func NewMCPClient(
	httpClient connect.HTTPClient, baseURL string, options ...connect.ClientOption,
) MCPService {
	return skillclient.NewSkillServiceClient(httpClient, baseURL, options...)
}

// MCPSource serves the gateway's Skills extension from a skills service reached over
// ConnectRPC.
//
// It is the seam between the two halves of the feature, and it is deliberately a *client* of
// this subsystem's own contract rather than a second implementation of it: the gateway asks for
// skills, the subsystem answers over the wire, and the projection happens here because it
// depends on which client is asking — a fact the gateway has and the service call does not.
//
// The projection is the reader's own, not a second one. A template comes back from the service
// as a frontmatter document, a body and a manifest; it is rebuilt into the reader's form and
// distilled by the same function that distills one read from a directory, so a skill served
// through the gateway and a skill served in process are the same artefact.
type MCPSource struct {
	client MCPService
}

// NewMCPSource adapts a skills service client to what the gateway asks for.
func NewMCPSource(client MCPService) *MCPSource { return &MCPSource{client: client} }

// ListSkills implements the gateway's skills source.
func (s *MCPSource) ListSkills(
	ctx context.Context, client skills.Client,
) ([]skills.Entry, error) {
	response, err := s.client.ListSkills(ctx, connect.NewRequest(&skillv1skill.ListSkillsRequest{}))
	if err != nil {
		return nil, err
	}
	entries := make([]skills.Entry, 0, len(response.Msg.GetSkills()))
	for _, listed := range response.Msg.GetSkills() {
		entry, err := s.project(ctx, client, listed.GetRef().GetCatalog(), listed.GetRef().GetName())
		if err != nil {
			// A skill that cannot be read is skipped rather than failing the whole listing.
			// The listing is a listing, and a broken skill should not make every other skill
			// unreachable — a person hears about it from Validate, which exists for this.
			continue
		}
		entry.Outdated = listed.GetOutdated()
		entries = append(entries, entry)
	}
	return entries, nil
}

// GetSkill implements the gateway's skills source.
func (s *MCPSource) GetSkill(
	ctx context.Context, client skills.Client, catalog, name string,
) (skills.Entry, error) {
	return s.project(ctx, client, catalog, name)
}

// ReadSkillFile implements the gateway's skills source.
//
// A skill's own document is served as the *projection*, not as the template, because the entry
// a client fetched it under describes the projection: the served frontmatter has this
// framework's own properties removed, and a file still carrying them would disagree with the
// entry the client is holding. Everything else is served as the catalog holds it, with the
// catalog's own digest — a supporting file is not a document this framework rewrites, so
// recomputing its digest here would be checking the catalog against itself.
func (s *MCPSource) ReadSkillFile(
	ctx context.Context, client skills.Client, catalog, name, path string,
) ([]byte, skills.File, error) {
	if path == skills.SkillFileName {
		entry, err := s.project(ctx, client, catalog, name)
		if err != nil {
			return nil, skills.File{}, err
		}
		served, found := entry.File(skills.SkillFileName)
		if !found {
			return nil, skills.File{}, api.Errorf(api.KindInternal,
				"the skill %s.%s was projected without its own %s", catalog, name, skills.SkillFileName)
		}
		return entry.Content, served, nil
	}
	response, err := s.client.ReadSkillFile(ctx, connect.NewRequest(&skillv1skill.ReadSkillFileRequest{
		Ref:  catalog + "." + name,
		Path: path,
	}))
	if err != nil {
		return nil, skills.File{}, err
	}
	return response.Msg.GetContent(), skills.File{
		Path:     response.Msg.GetPath(),
		Size:     response.Msg.GetSize(),
		Digest:   response.Msg.GetDigest(),
		MIMEType: response.Msg.GetMimeType(),
	}, nil
}

// project reads one skill as a template and distills it for one client.
//
// The pin is recorded by the service when the skill is read, so a *later* change is visible; the
// projection here is what a particular client receives, and its digests describe what this
// server is about to send rather than what the catalog holds.
func (s *MCPSource) project(
	ctx context.Context, client skills.Client, catalog, name string,
) (skills.Entry, error) {
	if strings.TrimSpace(catalog) == "" || strings.TrimSpace(name) == "" {
		return skills.Entry{}, api.Errorf(api.KindInvalid,
			"a skill is identified by the catalog that holds it and its own name")
	}
	response, err := s.client.GetSkill(ctx, connect.NewRequest(&skillv1skill.GetSkillRequest{
		Ref: catalog + "." + name,
	}))
	if err != nil {
		return skills.Entry{}, err
	}
	template, err := templateFrom(response.Msg.GetEntry())
	if err != nil {
		return skills.Entry{}, err
	}
	entry, err := skills.Distill(template, client, skills.DistillOptions{
		Outdated: response.Msg.GetOutdated(),
	})
	if err != nil {
		return skills.Entry{}, err
	}
	// Unmet requirements are checked by the service, which is the only party that knows what
	// the deployment exposes. They are carried onto the projection rather than re-derived,
	// because re-deriving them here would need a view of the deployment's surface that this
	// adapter deliberately does not have.
	entry.Unmet = requirementsFrom(response.Msg.GetUnmet())
	return entry, nil
}

// templateFrom rebuilds the reader's form out of what the service served.
//
// A `SkillEntry` carries a frontmatter document, a body and a manifest, which is everything the
// reader needs — and the document's own digest in the manifest is recomputed from the content
// rather than copied, so the template's manifest is self-consistent before the projection even
// starts. A service that served a manifest contradicting its own content would produce a
// template whose pin does not describe its bytes, and the projection would then serve a digest
// computed from the same wrong source.
func templateFrom(entry *skillv1.SkillEntry) (skills.Template, error) {
	if entry == nil {
		return skills.Template{}, api.Errorf(api.KindInternal,
			"the skills service answered with no skill")
	}
	document := entry.GetFrontmatter().AsMap()
	content := skills.RenderSkillMarkdown(document, entry.GetBody())

	manifest := fromManifest(entry.GetResources())
	for index := range manifest {
		if manifest[index].Path == skills.SkillFileName {
			manifest[index] = skills.NewFile(skills.SkillFileName, content)
		}
	}
	return skills.Parse(entry.GetRef().GetCatalog(), entry.GetRef().GetName(), content, manifest)
}

// requirementsFrom turns the service's report of unmet requirements into the reader's own form.
func requirementsFrom(reported []*skillv1.SkillRef) []skills.ToolRequirement {
	requirements := make([]skills.ToolRequirement, 0, len(reported))
	for _, one := range reported {
		requirements = append(requirements, skills.ToolRequirement{
			Name:        one.GetName(),
			Description: one.GetDescription(),
		})
	}
	return requirements
}
