package mcp

import (
	"sort"
	"strings"
)

// Tool names are derived from a service and a method, which keeps them short and
// readable. The derivation takes the service's last name segment, so two services
// whose last segments match produce the same name — and several providers serving
// one contract is exactly that case, by design rather than by accident.
//
// A name a client can reach has to identify one operation, so a name more than one
// operation claims is qualified with whatever tells them apart: the API a
// description belongs to, or the endpoint that serves a service. Nothing is
// pre-emptively lengthened, because a name that does not need the qualifier must
// keep the name a client already learned.

// toolNameOwner is one operation's naming inputs, plus whatever distinguishes it
// from an operation that would reduce to the same name.
type toolNameOwner struct {
	// qualified is the API-and-service name, or the service name alone.
	qualified string
	// owner is the API identifier or the endpoint name that serves it.
	owner string
	// method is the operation's method name.
	method string
}

// toolNamer assigns each operation the name a client should use.
//
// It is built over every operation in the surface before any tool is named, so the
// result does not depend on the order operations were discovered in.
type toolNamer struct {
	// short and qualified count how many operations each name was claimed by, at
	// each level of qualification.
	short     map[string]int
	qualified map[string]int
}

// newToolNamer collects the operations that will be named.
//
// Every candidate is counted, including one a policy will refuse: a denied
// operation still occupies its name, and a name that moved because a hidden
// operation was present would change what an exposed tool is called.
func newToolNamer(candidates []toolNameOwner) *toolNamer {
	namer := &toolNamer{
		short:     make(map[string]int, len(candidates)),
		qualified: make(map[string]int, len(candidates)),
	}
	for _, candidate := range candidates {
		namer.short[namer.shortName(candidate)]++
	}
	// The qualified names are counted in a second pass, over the operations that
	// need one. Counting them in the first would miss a collision: the first
	// claimant of a name does not know yet that a second is coming.
	for _, candidate := range candidates {
		short := namer.shortName(candidate)
		if namer.short[short] > 1 {
			namer.qualified[namer.qualify(candidate, short)]++
		}
	}
	return namer
}

// name returns the tool name for one operation.
func (n *toolNamer) name(candidate toolNameOwner) string {
	short := n.shortName(candidate)
	if n.short[short] <= 1 {
		return short
	}
	qualified := n.qualify(candidate, short)
	if n.qualified[qualified] <= 1 {
		return qualified
	}
	// A longer name is better than a name that reaches the wrong operation, so the
	// fully qualified service name is the last resort.
	return strings.ToLower(candidate.qualified) + "__" + short
}

func (n *toolNamer) shortName(candidate toolNameOwner) string {
	return shortToolName(candidate.qualified, candidate.method)
}

// qualify prefixes a name with the owner that tells two operations apart.
func (n *toolNamer) qualify(candidate toolNameOwner, short string) string {
	if candidate.owner == "" {
		return short
	}
	return shortToolName(candidate.owner, short)
}

// shortToolName reduces a qualified service and a method to a tool name, keeping
// only the last service segment and a readable method.
func shortToolName(qualified, method string) string {
	parts := strings.Split(qualified, ".")
	short := parts[len(parts)-1]
	short = strings.TrimSuffix(short, "Service")
	name := snakeCase(short)
	if method != "" {
		name += "__" + snakeCase(method)
	}
	return capToolName(name)
}

// sortOwners orders candidates deterministically, so a surface built twice names
// its tools the same way both times.
func sortOwners(candidates []toolNameOwner) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].owner != candidates[j].owner {
			return candidates[i].owner < candidates[j].owner
		}
		if candidates[i].qualified != candidates[j].qualified {
			return candidates[i].qualified < candidates[j].qualified
		}
		return candidates[i].method < candidates[j].method
	})
}
