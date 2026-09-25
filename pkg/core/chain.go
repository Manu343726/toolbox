package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// A subsystem that calls a peer has two places to look: the endpoints this process
// started, and the core it was told about. A chain tries them in that order, because
// a local endpoint needs no network hop and is authoritative — a subsystem this
// process started is certainly the one that process should call.
//
// What a chain must not do is flatten its legs into one answer. "This process did
// not start it" and "the core was asked and does not have it" and "the core could
// not be reached" are three different situations, and a caller that cannot tell
// them apart cannot decide what to do. One is a deployment that legitimately runs
// without the peer, one is a name that is wrong, and one is an outage. A chain that
// reported all three as "not found" would have a subsystem either refusing to start
// or hanging on every call, and neither failure would say why.

var (
	// ErrUnreachable indicates that a resolver could not be consulted at all, which
	// is different from consulting it and being told the service is not there.
	ErrUnreachable = errors.New("resolver unreachable")
	// ErrNoResolver indicates that a chain has nothing to try.
	ErrNoResolver = errors.New("no resolver configured")
)

// Leg is one place a chain looks.
type Leg struct {
	// Name identifies the leg in an error. It is a place or a mechanism — "this
	// process", "the core" — not a service name.
	Name string
	// Resolver answers for this leg. Nil is ignored, so a caller that has no core
	// configured simply leaves the leg out rather than passing something that will
	// fail every time.
	Resolver Resolver
	// Optional marks a leg whose failure to answer is not worth reporting. A
	// configured core that happens to be down is worth reporting; a leg that was
	// never meant to be there is not.
	Optional bool
}

// Chain resolves through an ordered list of legs.
type Chain struct {
	mu    sync.RWMutex
	legs  []Leg
	cache map[string]Endpoint
}

// NewChain creates a chain from legs, in the order they should be tried.
func NewChain(legs ...Leg) *Chain {
	return &Chain{legs: append([]Leg(nil), legs...), cache: make(map[string]Endpoint)}
}

// Register adds or replaces a leg. A leg's name identifies it, so replacing one
// keeps its position rather than appending a second copy of the same place.
func (c *Chain) Register(leg Leg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index, existing := range c.legs {
		if existing.Name == leg.Name {
			c.legs[index] = leg
			return
		}
	}
	c.legs = append(c.legs, leg)
}

// Legs returns the chain's legs in the order they are tried.
func (c *Chain) Legs() []Leg {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Leg(nil), c.legs...)
}

// Resolve returns the first endpoint any leg knows.
//
// A leg that fails because it could not be reached is remembered and reported
// alongside a not-found answer, because "I asked and could not hear back" is the
// answer a caller needs when a peer is missing. A leg that answers "not there" is
// simply the next leg's business and is not reported.
func (c *Chain) Resolve(ctx context.Context, serviceName string) (Endpoint, error) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return Endpoint{}, fmt.Errorf("%w: empty service name", ErrNotFound)
	}
	c.mu.RLock()
	cached, haveCached := c.cache[serviceName]
	legs := append([]Leg(nil), c.legs...)
	c.mu.RUnlock()
	if haveCached {
		return cached, nil
	}

	if len(legs) == 0 {
		return Endpoint{}, fmt.Errorf("%w: nothing to resolve %s against", ErrNoResolver, serviceName)
	}

	tried := make([]string, 0, len(legs))
	unreachable := make([]error, 0, len(legs))
	for _, leg := range legs {
		if leg.Resolver == nil {
			continue
		}
		tried = append(tried, leg.Name)
		endpoint, err := leg.Resolver.Resolve(ctx, serviceName)
		if err == nil {
			c.remember(serviceName, endpoint)
			return endpoint, nil
		}
		if errors.Is(err, ErrNotFound) {
			// The leg answered. It simply does not have the service, which is the
			// ordinary case for a peer this process did not start.
			continue
		}
		// Anything else is a leg that could not answer, and that is worth saying.
		unreachable = append(unreachable, fmt.Errorf("%s: %w", leg.Name, err))
	}

	if len(unreachable) > 0 {
		return Endpoint{}, &UnreachableError{Service: serviceName, Tried: tried, Causes: unreachable}
	}
	return Endpoint{}, &NotFoundError{Service: serviceName, Tried: tried}
}

// remember caches an answer, and forgets a name that resolves to an endpoint that
// has since been removed. The cache is bounded by what the chain has been asked
// for, which is the right bound: a chain is asked about peers, not about every
// service in a deployment.
func (c *Chain) remember(serviceName string, endpoint Endpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[serviceName] = endpoint
}

// Forget drops a cached answer, so the next resolution asks again.
//
// A caller that learned a peer's endpoint has changed — from a registry change
// event, or from a failed call — forgets it here, and the next call goes back to
// the chain rather than to a stale table.
func (c *Chain) Forget(serviceName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, serviceName)
}

// ForgetAll empties the cache.
func (c *Chain) ForgetAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]Endpoint)
}

// NotFoundError reports that every leg was asked and none had the service.
type NotFoundError struct {
	// Service is the name that was not found.
	Service string
	// Tried names the legs that were consulted, in order.
	Tried []string
}

func (e *NotFoundError) Error() string {
	if len(e.Tried) == 0 {
		return fmt.Sprintf("%s: no resolver knew %s", ErrNotFound, e.Service)
	}
	return fmt.Sprintf("%s: %s is not served by %s", ErrNotFound, e.Service, strings.Join(e.Tried, " or "))
}

func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// UnreachableError reports that at least one leg could not be consulted, which is a
// different answer from "nobody has it".
type UnreachableError struct {
	// Service is the name being resolved.
	Service string
	// Tried names the legs that were consulted, in order.
	Tried []string
	// Causes is one entry per leg that could not answer.
	Causes []error
}

func (e *UnreachableError) Error() string {
	messages := make([]string, 0, len(e.Causes))
	for _, cause := range e.Causes {
		messages = append(messages, cause.Error())
	}
	return fmt.Sprintf("could not resolve %s: %s", e.Service, strings.Join(messages, "; "))
}

func (e *UnreachableError) Is(target error) bool { return target == ErrUnreachable }

// Unwrap exposes the leg failures, so errors.Is reaches a transport cause through
// them.
func (e *UnreachableError) Unwrap() []error { return e.Causes }

// ServedBy reports which leg answered for a service, given a chain that can be
// asked. It exists for diagnostics: "which process answered" is the first question
// anyone asks when a call reaches the wrong subsystem.
func ServedBy(ctx context.Context, chain *Chain, serviceName string) (string, error) {
	if chain == nil {
		return "", fmt.Errorf("%w: no chain", ErrNoResolver)
	}
	for _, leg := range chain.Legs() {
		if leg.Resolver == nil {
			continue
		}
		if _, err := leg.Resolver.Resolve(ctx, serviceName); err == nil {
			return leg.Name, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, serviceName)
}

// SortedLegNames returns leg names in a stable order, for a diagnostic message.
func SortedLegNames(legs []Leg) []string {
	names := make([]string, 0, len(legs))
	for _, leg := range legs {
		if leg.Name != "" {
			names = append(names, leg.Name)
		}
	}
	sort.Strings(names)
	return names
}
