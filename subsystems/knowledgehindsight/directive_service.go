package knowledgehindsight

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/Manu343726/toolbox/pkg/api"
	kh "github.com/Manu343726/toolbox/pkg/knowledge/hindsight"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
	knowledgev1connect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

// directiveService manages the rules the reasoning step follows.
//
// It is small and it is separate because the thing it manages is categorically different: a
// directive is the only content in a base a person writes and the system never rewrites. Everything
// else in a base is either extracted from a document or synthesized from facts, and both are
// replaced whenever the inputs change. A directive is not. So a caller reaching for this service is
// doing something no other call in the contract does — deciding what the system is told — and it
// deserves a name of its own.
type directiveService struct {
	knowledgev1connect.UnimplementedDirectiveServiceHandler
	p *Provider
}

func (p *Provider) directiveHandler() *directiveService { return &directiveService{p: p} }

func (s *directiveService) ListDirectives(ctx context.Context, req *connect.Request[knowledgev1.ListDirectivesRequest]) (*connect.Response[knowledgev1.ListDirectivesResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if err := checkToken(req.Msg.GetPageToken()); err != nil {
		return nil, err
	}
	limit, offset := pageWindow(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	rules, total, err := s.p.client.ListRules(ctx, baseID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ListDirectivesResponse{}
	// Filtering happens here rather than being sent, because the backend's own filter takes a tag
	// match mode and this contract's takes a list of tags to carry. Both are applied: a
	// directive that is not active is excluded when only active ones were asked for, and a
	// directive missing a named tag is excluded when tags were named. The total is left as the
	// backend reported it, because the backend did not apply either filter, and a token
	// computed from an unfiltered total would walk past the filtered list.
	for _, rule := range rules {
		if req.Msg.GetEnabledOnly() && !rule.Enabled {
			continue
		}
		if len(req.Msg.GetTags()) > 0 && !hasEveryTag(rule.Tags, req.Msg.GetTags()) {
			continue
		}
		out.Directives = append(out.Directives, ruleMessage(baseID, rule))
	}
	out.NextPageToken = nextToken(offset, int32(len(rules)), total)
	return connect.NewResponse(out), nil
}

func (s *directiveService) CreateDirective(ctx context.Context, req *connect.Request[knowledgev1.CreateDirectiveRequest]) (*connect.Response[knowledgev1.CreateDirectiveResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetText() == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a directive needs text; a rule with no words in it is a rule that governs nothing and reads as though it governs everything"}
	}
	// The backend requires a name and this contract does not ask for one, because a name is not
	// something a reader has to supply to write a rule. It is derived from the text instead —
	// the first words, trimmed to something a list can show.
	rule, err := s.p.client.CreateRule(ctx, baseID, ruleName(req.Msg.GetText()), req.Msg.GetText(), req.Msg.GetTags(), nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.CreateDirectiveResponse{Directive: ruleMessage(baseID, rule)}), nil
}

func (s *directiveService) GetDirective(ctx context.Context, req *connect.Request[knowledgev1.GetDirectiveRequest]) (*connect.Response[knowledgev1.GetDirectiveResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	rule, err := s.p.client.GetRule(ctx, baseID, req.Msg.GetDirectiveId())
	if err != nil {
		return nil, err
	}
	if rule.ID == "" {
		return nil, &api.Error{Kind: api.KindNotFound, Message: "no directive called " + req.Msg.GetDirectiveId() + " in base " + baseID}
	}
	return connect.NewResponse(&knowledgev1.GetDirectiveResponse{Directive: ruleMessage(baseID, rule)}), nil
}

func (s *directiveService) UpdateDirective(ctx context.Context, req *connect.Request[knowledgev1.UpdateDirectiveRequest]) (*connect.Response[knowledgev1.UpdateDirectiveResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	update := kh.RuleUpdate{Tags: req.Msg.GetTags()}
	if req.Msg.Text != nil {
		if *req.Msg.Text == "" {
			return nil, &api.Error{
				Kind:    api.KindInvalid,
				Message: "a directive's text cannot be emptied. Turn it off with enabled = false instead: an emptied rule and a disabled one are different states, and a caller that emptied the text probably meant to stop the rule governing answers",
			}
		}
		update.Text = req.Msg.Text
	}
	update.Enabled = req.Msg.Enabled
	rule, err := s.p.client.UpdateRule(ctx, baseID, req.Msg.GetDirectiveId(), update)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.UpdateDirectiveResponse{Directive: ruleMessage(baseID, rule)}), nil
}

// DeleteDirective removes a rule, and only when the caller says so twice.
//
// A rule that is quietly retained after somebody believes they removed it is a rule that keeps
// governing every answer the base gives, and nothing in the base would say so. So the removal takes
// a confirmation, and the way to stop a rule governing answers without removing it is
// `UpdateDirective` with `enabled` false — which is a visible state rather than an absence.
func (s *directiveService) DeleteDirective(ctx context.Context, req *connect.Request[knowledgev1.DeleteDirectiveRequest]) (*connect.Response[knowledgev1.DeleteDirectiveResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	rule, err := s.p.client.GetRule(ctx, baseID, req.Msg.GetDirectiveId())
	if err != nil {
		return nil, err
	}
	if rule.ID == "" {
		return nil, &api.Error{Kind: api.KindNotFound, Message: "no directive called " + req.Msg.GetDirectiveId() + " in base " + baseID}
	}
	if !req.Msg.GetConfirm() {
		return nil, &api.Error{
			Kind: api.KindInvalid,
			Message: fmt.Sprintf(
				"removing a directive means every answer the base gives stops being governed by %q, with nothing in the base recording that it ever was. Send confirm = true, or set enabled = false instead, which turns the rule off in a way the base can show",
				ruleName(rule.Text)),
		}
	}
	if err := s.p.client.DeleteRule(ctx, baseID, req.Msg.GetDirectiveId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.DeleteDirectiveResponse{Deleted: true}), nil
}

func ruleName(text string) string {
	// The first few words, on a word boundary and under a length a list can show. It exists
	// because the backend requires a name and a reader should not have to invent one to write a
	// rule; it is not an identifier and nothing resolves it.
	words := text
	if len(words) > 48 {
		words = words[:48]
		if cut := lastSpace(words); cut > 0 {
			words = words[:cut]
		}
		words += "…"
	}
	if words == "" {
		words = "(untitled directive)"
	}
	return words
}

func lastSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' {
			return i
		}
	}
	return -1
}

func hasEveryTag(have, want []string) bool {
	for _, w := range want {
		if !hasString(have, w) {
			return false
		}
	}
	return true
}

func ruleMessage(baseID string, r kh.Rule) *knowledgev1.Directive {
	return &knowledgev1.Directive{
		Id: r.ID, BaseId: baseID, Text: r.Text, Tags: r.Tags,
		CreatedAt: timestampOrNil(r.Created), UpdatedAt: timestampOrNil(r.Updated), Enabled: r.Enabled,
	}
}

// observationService is administration over the consolidation layer.
//
// It is separate from the query services because consolidation is the expensive, asynchronous,
// occasionally-wrong part of a base, and an operator reaching for it is usually reaching for it
// because something looked wrong. Those are different conversations from "what does this base know",
// and putting them together would make a read of the base's beliefs require the rights to rebuild
// them.
type observationService struct {
	knowledgev1connect.UnimplementedObservationServiceHandler
	p *Provider
}

func (p *Provider) observationHandler() *observationService { return &observationService{p: p} }

func (s *observationService) ListObservationScopes(ctx context.Context, req *connect.Request[knowledgev1.ListObservationScopesRequest]) (*connect.Response[knowledgev1.ListObservationScopesResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	scopes, err := s.p.client.ListObservationScopes(ctx, baseID)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.ListObservationScopesResponse{}
	for _, scope := range scopes {
		out.Scopes = append(out.Scopes, &knowledgev1.ObservationScope{
			Id: scope.ID, BaseId: baseID, Mode: scope.Mode, Tags: scope.Tags,
			ObservationCount: scope.Count, LastConsolidatedAt: timestampOrNil(scope.LastRun),
		})
	}
	return connect.NewResponse(out), nil
}

func (s *observationService) PreviewConsolidation(ctx context.Context, req *connect.Request[knowledgev1.PreviewConsolidationRequest]) (*connect.Response[knowledgev1.PreviewConsolidationResponse], error) {
	baseID, err := s.p.resolveBaseForRead(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetStrategies()) == 0 {
		// The endpoint answers "given these strategies, which facts would each take", so a
		// preview naming none would be asking the backend to supply the answer being checked.
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a preview names the strategies it is previewing. An empty set is not \"whatever the backend would default to\": the claims are a function of the strategies, so a preview of nothing is a preview of a decision nobody made",
		}
	}
	strategies := make([]kh.Strategy, 0, len(req.Msg.GetStrategies()))
	for i, s := range req.Msg.GetStrategies() {
		one, serr := strategyFrom(baseID, int32(i), s)
		if serr != nil {
			return nil, serr
		}
		strategies = append(strategies, one)
	}
	preview, err := s.p.client.PreviewConsolidation(ctx, baseID, strategies)
	if err != nil {
		return nil, err
	}
	out := &knowledgev1.PreviewConsolidationResponse{
		ScopesScanned: preview.ScopesScanned, Complete: preview.Complete,
		Notes: []string{
			"consolidation is an LLM pass, so its output cannot be predicted without running it. What is previewed is which facts each candidate strategy would claim, which is what makes a surprising consolidation diagnosable",
		},
	}
	for _, claim := range preview.Claims {
		msg := &knowledgev1.StrategyClaim{
			Index: claim.Index, Active: claim.Active, FactsClaimed: claim.FactsClaimed,
		}
		for _, r := range claim.Rules {
			// A rule that matched and was dropped is reported with a zero rather than
			// omitted, because a scope the strategy does nothing with is where a proposal is
			// quietly failing and a list of successful rules would not show it.
			msg.Rules = append(msg.Rules, &knowledgev1.ClaimedScopeRule{
				Matched: r.Matched, Taken: r.Taken, Observations: r.Observations, Scopes: r.Scopes,
			})
		}
		out.Claims = append(out.Claims, msg)
	}
	if !preview.Complete {
		out.Notes = append(out.Notes,
			"the backend reported it did not have everything it needed, so these claims are computed from part of the base and are a number about that part rather than about the base")
	}
	return connect.NewResponse(out), nil
}

// strategyFrom converts one candidate strategy and checks the match mode it names.
func strategyFrom(baseID string, index int32, msg *knowledgev1.ConsolidationStrategy) (kh.Strategy, error) {
	out := kh.Strategy{Mission: msg.GetMission(), MaxObservationsPerScope: msg.GetMaxObservationsPerScope()}
	for i, pattern := range msg.GetScopes() {
		scope := kh.ScopePattern{Tags: pattern.GetTags()}
		if match := pattern.GetTagsMatch(); match != "" {
			switch match {
			case "any", "all", "any_strict", "all_strict":
				scope.TagsMatch = match
			default:
				return out, &api.Error{
					Kind: api.KindInvalid,
					Message: fmt.Sprintf(
						"strategy %d, scope %d: the tag match mode is %q, not one of any, all, any_strict, all_strict", index, i, match),
				}
			}
		}
		if len(scope.Tags) == 0 {
			// A scope with no tags is the base's whole untagged content, which is a
			// meaningful thing to consolidate and a surprising one. It is allowed, and it is
			// allowed silently — unlike a named mode that does not exist, which is a mistake
			// with no reading at all.
			continue
		}
		out.Scopes = append(out.Scopes, scope)
	}
	return out, nil
}

func (s *observationService) TriggerConsolidation(ctx context.Context, req *connect.Request[knowledgev1.TriggerConsolidationRequest]) (*connect.Response[knowledgev1.TriggerConsolidationResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	scopes, err := s.scopesToConsolidate(ctx, baseID, req.Msg.GetObservationScope())
	if err != nil {
		return nil, err
	}
	opID, err := s.p.client.TriggerConsolidation(ctx, baseID, scopes)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.TriggerConsolidationResponse{OperationId: opID}), nil
}

// scopesToConsolidate turns a requested scope into the backend's list of tag groups.
//
// The backend takes a list of *scopes* rather than a scope name, so a caller naming one scope sends
// one scope and a caller naming none sends every scope the base actually has. An empty list is not
// "everything" to that endpoint — it consolidates nothing — so the scopes are read and sent
// explicitly rather than left to be interpreted.
func (s *observationService) scopesToConsolidate(ctx context.Context, baseID, want string) ([][]string, error) {
	if want == "" {
		return nil, nil
	}
	scopes, err := s.p.client.ListObservationScopes(ctx, baseID)
	if err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		if scope.ID == want || want == scope.Mode {
			tags := scope.Tags
			if len(tags) == 0 {
				// A scope with no tags is the global one, and the backend's list form cannot
				// say "no tags" — it says an empty string.
				tags = []string{""}
			}
			return [][]string{tags}, nil
		}
	}
	return nil, &api.Error{
		Kind: api.KindNotFound,
		Message: fmt.Sprintf(
			"no observation scope called %q in base %s. `ListObservationScopes` reports the ones in use, and consolidating a scope that is not there would do nothing while reporting an operation",
			want, baseID),
	}
}

func (s *observationService) RecoverConsolidation(ctx context.Context, req *connect.Request[knowledgev1.RecoverConsolidationRequest]) (*connect.Response[knowledgev1.RecoverConsolidationResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	// The backend recovers whatever is interrupted and takes no scope, so a request that named
	// one is refused rather than quietly consolidated something else. What it reports back is a
	// count of scopes retried, not an operation to follow.
	if req.Msg.GetObservationScope() != "" {
		return nil, &api.Error{
			Kind:    api.KindUnsupported,
			Message: "recovery resumes whatever is interrupted, and this build cannot restrict it to one scope: the backend's recovery takes no scope parameter. Run `ListObservationScopes` to see what it resumed, or trigger a consolidation for a single scope instead",
		}
	}
	retried, err := s.p.client.RecoverConsolidation(ctx, baseID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.RecoverConsolidationResponse{
		Notes: []string{
			fmt.Sprintf("the backend resumed %d scope(s); it reports a count rather than an operation, so a caller watches the scopes rather than following a job", retried),
			"there is no operation to follow because recovery is synchronous in the backend, so a count is the whole of what happened",
		},
	}), nil
}

func (s *observationService) ClearBaseObservations(ctx context.Context, req *connect.Request[knowledgev1.ClearBaseObservationsRequest]) (*connect.Response[knowledgev1.ClearBaseObservationsResponse], error) {
	baseID, err := s.p.resolveBase(ctx, req.Msg.GetBaseId())
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetObservationScopes()) > 0 {
		return nil, &api.Error{
			Kind:    api.KindUnsupported,
			Message: "clearing observations is a whole-base operation: the backend's clear takes no scope parameter. Refusing the scopes rather than ignoring them, because a caller that named two and got all of them has lost more than it asked to",
		}
	}
	// What is about to go is counted first. The facts stay, so this is recoverable by
	// consolidating again — but a caller that has not looked should be able to.
	scopes, err := s.p.client.ListObservationScopes(ctx, baseID)
	if err != nil {
		return nil, err
	}
	var total int32
	for _, scope := range scopes {
		total += scope.Count
	}
	cleared, err := s.p.client.ClearObservations(ctx, baseID)
	if err != nil {
		return nil, err
	}
	notes := []string{
		"the facts are still there; this removed what was consolidated from them, and a consolidation pass rebuilds it",
		fmt.Sprintf("the base held %d observation(s) across %d scope(s) when it was cleared", total, len(scopes)),
	}
	if len(scopes) == 0 {
		notes = append(notes, "the base reported no consolidation scopes, so there was nothing consolidated to remove and the clear was a no-op")
	}
	return connect.NewResponse(&knowledgev1.ClearBaseObservationsResponse{Cleared: cleared, Notes: notes}), nil
}

func scopeMessage(baseID string, scope kh.ObservationScope) *knowledgev1.ObservationScope {
	return &knowledgev1.ObservationScope{
		Id: scope.ID, BaseId: baseID, Mode: scope.Mode, Tags: scope.Tags,
		ObservationCount: scope.Count, LastConsolidatedAt: timestampOrNil(scope.LastRun),
	}
}
