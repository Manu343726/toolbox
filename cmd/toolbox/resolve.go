package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/pkg/host"
)

// The main CLI generates a command per operation, and a command resolves its service before
// it can describe or call it. Two things make that possible here, and both are deliberate
// choices rather than conveniences.

// hostResolver resolves a service to the subsystem in this process that serves it.
//
// The host is consulted when a call is made rather than when the command tree is built,
// because a command's flags have to exist before anything starts. So the tree is built from
// the contracts a deployment would serve, and the endpoint is looked up once there is a
// subsystem to have an endpoint.
type hostResolver struct {
	host *host.Host
	// fallback resolves a service this process does not serve. It is the leg of the chain
	// that answers for a subsystem a core elsewhere started, so a command reaches a peer
	// without this process hosting it.
	fallback core.Resolver
}

// Resolve returns the endpoint serving a service: this process first, because a subsystem
// this process started needs no network hop and is authoritative about itself, and the
// configured core second for anything this process does not serve.
//
// A service this process does not serve is reported as core.ErrNotFound rather than as a
// plain error, because that is the chain's word for "nobody here has it" and a leg that
// said anything else would be read as a leg that could not be consulted. The distinction
// decides whether the chain tries its next leg or stops and reports.
func (r hostResolver) Resolve(ctx context.Context, serviceName string) (core.Endpoint, error) {
	endpoint, err := r.local(serviceName)
	if err == nil {
		return endpoint, nil
	}
	if r.fallback == nil {
		return core.Endpoint{}, err
	}
	fallback, fallbackErr := r.fallback.Resolve(ctx, serviceName)
	if fallbackErr != nil {
		// Neither leg had it. Both answers are reported, because a caller who typed a
		// wrong name and a caller whose deployment is incomplete need different things
		// done, and the message is the only place that can tell them apart.
		return core.Endpoint{}, fmt.Errorf("%w (this process: %v; the core: %v)",
			core.ErrNotFound, err, fallbackErr)
	}
	return fallback, nil
}

// local resolves against the subsystems in this process.
func (r hostResolver) local(serviceName string) (core.Endpoint, error) {
	servers := r.host.Servers()
	if len(servers) == 0 {
		return core.Endpoint{}, fmt.Errorf("%w: no subsystem is running", core.ErrNotFound)
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		server := servers[name]
		for _, service := range server.Services() {
			if service.Name != serviceName {
				continue
			}
			return core.Endpoint{
				Name:         name,
				URL:          server.Endpoint(),
				ServiceNames: []string{serviceName},
			}, nil
		}
	}
	return core.Endpoint{}, fmt.Errorf("%w: this process runs %s",
		core.ErrNotFound, strings.Join(names, ", "))
}
