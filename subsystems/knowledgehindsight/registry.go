package knowledgehindsight

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
)

// baseRegistry knows which base a caller means, and what corpus each one reconciles.
//
// The registry is in process rather than in the backend for one reason: the corpus roots are
// directories on this host, and the backend has no way to know them. A base's identifier is the
// backend's; which files belong to it is this deployment's.
//
// A caller may address a base by its identifier or by its display name, and a deployment's
// configuration chooses a name far more often than a generated identifier. Resolving both here
// means no caller has to know which it was given.
type baseRegistry struct {
	mu sync.RWMutex

	// roots are the configured corpus roots, in declaration order.
	roots []knowledge.CorpusRoot
	// stateDir is where ownership records live.
	stateDir string
	// batchSize and concurrency bound a reconcile.
	batchSize   int
	concurrency int
	// names maps a lowercased alias onto a base identifier. It is filled lazily from the
	// backend, so a base created by another deployment is still addressable.
	names map[string]string
	// display maps a base identifier onto the name it should be shown as.
	display map[string]string
}

func newBaseRegistry(roots []knowledge.CorpusRoot, stateDir string, batchSize, concurrency int) *baseRegistry {
	return &baseRegistry{
		roots:       append([]knowledge.CorpusRoot(nil), roots...),
		stateDir:    stateDir,
		batchSize:   batchSize,
		concurrency: concurrency,
		names:       map[string]string{},
		display:     map[string]string{},
	}
}

func (r *baseRegistry) rootNames() []string {
	out := make([]string, 0, len(r.roots))
	for _, root := range r.roots {
		out = append(out, root.Name)
	}
	return out
}

// corpus builds a walker over the configured roots, with the scope this provider was configured
// with.
//
// It is rebuilt per operation rather than cached so that a host adding a root at runtime and a
// test writing a temporary directory both see the truth. The cost is a few `stat` calls; a
// walker holds no connection and no cache that could go stale.
func (r *baseRegistry) corpus(scope []string) (*knowledge.Corpus, error) {
	opts := knowledge.CorpusOptions{}
	for _, s := range scope {
		if strings.TrimSpace(s) == "" {
			continue
		}
		opts.Exclude = append(opts.Exclude, s)
	}
	return knowledge.NewCorpus(r.roots, opts)
}

// resolve turns a caller-supplied name into a base identifier.
//
// The backend's own alias list is consulted so that a base created elsewhere is still addressable
// by name; a failure there is not fatal, because a caller addressing a base by its identifier must
// work even when the alias call does not.
func (r *baseRegistry) resolve(ctx context.Context, client *kh.Client, name string) (string, error) {
	return r.resolveWith(ctx, client, name, false)
}

// resolveLocal resolves a name for an operation that does not write.
//
// A plan is a comparison between a directory and a record, and both live on this host. Requiring the
// backend to already hold the base would make the one operation that shows a person what a
// deployment believes unavailable exactly when the engine is down — and it would also make it
// impossible to plan the reconcile that populates a base somebody has just created the intent of.
//
// So a plan passes an identifier-shaped name through unverified, and says so. The write is where an
// unverified name is caught, by the backend, which will not have heard of it.
func (r *baseRegistry) resolveLocal(ctx context.Context, client *kh.Client, name string) (string, error) {
	return r.resolveWith(ctx, client, name, true)
}

func (r *baseRegistry) resolveWith(ctx context.Context, client *kh.Client, name string, local bool) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", &api.Error{Kind: api.KindInvalid, Message: "a base name or identifier is required"}
	}
	if cached, ok := r.cached(trimmed); ok {
		return cached, nil
	}
	// An identifier needs no lookup: a base whose name is literally what the caller sent is
	// the base they meant, and making every call on a correctly-identified base depend on a
	// round trip is a cost paid for nothing.
	if isLikelyIdentifier(trimmed) {
		if exists, err := baseExists(ctx, client, trimmed); err == nil {
			if exists || local {
				r.remember(trimmed, trimmed)
				return trimmed, nil
			}
		} else if err != nil {
			// The backend could not be reached, and the corpus half does not need it. A
			// reconcile plan is a comparison between a directory and a record, and both
			// live here; refusing to plan because the engine is down would make the one
			// operation that can be used to see what a deployment believes unavailable
			// exactly when the engine is misbehaving.
			return offlineResolution(trimmed, err)
		}
	}
	if id, err := resolveByAlias(ctx, client, trimmed); err == nil && id != "" {
		r.remember(trimmed, id)
		return id, nil
	} else if err != nil {
		var apiErr *api.Error
		if !asAPIError(err, &apiErr) || apiErr.Kind != api.KindUnavailable {
			// An alias lookup that cannot reach the backend is worth reporting, because
			// the caller is about to be told the base does not exist and the real answer
			// is that this deployment cannot see any base.
			return "", err
		}
		return offlineResolution(trimmed, err)
	}
	return "", &api.Error{
		Kind:    api.KindNotFound,
		Message: fmt.Sprintf("no base is called %q in this deployment; call ListBases to see what is addressable, and note that the configured corpus is %s", name, strings.Join(r.rootNames(), ", ")),
	}
}

// offlineResolution passes the name through when the backend cannot be reached.
//
// This is a deliberate asymmetry, and it is the reason the corpus half of the system is usable at
// all during an outage. Authored content lives in a directory and its ownership record lives on
// disk, so every operation over them is local; the engine half genuinely needs the backend and will
// say so when it is called. The name is passed through unverified rather than refused, and the
// call that *writes* is where an unverified name is caught — by the backend, which will not have
// heard of it.
func offlineResolution(name string, cause error) (string, error) {
	if isLikelyIdentifier(name) {
		return name, nil
	}
	// A display name cannot be verified without the alias list, and guessing an identifier
	// from one would be a guess that ends in a write to whatever base happened to match.
	return "", &api.Error{
		Kind: api.KindUnavailable,
		Message: fmt.Sprintf("cannot resolve the base called %q while the backend is unreachable (%v); "+
			"an identifier would be used as given, and a name needs the alias list the backend holds", name, cause),
		Err: cause,
	}
}

// isLikelyIdentifier reports whether a name looks like an identifier rather than a display name.
//
// It is a shape check and nothing more: the backend's identifiers are slugs, and a display name is
// whatever somebody typed. A name that looks like a slug and is not one resolves to nothing and
// the caller gets a not-found naming both, which is the answer either way.
func isLikelyIdentifier(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func (r *baseRegistry) cached(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id, ok := r.names[key]; ok {
		return id, true
	}
	// The display name is a second key: a base addressed by the name it shows under.
	if id, ok := r.display[key]; ok {
		return id, true
	}
	return "", false
}

func (r *baseRegistry) remember(alias, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names[strings.ToLower(alias)] = id
	r.display[strings.ToLower(id)] = alias
}

// recordDisplay notes the name a base should be shown as, so the next resolution is free.
func (r *baseRegistry) recordDisplay(id, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.display[strings.ToLower(id)] = name
}

// identity builds the ownership binding for a base over the configured roots.
//
// The corpus root is every configured root joined, which is what makes a record bound to "this
// deployment's corpus" rather than to one directory: a reconcile over two roots is one operation,
// and a record that bound only one of them would be reusable by a reconcile that had silently
// lost the other.
func (r *baseRegistry) identity(client *kh.Client, baseID, namespace string) knowledge.OwnershipIdentity {
	paths := make([]string, 0, len(r.roots))
	for _, root := range r.roots {
		paths = append(paths, root.Path)
	}
	sort.Strings(paths)
	return knowledge.OwnershipIdentity{
		BackendOrigin: client.Endpoint(),
		BaseID:        baseID,
		CorpusRoot:    strings.Join(paths, string(filepath.ListSeparator)),
		Namespace:     namespace,
	}
}

// recordPath returns where a base's ownership record lives.
//
// The file name carries a fingerprint of the binding, so two destinations get two files rather
// than one file that both of them refuse. The refusal is the real safety property; the
// fingerprint is what stops it being the everyday outcome.
func (r *baseRegistry) recordPath(id knowledge.OwnershipIdentity) string {
	return knowledge.DefaultRecordPath(filepath.Join(r.stateDir, "ownership"), id)
}

func asAPIError(err error, into **api.Error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*api.Error); ok {
		*into = e
		return true
	}
	return false
}
