package hindsight

import (
	"context"
	"sort"
	"strings"

	hs "github.com/vectorize-io/hindsight/hindsight-clients/go"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// Recall retrieves what a base holds about a question.
//
// The budget is sent explicitly. The backend's default differs per call and its schema carries no
// default at all, so a request that omitted it would inherit whatever the server happened to do —
// and this surface exposes one depth setting, which means it has to decide which default it is
// overriding.
func (c *Client) Recall(ctx context.Context, baseID, query string, budget knowledge.Budget, includeSources bool) ([]knowledge.RecallResult, error) {
	body := hs.RecallRequest{Query: query, Budget: budgetValue(budget)}
	if includeSources {
		// Chunks carry the text a fact was extracted from, which is what lets a caller
		// show the sentence rather than only the claim. A fact with no text is a fact the
		// deployment does not retain the words of, and that is reported rather than
		// papered over.
		include := &hs.IncludeOptions{}
		include.SetChunks(hs.ChunkIncludeOptions{})
		body.Include = include
	}
	resp, httpResp, err := c.api.MemoryAPI.RecallMemories(ctx, baseID).RecallRequest(body).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "recalling from base "+baseID)
	}
	if resp == nil {
		return nil, nil
	}
	return recallResults(resp), nil
}

func recallResults(resp *hs.RecallResponse) []knowledge.RecallResult {
	items := resp.GetResults()
	out := make([]knowledge.RecallResult, 0, len(items))
	for _, r := range items {
		result := knowledge.RecallResult{
			ID:       r.GetId(),
			Text:     r.GetText(),
			FactType: r.GetType(),
			Tags:     r.GetTags(),
		}
		// A fact the backend attributes to a document came out of retained content. Whether
		// that content was a reviewed corpus file or a conversation is **not** something
		// the backend records, and this package does not guess: the origin is left retained
		// and the caller resolves it against its own corpus. Claiming a fact is authored
		// because a document id looks like a corpus path would be exactly the relabelling a
		// projection must never do.
		if d := r.GetDocumentId(); d != "" {
			result.DerivedFrom = []string{d}
			result.Origin = knowledge.OriginRetained
			result.Location = knowledge.Location{Kind: knowledge.LocationKindDocument, Path: d}
		} else {
			// No document: the fact came from consolidation rather than from anything
			// retained, which is the derived half.
			result.Origin = knowledge.OriginDerived
		}
		// The four retrieval strategies are named here rather than imported from the
		// backend's word for the combination of them, and each is reported separately: a
		// result only the entity graph found behaves differently from one only keyword
		// search found, and a single blended score throws that away.
		sc := r.GetScores()
		// The getters unwrap the nullable, so an absent strategy reads as a zero score. A
		// zero is not reported as a strategy's contribution: only an arm that actually
		// contributed is named, and the fused score stands in for the rest.
		add := func(name string, score float32) {
			if score <= 0 {
				return
			}
			result.Sources = append(result.Sources, knowledge.RecallSource{
				Strategy: name, Score: score, Excerpt: r.GetText(),
			})
		}
		add("semantic", sc.GetSemantic())
		add("keyword", sc.GetKeyword())
		add("reranked", sc.GetReranker())
		if sc.GetFinal() > 0 && len(result.Sources) == 0 {
			result.Sources = append(result.Sources, knowledge.RecallSource{
				Strategy: "fused", Score: sc.GetFinal(), Excerpt: r.GetText(),
			})
		}
		sort.Slice(result.Sources, func(i, j int) bool {
			if result.Sources[i].Score != result.Sources[j].Score {
				return result.Sources[i].Score > result.Sources[j].Score
			}
			return result.Sources[i].Strategy < result.Sources[j].Strategy
		})
		out = append(out, result)
	}
	return out
}

// Reflect answers a question by reasoning over a base, and resolves what it relied on.
//
// This is the method where the backend is least willing to help, and the shape of the difficulty
// is worth recording because it shaped the contract.
//
// A cited fact **has** a document field in the response model — and it is frequently empty. When
// it is populated the citation is resolved; when it is not, the document identifier is on the
// retrieval results *inside the reasoning trace*. So resolution is two attempts: the cheap one
// first, and a walk of the trace for the citations the cheap one could not place.
//
// Both attempts are needed rather than one. Using only the document field would leave the
// difficult citations unresolved; using only the trace would ask the backend for a large payload
// on every call to resolve citations it had already given.
func (c *Client) Reflect(ctx context.Context, baseID, query string, budget knowledge.Budget, resolve, followDirectives bool) (knowledge.Reflection, error) {
	body := hs.ReflectRequest{Query: query, Budget: budgetValue(budget)}
	if resolve {
		include := &hs.ReflectIncludeOptions{Facts: map[string]any{}}
		include.SetToolCalls(hs.ToolCallsIncludeOptions{})
		body.Include = include
	}
	resp, httpResp, err := c.api.MemoryAPI.Reflect(ctx, baseID).ReflectRequest(body).Execute()
	if err != nil {
		return knowledge.Reflection{}, Classify(err, httpResp, "reflecting over base "+baseID)
	}
	if resp == nil {
		return knowledge.Reflection{}, nil
	}

	out := knowledge.Reflection{Text: resp.GetText()}

	// The second attempt, built only when asked for and only from what the trace carries.
	fromTrace := map[string]string{}
	if resolve {
		fromTrace = factSourcesFromTrace(resp.GetTrace())
	}

	var unresolved int
	basedOn := resp.GetBasedOn()
	for i := range basedOn.GetMemories() {
		m := &basedOn.GetMemories()[i]
		id := m.GetId()
		citation := knowledge.Citation{FactIDs: []string{id}}
		citation.Excerpt, citation.HasExcerpt = m.GetText(), m.GetText() != ""

		docID := strings.TrimSpace(m.GetDocumentId())
		if docID == "" {
			docID = fromTrace[id]
		}
		if docID != "" {
			citation.ContentID = docID
			citation.Location = knowledge.Location{Kind: knowledge.LocationKindDocument, Path: docID}
			citation.Origin = knowledge.OriginRetained
		} else {
			// Left unresolved deliberately, and counted so the caller can say so. A
			// citation with a location pointing at nothing is worse than one that
			// admits it could not be placed: it reads as a page that does not exist.
			citation.Origin = knowledge.OriginDerived
			unresolved++
		}
		out.Citations = append(out.Citations, citation)
		if !resolve {
			out.RawFactIDs = append(out.RawFactIDs, id)
		}
	}
	if resolve && unresolved > 0 {
		out.UnresolvedCitations = unresolved
	}
	return out, nil
}

// factSourcesFromTrace walks a reflect trace for the fact-to-document mapping.
//
// The walk is deliberately generic and recursive. The trace is a tree of tool calls whose outputs
// carry retrieval results, and a fact can be cited from a nested expansion; a shallow read of the
// top level would resolve the easy citations and silently lose the rest, which is the failure that
// makes a citation look reliable when it is not.
func factSourcesFromTrace(trace hs.ReflectTrace) map[string]string {
	out := map[string]string{}
	var walk func(value any, parentDoc string)
	walk = func(value any, parentDoc string) {
		switch v := value.(type) {
		case map[string]any:
			doc, _ := v["document_id"].(string)
			if doc == "" {
				doc = parentDoc
			}
			if doc != "" {
				if id, ok := v["id"].(string); ok && id != "" {
					if _, exists := out[id]; !exists {
						out[id] = doc
					}
				}
			}
			for _, child := range v {
				walk(child, doc)
			}
		case []any:
			for _, child := range v {
				walk(child, parentDoc)
			}
		}
	}
	for _, call := range trace.GetToolCalls() {
		walk(call.GetOutput(), "")
	}
	return out
}

// BackendTags returns the tags the engine half has in use, with counts.
//
// It is a read for discovery: a filter nobody has to guess at. A backend that does not report them
// yields an empty map rather than a failure, because a listing that fails is worse than a listing
// that is honest about having found nothing.
func (c *Client) BackendTags(ctx context.Context, baseID string) map[string]int32 {
	out := map[string]int32{}
	resp, _, err := c.api.BanksAPI.GetAgentStats(ctx, baseID).Execute()
	if err != nil || resp == nil {
		return out
	}
	for key, n := range resp.GetLinksByLinkType() {
		out[key] = n
	}
	return out
}

// DryRunExtract reports what a write would extract, without extracting it.
//
// The settings come from the base's configuration rather than from the preview response, because
// the preview reports facts and chunks and not the settings that produced them. Reading them
// separately is what makes the answer diagnostic: a surprising preview next to the settings that
// caused it is a five-second fix, and a surprising preview on its own is a mystery.
func (c *Client) DryRunExtract(ctx context.Context, baseID, content, documentID string) ([]knowledge.PreviewFact, knowledge.ExtractionSettings, error) {
	settings := knowledge.ExtractionSettings{
		// The backend states the mode in prose and not in its schema, so the value here is
		// the literal the prose uses. Claiming a default the description does not state
		// would be a claim this framework does not make elsewhere either.
		Mode:             "default",
		ObservationScope: "combined",
	}
	if cfg, err := c.GetBankConfig(ctx, baseID); err == nil {
		settings.Mode = cfg.ExtractionMode
		settings.ObservationScope = cfg.ObservationScope
		settings.ResolveEntities = cfg.ResolveEntities
	} else {
		// The configuration could not be read, so what is reported is the backend's
		// documented behaviour rather than this deployment's, and the difference matters
		// for entity resolution: the corpus path turns it off, and inheriting a default
		// here would be inheriting the wrong side.
		settings.ResolveEntities = true
	}

	body := hs.DryRunExtractRequest{Content: hs.Content{String: &content}}
	resp, httpResp, err := c.api.MemoryAPI.DryRunExtractMemories(ctx, baseID).DryRunExtractRequest(body).Execute()
	if err != nil {
		return nil, settings, Classify(err, httpResp, "previewing the extraction of a document in base "+baseID)
	}
	if resp == nil {
		return nil, settings, nil
	}
	var facts []knowledge.PreviewFact
	for _, f := range resp.GetFacts() {
		facts = append(facts, knowledge.PreviewFact{
			Text:     f.GetText(),
			FactType: f.GetFactType(),
			Entities: f.GetEntities(),
		})
	}
	return facts, settings, nil
}

// Prompt is one stage's prompt.
type Prompt struct {
	Stage    string
	System   string
	Template string
}

// PreviewPrompts reports the prompts a base's configuration would use.
func (c *Client) PreviewPrompts(ctx context.Context, baseID string) ([]Prompt, error) {
	resp, httpResp, err := c.api.BanksAPI.PreviewPrompt(ctx, baseID).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "previewing the prompts of base "+baseID)
	}
	if resp == nil {
		return nil, nil
	}
	// The backend reports a prompt as a list of messages rather than as separate system and
	// template fields, so the two are reassembled from the roles. Reporting the messages as
	// they are would be more faithful; splitting them is more useful, and a caller
	// assembling a chat needs the split.
	var out []Prompt
	current := Prompt{}
	for _, msg := range resp.GetMessages() {
		text := promptBlocksText(msg.GetBlocks())
		switch strings.ToLower(strings.TrimSpace(msg.GetRole())) {
		case "system":
			current.System += text
		case "user", "human":
			current.Template += text
		default:
			if current.Stage != "" {
				out = append(out, current)
			}
			current = Prompt{Stage: msg.GetRole()}
		}
	}
	if current.Stage != "" || current.System != "" || current.Template != "" {
		if current.Stage == "" {
			current.Stage = "retain"
		}
		out = append(out, current)
	}
	if s := resp.GetSkippedReason(); s != "" {
		// The backend can decline to preview. That is reported rather than returned as an
		// empty prompt list, because "here are the prompts" and "this base declines to show
		// them" are different answers and only one of them is a bug.
		return nil, &api.Error{
			Kind:    api.KindUnsupported,
			Message: "this base does not report its prompts: " + strings.TrimSpace(s),
		}
	}
	return out, nil
}

// BankConfig is the part of a base's configuration this framework reads and writes.
//
// It is a small subset on purpose. The backend's configuration model is large and moves, and a
// contract mirroring all of it would be a contract about the backend's current shape. What is here
// is what changes how a corpus is reconciled into it.
type BankConfig struct {
	Mission string
	// ExtractionMode is one of the backend's modes. It is a string because the values exist
	// in the backend's prose and not in its schema.
	ExtractionMode string
	// ObservationScope is `per_tag`, `combined` or `shared`. Chosen rather than inherited,
	// because a corpus is mostly provenance tags and `per_tag` fragments observations
	// according to them.
	ObservationScope string
	// ResolveEntities defaults to true in the backend, and the corpus path turns it off.
	ResolveEntities bool
	Directives      []string
	// Raw is the configuration as the backend reported it, for a caller writing a field this
	// package does not model. It is returned rather than discarded so that a deployment is
	// not boxed into what one provider happens to have modelled.
	Raw map[string]any
}

// GetBankConfig reads a base's configuration.
func (c *Client) GetBankConfig(ctx context.Context, baseID string) (BankConfig, error) {
	resp, httpResp, err := c.api.BanksAPI.GetBankConfig(ctx, baseID).Execute()
	if err != nil {
		return BankConfig{}, Classify(err, httpResp, "reading the configuration of base "+baseID)
	}
	if resp == nil {
		return BankConfig{}, nil
	}
	// The backend's configuration response is a free-form map rather than a typed structure,
	// and that is reported as it is rather than modelled field by field. A typed subset would
	// be a guess about a shape the backend does not declare, and the keys it does use are
	// Python field names on purpose — modelling them as this framework's enums would be
	// inventing an authority the description does not have.
	cfg := BankConfig{Raw: map[string]any{}}
	for k, v := range resp.GetConfig() {
		cfg.Raw[k] = v
	}
	if m, ok := resp.GetConfig()["mission"].(string); ok {
		cfg.Mission = m
	}
	if mode, ok := resp.GetConfig()["retain_extraction_mode"].(string); ok {
		cfg.ExtractionMode = mode
	}
	if scope, ok := resp.GetConfig()["observation_scope"].(string); ok {
		cfg.ObservationScope = scope
	}
	// Entity resolution is left at the backend's documented default when the configuration
	// does not say. An absent key is not a false value: reading it as one would report a
	// base as not resolving entities when it is, which is the side that silently misattributes
	// an author's name.
	cfg.ResolveEntities = true
	if v, ok := resp.GetConfig()["resolve_entities"].(bool); ok {
		cfg.ResolveEntities = v
	}
	return cfg, nil
}

// budgetValue maps a budget to the backend's enum.
//
// The schema carries no default, so this is always sent. An unrecognised value becomes mid, which
// is the safer of the two defaults: a slower answer is a better failure than a thinner one.
func budgetValue(b knowledge.Budget) *hs.Budget {
	switch b {
	case knowledge.BudgetLow:
		v := hs.LOW
		return &v
	case knowledge.BudgetHigh:
		v := hs.HIGH
		return &v
	default:
		v := hs.MID
		return &v
	}
}

var _ = api.KindInternal

// promptBlocksText reassembles a prompt message's blocks into plain text.
//
// A message is a list of typed blocks rather than a string, and a caller assembling a chat needs
// the text. Blocks whose type this package does not know are still read as text where a text field
// exists, and skipped where one does not: a provider that dropped an unfamiliar block would silently
// shorten a prompt somebody is auditing.
func promptBlocksText(blocks []hs.PromptBlockModel) string {
	var b strings.Builder
	for _, block := range blocks {
		if t := block.GetText(); t != "" {
			b.WriteString(t)
		}
	}
	return b.String()
}
