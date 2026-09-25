package mcp

import "github.com/Manu343726/toolbox/pkg/api"

// This file used to hold the gateway's own authorization vocabulary: a policy
// interface, three implementations, and a rule that turned a source's capability
// metadata into that policy. All of it is gone, and the reason is worth recording.
//
// What a reflected method may be called is now answered by one policy in the
// foundation, over two facts the description already carries: the operation's
// identifier and what its contract says invoking it does. A gateway that derived
// its own answers from a discovery artifact had two problems. It could not be
// coarse, because a capability was a unique name as well as a classification, so
// "every read" meant naming every read. And it invented authorization data when
// the artifact did not carry it: several subsystems serving one contract arrived
// with a flattened capability list, which the gateway then copied onto every
// service, so a service that had declared nothing was authorized by its sibling.
//
// The rule is now one sentence and it lives in pkg/api: a policy permits an
// operation, and an operation the policy does not permit is not a tool. Where the
// policy comes from is the deployment's business.

// APIPolicy returns a policy that permits every operation.
//
// It exists for a composition that has already decided its surface — a standalone
// subsystem command serving that subsystem's own contract, or a test — and it is
// never a default. A gateway with no policy permits nothing, because "no policy"
// and "every policy" must not be the same value.
func APIPolicy() api.Policy { return api.AllowAll() }

// FactsFor returns what a policy is evaluated against for one operation.
func FactsFor(identifier string, effects []api.SideEffect) api.OperationFacts {
	return api.OperationFacts{ID: identifier, SideEffects: effects}
}
