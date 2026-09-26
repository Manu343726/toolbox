package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SkillsExtension is the identifier the MCP Skills extension is declared under.
const SkillsExtension = "io.modelcontextprotocol/skills"

// SkillsSource is what the gateway serves skills from.
//
// It is an interface rather than a concrete subsystem so that `pkg/mcp` keeps no dependency on
// the subsystem that aggregates catalogs: the gateway knows the *protocol* and the subsystem
// knows the *content*. A deployment that serves no skills passes none, and the extension is not
// declared at all — declaring an extension a server does not implement would be a claim it
// cannot keep, and a client that reads declarations the spec-correct way would look for
// methods that answer "not found".
type SkillsSource interface {
	// ListSkills returns the skills a project may use, each already projected for the client
	// that is asking.
	ListSkills(ctx context.Context, client skills.Client) ([]skills.Entry, error)

	// GetSkill returns one skill, projected for one client.
	GetSkill(ctx context.Context, client skills.Client, catalog, name string) (skills.Entry, error)

	// ReadSkillFile returns one file of a skill, with the digest and size its manifest gave
	// for it, so the caller can verify what it fetched.
	//
	// It takes the client because a skill's *own* document is served as the projection rather
	// than as the template: the entry a client fetched it under describes the projection, and a
	// file still carrying this framework's own frontmatter properties would disagree with the
	// entry in the client's hand.
	ReadSkillFile(ctx context.Context, client skills.Client, catalog, name, path string) ([]byte, skills.File, error)
}

// DirectoryRead is the extension's setting for directory reads, and it is false.
//
// The specification positions a directory read as useful for a skill whose content is generated
// such that no stable set of files can be published. A skill read from a directory has its
// files, their digests are computable, and a client holding an entry can already enumerate
// everything — so the setting is a way of saying "nothing here needs it", and saying so is
// better than declaring a capability a catalog of files does not have.
const DirectoryRead = false

// The extension's two required methods.
const (
	methodListSkills = "skills/list"
	methodGetSkill   = "skills/get"
)

// listSkillsParams is `skills/list`'s parameters. They are empty and the type exists only
// because the SDK's custom-method registration is generic over a params type — a request with
// nothing in it still has to be a request with a declared shape.
type listSkillsParams struct {
	sdkmcp.ParamsBase
}

// listSkillsResult is `skills/list`'s result.
//
// The entry shape is the specification's, and the field names are written out rather than
// marshalled from a Go struct so that a change to this package's internals cannot change what
// a client sees.
type listSkillsResult struct {
	sdkmcp.ResultBase
	// Skills is every skill this server serves.
	//
	// It is empty rather than absent when a deployment serves none: an empty list is how the
	// specification says a server serves no skills, and a client cannot tell that from a
	// failure it would have to retry.
	Skills []skillsEntry `json:"skills"`
}

// getSkillParams is `skills/get`'s parameters.
type getSkillParams struct {
	sdkmcp.ParamsBase
	// URI is the resource URI of the skill's SKILL.md.
	URI string `json:"uri"`
}

// getSkillResult is `skills/get`'s result: one entry, the same shape a listing returns.
type getSkillResult struct {
	sdkmcp.ResultBase
	// Skill is the entry for the requested skill.
	Skill skillsEntry `json:"skill"`
}

// skillsEntry is the extension's entry shape, which is the same for `skills/list` and
// `skills/get`.
type skillsEntry struct {
	// URI is the resource URI of the SKILL.md.
	URI string `json:"uri"`
	// Frontmatter is the frontmatter as a JSON object, carrying **every** field the author
	// wrote rather than a curated subset.
	//
	// A host builds its skill registry from these entries alone, without fetching each
	// SKILL.md, so a field dropped here is a field no client could ever see. It is why
	// `allowed-tools` is the only field that never reaches a client: it is not dropped here
	// because it is dropped on the way in, by the projection, for a reason recorded where that
	// decision is made.
	Frontmatter map[string]any `json:"frontmatter"`
	// Resources is the complete manifest, each file with its digest and byte length.
	//
	// Complete by construction, which is the specification's requirement: a file is never
	// dropped from a skill to suit a client.
	Resources []skillsResource `json:"resources"`
	// Unmet names requirements this deployment cannot provide, so a host and a reader can see
	// that a dependency is missing rather than inferring that nothing was declared.
	Unmet []string `json:"unmet,omitempty"`
	// Outdated is whether the manifest differs from the one the project pinned.
	Outdated bool `json:"outdated,omitempty"`
}

// skillsResource is one file in a served manifest.
type skillsResource struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// installSkills declares the Skills extension and serves its methods.
//
// No released Go SDK implements this extension, so it is built on the SDK's own extension
// points rather than on a fork or on an unmerged pull request:
//
//   - `AddReceivingCustomMethod` for `skills/list` and `skills/get`. Receiving *middleware*
//     cannot do this: the SDK checks the method against its table in `checkRequest` before the
//     middleware chain runs, and a method it does not know is refused there. Custom methods
//     are the SDK's supported way to serve a method it does not know, and they go through the
//     same middleware chain a standard method does.
//   - a `skill://` resource template for `resources/read`, since the SDK matches URI templates
//     as well as exact URIs and every file of a skill is then an ordinary MCP resource.
//   - `ServerCapabilities.AddExtension`, declared at construction because capabilities are fixed
//     when the server is created, which is where protocol revision 2026-07-28 says extensions
//     are declared: in the `extensions` field of the `server/discover` response.
//
// The declaration is what makes the rest honest. A server that declares the extension must
// implement the two methods, and a server that implements them without declaring it would have
// a client that never looks.
func (s *Server) installSkills(source SkillsSource) error {
	if source == nil {
		return nil
	}
	s.skills = source

	// Every file of a skill is an ordinary MCP resource under a `skill://` URI, read with the
	// standard `resources/read`.
	//
	// The path is a *reserved* expansion (`{+path}`) because a skill's files are nested —
	// `references/check.md` is two segments — and a plain `{path}` matches one segment
	// only, so every skill with a supporting file would be unreachable. The URI is parsed
	// here rather than by the template matcher, because matching decides *whether* a URI is
	// one of ours and parsing decides what it names, and only the second should be trusted
	// to produce a catalog and a name.
	s.sdk.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		URITemplate: "skill://{catalog}/{name}/{+path}",
		Name:        "toolbox-skill-file",
		Description: "One file of an agent skill, served as it is to the client asking.",
	}, s.readSkillResource)

	if err := sdkmcp.AddReceivingCustomMethod(s.sdk, methodListSkills,
		func(ctx context.Context, _ *sdkmcp.ServerSession, params *listSkillsParams) (*listSkillsResult, error) {
			return s.listSkills(ctx, params)
		}); err != nil {
		return fmt.Errorf("registering %s: %w", methodListSkills, err)
	}
	return sdkmcp.AddReceivingCustomMethod(s.sdk, methodGetSkill,
		func(ctx context.Context, _ *sdkmcp.ServerSession, params *getSkillParams) (*getSkillResult, error) {
			return s.getSkill(ctx, params)
		})
}

// listSkills answers `skills/list`.
//
// Every skill is returned, and each is projected for the client that is asking. A client never
// fails to see a skill because of what it can do — it receives the skill in the form it can act
// on — which is why this is not filtered by what the client supports.
func (s *Server) listSkills(ctx context.Context, params *listSkillsParams) (*listSkillsResult, error) {
	entries, err := s.skills.ListSkills(ctx, clientOf(params.GetMeta()))
	if err != nil {
		return nil, skillsFailure(err)
	}
	listed := make([]skillsEntry, 0, len(entries))
	for _, entry := range entries {
		listed = append(listed, serveEntry(entry))
	}
	return &listSkillsResult{Skills: listed}, nil
}

// getSkill answers `skills/get` for the URI of one skill's SKILL.md.
//
// The URI is the only thing a client sends, and one that names no skill this server serves is
// refused rather than guessed at: the specification's code for that is -32602, and a server
// that resolved it to *some* skill would be serving instructions its reader did not ask for,
// with the mistake invisible until a model had followed them.
func (s *Server) getSkill(ctx context.Context, params *getSkillParams) (*getSkillResult, error) {
	catalog, name, err := skills.ParseSkillURI(params.URI)
	if err != nil {
		return nil, invalidParams(err.Error(), err)
	}
	entry, err := s.skills.GetSkill(ctx, clientOf(params.GetMeta()), catalog, name)
	if err != nil {
		return nil, skillsFailure(err)
	}
	return &getSkillResult{Skill: serveEntry(entry)}, nil
}

// readSkillResource serves `resources/read` for one file of a skill.
//
// The bytes are verified against the manifest entry they are being served under, so a file
// whose content is not what the entry described is refused rather than sent. The specification
// has the *host* verify what it fetched; a server that verifies first can refuse to serve at
// all, which is a better outcome than a host discovering it.
//
// What a digest is not is a trust anchor, and this does not make it one. It confirms that the
// entry and the bytes agree, and it cannot establish that a skill is safe, cannot defend
// against the catalog itself, and cannot detect an intermediary that rewrote both together.
func (s *Server) readSkillResource(
	ctx context.Context, request *sdkmcp.ReadResourceRequest,
) (*sdkmcp.ReadResourceResult, error) {
	catalog, name, relative, err := skills.ParseResourceURI(request.Params.URI)
	if err != nil {
		return nil, invalidParams(err.Error(), err)
	}
	content, entry, err := s.skills.ReadSkillFile(
		ctx, clientOf(request.Params.GetMeta()), catalog, name, relative)
	if err != nil {
		return nil, skillsFailure(err)
	}
	actual := skills.NewFile(relative, content)
	if actual.Digest != entry.Digest {
		return nil, fmt.Errorf(
			"the file %s of %s is %d bytes hashing to %s, and its manifest says %d bytes "+
				"hashing to %s: the content is not what the manifest described, so it is not "+
				"served",
			relative, catalog+"."+name, actual.Size, actual.Digest, entry.Size, entry.Digest)
	}
	mimeType := entry.MIMEType
	if mimeType == "" {
		mimeType = actual.MIMEType
	}
	return &sdkmcp.ReadResourceResult{
		Contents: []*sdkmcp.ResourceContents{{
			URI:      request.Params.URI,
			MIMEType: mimeType,
			Text:     string(content),
		}},
	}, nil
}

func serveEntry(entry skills.Entry) skillsEntry {
	manifest := make([]skillsResource, 0, len(entry.Files))
	for _, file := range entry.Files {
		manifest = append(manifest, skillsResource{
			URI:    entry.ResourceURI(file.Path),
			Digest: file.Digest,
			Size:   file.Size,
		})
	}
	served := skillsEntry{
		URI:         entry.URI,
		Frontmatter: entry.Frontmatter,
		Resources:   manifest,
		Outdated:    entry.Outdated,
	}
	for _, requirement := range entry.Unmet {
		served.Unmet = append(served.Unmet, requirement.Name)
	}
	return served
}

// clientOf reads which client is asking from a request's metadata.
//
// Under protocol revision 2026-07-28 there is no connection to ask: the revision has no
// `initialize` handshake, so every request carries `clientInfo` in its own `_meta` and the SDK
// leaves `ServerSession.InitializeParams` nil precisely because there is no single answer. So
// this is not a workaround for a stateless transport — it is where the information is.
//
// Reading it from anywhere else would be reading configuration the client never wrote, and a
// projection that guessed wrong hands a client content it cannot use with nothing in the result
// to reveal the guess. A client that said nothing gets the common denominator rather than an
// error: a client this framework has never heard of is still served, in the form every client
// understands.
func clientOf(meta map[string]any) skills.Client {
	raw, found := meta[sdkmcp.MetaKeyClientInfo]
	if !found {
		return skills.Client{}
	}
	information, ok := raw.(map[string]any)
	if !ok {
		return skills.Client{}
	}
	client := skills.Client{}
	if name, ok := information["name"].(string); ok {
		client.Name = name
	}
	if version, ok := information["version"].(string); ok {
		client.Version = version
	}
	return client
}

// skillsFailure gives a failure the extension's own error code.
//
// The specification's table for this extension says a URI identifying no skill, and a resource
// this server does not serve, are both -32602 — which is JSON-RPC's *invalid params*. That is
// not a quirk: a URI is the parameter, and a parameter naming something the server does not have
// is an invalid one. A failure that reached the client as a bare error would carry code 0,
// which no client can act on.
//
// Anything else keeps its own classification, so a deployment's unavailability is reported as
// unavailability rather than as a caller's mistake.
func skillsFailure(err error) error {
	if err == nil {
		return nil
	}
	// The kind is read through the transport, because the failure arrived over ConnectRPC
	// from the subsystem and carries a code rather than this package's classification.
	if code, found := skillsErrorCodes[api.ConnectKind(err)]; found {
		return &jsonrpc.Error{Code: code, Message: err.Error()}
	}
	return err
}

// skillsErrorCodes maps this framework's classifications onto the extension's codes.
var skillsErrorCodes = map[api.ErrorKind]int64{
	api.KindInvalid:  jsonrpc.CodeInvalidParams,
	api.KindNotFound: jsonrpc.CodeInvalidParams,
}

// invalidParams is the error a malformed request gets.
//
// -32602 is the specification's own code for a URI that identifies no skill this server serves,
// and it is a JSON-RPC code rather than a ConnectRPC one — so it is returned as the public
// jsonrpc.Error rather than borrowed from a transport that speaks a different one.
func invalidParams(message string, cause error) error {
	if cause != nil && !strings.Contains(message, cause.Error()) {
		message = message + ": " + cause.Error()
	}
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: message}
}
