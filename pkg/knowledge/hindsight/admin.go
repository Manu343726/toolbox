package hindsight

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	hs "github.com/vectorize-io/hindsight/hindsight-clients/go"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// --- memory.

// Fact is one extracted statement, in this framework's vocabulary.
type Fact struct {
	ID         string
	Text       string
	FactType   string
	DocumentID string
	Entities   []string
	Tags       []string
	ObservedAt time.Time
	UpdatedAt  time.Time
	// Curated reports that a person corrected what the extractor produced, and
	// SupersededText is what it said before. A correction with no record of what it replaced is
	// indistinguishable from one somebody changed by accident.
	Curated        bool
	SupersededText string
}

// GetFact reads one fact.
//
// The backend's API description declares **an empty schema** for this endpoint, and the generated
// client reflects that honestly by returning `interface{}` rather than inventing a type. The shape
// this decodes is therefore inferred from the one sibling that *is* declared — the item shape a list
// of facts uses — and that inference is the only assumption in the file worth flagging.
//
// It is decoded rather than read as a map so that a shape change shows up as a field going missing
// rather than as a silent zero, which is what a map of `any` would give.
func (c *Client) GetFact(ctx context.Context, baseID, id string) (Fact, error) {
	raw, httpResp, err := c.api.MemoryAPI.GetMemory(ctx, baseID, id).Execute()
	if err != nil {
		return Fact{}, Classify(err, httpResp, "reading fact "+id)
	}
	var item hs.MemoryUnitListItem
	if rerr := remarshal(raw, &item); rerr != nil {
		return Fact{}, &api.Error{
			Kind:    api.KindInternal,
			Message: "the backend returned a fact this build cannot read, and the endpoint declares no schema for it; a shape change here is a version difference, and the fact is reported as unreadable rather than empty",
			Err:     rerr,
		}
	}
	return factFrom(item), nil
}

// remarshal moves a decoded `interface{}` into a typed struct.
//
// The generated client returns `interface{}` for the endpoints whose responses it could not type, so
// the only way to read them is to put the value through the same encoding the wire already used.
func remarshal(raw any, into any) error {
	if raw == nil {
		return nil
	}
	blob, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(blob, into)
}

func factFrom(m hs.MemoryUnitListItem) Fact {
	f := Fact{ID: m.GetId(), Tags: m.GetTags()}
	if t := m.GetText(); t != "" {
		f.Text = t
	}
	if ft := m.GetFactType(); ft != "" {
		f.FactType = strings.ToLower(strings.TrimSpace(ft))
	}
	if d := m.GetDocumentId(); d != "" {
		f.DocumentID = d
	}
	if e := m.GetEntities(); e != "" {
		// The backend packs entities into one string rather than a list, which is the kind of
		// shape a contract should not inherit. It is split here so that a caller filtering on
		// an entity gets a list to filter.
		for _, name := range strings.Split(e, ",") {
			if name = strings.TrimSpace(name); name != "" {
				f.Entities = append(f.Entities, name)
			}
		}
	}
	f.ObservedAt = parseLoose(m.GetMentionedAt())
	f.UpdatedAt = parseLoose(m.GetUpdatedAt())
	// The backend records an edit time on a fact a person corrected and not on one the
	// extractor produced. That is the only signal it gives, so it is the flag: a correction
	// with no way to be told apart from an extraction is a correction a reader cannot trust
	// and a history that cannot explain itself.
	if edited := parseLoose(m.GetEditedAt()); !edited.IsZero() {
		f.Curated, f.UpdatedAt = true, edited
	}
	return f
}

// UpdateFact corrects a fact in place.
func (c *Client) UpdateFact(ctx context.Context, baseID, id, text string, reason string) error {
	// The reason is sent alongside the correction. A correction with no record of why is
	// indistinguishable from one somebody changed by accident, and the backend has a place to
	// keep it.
	body := hs.UpdateMemoryRequest{Text: *hs.NewNullableString(&text)}
	if reason != "" {
		body.Reason = *hs.NewNullableString(&reason)
	}
	_, httpResp, err := c.api.MemoryAPI.UpdateMemory(ctx, baseID, id).UpdateMemoryRequest(body).Execute()
	if err != nil {
		return Classify(err, httpResp, "correcting fact "+id)
	}
	return nil
}

// FactRevision is one version of a fact.
type FactRevision struct {
	Text   string
	At     time.Time
	Source string
	Reason string
}

// FactHistory is what a fact has said, and when.
//
// It is the answer to "why does the base believe this", which is a different question from "what does
// it believe" and the one a person asks when they are about to argue with it.
func (c *Client) FactHistory(ctx context.Context, baseID, id string) ([]FactRevision, error) {
	// Untyped for the same reason as GetFact: the endpoint declares no schema. It is decoded
	// into the shape a declared sibling uses, and read defensively — a history that gained a
	// field is not a failure, and one that lost the text is reported as such rather than as
	// an empty history.
	raw, httpResp, err := c.api.MemoryAPI.GetObservationHistory(ctx, baseID, id).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "reading the history of fact "+id)
	}
	var page struct {
		Items []hs.MemoryUnitListItem `json:"items"`
	}
	if rerr := remarshal(raw, &page); rerr != nil {
		return nil, &api.Error{
			Kind:    api.KindInternal,
			Message: "the backend returned a history this build cannot read; the endpoint declares no schema for it",
			Err:     rerr,
		}
	}
	out := make([]FactRevision, 0, len(page.Items))
	for _, r := range page.Items {
		rev := FactRevision{At: parseLoose(r.GetUpdatedAt())}
		if t := r.GetText(); t != "" {
			rev.Text = t
		}
		// An edited version is a person; an uneedited one is the extractor. That distinction
		// is the whole point of the history: "why does the base believe this" is usually
		// answered by which of the two last touched it.
		if e := r.GetEditedAt(); e != "" {
			rev.Source = "curation"
		} else {
			rev.Source = "extraction"
		}
		out = append(out, rev)
	}
	return out, nil
}

// GraphNode and GraphLink are the structural reads, which answer "what is connected" and not "what
// is relevant". A caller that wants relevance wants a recall.
type GraphNode struct {
	ID    string
	Kind  string
	Label string
}

type GraphLink struct {
	From   string
	To     string
	Kind   string
	Weight float32
}

// EntityGraph returns the entity graph, and whether the walk was truncated.
//
// Truncation is reported rather than left to be inferred from a node count: a graph that silently
// stopped early is a graph a reader will over-read.
func (c *Client) EntityGraph(ctx context.Context, baseID string, limit int32) (nodes []GraphNode, links []GraphLink, truncated bool, err error) {
	resp, httpResp, err := c.api.EntitiesAPI.GetEntityGraph(ctx, baseID).Execute()
	if err != nil {
		return nil, nil, false, Classify(err, httpResp, "reading the entity graph of base "+baseID)
	}
	if resp == nil {
		return nil, nil, false, nil
	}
	// The graph wraps each node and edge in a `data` object, so the identity is one level
	// down from where a reader would expect it. That shape is the backend's and is read as it
	// is rather than flattened into something that looks tidier and would break on the next
	// version.
	for _, n := range resp.GetNodes() {
		data := n.GetData()
		node := GraphNode{Kind: "entity", ID: data.GetId(), Label: data.GetId()}
		if label := data.GetLabel(); label != "" {
			node.Label = label
		}
		nodes = append(nodes, node)
	}
	for _, e := range resp.GetEdges() {
		edge := e.GetData()
		// The backend expresses a weight as a whole percentage, so it is divided here rather
		// than carried as 0..100 next to a `float` field every other score in this package
		// fills from 0..1. A reader comparing a graph weight to a recall score would
		// otherwise be comparing two scales and conclude the graph was confident.
		weight := float32(0)
		if w := edge.GetWeight(); w != 0 {
			weight = float32(w) / 100
		}
		links = append(links, GraphLink{From: edge.GetSource(), To: edge.GetTarget(), Kind: edge.GetLinkType(), Weight: weight})
	}
	truncated = int32(len(nodes)) > limit && limit > 0
	return nodes, links, truncated, nil
}

// --- entities.

// Entity is a name the extractor found and resolved.
type Entity struct {
	ID           string
	Name         string
	MentionCount int32
	FirstSeen    time.Time
	LastSeen     time.Time
	Tags         []string
	// FactIDs are the facts an entity is mentioned in, which is how a reader checks a claim
	// rather than believing it.
	FactIDs []string
	// ObservationCount is how many consolidated observations mention it. The backend's own
	// references carry the claim's text rather than an identifier, so there is nothing to
	// traverse and the count is what says the name is part of the base's beliefs rather than
	// only part of one fact.
	ObservationCount int32
}

// ListEntities returns the entities a base knows, filtered by a name fragment.
func (c *Client) ListEntities(ctx context.Context, baseID, query string, limit, offset int32) ([]Entity, int32, error) {
	req := c.api.EntitiesAPI.ListEntities(ctx, baseID)
	if limit > 0 {
		req = req.Limit(limit)
	}
	if offset > 0 {
		req = req.Offset(offset)
	}
	resp, httpResp, err := req.Execute()
	if err != nil {
		return nil, 0, Classify(err, httpResp, "listing the entities of base "+baseID)
	}
	if resp == nil {
		return nil, 0, nil
	}
	out := make([]Entity, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		e := Entity{
			ID: item.GetId(), Name: item.GetCanonicalName(), MentionCount: item.GetMentionCount(),
			FirstSeen: parseLoose(item.GetFirstSeen()), LastSeen: parseLoose(item.GetLastSeen()),
		}
		// The backend offers no name filter on this endpoint, so a query is applied here. It
		// filters what the backend returned rather than asking for fewer, which means the
		// total is the unfiltered one and a page of results can come back short — and the
		// caller is told so rather than being handed a short page as if it were the whole
		// answer.
		if query != "" && !strings.Contains(strings.ToLower(e.Name), strings.ToLower(query)) {
			continue
		}
		out = append(out, e)
	}
	return out, resp.GetTotal(), nil
}

// GetEntity reads one entity and what it is mentioned in.
func (c *Client) GetEntity(ctx context.Context, baseID, id string) (Entity, error) {
	resp, httpResp, err := c.api.EntitiesAPI.GetEntity(ctx, baseID, id).Execute()
	if err != nil {
		return Entity{}, Classify(err, httpResp, "reading entity "+id)
	}
	if resp == nil {
		return Entity{}, nil
	}
	e := Entity{
		ID: resp.GetId(), Name: resp.GetCanonicalName(), MentionCount: resp.GetMentionCount(),
		FirstSeen: parseLoose(resp.GetFirstSeen()), LastSeen: parseLoose(resp.GetLastSeen()),
	}
	// The observation references carry the claim's *text* rather than an identifier, so there
	// is no identifier to collect. The count is reported instead and the text dropped: an
	// identifier field filled with prose would be worse than an absent one, and the text is
	// what a reader gets from the observations themselves.
	e.ObservationCount = int32(len(resp.GetObservations()))
	return e, nil
}

// --- directives.

// Rule is a hand-written instruction the reasoning step must follow.
//
// It is the only content in a base a person writes and the system never rewrites, which is what
// makes it categorically different from everything else here.
type Rule struct {
	ID      string
	Name    string
	Text    string
	Tags    []string
	Enabled bool
	Created time.Time
	Updated time.Time
}

// ListRules returns a base's directives.
func (c *Client) ListRules(ctx context.Context, baseID string, limit, offset int32) ([]Rule, int32, error) {
	req := c.api.DirectivesAPI.ListDirectives(ctx, baseID)
	if limit > 0 {
		req = req.Limit(limit)
	}
	if offset > 0 {
		req = req.Offset(offset)
	}
	resp, httpResp, err := req.Execute()
	if err != nil {
		return nil, 0, Classify(err, httpResp, "listing the directives of base "+baseID)
	}
	if resp == nil {
		return nil, 0, nil
	}
	out := make([]Rule, 0, len(resp.GetItems()))
	for _, d := range resp.GetItems() {
		out = append(out, ruleFrom(d))
	}
	return out, resp.GetTotal(), nil
}

// GetRule reads one directive.
func (c *Client) GetRule(ctx context.Context, baseID, id string) (Rule, error) {
	resp, httpResp, err := c.api.DirectivesAPI.GetDirective(ctx, baseID, id).Execute()
	if err != nil {
		return Rule{}, Classify(err, httpResp, "reading directive "+id)
	}
	return ruleFrom(*resp), nil
}

func ruleFrom(d hs.DirectiveResponse) Rule {
	// The default is active. A directive with no explicit flag is a rule that governs answers, and
	// reading an absent flag as false would silently turn off every rule a base ever created.
	return Rule{
		ID: d.GetId(), Name: d.GetName(), Text: d.GetContent(), Tags: d.GetTags(),
		Enabled: nullableBool(d.IsActive, true),
		Created: parseLoose(d.GetCreatedAt()), Updated: parseLoose(d.GetUpdatedAt()),
	}
}

// nullableBool reads a nullable flag, treating an absent one as the given default.
//
// The default matters: a directive with no explicit flag is a rule that governs answers, and
// reading an absence as false would silently turn off every rule a base ever created.
func nullableBool(v *bool, whenAbsent bool) bool {
	if v == nil {
		return whenAbsent
	}
	return *v
}

// nullableString reads a nullable string.
func nullableString(v hs.NullableString) string {
	if !v.IsSet() {
		return ""
	}
	got := v.Get()
	if got == nil {
		return ""
	}
	return *got
}

// CreateRule adds a directive.
func (c *Client) CreateRule(ctx context.Context, baseID, name, text string, tags []string, active *bool) (Rule, error) {
	body := hs.CreateDirectiveRequest{Name: name, Content: text, Tags: tags}
	if active != nil {
		body.IsActive = active
	}
	resp, httpResp, err := c.api.DirectivesAPI.CreateDirective(ctx, baseID).CreateDirectiveRequest(body).Execute()
	if err != nil {
		return Rule{}, Classify(err, httpResp, "creating a directive in base "+baseID)
	}
	return ruleFrom(*resp), nil
}

// RuleUpdate is a partial directive change. Every field is three-valued: absent leaves it, present
// sets it, and conflating the two is how a re-tag silently empties a field.
type RuleUpdate struct {
	Text    *string
	Tags    []string
	Enabled *bool
}

// UpdateRule changes a directive.
func (c *Client) UpdateRule(ctx context.Context, baseID, id string, update RuleUpdate) (Rule, error) {
	body := hs.UpdateDirectiveRequest{}
	if update.Text != nil {
		body.Content = *hs.NewNullableString(update.Text)
	}
	if update.Tags != nil {
		body.Tags = update.Tags
	}
	if update.Enabled != nil {
		body.IsActive = *hs.NewNullableBool(update.Enabled)
	}
	resp, httpResp, err := c.api.DirectivesAPI.UpdateDirective(ctx, baseID, id).UpdateDirectiveRequest(body).Execute()
	if err != nil {
		return Rule{}, Classify(err, httpResp, "updating directive "+id)
	}
	return ruleFrom(*resp), nil
}

// DeleteRule removes a directive.
func (c *Client) DeleteRule(ctx context.Context, baseID, id string) error {
	_, httpResp, err := c.api.DirectivesAPI.DeleteDirective(ctx, baseID, id).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == 404 {
			return nil
		}
		return Classify(err, httpResp, "deleting directive "+id)
	}
	return nil
}

// --- mental models.

// Model is a synthesized document kept and rewritten as the base changes.
type Model struct {
	ID          string
	Name        string
	SourceQuery string
	Text        string
	HasText     bool
	Tags        []string
	MaxTokens   int32
	UpdatedAt   time.Time
	// Stale reports that the model has not been refreshed since its inputs changed, which is
	// the difference between a model that is wrong and one that is merely behind.
	Stale         bool
	RefreshFailed time.Time
	// Trigger is what rebuilds it.
	Trigger knowledge.Trigger
}

// CreateModel creates a mental model directly, with no page in front of it.
func (c *Client) CreateModel(ctx context.Context, baseID, name, sourceQuery string, tags []string, trigger *knowledge.Trigger) (Model, error) {
	body := hs.CreateMentalModelRequest{Name: name, SourceQuery: sourceQuery, Tags: tags}
	_ = body
	if trigger != nil {
		body.Trigger = triggerInput(*trigger)
	}
	resp, httpResp, err := c.api.MentalModelsAPI.CreateMentalModel(ctx, baseID).CreateMentalModelRequest(body).Execute()
	if err != nil {
		return Model{}, Classify(err, httpResp, "creating a mental model in base "+baseID)
	}
	// A create returns only identifiers, and creating a model is an asynchronous operation. The
	// model is therefore read back by the identifier it reports, so a caller cannot be handed a
	// model this provider inferred from a different response shape than the one a read produces.
	id := resp.GetMentalModelId()
	if id == "" {
		return Model{}, nil
	}
	return c.GetModel(ctx, baseID, id)
}

func modelFrom(m hs.MentalModelResponse) Model {
	out := Model{
		ID: m.GetId(), Name: m.GetName(), SourceQuery: m.GetSourceQuery(), Tags: m.GetTags(),
		UpdatedAt: parseLoose(m.GetLastRefreshedAt()),
	}
	// The body is a nullable field, and nullable *because* a model that has never been
	// refreshed is empty. Reporting that as an absent body rather than as an empty one is the
	// difference between "this has not run" and "this says nothing".
	if content := m.GetContent(); strings.TrimSpace(content) != "" {
		out.Text, out.HasText = content, true
	}
	if m.GetLastRefreshFailedAt() != "" {
		out.RefreshFailed = parseLoose(m.GetLastRefreshFailedAt())
		out.Stale = true
	}
	// The trigger wrapper is only set when the model has one, and an absent trigger is
	// reported as absent rather than as the defaults: a model with no trigger is a model the
	// backend did not describe, and claiming it uses the defaults would be claiming a
	// configuration nobody chose.
	if m.Trigger.IsSet() {
		if trigger := m.Trigger.Get(); trigger != nil {
			out.Trigger = triggerFrom(*trigger)
		}
	}
	return out
}

// GetModel reads one mental model.
func (c *Client) GetModel(ctx context.Context, baseID, id string) (Model, error) {
	resp, httpResp, err := c.api.MentalModelsAPI.GetMentalModel(ctx, baseID, id).Execute()
	if err != nil {
		return Model{}, Classify(err, httpResp, "reading mental model "+id)
	}
	return modelFrom(*resp), nil
}

// ListModels returns the mental models a base holds.
func (c *Client) ListModels(ctx context.Context, baseID string, limit, offset int32) ([]Model, int32, error) {
	req := c.api.MentalModelsAPI.ListMentalModels(ctx, baseID)
	if limit > 0 {
		req = req.Limit(limit)
	}
	if offset > 0 {
		req = req.Offset(offset)
	}
	resp, httpResp, err := req.Execute()
	if err != nil {
		return nil, 0, Classify(err, httpResp, "listing the mental models of base "+baseID)
	}
	if resp == nil {
		return nil, 0, nil
	}
	out := make([]Model, 0, len(resp.GetItems()))
	for _, m := range resp.GetItems() {
		out = append(out, modelFrom(m))
	}
	return out, resp.GetTotal(), nil
}

// UpdateModel changes a model's question, tags or trigger — never its body.
//
// The body is regenerated from the question, and accepting one here would make a model's current
// beliefs something a caller chose rather than something the system derived.
func (c *Client) UpdateModel(ctx context.Context, baseID, id string, name, sourceQuery *string, tags []string, trigger *knowledge.Trigger) (Model, error) {
	body := hs.UpdateMentalModelRequest{}
	if name != nil {
		body.Name = *hs.NewNullableString(name)
	}
	if sourceQuery != nil {
		body.SourceQuery = *hs.NewNullableString(sourceQuery)
	}
	if tags != nil {
		body.Tags = tags
	}
	if trigger != nil {
		body.Trigger = *hs.NewNullableMentalModelTriggerInput(triggerInput(*trigger))
	}
	resp, httpResp, err := c.api.MentalModelsAPI.UpdateMentalModel(ctx, baseID, id).UpdateMentalModelRequest(body).Execute()
	if err != nil {
		return Model{}, Classify(err, httpResp, "updating mental model "+id)
	}
	return modelFrom(*resp), nil
}

// DeleteModel removes a model and the document it maintains.
func (c *Client) DeleteModel(ctx context.Context, baseID, id string) error {
	_, httpResp, err := c.api.MentalModelsAPI.DeleteMentalModel(ctx, baseID, id).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == 404 {
			return nil
		}
		return Classify(err, httpResp, "deleting mental model "+id)
	}
	return nil
}

// ClearModel empties a model's body, leaving the model and its configuration.
//
// It is a write even though it leaves a document in place, because what it destroys is derived
// content, and an empty model and a model that does not exist are different states with different
// next steps.
func (c *Client) ClearModel(ctx context.Context, baseID, id string) error {
	_, httpResp, err := c.api.MentalModelsAPI.ClearMentalModel(ctx, baseID, id).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == 404 {
			return nil
		}
		return Classify(err, httpResp, "clearing mental model "+id)
	}
	return nil
}

// ModelRevision is one refresh of a model.
type ModelRevision struct {
	Text    string
	At      time.Time
	Trigger string
	// Failed is reported, and a history that shows only successes reads as a model that has
	// never been wrong.
	Failed  bool
	Failure string
}

// ModelHistory returns a model's refresh history.
func (c *Client) ModelHistory(ctx context.Context, baseID, id string, limit, offset int32) ([]ModelRevision, error) {
	// The history endpoint takes no paging parameters, so the whole history comes back and this
	// package's page fields are applied to it. Reporting that here rather than silently paging
	// is the point: a caller that asks for five revisions of a model with two hundred of them
	// would otherwise be told it has two.
	req := c.api.MentalModelsAPI.GetMentalModelHistory(ctx, baseID, id)
	raw, httpResp, err := req.Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "reading the refresh history of mental model "+id)
	}
	// Untyped: the endpoint declares no response schema, so the same inference the fact read
	// makes is made here and the shape is read defensively. A history that gained a field is
	// not a failure.
	var page struct {
		Items []struct {
			Content      string `json:"content"`
			CreatedAt    string `json:"created_at"`
			Trigger      string `json:"trigger"`
			ErrorMessage string `json:"error_message"`
		} `json:"items"`
	}
	if rerr := remarshal(raw, &page); rerr != nil {
		return nil, &api.Error{
			Kind:    api.KindInternal,
			Message: "the backend returned a refresh history this build cannot read; the endpoint declares no schema for it",
			Err:     rerr,
		}
	}
	out := make([]ModelRevision, 0, len(page.Items))
	for _, r := range page.Items {
		rev := ModelRevision{Text: r.Content, At: parseLoose(r.CreatedAt), Trigger: r.Trigger}
		if r.ErrorMessage != "" {
			rev.Failed, rev.Failure = true, r.ErrorMessage
		}
		out = append(out, rev)
	}
	return out, nil
}

// RefreshModel rebuilds a model, and returns the operation doing it.
func (c *Client) RefreshModel(ctx context.Context, baseID, id string, full bool) (string, error) {
	// The endpoint takes no parameters: whether a refresh is full or incremental is a property of
	// the model's trigger, and the caller sets it there. A flag here would be a second place to
	// change the same thing and a way for the two to disagree.
	_ = full
	req := c.api.MentalModelsAPI.RefreshMentalModel(ctx, baseID, id)
	resp, httpResp, err := req.Execute()
	if err != nil {
		return "", Classify(err, httpResp, "refreshing mental model "+id)
	}
	if resp == nil {
		return "", nil
	}
	if v := resp.GetOperationId(); v != "" {
		return v, nil
	}
	return "", nil
}

// PreviewModelRefresh reports what a refresh would use, and does nothing.
//
// A refresh spends a model call, and the thing worth seeing is what the page would be built from: a
// page whose trigger matches nothing is a page that will stay empty however many times it is
// refreshed, and that is invisible until somebody refreshes it and gets nothing.
func (c *Client) PreviewModelRefresh(ctx context.Context, baseID, id string) (tags, factTypes []string, stale bool, err error) {
	resp, httpResp, err := c.api.MentalModelsAPI.DryRunRefreshMentalModel(ctx, baseID, id).Execute()
	if err != nil {
		return nil, nil, false, Classify(err, httpResp, "previewing a refresh of mental model "+id)
	}
	if resp == nil {
		return nil, nil, false, nil
	}
	// The dry run reports an outcome and a scope rather than a list of inputs. The outcome is
	// the useful half: it says whether the refresh would change anything, which is the question
	// a preview is asked, and a list of inputs would say it without answering that.
	_ = resp
	return nil, nil, resp.GetWouldPersist(), nil
}

// --- operations.

// Job is a unit of asynchronous work.
type Job struct {
	ID     string
	BaseID string
	Kind   string
	Status string
	// Progress is nil when the backend reports none, and nil means "no idea" rather than
	// "just started" — the two are different states and a zero would conflate them.
	Progress   *int32
	ItemsTotal int32
	ItemsDone  int32
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Error      string
	// Retryable reports whether the backend says a retry could succeed, which is a different
	// question from whether this package would offer one.
	Retryable  bool
	RetryCount int32
}

// ListJobs returns a base's asynchronous work, newest first.
func (c *Client) ListJobs(ctx context.Context, baseID string, limit, offset int32) ([]Job, int32, error) {
	req := c.api.OperationsAPI.ListOperations(ctx, baseID)
	if limit > 0 {
		req = req.Limit(limit)
	}
	if offset > 0 {
		req = req.Offset(offset)
	}
	resp, httpResp, err := req.Execute()
	if err != nil {
		return nil, 0, Classify(err, httpResp, "listing the operations of base "+baseID)
	}
	if resp == nil {
		return nil, 0, nil
	}
	out := make([]Job, 0, len(resp.GetOperations()))
	for _, o := range resp.GetOperations() {
		job := Job{
			ID: o.GetId(), BaseID: resp.GetBankId(), Kind: o.GetTaskType(),
			Status: o.GetStatus(), CreatedAt: parseLoose(o.GetCreatedAt()),
			FinishedAt: parseLoose(o.GetUpdatedAt()),
			Error:      o.GetErrorMessage(),
			ItemsTotal: o.GetItemsCount(),
		}
		job.Progress, job.ItemsDone, job.ItemsTotal = progressOf(o.GetProgress())
		job.RetryCount = o.GetRetryCount()
		job.Retryable = job.Status == "failed" && job.RetryCount == 0
		out = append(out, job)
	}
	return out, resp.GetTotal(), nil
}

// GetJob reads one operation's status.
func (c *Client) GetJob(ctx context.Context, baseID, id string) (Job, error) {
	resp, httpResp, err := c.api.OperationsAPI.GetOperationStatus(ctx, baseID, id).Execute()
	if err != nil {
		return Job{}, Classify(err, httpResp, "reading operation "+id)
	}
	if resp == nil {
		return Job{}, nil
	}
	job := Job{
		ID: resp.GetOperationId(), BaseID: baseID, Kind: resp.GetOperationType(),
		Status: resp.GetStatus(), Error: resp.GetErrorMessage(),
		CreatedAt:  parseLoose(resp.GetCreatedAt()),
		StartedAt:  parseLoose(resp.GetUpdatedAt()),
		FinishedAt: parseLoose(resp.GetCompletedAt()),
	}
	// A failed operation with a recorded retry count has been retried and the backend schedules
	// the next one. Reporting it as retryable would invite a caller to start a duplicate of
	// work the backend is already going to do.
	job.RetryCount = resp.GetRetryCount()
	job.Retryable = job.Status == "failed" && job.RetryCount == 0
	job.Progress, job.ItemsDone, job.ItemsTotal = progressOf(resp.GetProgress())
	return job, nil
}

// CancelJob asks for an operation to stop, and reports what the backend said.
//
// It asks rather than stops: the work is in the backend and a cancel is a request it may not honour
// immediately. "I asked and it was already finished" is a different answer from "it was cancelled",
// and conflating them leaves a caller believing a job is stopped when it is not.
func (c *Client) CancelJob(ctx context.Context, baseID, id string) (accepted bool, note string, err error) {
	resp, httpResp, err := c.api.OperationsAPI.CancelOperation(ctx, baseID, id).Execute()
	if err != nil {
		if httpResp != nil && (httpResp.StatusCode == 404 || httpResp.StatusCode == 409) {
			return false, "the operation does not exist or has already finished", nil
		}
		return false, "", Classify(err, httpResp, "cancelling operation "+id)
	}
	if resp == nil {
		return true, "", nil
	}
	return resp.GetSuccess(), resp.GetMessage(), nil
}

// RetryJob starts a failed operation again and returns the new one.
func (c *Client) RetryJob(ctx context.Context, baseID, id string) (Job, error) {
	resp, httpResp, err := c.api.OperationsAPI.RetryOperation(ctx, baseID, id).Execute()
	if err != nil {
		return Job{}, Classify(err, httpResp, "retrying operation "+id)
	}
	if resp == nil {
		return Job{}, nil
	}
	return Job{
		ID: resp.GetOperationId(), BaseID: baseID, Status: "pending",
		// The retry succeeded and produced *new* work with a new identifier. It is returned as
		// pending because that is what it is, and a caller following the original operation
		// would otherwise be told about work that is no longer happening.
		CreatedAt: time.Now().UTC(),
	}, nil
}

// DeleteJob removes an operation's record.
func (c *Client) DeleteJob(ctx context.Context, baseID, id string) error {
	_, httpResp, err := c.api.OperationsAPI.DeleteOperation(ctx, baseID, id).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == 404 {
			return nil
		}
		return Classify(err, httpResp, "deleting operation "+id)
	}
	return nil
}

// --- templates and observations.

// Manifest is a base's configuration as a portable, versioned document.
//
// It is configuration and content *definitions* — directives and the questions for models — and not
// content. The corpus is in a repository and the derived half is not portable at all: its identifiers
// are a function of extraction, so a "backup" that included it would be a backup of things nobody
// can restore.
type Manifest struct {
	Version      string
	Config       map[string]any
	Directives   []ManifestDirective
	Models       []ManifestModel
	Raw          any
	Operations   []string
	Observations []ObservationScope
}

// ManifestDirective is a directive a manifest defines.
type ManifestDirective struct {
	Name    string
	Content string
	Tags    []string
	Active  *bool
}

// ManifestModel is a mental model a manifest defines.
type ManifestModel struct {
	Name        string
	SourceQuery string
	Tags        []string
	Trigger     *knowledge.Trigger
}

// ExportManifest reads a base's configuration as a manifest.
func (c *Client) ExportManifest(ctx context.Context, baseID string) (Manifest, error) {
	resp, httpResp, err := c.api.BankTemplatesAPI.ExportBankTemplate(ctx, baseID).Execute()
	if err != nil {
		return Manifest{}, Classify(err, httpResp, "exporting base "+baseID+" as a template")
	}
	if resp == nil {
		return Manifest{}, nil
	}
	return manifestFrom(*resp), nil
}

func manifestFrom(m hs.BankTemplateManifest) Manifest {
	out := Manifest{Version: m.GetVersion()}
	// A manifest's bank block is a typed structure rather than a free-form map, and it is far
	// larger than this contract models. The fields that change how a corpus is reconciled are
	// read from it; the whole manifest is also kept as JSON so that a caller can carry fields
	// this package does not know about through a round trip without losing them.
	out.Config = bankConfigMap(m.GetBank())
	for _, d := range m.GetDirectives() {
		out.Directives = append(out.Directives, ManifestDirective{
			Name: d.GetName(), Content: d.GetContent(), Tags: d.GetTags(), Active: d.IsActive,
		})
	}
	for _, mm := range m.GetMentalModels() {
		entry := ManifestModel{Name: mm.GetName(), SourceQuery: mm.GetSourceQuery(), Tags: mm.GetTags()}
		if trigger := mm.Trigger; trigger != nil {
			t := triggerFrom(*trigger)
			entry.Trigger = &t
		}
		out.Models = append(out.Models, entry)
	}
	return out
}

// Applied is what a manifest import did.
//
// The backend reports this rather than leaving it to be inferred: a directive that already existed
// is *updated* and one that did not is *created*, and a caller told a directive was created when it
// was updated would conclude a rule they had written had been replaced.
type Applied struct {
	BaseID        string
	ConfigApplied bool
	ModelsCreated []string
	ModelsUpdated []string
	// RulesCreated and RulesUpdated are directives. They are not named `directives` because
	// this package already uses `Rule` for a directive and `ClaimedScope` for a proposal's
	// arithmetic, and a third name that reads like the other two is a trap.
	RulesCreated []string
	RulesUpdated []string
}

// ImportManifest applies a manifest to a base, creating it if it does not exist.
//
// The content it defines is generated, which is the difference from a corpus write: a template
// defines what the system should know and what rules should govern its reasoning, and the text of
// every document it creates is the system's own.
func (c *Client) ImportManifest(ctx context.Context, baseID string, manifest Manifest) (Applied, error) {
	// The import takes a whole manifest, and the manifest's bank block is a nullable wrapper
	// around a typed struct, so the value is built first and wrapped at the end.
	body := hs.BankTemplateManifest{Version: manifest.Version}
	if manifest.Config != nil {
		// Only the fields this package models are carried across. Writing a whole config map
		// through a typed struct would mean claiming every field, and a field this build does
		// not know about would be dropped on the round trip — which is the one thing a
		// configuration export must not do.
		bank := &hs.BankTemplateConfig{}
		if aerr := applyConfigMap(bank, manifest.Config); aerr != nil {
			return Applied{}, aerr
		}
		body.Bank = *hs.NewNullableBankTemplateConfig(bank)
	}
	for _, d := range manifest.Directives {
		body.Directives = append(body.Directives, hs.BankTemplateDirective{
			Name: d.Name, Content: d.Content, Tags: d.Tags, IsActive: d.Active,
		})
	}
	for _, m := range manifest.Models {
		entry := hs.BankTemplateMentalModel{Name: m.Name, SourceQuery: m.SourceQuery, Tags: m.Tags}
		if m.Trigger != nil {
			entry.Trigger = triggerOutput(*m.Trigger)
		}
		body.MentalModels = append(body.MentalModels, entry)
	}
	resp, httpResp, err := c.api.BankTemplatesAPI.ImportBankTemplate(ctx, baseID).BankTemplateManifest(body).Execute()
	if err != nil {
		return Applied{}, Classify(err, httpResp, "importing a template into base "+baseID)
	}
	if resp == nil {
		return Applied{BaseID: baseID}, nil
	}
	// `config_applied` is read rather than assumed. A manifest whose configuration was
	// refused while its directives went in leaves a base with new rules and old settings, and
	// nothing in the base would say the two disagree.
	return Applied{
		BaseID:        resp.GetBankId(),
		ConfigApplied: resp.GetConfigApplied(),
		ModelsCreated: resp.GetMentalModelsCreated(),
		ModelsUpdated: resp.GetMentalModelsUpdated(),
		RulesCreated:  resp.GetDirectivesCreated(),
		RulesUpdated:  resp.GetDirectivesUpdated(),
	}, nil
}

// TemplateSchemaField is one field a template may carry.
type TemplateSchemaField struct {
	Name        string
	Type        string
	Required    bool
	Description string
	Default     string
}

// TemplateSchema reports the fields a template may carry.
func (c *Client) TemplateSchema(ctx context.Context) ([]TemplateSchemaField, string, error) {
	// Untyped: the endpoint declares no schema, so the same inference is made as elsewhere and
	// read defensively. A schema this build cannot read is a schema a caller cannot validate
	// against, which is why the error says so rather than returning an empty field list.
	raw, httpResp, err := c.api.BankTemplatesAPI.GetBankTemplateSchema(ctx).Execute()
	if err != nil {
		return nil, "", Classify(err, httpResp, "reading the template schema")
	}
	var page struct {
		Version string `json:"version"`
		Fields  []struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Required    bool   `json:"required"`
			Description string `json:"description"`
			Default     string `json:"default"`
		} `json:"fields"`
	}
	if rerr := remarshal(raw, &page); rerr != nil {
		return nil, "", &api.Error{
			Kind:    api.KindInternal,
			Message: "the backend returned a template schema this build cannot read; the endpoint declares no schema for it, and a caller cannot validate a template against a schema it could not read",
			Err:     rerr,
		}
	}
	out := make([]TemplateSchemaField, 0, len(page.Fields))
	for _, f := range page.Fields {
		out = append(out, TemplateSchemaField{
			Name: f.Name, Type: f.Type, Required: f.Required,
			Description: f.Description, Default: f.Default,
		})
	}
	return out, page.Version, nil
}

// ObservationScope is one consolidation scope, and how many consolidation passes a tagged memory
// gets.
//
// It matters more than it looks: a corpus is mostly provenance tags, and a scope that runs one pass
// per tag fragments observations according to them.
type ObservationScope struct {
	ID      string
	Mode    string
	Tags    []string
	Count   int32
	LastRun time.Time
}

// ListObservationScopes reports the consolidation scopes in use.
func (c *Client) ListObservationScopes(ctx context.Context, baseID string) ([]ObservationScope, error) {
	resp, httpResp, err := c.api.MemoryAPI.ListObservationScopes(ctx, baseID).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "listing the observation scopes of base "+baseID)
	}
	if resp == nil {
		return nil, nil
	}
	var out []ObservationScope
	for _, s := range resp.GetScopes() {
		// A scope has tags and a count and no name of its own. Its identity is what it filters
		// on, so a scope with no tags is reported as the global one rather than as an empty
		// string that looks like a scope nothing consolidated into.
		scope := ObservationScope{Tags: s.GetTags(), Count: s.GetCount()}
		switch {
		case len(scope.Tags) == 0:
			scope.ID, scope.Mode = "(global)", "shared"
		case len(scope.Tags) == 1:
			scope.ID, scope.Mode = scope.Tags[0], "per_tag"
		default:
			scope.ID, scope.Mode = strings.Join(scope.Tags, "+"), "combined"
		}
		out = append(out, scope)
	}
	return out, nil
}

// TriggerConsolidation starts a consolidation pass and returns its operation.
//
// The backend takes a list of *scopes* rather than a scope name and a set of facts, which is a
// different shape from what a caller asked for and worth being explicit about: the scopes say which
// grouping of tags gets its own pass, and the fact selection is a property of the scope rather than
// something sent alongside it. So a caller that named one scope sends one scope, and the facts are
// the ones in it.
func (c *Client) TriggerConsolidation(ctx context.Context, baseID string, scopes [][]string) (string, error) {
	body := hs.ConsolidationRequest{ObservationScopes: scopes}
	if len(scopes) == 0 {
		// No scopes at all is not "every scope" to this endpoint: an empty list consolidates
		// nothing. The base's existing scopes are what a caller means by "consolidate this
		// base", so they are read and sent explicitly rather than left to be interpreted.
		known, lerr := c.ListObservationScopes(ctx, baseID)
		if lerr != nil {
			return "", lerr
		}
		for _, scope := range known {
			tags := scope.Tags
			if len(tags) == 0 {
				tags = []string{""}
			}
			body.ObservationScopes = append(body.ObservationScopes, tags)
		}
	}
	resp, httpResp, err := c.api.BanksAPI.TriggerConsolidation(ctx, baseID).ConsolidationRequest(body).Execute()
	if err != nil {
		return "", Classify(err, httpResp, "triggering a consolidation in base "+baseID)
	}
	if resp == nil {
		return "", nil
	}
	return resp.GetOperationId(), nil
}

// RecoverConsolidation resumes an interrupted pass.
//
// It is separate from triggering a new one because the two answer different questions: a new pass
// starts from scratch, and recovery resumes one that stopped half-way. Starting a new one to achieve
// the second is how a base loses the work the first had already done. It also returns no operation,
// because the backend reports how many scopes it retried rather than what it is doing — so a caller
// polls the base's scopes rather than following a job.
func (c *Client) RecoverConsolidation(ctx context.Context, baseID string) (int32, error) {
	resp, httpResp, err := c.api.BanksAPI.RecoverConsolidation(ctx, baseID).Execute()
	if err != nil {
		return 0, Classify(err, httpResp, "recovering a consolidation in base "+baseID)
	}
	if resp == nil {
		return 0, nil
	}
	return resp.GetRetriedCount(), nil
}

// ClearObservations removes a base's observations, keeping its facts.
//
// It is the middle of the three clear operations and the one an operator actually reaches for:
// facts are expensive to produce and cheap to keep, observations are the other way round, and
// dropping the observations is what makes derived beliefs be rebuilt from facts that are still there.
func (c *Client) ClearObservations(ctx context.Context, baseID string) (int32, error) {
	req := c.api.BanksAPI.ClearObservations(ctx, baseID)
	resp, httpResp, err := req.Execute()
	if err != nil {
		return 0, Classify(err, httpResp, "clearing the observations of base "+baseID)
	}
	if resp == nil {
		return 0, nil
	}
	return resp.GetDeletedCount(), nil
}

// Strategy is a candidate consolidation strategy: what a pass would consolidate, and over which
// facts.
type Strategy struct {
	Mission string
	// Scopes are patterns over the tag namespace. These are the flat pattern the backend's own
	// consolidation scope uses and not the recursive boolean tree a retrieval filter uses,
	// because a consolidation scope cannot be a tree — it is one set of tags per scope.
	Scopes []ScopePattern
	// MaxObservationsPerScope is the cap the strategy would apply. Zero is the backend's own
	// and is sent as absent rather than as zero, which would be a cap of no observations.
	MaxObservationsPerScope int32
}

// ScopePattern is one set of tags and how they combine.
type ScopePattern struct {
	TagsMatch string
	Tags      []string
}

// StrategyClaim is what one candidate strategy would claim.
type StrategyClaim struct {
	// Index is the strategy's position in the request. The backend identifies strategies by
	// index and carries no name, so the index is the only way a caller can line a claim up
	// with the strategy that produced it — which is the whole use of a preview.
	Index int32
	// Active reports whether the strategy would run. An inactive one claims nothing, and
	// reporting its zero alongside an active one's zero would read as equal.
	Active bool
	// FactsClaimed is how many facts this strategy would take. Two strategies claiming the
	// same fact is fragmentation, and it is visible here rather than discovered in the
	// observations afterwards.
	FactsClaimed int32
	Rules        []ClaimedScope
}

// ClaimedScope is one scope a strategy would consolidate, with what it would do with it.
//
// It is not the same thing as a `Rule`, which is a directive: this is a count of what a proposal
// would take, and a directive is a sentence a person wrote that the reasoning step must follow.
// Sharing a name would make a preview's arithmetic look like a policy.
type ClaimedScope struct {
	// Matched is how many facts the scope selects, and Taken is how many the strategy would
	// keep. They differ when a strategy discards, and a caller reading only the taken count
	// would not know the scope was mostly noise.
	Matched int32
	Taken   int32
	// Observations is how many observations the strategy would produce. Zero is worth seeing:
	// it is a scope that matched and was dropped, which is where a strategy is quietly doing
	// nothing.
	Observations int32
	// Scopes names the scopes themselves, which is how a caller finds the one that matched
	// nothing.
	Scopes []string
}

// StrategyPreview is what a set of candidate strategies would do.
type StrategyPreview struct {
	Claims []StrategyClaim
	// ScopesScanned is the denominator a claim is a fraction of.
	ScopesScanned int32
	// Complete reports whether the backend had what it needed. A preview that stopped early
	// says so, because a claim computed from half a base is a number about half a base.
	Complete bool
}

// PreviewConsolidation reports what candidate strategies would claim, without consolidating.
//
// It is a preview of a *proposal* rather than of a base, which is why it takes strategies: the
// backend's endpoint answers "given these strategies, which facts would each take", and asking it
// without a proposal would mean asking it to supply the answer being checked.
//
// It cannot report the observations a pass would produce. Consolidation is an LLM pass, and
// predicting its output without running it would be inventing it.
func (c *Client) PreviewConsolidation(ctx context.Context, baseID string, strategies []Strategy) (StrategyPreview, error) {
	if len(strategies) == 0 {
		return StrategyPreview{}, errInvalid(
			"a preview names the strategies it is previewing. An empty set is not \"whatever the backend would default to\": the claims are a function of the strategies, and a preview of nothing is a preview of a decision nobody made")
	}
	req := hs.ConsolidationStrategiesPreviewRequest{}
	for _, s := range strategies {
		spec := hs.ConsolidationStrategySpec{}
		if s.Mission != "" {
			spec.ObservationsMission = *hs.NewNullableString(&s.Mission)
		}
		for _, scope := range s.Scopes {
			pattern := hs.ConsolidationScopePattern{Tags: scope.Tags}
			if scope.TagsMatch != "" {
				pattern.TagsMatch = *hs.NewNullableString(&scope.TagsMatch)
			}
			spec.Scopes = append(spec.Scopes, pattern)
		}
		if s.MaxObservationsPerScope > 0 {
			spec.MaxObservationsPerScope = *hs.NewNullableInt32(&s.MaxObservationsPerScope)
		}
		req.Strategies = append(req.Strategies, spec)
	}
	resp, httpResp, err := c.api.MemoryAPI.PreviewConsolidationStrategies(ctx, baseID).ConsolidationStrategiesPreviewRequest(req).Execute()
	if err != nil {
		return StrategyPreview{}, Classify(err, httpResp, "previewing a consolidation in base "+baseID)
	}
	if resp == nil {
		return StrategyPreview{}, nil
	}
	out := StrategyPreview{ScopesScanned: resp.GetScopesScanned(), Complete: resp.GetComplete()}
	for i, s := range resp.GetStrategies() {
		claim := StrategyClaim{Index: int32(i), Active: s.GetActive(), FactsClaimed: s.GetClaimedCount()}
		for _, r := range s.GetRules() {
			scope := ClaimedScope{
				Matched: r.GetMatchCount(), Taken: r.GetTakenCount(), Observations: r.GetObservationCount(),
			}
			for _, sample := range r.GetSamples() {
				// A sample scope carries its tags and how many facts it holds. The tags
				// are joined rather than kept as a list because a scope is a set and a
				// set printed as one string is what a rule reads like in the backend's own
				// output — and matching it is the caller's next step.
				name := strings.Join(sample.GetTags(), "+")
				if name == "" {
					name = "(untagged)"
				}
				scope.Scopes = append(scope.Scopes, fmt.Sprintf("%s (%d)", name, sample.GetCount()))
			}
			claim.Rules = append(claim.Rules, scope)
		}
		out.Claims = append(out.Claims, claim)
	}
	return out, nil
}

// bankConfigMap reads the fields this contract models out of a manifest's typed bank block.
//
// The map is deliberately partial. A configuration export that carried only what this build knows
// would silently drop a field a caller had set, and a configuration round trip that drops fields is
// worse than one that does not claim to be complete.
func bankConfigMap(cfg hs.BankTemplateConfig) map[string]any {
	out := map[string]any{}
	// The getters already unwrap the nullable and return the value, so an empty string is the
	// only "not set" signal available. It is carried as an absent key rather than as "", because
	// an absent field and an empty one mean different things in a three-valued update.
	put := func(key, v string) {
		if v != "" {
			out[key] = v
		}
	}
	put("reflect_mission", cfg.GetReflectMission())
	put("retain_mission", cfg.GetRetainMission())
	put("retain_extraction_mode", cfg.GetRetainExtractionMode())
	put("retain_custom_instructions", cfg.GetRetainCustomInstructions())
	put("retain_default_strategy", cfg.GetRetainDefaultStrategy())
	put("observations_mission", cfg.GetObservationsMission())
	put("recall_budget_function", cfg.GetRecallBudgetFunction())
	if cfg.EntitiesAllowFreeForm.IsSet() {
		out["entities_allow_free_form"] = cfg.EntitiesAllowFreeForm.Get()
	}
	if cfg.EnableTextSearch.IsSet() {
		out["enable_text_search"] = cfg.EnableTextSearch.Get()
	}
	if cfg.EnableObservations.IsSet() {
		out["enable_observations"] = cfg.EnableObservations.Get()
	}
	if cfg.StoreDocumentText.IsSet() {
		out["store_document_text"] = cfg.StoreDocumentText.Get()
	}
	if cfg.EnableAutoConsolidation.IsSet() {
		out["enable_auto_consolidation"] = cfg.EnableAutoConsolidation.Get()
	}
	if len(cfg.RetainStrategies) > 0 {
		out["retain_strategies"] = cfg.RetainStrategies
	}
	return out
}

// applyConfigMap writes the modelled fields back into a manifest's bank block, and refuses keys it
// does not model rather than dropping them.
//
// Refusing is the right answer for a field this build cannot represent: silently ignoring it would
// report a successful import that did not apply what the caller asked for, which is the one outcome
// worse than an error.
func applyConfigMap(cfg *hs.BankTemplateConfig, in map[string]any) error {
	known := map[string]func(any) error{
		"reflect_mission":            func(v any) error { return setString(&cfg.ReflectMission, v) },
		"retain_mission":             func(v any) error { return setString(&cfg.RetainMission, v) },
		"retain_extraction_mode":     func(v any) error { return setString(&cfg.RetainExtractionMode, v) },
		"retain_custom_instructions": func(v any) error { return setString(&cfg.RetainCustomInstructions, v) },
		"retain_default_strategy":    func(v any) error { return setString(&cfg.RetainDefaultStrategy, v) },
		"observations_mission":       func(v any) error { return setString(&cfg.ObservationsMission, v) },
		"recall_budget_function":     func(v any) error { return setString(&cfg.RecallBudgetFunction, v) },
		"entities_allow_free_form":   func(v any) error { return setBool(&cfg.EntitiesAllowFreeForm, v) },
		"enable_text_search":         func(v any) error { return setBool(&cfg.EnableTextSearch, v) },
		"enable_observations":        func(v any) error { return setBool(&cfg.EnableObservations, v) },
		"store_document_text":        func(v any) error { return setBool(&cfg.StoreDocumentText, v) },
		"enable_auto_consolidation":  func(v any) error { return setBool(&cfg.EnableAutoConsolidation, v) },
		"retain_strategies": func(v any) error {
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("retain_strategies is an object, not %T", v)
			}
			cfg.RetainStrategies = m
			return nil
		},
	}
	var unknown []string
	for key, value := range in {
		apply, ok := known[key]
		if !ok {
			unknown = append(unknown, key)
			continue
		}
		if err := apply(value); err != nil {
			return fmt.Errorf("the configuration field %q: %w", key, err)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return &api.Error{
			Kind: api.KindUnsupported,
			Message: fmt.Sprintf(
				"this build cannot apply %s. It is refused rather than ignored: a template import that reported success while dropping fields would leave a base configured differently from what the caller asked for, and nothing in the base would say so",
				strings.Join(quoteAll(unknown), ", ")),
		}
	}
	return nil
}

func setString(target *hs.NullableString, v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("expected a string, got %T", v)
	}
	*target = *hs.NewNullableString(&s)
	return nil
}

func setBool(target *hs.NullableBool, v any) error {
	b, ok := v.(bool)
	if !ok {
		return fmt.Errorf("expected true or false, got %T", v)
	}
	*target = *hs.NewNullableBool(&b)
	return nil
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// progressOf turns a progress report into a percentage and a count.
//
// A total of zero means the backend reported no progress rather than that nothing is done, and the
// two are different states. Returning nil for the percentage keeps them apart: a caller polling a job
// sees "no idea yet" instead of a confident zero.
func progressOf(p hs.OperationProgress) (*int32, int32, int32) {
	processed, total := p.GetProcessed(), p.GetTotal()
	if total <= 0 {
		return nil, 0, 0
	}
	pct := processed * 100 / total
	return &pct, processed, total
}

// operationIDOf reads the operation an asynchronous submit reported, or the empty string.
//
// An endpoint that submits a *list* of operations returns several identifiers and there is no one
// to report; taking the first would attribute a caller's work to an operation that is only part of
// it. The empty string is returned and the caller says so rather than guessing.
func operationIDOf(resp *hs.AsyncOperationSubmitResponse) string {
	return strings.TrimSpace(resp.GetOperationId())
}

var _ = fmt.Sprintf
var _ = api.KindInternal
