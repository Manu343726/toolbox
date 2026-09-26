package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Manu343726/toolbox/pkg/config"

	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/host"
	toolboxmcp "github.com/Manu343726/toolbox/pkg/mcp"
	"github.com/Manu343726/toolbox/subsystems/skill"
)

// skillsSource binds the skills service and returns what the gateway's Skills extension is
// served from.
//
// The skills subsystem is reached over ConnectRPC like every other subsystem: the gateway
// depends on a shape it declares, and the composition root is what wires the two together. That
// is what lets a deployment leave the skills subsystem out entirely — a gateway with no skills
// source declares no extension, which is a smaller and more honest surface than one that
// declares methods nothing answers.
//
// A failure to bind is reported and returns nil rather than being fatal. A deployment that has
// not started the skills subsystem still serves its tools, and refusing to start would make an
// optional feature a requirement.
func skillsSource(ctx context.Context, h *host.Host) toolboxmcp.SkillsSource {
	servers := h.Servers()
	server, running := servers[skill.Name]
	if !running {
		return nil
	}
	client := core.NewClient(core.ClientOptions{
		Resolver: hostResolver{host: h},
	})
	service, err := core.Bind(ctx, client, skill.ServiceName, skill.NewMCPClient)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"toolbox: the skills contract at %s did not resolve, so no skills are served: %v\n",
			server.Endpoint(), err)
		return nil
	}
	return skill.NewMCPSource(service)
}

// projectDir is the directory a project's own configuration lives in, which is where the local
// catalog and the project's `skills:` list are.
//
// It is derived from the resolved configuration's own path rather than searched for again, so
// the skills a deployment serves come from the same file every other setting came from — and a
// configuration that names no file has no project, which is a deployment offering nothing
// rather than one that failed to start.
func projectDir(resolved config.Config) string {
	if path := strings.TrimSpace(resolved.Path); path != "" {
		return filepath.Dir(path)
	}
	return ""
}

// withSkills attaches the Skills extension to a gateway's options.
//
// One place, so both gateway construction paths add the extension the same way. Two call sites
// would be two things to keep in step, and a gateway that served tools but forgot to declare an
// extension it implemented would have a client that never looked.
func withSkills(ctx context.Context, h *host.Host, options toolboxmcp.Options) toolboxmcp.Options {
	options.Skills = skillsSource(ctx, h)
	return options
}
