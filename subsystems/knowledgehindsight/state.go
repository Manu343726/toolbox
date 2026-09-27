package knowledgehindsight

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
)

// baseExists reports whether the backend has a base with this identifier.
func baseExists(ctx context.Context, client *kh.Client, id string) (bool, error) {
	bases, err := client.ListBases(ctx)
	if err != nil {
		return false, err
	}
	for _, b := range bases {
		if b.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// resolveByAlias finds a base by one of its aliases.
func resolveByAlias(ctx context.Context, client *kh.Client, name string) (string, error) {
	aliases, err := client.ListBaseAliases(ctx)
	if err != nil {
		return "", err
	}
	// The backend's own list is authoritative for what a base can be called, so an alias
	// lookup is the right place to learn the display name too.
	if b, ok := aliases[strings.ToLower(strings.TrimSpace(name))]; ok {
		return b, nil
	}
	for alias, id := range aliases {
		if strings.EqualFold(alias, name) {
			return id, nil
		}
	}
	// The refusal names what the deployment *does* have, because "no alias" on its own is the
	// least useful sentence a caller can be given: they already know the name they typed does
	// not work, and what they need is the list that does.
	known := make([]string, 0, len(aliases))
	for alias := range aliases {
		known = append(known, alias)
	}
	sort.Strings(known)
	if len(known) == 0 {
		known = append(known, "(none)")
	}
	return "", &api.Error{
		Kind:    api.KindNotFound,
		Message: fmt.Sprintf("no base is called %q in this deployment; it knows %s", name, strings.Join(known, ", ")),
	}
}

// corpusState is everything a reconcile needs about one base, gathered once.
type corpusState struct {
	corpus   *knowledge.Corpus
	record   *knowledge.OwnershipRecord
	identity knowledge.OwnershipIdentity
	// recordPath is where the record is persisted, and is reported so an operator can find
	// the file that governs what this deployment may delete.
	recordPath string
	// degraded names a capability that is unavailable because the record could not be
	// loaded, and is surfaced rather than swallowed.
	degraded string
}

// prepare builds the corpus and loads the ownership record for a base.
//
// The namespace is derived from the caller's request rather than from the provider, because two
// deployments reconciling the same directory into the same base under different namespaces must
// not share a record: they are numbering their documents differently, and a record that
// crossed between them would classify the other's files as already reconciled.
func (p *Provider) prepare(ctx context.Context, baseID, namespace string, exclude []string) (corpusState, error) {
	var state corpusState

	corpus, err := p.bases.corpus(exclude)
	if err != nil {
		return state, err
	}
	state.corpus = corpus
	state.identity = p.bases.identity(p.client, baseID, namespace)
	state.recordPath = p.bases.recordPath(state.identity)

	record, err := knowledge.LoadOwnership(state.recordPath, state.identity)
	if err != nil {
		var idErr *knowledge.IdentityError
		if asIdentityError(err, &idErr) {
			// A record for another destination is refused and named, and it is not
			// repaired: repairing it is what the upstream data-loss bug did. The
			// operator's options are a separate record per destination or a fresh one.
			return state, &api.Error{Kind: api.KindFailedPrecondition, Message: idErr.Message, Err: err}
		}
		return state, &api.Error{Kind: api.KindInternal, Message: "loading the ownership record", Err: err}
	}
	state.record = record
	state.degraded = record.Degraded
	return state, nil
}

func asIdentityError(err error, into **knowledge.IdentityError) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*knowledge.IdentityError); ok {
		*into = e
		return true
	}
	return false
}

// derivedClaims reads the engine half for drift detection.
//
// It is optional and its absence is not reported as "no drift". A reconcile that was not given
// the derived half knows nothing about what the base believes, and saying there is no drift would
// be a claim it cannot make.
func (p *Provider) derivedClaims(ctx context.Context, baseID string) ([]knowledge.DerivedClaim, error) {
	docs, err := p.client.ListDocuments(ctx, baseID)
	if err != nil {
		return nil, err
	}
	// Documents are not claims, and this function is the honest half of a larger
	// implementation: what the base holds as documents, so a caller can see which content
	// the corpus would be contradicting. Turning documents into claims needs the fact
	// list, and reporting a document's existence as a claim would be a different claim.
	out := make([]knowledge.DerivedClaim, 0, len(docs))
	for _, d := range docs {
		if d.ID == "" {
			continue
		}
		out = append(out, knowledge.DerivedClaim{ID: d.ID, Origin: knowledge.OriginRetained})
	}
	return out, nil
}

// commitOf returns the corpus revision a reconcile should use.
//
// It defaults to the resolved `HEAD` of the configured roots, and the default is the point: a
// reconcile that ran against a working tree would believe something no reviewer has seen, and the
// reproducibility property would be a claim rather than a fact. A caller that wants to preview
// uncommitted work runs the plan, which is a read and believes nothing.
func (p *Provider) commitOf(ctx context.Context, requested string) (string, error) {
	if c := strings.TrimSpace(requested); c != "" {
		return c, nil
	}
	c, err := resolvedHead(ctx, p.bases.roots)
	if err != nil {
		// A corpus that is not a git checkout still reconciles; the commit is then
		// unknown and recorded as such, which is better than refusing. The record's
		// commit field is allowed to be empty and the contract says so.
		return "", nil
	}
	return c, nil
}
