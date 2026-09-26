package host

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/subsystem"
)

// A composition applies its contributors' contributions before it starts anything.
//
// The alternative — letting a contributing subsystem start and then register what it provides
// — leaves a window in which a subsystem is up and the thing it contributes is not in the
// provider directory. Anything that resolved during that window would see a deployment that
// looks misconfigured, and the cause would be a race rather than a mistake. So the phases are
// separate and the order is fixed: build, then configure, then start.
//
// It also means a contributor cannot start a subsystem before the composition it is part of
// exists, because `Compose` takes a factory rather than a built server. A contribution is a
// decision about what should be there; whether it starts is the composition's business.

// compositor is the Compositor the host hands to a contributing subsystem.
//
// It is the host itself under another name: the composition is what a contribution is applied
// to, so there is one implementation rather than a facade over one. The interface exists so
// that `pkg/subsystem` can declare what a contribution is without naming this package.
//
// One compositor is created **per wave**, because `order` is what this wave contributed and
// carrying it across waves is what would make the phase loop. `seen` is shared across every
// wave, because whether a name is taken is not a per-wave fact: two waves must not both
// contribute one name. Those two have different lifetimes, which is why they are two fields
// rather than one struct reused for both.
type compositor struct {
	host *Host

	// seen is every name contributed so far, across all waves.
	seen map[string]subsystem.Factory
	// order is the names contributed by *this* wave, in the order they were contributed.
	order []string
	// lock guards the maps above, so a contributor composing from several goroutines cannot
	// corrupt the set the next wave reads.
	lock *sync.Mutex
}

// Compose implements subsystem.Compositor.
func (c *compositor) Compose(name string, factory subsystem.Factory) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || factory == nil {
		return fmt.Errorf("a contribution needs a name and a factory, because a name is what " +
			"the deployment calls this and a factory is what it runs")
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.host.has(trimmed) {
		// The refusal says what to do, because a contributor that cannot compose anything
		// has no way to guess why. Compose it under its own name: two subsystems serving
		// the same contract is what the provider model is for, and resolution is by
		// identifier, so both stay reachable.
		return fmt.Errorf("a subsystem is already registered as %q, and a contribution adds "+
			"one rather than replacing one. Compose it under another name, and the "+
			"deployment will reach both: providers are resolved by identifier", trimmed)
	}
	if _, taken := c.seen[trimmed]; taken {
		return fmt.Errorf("two contributions both named %q, and a name is the deployment's "+
			"word for one thing", trimmed)
	}
	c.seen[trimmed] = factory
	c.order = append(c.order, trimmed)
	return nil
}

func (h *Host) has(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, registered := h.factories[name]
	return registered
}

// contribute applies every contributor's contribution and builds what they added.
//
// Contributors are asked in waves, and each wave's new subsystems are built before the next
// wave is asked — so a contributor that composes a subsystem which also contributes gets its
// contribution applied.
//
// **Termination is structural, not an argument.** A name can be composed once and a subsystem
// can be built once, so every pass either builds something new or ends. The invariant is
// checked rather than assumed: `built` refuses to build a name twice, so a mistake in the loop
// is a startup failure with a message rather than an unbounded allocation. An earlier version
// of this loop carried one wave's contributions into the next, which satisfied the duplicate
// check and defeated the termination, and grew without limit — which is the argument for
// checking the invariant rather than only reasoning about it.
func (h *Host) contribute(ctx context.Context, servers []*subsystem.Server) ([]*subsystem.Server, error) {
	built := append([]*subsystem.Server(nil), servers...)
	asked := map[string]bool{}
	contributed := map[string]subsystem.Factory{}
	constructed := map[string]bool{}
	pending := servers

	for _, server := range servers {
		constructed[server.Descriptor().SubsystemName] = true
	}

	for len(pending) > 0 {
		wave := pending
		pending = nil

		into := &compositor{host: h, seen: contributed, lock: &sync.Mutex{}}
		for _, server := range wave {
			name := server.Descriptor().SubsystemName
			if !server.IsContributor() || asked[name] {
				continue
			}
			asked[name] = true
			if err := server.Configure(ctx, into); err != nil {
				return nil, fmt.Errorf("apply the contribution of subsystem %q: %w", name, err)
			}
		}
		if len(into.order) == 0 {
			continue
		}
		// Composed in the order they were contributed, not sorted, so a deployment's start
		// order reads the same way in a log as it did in the code that decided it. The order
		// is already deterministic: the contributors ran in selection order.
		for _, name := range into.order {
			if constructed[name] {
				// Unreachable while `Compose` refuses a name it has already seen, which is
				// the whole reason it keeps one set across waves. Checked rather than
				// assumed, because the failure mode of being wrong is unbounded.
				return nil, fmt.Errorf(
					"the contribution of the composition produced a second subsystem named "+
						"%q; a name is the deployment's word for one thing, and a phase that "+
						"rebuilds one cannot finish", name)
			}
			constructed[name] = true
			server, err := contributed[name]()
			if err != nil {
				return nil, fmt.Errorf("construct contributed subsystem %q: %w", name, err)
			}
			built = append(built, server)
			pending = append(pending, server)
		}
	}
	return built, nil
}

// registering returns the servers that register with the deployment, which is every server
// except one that has nothing to serve.
//
// A subsystem that only contributed is not registered, not started, and not listening: it
// decided how the rest of the composition is wired and has nothing to be reached for. See
// `subsystem.Serves` for why that is a property of the server rather than a flag.
func registering(servers []*subsystem.Server) []*subsystem.Server {
	live := make([]*subsystem.Server, 0, len(servers))
	for _, server := range servers {
		if server.Serves() {
			live = append(live, server)
		}
	}
	return live
}
