package knowledgehindsight

import (
	"context"
	"time"

	"github.com/Manu343726/toolbox/pkg/knowledge"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// projectionSource builds the cache a mount serves, out of the same two reads `ExportWiki` uses.
//
// It lives here, outside any build tag, so that both builds have it and so that a caller can hold a
// projection without a filesystem at all — which is what makes the whole-wiki view testable and what
// makes a mount a consumer of the feature rather than the feature itself.
type projectionSource struct {
	cache   *knowledge.ProjectionCache
	filter  knowledge.Origin
	subtree string
}

// newProjectionSource wires a fetcher and a revision source over one base's projection.
func newProjectionSource(_ context.Context, p *Provider, baseID string, corpus *knowledge.Corpus, origin knowledge.Origin, subtree string) (*projectionSource, error) {
	src := &projectionSource{filter: origin, subtree: subtree}
	src.cache = knowledge.NewProjectionCache(
		func(ctx context.Context) (knowledge.Wiki, error) {
			return p.exportWiki(ctx, baseID, corpus, origin, subtree)
		},
		func(ctx context.Context) (knowledge.ProjectionRevision, error) {
			return p.projectionRevision(ctx, baseID, corpus, "")
		},
	)
	return src, nil
}

// Cache returns the underlying cache, which is what a filesystem serves.
func (s *projectionSource) Cache() *knowledge.ProjectionCache { return s.cache }

// exportWiki composes the whole-wiki view for a base.
//
// The two halves are read from where each half actually lives: the corpus is a directory on this
// host and the pages are a backend export. Nothing here re-renders either — an authored file is
// copied verbatim and a page comes from the backend's own bundle — because re-rendering a reviewed
// document produces a *different* document, and a diff between what was reviewed and what is
// published is a question nobody asked.
func (p *Provider) exportWiki(ctx context.Context, baseID string, corpus *knowledge.Corpus, origin knowledge.Origin, subtree string) (knowledge.Wiki, error) {
	in := knowledge.WikiInput{
		BaseID:       baseID,
		GeneratedAt:  time.Now().UTC(),
		OriginFilter: origin,
		Subtree:      subtree,
	}
	rec, _ := p.ownershipFor(ctx, baseID)
	if rec != nil {
		in.Commit = rec.LastCommit
	}

	if origin == knowledge.OriginUnspecified || origin == knowledge.OriginAuthored {
		walk, err := corpus.Walk(ctx, knowledge.WalkOptions{})
		if err != nil {
			return knowledge.Wiki{}, err
		}
		for _, f := range walk.Files {
			if !f.Read {
				// A file the walk skipped cannot be projected, and projecting a blank
				// one would put an empty file in front of a reader where their runbook
				// should be. It is reported by the projection's `skipped` list rather
				// than silently absent.
				continue
			}
			in.Authored = append(in.Authored, knowledge.ProjectedFile{
				Path:       f.Path,
				Content:    f.Raw,
				SourcePath: f.Path,
				ContentID:  f.ID,
				Digest:     f.Digest,
			})
		}
	}

	if origin == knowledge.OriginUnspecified || origin == knowledge.OriginDerived {
		pages, err := p.client.ExportPages(ctx, baseID)
		if err != nil {
			return knowledge.Wiki{}, err
		}
		in.Pages = pages
		tree, terr := p.client.PageTree(ctx, baseID)
		if terr == nil {
			for _, n := range tree {
				if n.IsStale {
					in.StalePages++
				}
			}
		}
	}

	return knowledge.ProjectWiki(in)
}

// projectionRevision computes the projection's change signal for a base.
//
// It is two cheap probes: a directory stat per corpus file and one page-tree call. Neither reads
// content, because at three thousand files a digest per file is a full read of the corpus on every
// poll and the projection is not changing because somebody touched a file without editing it.
func (p *Provider) projectionRevision(ctx context.Context, baseID string, corpus *knowledge.Corpus, commit string) (knowledge.ProjectionRevision, error) {
	if corpus == nil {
		built, err := p.bases.corpus(nil)
		if err != nil {
			return knowledge.ProjectionRevision{}, err
		}
		corpus = built
	}
	source := knowledge.NewRevisionSource(corpus, p.client.PageTree)
	return source.Revision(ctx, baseID, commit)
}

// ownershipFor loads the ownership record for a base, or nil when it cannot be loaded.
//
// It is a best-effort read for a *display* purpose — a projection saying which commit it reflects.
// A record that cannot be loaded must not stop a projection: the projection is derived and
// regenerable, and refusing to show a reader their own documentation because a status file is
// missing would be the wrong way round.
func (p *Provider) ownershipFor(ctx context.Context, baseID string) (*knowledge.OwnershipRecord, error) {
	identity := p.bases.identity(p.client, baseID, defaultNamespace)
	rec, err := knowledge.LoadOwnership(p.bases.recordPath(identity), identity)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// filterOrigin converts the contract's origin into the domain's, for a projection filter.
func filterOrigin(o knowledgev1.Origin) knowledge.Origin {
	switch o {
	case knowledgev1.Origin_ORIGIN_AUTHORED:
		return knowledge.OriginAuthored
	case knowledgev1.Origin_ORIGIN_DERIVED:
		return knowledge.OriginDerived
	case knowledgev1.Origin_ORIGIN_RETAINED:
		// A retained document is not part of the projection, so filtering for one is
		// reported as no filter rather than as a filter that matches nothing silently.
		return knowledge.OriginUnspecified
	default:
		return knowledge.OriginUnspecified
	}
}
