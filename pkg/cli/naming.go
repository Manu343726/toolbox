package cli

import "strings"

// A command name is derived from a service, taking its last name segment — so two
// services whose last segments match produce the same name. That is not an accident to
// guard against: the protocol's own reflection services are `v1.ServerReflection` and
// `v1alpha.ServerReflection`, and a subsystem may well serve two versions of one
// contract.
//
// Cobra resolves a repeated name to whichever command was added first, so a collision
// does not fail. The second command is unreachable, silently, from help and from shell
// completion — which is worse than a duplicate that is visible. So a name more than one
// service claims is qualified with whatever tells them apart.
//
// This is the same rule the Model Context Protocol gateway applies to tool names, for
// the same reason and with the same ladder: keep the short name where it is unambiguous,
// add the least that disambiguates, and fall back to the whole package path when even
// that is not enough. Two implementations of one rule would agree until one changed.

// commandNamer assigns each service the command name a caller should type.
//
// It is built over every service in the surface before any command is named, so the
// result does not depend on the order the services were discovered in.
type commandNamer struct {
	// short counts how many services claim each short name.
	short map[string]int
	// qualified counts how many services claim each qualified name, over the
	// services that need one.
	qualified map[string]int
	// full counts the whole-package-path names, over the services that need one.
	full map[string]int
	// services is every service being named, in the order they were given.
	services []string
}

func newCommandNamer(services []string) *commandNamer {
	namer := &commandNamer{
		short:     make(map[string]int, len(services)),
		qualified: make(map[string]int, len(services)),
		full:      make(map[string]int, len(services)),
		services:  append([]string(nil), services...),
	}
	for _, service := range services {
		namer.short[shortServiceName(service)]++
	}
	// Counted in later passes over the services that need them. Counting a qualified
	// name in the first pass would miss the collision: the first claimant does not know
	// yet that a second is coming, which is the same trap as assigning a name before
	// seeing the whole set.
	for _, service := range services {
		short := shortServiceName(service)
		if namer.short[short] > 1 {
			namer.qualified[packageQualifiedName(service, short)]++
		}
	}
	for _, service := range services {
		short := shortServiceName(service)
		if namer.short[short] <= 1 {
			continue
		}
		if namer.qualified[packageQualifiedName(service, short)] > 1 {
			namer.full[fullQualifiedName(service, short)]++
		}
	}
	return namer
}

// name returns the command name for one service.
func (n *commandNamer) name(service string) string {
	short := shortServiceName(service)
	if n.short[short] <= 1 {
		return short
	}
	qualified := packageQualifiedName(service, short)
	if n.qualified[qualified] <= 1 {
		return qualified
	}
	return fullQualifiedName(service, short)
}

// packageQualifiedName prefixes the short name with the last segment of the service's
// package, which is the part that usually tells two versions of one contract apart:
// v1.server-reflection and v1alpha.server-reflection.
func packageQualifiedName(service, short string) string {
	pkg := packageSegments(service)
	if len(pkg) == 0 {
		return short
	}
	return camelToKebab(pkg[len(pkg)-1]) + "." + short
}

// fullQualifiedName uses the whole package path, which is long and ugly and reachable.
// A name that reaches the wrong service is worse than an ugly one.
func fullQualifiedName(service, short string) string {
	pkg := packageSegments(service)
	if len(pkg) == 0 {
		return short
	}
	parts := make([]string, 0, len(pkg))
	for _, segment := range pkg {
		parts = append(parts, camelToKebab(segment))
	}
	return strings.Join(parts, ".") + "." + short
}

// packageSegments is everything before the service's own name.
func packageSegments(service string) []string {
	parts := strings.Split(service, ".")
	if len(parts) < 2 {
		return nil
	}
	return parts[:len(parts)-1]
}
