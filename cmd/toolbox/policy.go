package main

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/Manu343726/toolbox/pkg/api"
)

// A policy is a document, so the default one is too. Putting it in a file rather
// than in Go means a deployment's answer to "what may an agent call" is something
// a person wrote, can read, and can replace, instead of a value hidden in a
// constructor. It is also the same text the policy subsystem will store, so
// replacing this default with a centrally held policy changes where the document
// comes from and not what a rule means.
//
//go:embed policy/toolbox.policy
var defaultPolicyDocument string

// defaultPolicy is the parsed default document.
func defaultPolicy() api.Policy {
	policy, err := api.ParsePolicy(defaultPolicyDocument)
	if err != nil {
		// The document is compiled into the binary and its contents are asserted by
		// a test, so this cannot happen at runtime. Failing closed is the safe
		// direction: a host that cannot read its own policy exposes nothing.
		return api.DenyAll()
	}
	return policy
}

// loadPolicy reads the policy a deployment stated, or returns the default.
//
// A path that cannot be read is an error rather than a fallback: a deployment that
// named a policy and did not get it would otherwise run on a policy nobody chose,
// which is the worst of the three outcomes — not the one they asked for, not the
// documented default, and not visibly wrong.
func loadPolicy(path string) (api.Policy, string, error) {
	if path == "" {
		return defaultPolicy(), "", nil
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return api.Policy{}, "", fmt.Errorf("read the policy at %s: %w", path, err)
	}
	policy, err := api.ParsePolicy(string(document))
	if err != nil {
		return api.Policy{}, "", fmt.Errorf("the policy at %s: %w", path, err)
	}
	return policy, path, nil
}
