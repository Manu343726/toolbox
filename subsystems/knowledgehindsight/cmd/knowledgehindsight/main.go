// Command knowledgehindsight serves the knowledge base provider.
//
// It is a standalone command as well as something a host composes, because a deployment's knowledge
// outlives any process: one that keeps its corpus and its ownership record somewhere a person can
// see, and that a person can delete from, is a deployment whose documentation does not disappear
// with a restart.
//
// Configuration is read from the environment rather than from flags, and the reason is that this
// command sits on top of a generated command tree — the RPC methods and the `mcp` subcommand — and a
// hand-rolled flag set would have to be reconciled with the generated one for no gain.
//
// Everything is validated and refused rather than defaulted. A deployment that started with no
// corpus would serve the engine half and nothing a person can review, and finding that out from a
// log line is the wrong way round.
//
//	TOOLBOX_KNOWLEDGE_CORPUS        one directory of markdown
//	TOOLBOX_KNOWLEDGE_CORPUS_ROOTS  several, as a list-separator-joined run of name=path
//	HINDSIGHT_API_URL               the memory backend's base URL
//	HINDSIGHT_API_TOKEN             its token; empty means an unauthenticated deployment
//	TOOLBOX_KNOWLEDGE_OWNER         the identity written into content's ownership marker
//	TOOLBOX_KNOWLEDGE_STATE_DIR     where the ownership record lives; see DefaultStateDir
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Manu343726/toolbox/pkg/cliapp"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	"github.com/Manu343726/toolbox/pkg/subsystem"
	knowledgehindsight "github.com/Manu343726/toolbox/subsystems/knowledgehindsight"
	"github.com/Manu343726/toolbox/subsystems/registry"
)

func main() {
	cliapp.Main(cliapp.Options{
		Name:        knowledgehindsight.Name,
		Description: knowledgehindsight.AdvertisedDescription(),
		// A core is at one address, and that address is its registry's. This is how a
		// command finds the peer a core hosts, so pointing this at a core reaches the
		// deployment rather than a private instance of the provider.
		ResolverFor: registry.CoreResolver,
		Factory: func() (*subsystem.Server, error) {
			roots, err := corpusRootsFromEnv()
			if err != nil {
				return nil, err
			}
			return knowledgehindsight.New(knowledgehindsight.Options{
				BackendEndpoint: envOr("HINDSIGHT_API_URL", "http://127.0.0.1:8888"),
				APIKey:          strings.TrimSpace(os.Getenv("HINDSIGHT_API_TOKEN")),
				CorpusRoots:     roots,
				StateDir:        knowledgehindsight.DefaultStateDir(),
				Owner:           strings.TrimSpace(os.Getenv("TOOLBOX_KNOWLEDGE_OWNER")),
			})
		},
	})
}

// corpusRootsFromEnv reads the configured corpus roots.
//
// A root is `name=path` and the name is required. It namespaces identifiers so two corpora in one
// base cannot collide, and inferring it from the path would make it depend on where the repository
// was cloned — which is the one property a record bound to a destination must not have, because a
// fresh clone elsewhere would fail closed and force a full re-extraction of the whole corpus.
//
// The separator is the platform's list separator, so a Windows deployment can name several roots
// without quoting.
func corpusRootsFromEnv() ([]knowledge.CorpusRoot, error) {
	raw := strings.TrimSpace(os.Getenv("TOOLBOX_KNOWLEDGE_CORPUS_ROOTS"))
	if raw == "" {
		// A single-root deployment is the common case, and being made to write `docs=/path`
		// for one directory is a papercut. The name is still a choice, and it is a constant
		// rather than a function of the path for the reason above.
		if one := strings.TrimSpace(os.Getenv("TOOLBOX_KNOWLEDGE_CORPUS")); one != "" {
			return []knowledge.CorpusRoot{{
				Name: knowledgehindsight.Name,
				Path: filepath.Clean(one),
			}}, nil
		}
		return nil, fmt.Errorf(
			"no corpus root is configured: set %s to a directory of markdown, or %s to a %s-separated list of name=path pairs. "+
				"A knowledge base with no corpus has the engine half and nothing a person can review, so this is refused rather than started empty",
			"TOOLBOX_KNOWLEDGE_CORPUS", "TOOLBOX_KNOWLEDGE_CORPUS_ROOTS", string(filepath.ListSeparator))
	}

	var out []knowledge.CorpusRoot
	seen := map[string]struct{}{}
	for _, entry := range strings.Split(raw, string(filepath.ListSeparator)) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, path, found := strings.Cut(entry, "=")
		name, path = strings.TrimSpace(name), strings.TrimSpace(path)
		switch {
		case !found || name == "":
			return nil, fmt.Errorf("the corpus root %q is not name=path; the name namespaces identifiers and cannot be inferred from a path that depends on where the repository was cloned", entry)
		case path == "":
			return nil, fmt.Errorf("the corpus root %q has no path", entry)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("two corpus roots are both named %q; their identifiers would collide", name)
		}
		seen[name] = struct{}{}
		out = append(out, knowledge.CorpusRoot{Name: name, Path: filepath.Clean(path)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s is set but names no roots", "TOOLBOX_KNOWLEDGE_CORPUS_ROOTS")
	}
	return out, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
