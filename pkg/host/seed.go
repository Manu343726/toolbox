package host

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/discovery"
)

// This file is the automatic exposure path: a host registers the subsystems it
// started in an API catalog, describes each one from the contract the subsystem
// itself serves, and exposes the operations the subsystem declared capabilities
// for.
//
// The point is that a subsystem becomes agent tools without anybody describing it
// twice. Its contract is already served, and its manifest already declares what its
// services are for. Registering it is a matter of reading both and joining them.

// Describer reads a standard description from a document, or from a live endpoint
// that describes itself.
//
// A contract reader satisfies it, which is how a host describes a
// protobuf-served subsystem in process: one function call, with no provider
// subsystem and no transport in between. A deployment whose subsystems speak
// something else supplies its own describer — the composition decides, not this
// package.
type Describer interface {
	Describe(context.Context, api.DescribeRequest) (api.DescribeResult, error)
}

// DefaultContractFormat is the description format a Toolbox subsystem's own
// contract is read as, because a subsystem serves a protobuf contract.
const DefaultContractFormat api.Format = "grpc"

// DefaultSubsystemTransport is the transport a Toolbox subsystem serves its
// contract over.
const DefaultSubsystemTransport api.Transport = "connectrpc"

// SeedOptions configures automatic registration.
type SeedOptions struct {
	// Format is the description format the endpoints' contracts are read as. It
	// defaults to DefaultContractFormat.
	Format api.Format
	// ExposeAll exposes every operation, including ones no capability covers. It
	// is false by default: an operation nobody declared a capability for is not a
	// tool, and "it was registered" is not a reason to make it callable.
	ExposeAll bool
	// ExcludeServices drops services from each description by name, in addition to
	// the platform's own plumbing.
	ExcludeServices []string
	// IncludeInfrastructure keeps the platform's own services in each description:
	// health, the registry, the documentation service, and the framework's
	// extension contracts. It is false by default, because an agent does not need
	// the plumbing a subsystem runs to offer them something else.
	IncludeInfrastructure bool
	// Only restricts registration to the named subsystems. Empty registers all.
	Only []string
}

// Seeded is the outcome for one subsystem.
type Seeded struct {
	// Subsystem is the subsystem that was considered.
	Subsystem string
	// ServerID is the catalog identifier of its server.
	ServerID string
	// APIID is the catalog identifier of its description.
	APIID string
	// Services is how many services the registered description declares.
	Services int
	// Operations is how many operations the registered description declares.
	Operations int
	// Exposed is how many operations the catalog now has exposed.
	Exposed int
	// Replaced reports that a previous description for the subsystem was replaced.
	Replaced bool
	// Skipped reports why a subsystem was not registered, when it was not.
	Skipped string
}

// SeedResult is the outcome for every selected subsystem, in name order.
type SeedResult struct {
	// Seeded holds one entry per subsystem, registered or not.
	Seeded []Seeded
	// Warnings are findings that did not stop any registration.
	Warnings []string
}

// Exposed counts the subsystems that ended up with at least one exposed operation.
func (r SeedResult) Exposed() int {
	total := 0
	for _, entry := range r.Seeded {
		if entry.Exposed > 0 {
			total++
		}
	}
	return total
}

// RegisterInto reads this host's started subsystems through a describer and stores
// them in a catalog.
//
// Each subsystem contributes one server record — its endpoint — and one API
// description read from the contract that endpoint serves. The capabilities the
// subsystem's manifest declared for a service are attached to that service,
// because a contract cannot state them: a protobuf contract knows which methods
// exist and nothing about what a deployment authorizes. Joining the two is what
// makes an operation exposable, and it happens here rather than in either of them.
//
// A subsystem that cannot be described is reported in its outcome and skipped. One
// broken subsystem must not leave an agent with no tools at all.
func (h *Host) RegisterInto(
	ctx context.Context,
	registrar api.Registrar,
	describer Describer,
	options SeedOptions,
) (SeedResult, error) {
	if registrar == nil {
		return SeedResult{}, fmt.Errorf("a catalog is required")
	}
	if describer == nil {
		return SeedResult{}, fmt.Errorf("a describer is required")
	}
	if options.Format == "" {
		options.Format = DefaultContractFormat
	}
	selected := map[string]bool{}
	for _, name := range options.Only {
		if name = strings.TrimSpace(name); name != "" {
			selected[name] = true
		}
	}
	endpoints := h.Endpoints()
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Subsystem < endpoints[j].Subsystem })
	result := SeedResult{}
	for _, endpoint := range endpoints {
		if len(selected) > 0 && !selected[endpoint.Subsystem] {
			continue
		}
		entry, warnings, err := h.registerOne(ctx, registrar, describer, endpoint, options)
		result.Warnings = append(result.Warnings, warnings...)
		if err != nil {
			return result, err
		}
		result.Seeded = append(result.Seeded, entry)
	}
	return result, nil
}

func (h *Host) registerOne(
	ctx context.Context,
	registrar api.Registrar,
	describer Describer,
	endpoint ProviderEndpoint,
	options SeedOptions,
) (Seeded, []string, error) {
	warnings := make([]string, 0)
	described, err := h.describe(ctx, describer, endpoint, options)
	if err != nil {
		// A subsystem the deployment could not describe is reported, not fatal.
		return Seeded{Subsystem: endpoint.Subsystem, Skipped: err.Error()}, warnings, nil
	}
	enriched := described.WithCapabilities(endpoint.ServiceCapabilities)
	server, _, err := registrar.RegisterServer(ctx, api.Server{
		ID:           endpoint.Subsystem,
		Name:         endpoint.Subsystem,
		BaseURL:      endpoint.Endpoint,
		Format:       described.Format,
		Transport:    DefaultSubsystemTransport,
		Description:  fmt.Sprintf("The %s subsystem, described from the contract it serves.", endpoint.Subsystem),
		Source:       described.Source,
		Capabilities: firstNonEmptyCapabilities(enriched.Capabilities, endpoint.Capabilities),
	})
	if err != nil {
		return Seeded{}, warnings, fmt.Errorf("register the %s server: %w", endpoint.Subsystem, err)
	}
	stored, replaced, err := registrar.RegisterAPI(ctx, enriched, server.ID)
	if err != nil {
		return Seeded{}, warnings, fmt.Errorf("register the %s contract: %w", endpoint.Subsystem, err)
	}
	operations := stored.Operations()
	exposed := 0
	for _, operation := range operations {
		if !options.ExposeAll && !stored.DeclaresCapability(operation) {
			continue
		}
		changed, err := registrar.SetExposed(ctx, stored.ID, operation.ID, true)
		if err != nil {
			// An operation the catalog's policy refuses is reported, never forced:
			// seeding is a convenience, and it is not an override of policy.
			warnings = append(warnings, fmt.Sprintf(
				"%s: operation %s stays hidden: %v", endpoint.Subsystem, operation.ID, err,
			))
			continue
		}
		if changed || exposedAlready(ctx, registrar, operation.ID) {
			exposed++
		}
	}
	return Seeded{
		Subsystem:  endpoint.Subsystem,
		ServerID:   server.ID,
		APIID:      stored.ID,
		Services:   len(stored.Services),
		Operations: len(operations),
		Exposed:    exposed,
		Replaced:   replaced,
	}, warnings, nil
}

// exposedAlready asks the catalog what it decided, so a count reflects the
// catalog's state rather than the seeder's optimism.
func exposedAlready(ctx context.Context, registrar api.Registrar, operationID string) bool {
	source, ok := registrar.(api.ExposureSource)
	if !ok {
		return false
	}
	exposures, err := source.Exposures(ctx, []string{operationID})
	if err != nil {
		return false
	}
	return exposures[operationID].Exposed
}

// describe reads one endpoint's contract and drops the platform's plumbing, so a
// catalog an agent reads offers only what a user adopted.
func (h *Host) describe(
	ctx context.Context,
	describer Describer,
	endpoint ProviderEndpoint,
	options SeedOptions,
) (api.API, error) {
	described, err := describer.Describe(ctx, api.DescribeRequest{
		Format:  options.Format,
		BaseURL: endpoint.Endpoint,
		APIID:   endpoint.Subsystem,
		Source:  api.Source{Kind: "reflection", Location: endpoint.Endpoint},
	})
	if err != nil {
		return api.API{}, err
	}
	enriched := described.API
	kept := make([]api.Service, 0, len(enriched.Services))
	for _, service := range enriched.Services {
		if isPlumbing(service.Name, options) {
			continue
		}
		kept = append(kept, service)
	}
	enriched.Services = kept
	if len(enriched.Services) == 0 {
		return api.API{}, fmt.Errorf("the subsystem serves no service a user adopted")
	}
	return enriched, nil
}

// isPlumbing reports whether a service belongs to the platform rather than to a
// capability a user adopted, and so stays out of the catalog by default.
func isPlumbing(name string, options SeedOptions) bool {
	if !options.IncludeInfrastructure && discovery.IsInfrastructureService(name) {
		return true
	}
	for _, candidate := range options.ExcludeServices {
		if strings.TrimSpace(candidate) == name {
			return true
		}
	}
	return false
}

func firstNonEmptyCapabilities(values ...[]string) []string {
	for _, candidate := range values {
		if len(candidate) > 0 {
			return append([]string(nil), candidate...)
		}
	}
	return nil
}
