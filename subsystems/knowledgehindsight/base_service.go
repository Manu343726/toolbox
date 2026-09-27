package knowledgehindsight

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
	knowledgev1connect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

// baseService is administration: which bases exist and how they are configured.
//
// It is separate from the content surface because those are different questions. "What does this
// base know" and "does this base exist, and is it configured sensibly" have different audiences,
// different failure modes and different reasons to be refused, and a service that answered both
// would make every content read depend on configuration rights.
type baseService struct {
	knowledgev1connect.UnimplementedKnowledgeBaseServiceHandler
	p *Provider
}

func (p *Provider) baseHandler() *baseService { return &baseService{p: p} }

// ListBases returns the bases this deployment can address, with their aliases and sizes.
func (s *baseService) ListBases(ctx context.Context, _ *connect.Request[knowledgev1.ListBasesRequest]) (*connect.Response[knowledgev1.ListBasesResponse], error) {
	bases, err := s.p.client.ListBases(ctx)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ListBasesResponse{}
	for _, b := range bases {
		msg, err := s.describe(ctx, b, nil)
		if err != nil {
			continue
		}
		out.Bases = append(out.Bases, msg)
	}
	return connect.NewResponse(out), nil
}

// GetBase returns one base, addressed by identifier or by alias.
func (s *baseService) GetBase(ctx context.Context, req *connect.Request[knowledgev1.GetBaseRequest]) (*connect.Response[knowledgev1.GetBaseResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	bases, err := s.p.client.ListBases(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range bases {
		if b.ID != baseID {
			continue
		}
		rec, _ := s.p.ownershipFor(ctx, baseID)
		msg, err := s.describe(ctx, b, rec)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&knowledgev1.GetBaseResponse{Base: msg}), nil
	}
	return nil, &api.Error{Kind: api.KindNotFound, Message: "no base called " + baseID}
}

// describe builds one base's message, reporting what the backend says about itself.
func (s *baseService) describe(ctx context.Context, b kh.Base, rec *knowledge.OwnershipRecord) (*knowledgev1.Base, error) {
	msg := &knowledgev1.Base{
		Id:             b.ID,
		IsPrimaryAlias: b.PrimaryName != "",
		CorpusRoots:    s.corpusRootsMessage(),
	}
	if b.PrimaryName != "" {
		msg.Name = b.PrimaryName
	} else {
		msg.Name = b.ID
	}
	if rec != nil {
		msg.OwnedDocuments = int32(len(rec.Files))
		msg.ReconciledCommit = rec.LastCommit
		if !rec.LastReconciledAt.IsZero() {
			msg.ReconciledAt = timestamppb.New(rec.LastReconciledAt)
		}
	}
	// The client is pinned to a commit rather than a release, so nothing in the build says
	// which backend the adapter was written for. Both facts are reported and compared, because
	// they are independent and the check is cheap.
	got, want, matches, err := s.p.client.CheckVersion(ctx)
	msg.ExpectedBackendVersion = want
	if err == nil {
		msg.BackendVersion = got
		if got == "" {
			msg.BackendVersion = "(not reported)"
		}
		msg.BackendVersionMatches = matches
	}
	return msg, nil
}

func (s *baseService) corpusRootsMessage() []*knowledgev1.CorpusRootSpec {
	roots := s.p.bases.roots
	out := make([]*knowledgev1.CorpusRootSpec, 0, len(roots))
	for _, r := range roots {
		out = append(out, &knowledgev1.CorpusRootSpec{Name: r.Name, Path: r.Path})
	}
	return out
}

// CreateBase creates a base and returns it.
//
// It creates nothing else. A base holds knowledge; what goes into it is the corpus reconcile's
// business and a retained write's, and a base that came into existence already full would make
// "what did we put here" unanswerable.
func (s *baseService) CreateBase(ctx context.Context, req *connect.Request[knowledgev1.CreateBaseRequest]) (*connect.Response[knowledgev1.CreateBaseResponse], error) {
	msg := req.Msg
	id := strings.TrimSpace(msg.GetId())
	if id == "" {
		// Derived from the display name so a caller that named a base does not have to invent
		// an identifier as well, and so the identifier is the one somebody would have chosen.
		id = slug(msg.GetName())
	}
	if id == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a base needs an identifier or a name; a base nobody can address is not a base",
		}
	}
	if _, err := s.p.client.CreateBase(ctx, id); err != nil {
		return nil, err
	}
	bases, err := s.p.client.ListBases(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range bases {
		if b.ID != id {
			continue
		}
		described, derr := s.describe(ctx, b, nil)
		if derr != nil {
			return nil, derr
		}
		described.Name = msg.GetName()
		described.Description = msg.GetDescription()
		return connect.NewResponse(&knowledgev1.CreateBaseResponse{Base: described}), nil
	}
	return connect.NewResponse(&knowledgev1.CreateBaseResponse{Base: &knowledgev1.Base{Id: id, Name: msg.GetName(), Description: msg.GetDescription()}}), nil
}

// UpdateBase changes a base's name or description.
func (s *baseService) UpdateBase(ctx context.Context, req *connect.Request[knowledgev1.UpdateBaseRequest]) (*connect.Response[knowledgev1.UpdateBaseResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetCorpusRoots()) > 0 {
		// The corpus roots are this deployment's configuration, not the backend's, and
		// they are bound into the ownership record. Changing them through a call to the
		// backend would silently desynchronise the two, so it is refused with the way to
		// do it rather than quietly ignored.
		return nil, &api.Error{
			Kind: api.KindFailedPrecondition,
			Message: "the corpus roots are this deployment's configuration and are bound into the ownership record, so they are not changed over the API; " +
				"change the provider's configuration and restart it. A root that changed without the record changing is a record that would authorise deleting the wrong documents",
		}
	}
	return connect.NewResponse(&knowledgev1.UpdateBaseResponse{Base: &knowledgev1.Base{
		Id: baseID, Name: req.Msg.GetName(), Description: req.Msg.GetDescription(),
		CorpusRoots: s.corpusRootsMessage(),
	}}), nil
}

// DeleteBase deletes a base and reports what it destroyed.
func (s *baseService) DeleteBase(ctx context.Context, req *connect.Request[knowledgev1.DeleteBaseRequest]) (*connect.Response[knowledgev1.DeleteBaseResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	// The corpus files are counted *before* the delete, because after it there is nothing to
	// count and "the base is gone" is not the same report as "the base is gone and here is
	// what it held, and none of it was yours to keep".
	rec, _ := s.p.ownershipFor(ctx, baseID)
	corpusFiles := 0
	if rec != nil {
		corpusFiles = len(rec.Files)
	}
	stats, _ := s.p.client.GetBaseStats(ctx, baseID)
	if err := s.p.client.DeleteBase(ctx, baseID); err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.DeleteBaseResponse{
		Deleted: true,
		Impact: &knowledgev1.DeleteImpact{
			Documents:            stats.Documents,
			Facts:                stats.Facts,
			Observations:         stats.Observations,
			CorpusFilesPreserved: int32(corpusFiles),
			Notes: []string{
				"the base's derived knowledge was destroyed. Nothing a person wrote was: every indexed file lives in a repository, and the reconcile reproduces the base from it",
				"the ownership record was left in place, so recreating the base reconciles incrementally rather than re-extracting everything",
			},
		},
	}), nil
}

// GetBaseConfig reads a base's configuration.
func (s *baseService) GetBaseConfig(ctx context.Context, req *connect.Request[knowledgev1.GetBaseConfigRequest]) (*connect.Response[knowledgev1.GetBaseConfigResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	cfg, err := s.p.client.GetBankConfig(ctx, baseID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.GetBaseConfigResponse{Config: configMessage(cfg)}), nil
}

// UpdateBaseConfig changes a base's configuration.
//
// Every field is three-valued: absent leaves it, present-and-empty clears it. Getting that wrong on
// an update is how a re-tag silently clears a field, and a caller that cannot tell "I did not
// mention it" from "I meant to empty it" will not attempt a partial update at all.
func (s *baseService) UpdateBaseConfig(ctx context.Context, req *connect.Request[knowledgev1.UpdateBaseConfigRequest]) (*connect.Response[knowledgev1.UpdateBaseConfigResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	cfg := req.Msg.GetConfig()
	if cfg == nil {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a configuration is required; an absent one and an empty one mean different things, and `config` being unset is ambiguous between them",
		}
	}
	if err := s.p.client.UpdateBankConfig(ctx, baseID, kh.BankConfigUpdate{
		Mission:           cfg.Mission,
		ExtractionMode:    cfg.ExtractionMode,
		ObservationScope:  cfg.ObservationScope,
		ResolveEntities:   cfg.ResolveEntities,
		DispositionTraits: cfg.GetDispositionTraits(),
	}); err != nil {
		return nil, err
	}
	updated, err := s.p.client.GetBankConfig(ctx, baseID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.UpdateBaseConfigResponse{Config: configMessage(updated)}), nil
}

// ResetBaseConfig restores the server's defaults, and reports what the configuration was.
//
// The distinction from overwriting is the whole point. Setting every field to a known value is only
// the same as a reset while the defaults are those values, and the day they are not, a caller
// overwriting a field it did not know about has silently changed behaviour. A reset asks the server
// what it wants.
func (s *baseService) ResetBaseConfig(ctx context.Context, req *connect.Request[knowledgev1.ResetBaseConfigRequest]) (*connect.Response[knowledgev1.ResetBaseConfigResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	previous, err := s.p.client.GetBankConfig(ctx, baseID)
	if err != nil {
		return nil, err
	}
	if err := s.p.client.ResetBankConfig(ctx, baseID); err != nil {
		return nil, err
	}
	after, err := s.p.client.GetBankConfig(ctx, baseID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.ResetBaseConfigResponse{
		Config: configMessage(after), Previous: configMessage(previous),
	}), nil
}

// GetBaseStats reports a base's size and composition.
func (s *baseService) GetBaseStats(ctx context.Context, req *connect.Request[knowledgev1.GetBaseStatsRequest]) (*connect.Response[knowledgev1.GetBaseStatsResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	stats, err := s.p.client.GetBaseStats(ctx, baseID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.GetBaseStatsResponse{Stats: &knowledgev1.BaseStats{
		Documents:       stats.Documents,
		Facts:           stats.Facts,
		Observations:    stats.Observations,
		WorldFacts:      stats.WorldFacts,
		ExperienceFacts: stats.ExperienceFacts,
	}}), nil
}

// GetBaseIngestionSeries reports how a base has grown.
func (s *baseService) GetBaseIngestionSeries(ctx context.Context, req *connect.Request[knowledgev1.GetBaseIngestionSeriesRequest]) (*connect.Response[knowledgev1.GetBaseIngestionSeriesResponse], error) {
	msg := req.Msg
	if _, err := s.p.resolveBase(ctx, msg.GetRef().GetName()); err != nil {
		return nil, err
	}
	if msg.GetBuckets() <= 0 {
		return nil, &api.Error{
			Kind: api.KindInvalid,
			Message: "a bucket count is required rather than defaulted: a base that has been running for a year and a base created an hour ago " +
				"deserve different answers, and the caller knows which it wants",
		}
	}
	series, err := s.p.client.IngestionSeries(ctx, msg.GetRef().GetName(), msg.GetGranularity(), int(msg.GetBuckets()))
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.GetBaseIngestionSeriesResponse{Granularity: msg.GetGranularity()}
	for _, b := range series {
		out.Buckets = append(out.Buckets, &knowledgev1.IngestionBucket{
			Start: timestamppb.New(b.Start), Facts: b.Facts,
		})
	}
	return connect.NewResponse(out), nil
}

// ListBaseAliases returns a base's friendly names.
func (s *baseService) ListBaseAliases(ctx context.Context, req *connect.Request[knowledgev1.ListBaseAliasesRequest]) (*connect.Response[knowledgev1.ListBaseAliasesResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	names, primary, err := s.p.client.ListAliasesFor(ctx, baseID)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ListBaseAliasesResponse{}
	for _, n := range names {
		out.Aliases = append(out.Aliases, &knowledgev1.BaseAlias{Id: n, Name: n, BaseId: baseID, IsPrimary: n == primary})
	}
	return connect.NewResponse(out), nil
}

// AddBaseAlias gives a base a friendly name.
func (s *baseService) AddBaseAlias(ctx context.Context, req *connect.Request[knowledgev1.AddBaseAliasRequest]) (*connect.Response[knowledgev1.AddBaseAliasResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "an alias needs a name"}
	}
	if err := s.p.client.AddBaseAlias(ctx, baseID, name); err != nil {
		return nil, err
	}
	alias := &knowledgev1.BaseAlias{Id: name, Name: name, BaseId: baseID}
	if req.Msg.GetPrimary() {
		if err := s.p.client.SetBankAliasPrimary(ctx, baseID, name); err != nil {
			return nil, err
		}
		alias.IsPrimary = true
	}
	s.p.bases.recordDisplay(baseID, name)
	return connect.NewResponse(&knowledgev1.AddBaseAliasResponse{Alias: alias}), nil
}

// SetPrimaryBaseAlias marks an alias as the one to display.
func (s *baseService) SetPrimaryBaseAlias(ctx context.Context, req *connect.Request[knowledgev1.SetPrimaryBaseAliasRequest]) (*connect.Response[knowledgev1.SetPrimaryBaseAliasResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	alias := strings.TrimSpace(req.Msg.GetAliasId())
	if alias == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "an alias is required"}
	}
	if err := s.p.client.SetBankAliasPrimary(ctx, baseID, alias); err != nil {
		return nil, err
	}
	s.p.bases.recordDisplay(baseID, alias)
	return connect.NewResponse(&knowledgev1.SetPrimaryBaseAliasResponse{
		Alias: &knowledgev1.BaseAlias{Id: alias, Name: alias, BaseId: baseID, IsPrimary: true},
	}), nil
}

// RemoveBaseAlias removes a friendly name.
func (s *baseService) RemoveBaseAlias(ctx context.Context, req *connect.Request[knowledgev1.RemoveBaseAliasRequest]) (*connect.Response[knowledgev1.RemoveBaseAliasResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetRef().GetName())
	if err != nil {
		return nil, err
	}
	alias := strings.TrimSpace(req.Msg.GetAliasId())
	if alias == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "an alias is required"}
	}
	if err := s.p.client.RemoveBankAlias(ctx, baseID, alias); err != nil {
		return nil, err
	}
	names, primary, err := s.p.client.ListAliasesFor(ctx, baseID)
	if err != nil {
		// The alias is gone. Reporting the new primary is a convenience, and failing the
		// call because a convenience could not be computed would be the wrong trade.
		return connect.NewResponse(&knowledgev1.RemoveBaseAliasResponse{Removed: true}), nil
	}
	out := &knowledgev1.RemoveBaseAliasResponse{Removed: true}
	if primary != "" && primary != alias {
		out.NewPrimary = &knowledgev1.BaseAlias{Id: primary, Name: primary, BaseId: baseID, IsPrimary: true}
	}
	_ = names
	return connect.NewResponse(out), nil
}

func configMessage(cfg kh.BankConfig) *knowledgev1.BaseConfig {
	out := &knowledgev1.BaseConfig{
		Mission:           strPtr(cfg.Mission),
		ExtractionMode:    strPtr(cfg.ExtractionMode),
		ObservationScope:  strPtr(cfg.ObservationScope),
		ResolveEntities:   boolPtr(cfg.ResolveEntities),
		DispositionTraits: cfg.Directives,
	}
	return out
}

func strPtr(s string) *string {
	if s == "" {
		// An absent field and an empty one mean different things in a three-valued update, and
		// turning "" into a present-but-empty value would clear a field the caller never
		// mentioned.
		return nil
	}
	return &s
}

func boolPtr(b bool) *bool { return &b }

// slug derives an identifier from a display name.
func slug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

var _ = fmt.Sprintf
