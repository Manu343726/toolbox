// Package skill aggregates every source of agent skills a deployment has into one surface, and
// serves that surface to the Model Context Protocol endpoint.
//
// A skill is a body of instructions for carrying out a task, written once and served to an
// agent that needs it. It is a *domain resource* like a workflow or a prompt, and what makes it
// different is that its content is not a field in a message: a skill is a directory of files,
// and this subsystem has to know what its files are, what they hash to, and which client it is
// serving them to.
//
// # What this subsystem is
//
// The **aggregator**. It holds the catalogs a deployment has integrated, resolves a qualified
// reference to the one that owns it, and presents every catalog's skills as one surface, so a
// project can reach a skill from any integrated catalog without knowing which subsystem provides
// it.
//
// That is the same shape as the API layer's provider resolution, and for the same reason:
// several implementations of one contract, selected at the point of use, with a deployment
// deciding which are present. The consequence to carry through is the one that applies to every
// provider here — resolution is by **identifier**, not by contract name, because several
// providers serve the same contract on purpose.
//
// # Three things it does, and why one subsystem does them
//
//  1. **Aggregates.** It resolves `<catalog>.<name>` to the catalog that owns it.
//  2. **Serves over MCP.** It is the deployment's `io.modelcontextprotocol/skills` endpoint, so a
//     skill reaches an agent through the same client that already sees the deployment's tools.
//  3. **Offers the tools that change what a project has.** It is how an agent finds a skill, adds
//     one, enables one and disables one.
//
// # What it is not
//
// It is not a store of skills. The catalogs own storage — a directory, a checkout, a download —
// and this subsystem never writes to one. Its only write is to a **project's configuration
// file**, adding a reference to the project's `skills:` list, and that write is proposed before
// it is made because a person writes and reviews that file. See AGENTS.md rule 17 and
// docs/skills.md.
//
// The versioned in-memory catalogue of skill metadata this subsystem used to hold is gone: it
// had no notion of a catalog, of a skill directory, of files or of digests, and none of those
// are things this subsystem can do without.
package skill

import (
	"context"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	catalogv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1/skillv1connect"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	"github.com/Manu343726/toolbox/subsystems/skill/skillv1/skillv1connect"
)

const (
	// Name is the stable subsystem name.
	Name = "skill"
	// Version is the reference implementation version.
	Version = "0.1.0"
	// Description is what a catalog reports this subsystem as.
	Description = "Aggregates agent skills from every integrated catalog and serves them over " +
		"the Model Context Protocol skills extension."
)

// Options configures the skills subsystem.
type Options struct {
	// Directory finds the catalogs available in the deployment.
	//
	// A deployment with no integrated catalogs beyond the implicit local one is a working
	// deployment, so this may be nil — and then only the project's own skills are served.
	Directory CatalogDirectory
	// ProjectDir is the project's `.toolbox` directory: where the local catalog and the
	// project's `skills:` list live.
	//
	// It is a directory rather than a configuration file because a person may configure a
	// project before its skills directory exists, and a local catalog with no directory is a
	// project that offers nothing rather than a project that failed to start.
	ProjectDir string
	// Config is the resolved project configuration, from which the `skills:` list is read and
	// to which the write tools write. It may be nil for a deployment serving only a
	// deployment's own configuration, in which case the project's skills are the local ones.
	Config ConfigSource
	// ExposedTools reports which tools the deployment exposes, so a skill's declared
	// requirements can be checked rather than granted. It may be nil, in which case no
	// requirement is reported unmet — a deployment that does not know its own surface cannot
	// tell a reader that a dependency is missing, and guessing would be worse.
	ExposedTools ExposedTools
	// ListenAddress defaults to 127.0.0.1:0.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// ExposedTools reports the tool names a deployment exposes.
//
// It exists so a skill's declared requirements can be *checked*. A dependency is a statement of
// need, and this framework honours the statement by reporting whether the deployment has what
// the skill says it needs — never by granting it, and never by pretending.
type ExposedTools interface {
	// ExposedTools returns the names of the tools this deployment currently exposes.
	ExposedTools(context.Context) ([]string, error)
}

// Providers describes this subsystem to a catalog's provider directory.
//
// It implements no provider contract. A catalog is a *source* of skills, and this subsystem
// aggregates sources rather than being one: a deployment that reached its own project directory
// through the provider mechanism would be a deployment that had to have a subsystem running in
// order to read the skills a person put in their project.
func Providers(string) []api.Provider { return nil }

// New builds the subsystem's server.
func New(options Options) (*subsystem.Server, error) {
	service, err := NewService(options)
	if err != nil {
		return nil, err
	}
	path, handler := skillv1connect.NewSkillServiceHandler(service)
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       service.version,
		Description:   Description,
		ListenAddress: options.ListenAddress,
		Services: []subsystem.Service{{
			Name:    skillv1connect.SkillServiceName,
			Path:    path,
			Handler: handler,
		}},
	})
}

// ServiceName is the contract this subsystem serves.
const ServiceName = skillv1connect.SkillServiceName

// CatalogServiceName is the contract a catalog provider serves, and this subsystem resolves to
// rather than serving it.
const CatalogServiceName = catalogv1.SkillCatalogServiceName

// ProviderRole is the role a catalog provider plays, re-exported so a deployment registering one
// does not have to import the root package to learn the role's name.
const ProviderRole = skills.ProviderRole
