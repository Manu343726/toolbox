package logfile

import (
	"context"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/log"
	"github.com/Manu343726/toolbox/pkg/subsystem"
)

// The provider role this subsystem plays.
//
// It is an open identifier by design — a role is a convention, not a closed set — so this
// adds to a vocabulary the framework already has rather than special-casing it. A deployment
// can therefore list which logging backends it has through the same catalog that lists its
// parsers and adapters, and a reader of that listing does not have to know that logging is
// different from anything else.
const Role api.ProviderRole = "loghandler"

// SubsystemOptions configure the subsystem, as distinct from the provider's own settings.
//
// The two are different acts: one is a logging backend a deployment composes in process, and
// the other is a process that can be started, described and health-checked like any other. A
// shared type would let a configuration set a ListenAddress on a file that never listens
// anywhere.
type SubsystemOptions struct {
	// ListenAddress is where the subsystem's own server binds. It serves no operations of its
	// own — a log file is a sink, not a service — so it defaults to loopback and exists so
	// the subsystem is describable and health-checkable like any other.
	ListenAddress string
	// Version overrides the implementation version.
	Version string
}

// New composes the subsystem.
//
// It starts no background work, because a rotating file needs none: lumberjack opens and
// writes on demand and has nothing to drain. That is worth saying rather than leaving a
// reader wondering whether a log file is a service — it is a provider, and this server exists
// only so the provider is declarable.
func New(options SubsystemOptions) (*subsystem.Server, error) {
	version := options.Version
	if version == "" {
		version = Version
	}
	return subsystem.NewServer(subsystem.Config{
		Name:          Name,
		Version:       version,
		Description:   Description,
		ListenAddress: options.ListenAddress,
		Background:    func(ctx context.Context) error { return nil },
	})
}

// Providers declares this subsystem as a logging provider at an endpoint.
//
// The declaration is what makes the backend discoverable: a deployment can ask its catalog
// which logging backends it has, by the same call that lists its parsers and adapters. What
// the declaration does not do is carry the log entries — those are handed to Provider in this
// process, because a network hop per log line would make logging able to fail for reasons
// unrelated to the log.
func Providers(endpoint string) []api.Provider {
	return []api.Provider{{
		ID:                    ProviderID,
		Subsystem:             Name,
		Role:                  Role,
		Endpoint:              endpoint,
		ImplementationVersion: Version,
	}}
}

// LogProvider returns the backend this subsystem provides, for a deployment that composes
// providers in process.
//
// It is the same value the subsystem's declaration names, so a deployment that registered this
// and a deployment that gathered the subsystem both end up with one backend rather than two
// that happen to write the same file.
func LogProvider() log.Provider { return Provider }
