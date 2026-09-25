package apitools

import "github.com/Manu343726/toolbox/pkg/api"

// What a registered operation may be exposed for is decided by one policy, in the
// foundation, over two facts the description already carries: the operation's
// identifier and what its contract says invoking it does. This file used to hold a
// second implementation of that question, and it answered it differently — a
// capability was a unique name as well as a classification, so no policy could say
// "every read" without naming every read.
//
// The store now holds an api.Policy rather than an OperationPolicy. The zero value
// refuses everything, which is the answer for a catalog nobody has stated a policy
// for; a deployment that wants its whole surface available says so with
// api.AllowAll, because "no policy" and "every policy" must never be the same
// value.

// AllowAllOperations is the policy a deployment installs to permit every
// registered operation.
func AllowAllOperations() api.Policy { return api.AllowAll() }

// DenyAllOperations is the policy a deployment installs to permit none. It is what
// a catalog holds by default, so a registered API is described and documented
// while nothing in it is callable.
func DenyAllOperations() api.Policy { return api.DenyAll() }
