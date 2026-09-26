package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/Manu343726/toolbox/pkg/core"
	"github.com/Manu343726/toolbox/subsystems/registry/registryv1"
)

// A core is at one address, and that address is the registry's. The subsystems a core hosts
// are at addresses of their own, and the registry is how a client finds them — so a client
// that knows only the core's address asks the registry, and the registry answers with an
// endpoint per service.
//
// That is why a client cannot simply treat the core's address as the address of every
// service: the core is where the directory is, not where everything is. A command that
// assumed otherwise would send a call to a path the core does not serve and get a 404, which
// reads as "that operation does not exist" rather than as "you asked the wrong place".
//
// So the resolver lives here, beside the contract it reads. A root package cannot depend on
// it, because a contract belongs to the subsystem that owns it; a command that wants to reach
// a core asks for one here, which is one line and no new machinery.

// CoreResolver returns a resolver for a core at an address, and a function that releases it.
//
// The resolver is live before it is returned: the first read has already happened, so a
// caller that gets a resolver has a directory that knows what the core holds, rather than one
// that would discover it on the first call and report every service as missing until then.
//
// A core that cannot be reached is an error, not an empty directory. A directory that has
// never synced knows nothing, and a caller handed that would read "this deployment has no
// services" where the truth is "the core is down" — two problems with two different fixes.
func CoreResolver(endpoint string) (core.Resolver, func(), error) {
	address := strings.TrimSpace(endpoint)
	if address == "" {
		return nil, nil, fmt.Errorf("a core address is required")
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	directory, err := NewDirectory(DirectoryOptions{Endpoint: address})
	if err != nil {
		return nil, nil, err
	}
	if err := directory.Start(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("the core at %s did not answer: %w", address, err)
	}
	return directory, directory.Close, nil
}

// endpointFromDescriptor converts a registration into the endpoint a caller resolves to.
//
// The capabilities the registration once carried are gone: what invoking an operation does
// is declared by the contract, not announced by whoever registered it, and a directory that
// reported a capability list would be reporting a deployment's opinion rather than a fact.
// What is left is what a caller needs to make a call.
func endpointFromDescriptor(descriptor *registryv1.ServiceDescriptor) core.Endpoint {
	return core.Endpoint{
		Name:         descriptor.GetSubsystemName(),
		URL:          descriptor.GetEndpoint(),
		Version:      descriptor.GetImplementationVersion(),
		ServiceNames: append([]string(nil), descriptor.GetServiceNames()...),
	}
}
