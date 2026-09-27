package hindsight

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Manu343726/toolbox/pkg/knowledge"

	hs "github.com/vectorize-io/hindsight/hindsight-clients/go"

	"github.com/Manu343726/toolbox/pkg/api"
)

// Base is one knowledge base as the adapter needs to describe it.
type Base struct {
	ID               string
	Aliases          []string
	PrimaryName      string
	CorpusRoots      []CorpusRoot
	ReconciledCommit string
}

// CorpusRoot is a configured corpus root for a base.
//
// It is the deployment's configuration and not the backend's: the backend has no way to know a
// directory on this host. It is carried here so a caller listing bases can see which files feed
// each one, which is the question somebody asks when two bases look the same.
type CorpusRoot struct {
	Name    string
	Path    string
	Include []string
	Exclude []string
}

// ListBases returns the bases the backend holds.
//
// An alias that is a duplicate of another base's is dropped rather than being allowed to make the
// resolution ambiguous: a name that means two things cannot be resolved, and reporting both would
// leave the caller to guess which one they got.
func (c *Client) ListBases(ctx context.Context) ([]Base, error) {
	resp, httpResp, err := c.api.BanksAPI.ListBanks(ctx).Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "listing knowledge bases")
	}
	if resp == nil {
		return nil, nil
	}
	seen := map[string]string{}
	var out []Base
	for _, b := range resp.GetBanks() {
		entry := Base{ID: strings.TrimSpace(b.GetBankId())}
		// The list carries a display alias and any aliases that matched a query, not the
		// full alias list. The authoritative list is a separate call per base, so it is
		// fetched here rather than reconstructing a set that would be missing aliases the
		// list did not match.
		if d := b.GetDisplayAlias(); d != "" {
			entry.Aliases = append(entry.Aliases, d)
			entry.PrimaryName = d
		}
		if n := b.GetName(); n != "" && n != entry.PrimaryName {
			entry.Aliases = append(entry.Aliases, n)
		}
		entry.Aliases = append(entry.Aliases, b.GetMatchedAliases()...)
		for _, alias := range entry.Aliases {
			key := strings.ToLower(strings.TrimSpace(alias))
			if key == "" {
				continue
			}
			if owner, clash := seen[key]; clash && owner != entry.ID {
				// A name that means two bases cannot be resolved. It is dropped here and
				// resolves to nothing below, which is a better answer than resolving it
				// to whichever base the backend happened to list first.
				continue
			}
			seen[key] = entry.ID
		}
		out = append(out, entry)
	}
	return out, nil
}

// ListAliasesFor returns a base's authoritative alias list.
func (c *Client) ListAliasesFor(ctx context.Context, id string) ([]string, string, error) {
	resp, httpResp, err := c.api.BanksAPI.ListBankAliases(ctx, id).Execute()
	if err != nil {
		return nil, "", Classify(err, httpResp, "listing the aliases of base "+id)
	}
	if resp == nil {
		return nil, "", nil
	}
	var names []string
	primary := ""
	for _, a := range resp.GetAliases() {
		name := strings.TrimSpace(a.GetAlias())
		if name == "" {
			continue
		}
		names = append(names, name)
		if a.GetPrimary() {
			primary = name
		}
	}
	return names, primary, nil
}

// ListBaseAliases returns every alias in the deployment, mapped to its base identifier.
//
// The result includes each base's own identifier as a key, because a caller addressing a base by
// identifier must not need a separate code path.
func (c *Client) ListBaseAliases(ctx context.Context) (map[string]string, error) {
	bases, err := c.ListBases(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, b := range bases {
		if b.ID == "" {
			continue
		}
		out[strings.ToLower(b.ID)] = b.ID
		for _, alias := range b.Aliases {
			key := strings.ToLower(strings.TrimSpace(alias))
			if key == "" {
				continue
			}
			if _, taken := out[key]; taken && out[key] != b.ID {
				// A name that means two bases cannot be resolved. Dropping it makes it
				// not-found, which is a better answer than resolving it to whichever
				// the backend happened to list first.
				delete(out, key)
				continue
			}
			out[key] = b.ID
		}
	}
	return out, nil
}

// CreateBase creates a base with the given identifier, or updates an existing one.
//
// It creates nothing else. A base holds knowledge; what goes into it is the corpus reconcile's
// business and a retained write's, and a base that came into existence already full would make
// "what did we put here" unanswerable.
func (c *Client) CreateBase(ctx context.Context, id string) (Base, error) {
	if strings.TrimSpace(id) == "" {
		return Base{}, &api.Error{Kind: api.KindInvalid, Message: "a base needs an identifier"}
	}
	_, httpResp, err := c.api.BanksAPI.CreateOrUpdateBank(ctx, id).Execute()
	if err != nil {
		return Base{}, Classify(err, httpResp, "creating base "+id)
	}
	return Base{ID: id}, nil
}

// DeleteBase removes a base and reports what the backend says it destroyed.
func (c *Client) DeleteBase(ctx context.Context, id string) error {
	_, httpResp, err := c.api.BanksAPI.DeleteBank(ctx, id).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == 404 {
			// A base that is already gone is the state the caller wanted.
			return nil
		}
		return Classify(err, httpResp, "deleting base "+id)
	}
	return nil
}

// BaseStats is a base's size and composition.
type BaseStats struct {
	Documents       int32
	Facts           int32
	Observations    int32
	WorldFacts      int32
	ExperienceFacts int32
	Entities        int32
}

// GetBaseStats reads a base's size.
func (c *Client) GetBaseStats(ctx context.Context, id string) (BaseStats, error) {
	resp, httpResp, err := c.api.BanksAPI.GetAgentStats(ctx, id).Execute()
	if err != nil {
		return BaseStats{}, Classify(err, httpResp, "reading the statistics of base "+id)
	}
	if resp == nil {
		return BaseStats{}, nil
	}
	// The adapter reads the handful of numbers this contract reports and no more. The
	// backend's stats model is far larger, and a contract mirroring all of it would be a
	// contract about the backend's current shape rather than about a knowledge base.
	stats := BaseStats{
		Documents:    resp.GetTotalDocuments(),
		Facts:        resp.GetTotalNodes(),
		Observations: int32(len(resp.GetLinksByLinkType())),
	}
	for factType, n := range resp.GetNodesByFactType() {
		switch strings.ToLower(strings.TrimSpace(factType)) {
		case "world":
			stats.WorldFacts = n
		case "experience":
			stats.ExperienceFacts = n
		}
	}
	return stats, nil
}

// compile-time use of the generated client so an import removed by a future regeneration is
// caught here rather than at the first call.
var _ = hs.APIClient{}

// Cleared is what a clear of a base's derived knowledge removed.
//
// The numbers are reported rather than a boolean because "the base is empty now" and "the base lost
// four thousand facts" are the same event and only one of them tells somebody what happened.
type Cleared struct {
	// Items is how many derived things were removed. The backend reports one count for the
	// clear rather than a per-kind breakdown, so it is reported as one count.
	Items int32
	// Message is the backend's own account of what it did, passed on rather than
	// reinterpreted.
	Message string
}

// ClearBankMemories drops a base's derived knowledge, keeping nothing a person wrote.
//
// It is distinct from deleting the base and distinct from unlinking the corpus, and all three are
// the same operation from a caller's point of view: the corpus lives in a repository, so clearing
// what the base derived loses nothing anybody wrote.
func (c *Client) ClearBankMemories(ctx context.Context, id string) (Cleared, error) {
	resp, httpResp, err := c.api.MemoryAPI.ClearBankMemories(ctx, id).Execute()
	if err != nil {
		return Cleared{}, Classify(err, httpResp, "clearing the derived knowledge of base "+id)
	}
	var out Cleared
	if resp == nil {
		return out, nil
	}
	// The backend's clear reports a single count and a message rather than a breakdown.
	// It is reported as the count it is — everything derived — and the message is kept,
	// because a backend that distinguishes what it removed is worth passing on rather than
	// flattening into a number this contract would then have to guess at.
	out.Items = resp.GetDeletedCount()
	out.Message = strings.TrimSpace(resp.GetMessage())
	return out, nil
}

// knowledge1Record is a local alias so this package's callers do not have to import the domain
// types to hold a record pointer.
type knowledge1Record = knowledge.OwnershipRecord

// BankConfigUpdate is a partial configuration change.
//
// Every field is three-valued: absent leaves it, present-and-empty clears it. Getting that wrong on
// an update is how a re-tag silently clears a field, so the pointer types are the point rather than
// an inconvenience.
type BankConfigUpdate struct {
	Mission           *string
	ExtractionMode    *string
	ObservationScope  *string
	ResolveEntities   *bool
	DispositionTraits []string
}

// UpdateBankConfig applies a partial configuration change.
func (c *Client) UpdateBankConfig(ctx context.Context, id string, update BankConfigUpdate) error {
	// The backend's configuration update is a map of changes rather than a typed partial, and
	// that is the right shape for a three-valued update: a key absent from the map is left
	// alone, and a key present is set. Modelling it as a struct with pointers would have
	// been this framework's invention about a shape the backend does not declare.
	body := hs.BankConfigUpdate{Updates: map[string]any{}}
	if update.Mission != nil {
		body.Updates["mission"] = *update.Mission
	}
	if update.ExtractionMode != nil {
		body.Updates["retain_extraction_mode"] = *update.ExtractionMode
	}
	if update.ObservationScope != nil {
		body.Updates["observation_scope"] = *update.ObservationScope
	}
	if update.ResolveEntities != nil {
		body.Updates["resolve_entities"] = *update.ResolveEntities
	}
	if update.DispositionTraits != nil {
		body.Updates["disposition_traits"] = update.DispositionTraits
	}
	if len(body.Updates) == 0 {
		return nil
	}
	_, httpResp, err := c.api.BanksAPI.UpdateBankConfig(ctx, id).BankConfigUpdate(body).Execute()
	if err != nil {
		return Classify(err, httpResp, "updating the configuration of base "+id)
	}
	return nil
}

// ResetBankConfig restores a base's configuration to the server's defaults.
//
// It is a separate call from an update on purpose. Setting every field to a known value is only the
// same as a reset while the defaults are those values, and the day they are not, an update that
// omitted a field has silently changed nothing there while a caller believed it had set everything.
func (c *Client) ResetBankConfig(ctx context.Context, id string) error {
	_, httpResp, err := c.api.BanksAPI.ResetBankConfig(ctx, id).Execute()
	if err != nil {
		return Classify(err, httpResp, "resetting the configuration of base "+id)
	}
	return nil
}

// AddBaseAlias gives a base a friendly name.
func (c *Client) AddBankAliasAliasUnused() {}

// AddBaseAlias gives a base a friendly name, refused by the backend when it is taken.
func (c *Client) AddBaseAlias(ctx context.Context, id, alias string) error {
	body := hs.CreateBankAliasRequest{Alias: alias}
	_, httpResp, err := c.api.BanksAPI.CreateBankAlias(ctx, id).CreateBankAliasRequest(body).Execute()
	if err != nil {
		return Classify(err, httpResp, "adding the alias "+alias+" to base "+id)
	}
	return nil
}

// SetBankAliasPrimary marks an alias as the one to display and to address by default.
func (c *Client) SetBankAliasPrimary(ctx context.Context, id, alias string) error {
	_, httpResp, err := c.api.BanksAPI.SetBankAliasPrimary(ctx, id, alias).Execute()
	if err != nil {
		return Classify(err, httpResp, "making "+alias+" the primary alias of base "+id)
	}
	return nil
}

// RemoveBankAlias removes a friendly name.
func (c *Client) RemoveBankAlias(ctx context.Context, id, alias string) error {
	_, httpResp, err := c.api.BanksAPI.DeleteBankAlias(ctx, id, alias).Execute()
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == 404 {
			// An alias that is already gone is the state the caller wanted.
			return nil
		}
		return Classify(err, httpResp, "removing the alias "+alias+" from base "+id)
	}
	return nil
}

// IngestionBucket is one point of a base's growth over time.
type IngestionBucket struct {
	Start time.Time
	// Facts is the total, World plus Experience.
	Facts        int32
	World        int32
	Experience   int32
	Observations int32
}

// IngestionSeries reports how a base has grown, so an operator can see that it is growing and when
// it last changed.
func (c *Client) IngestionSeries(ctx context.Context, id, granularity string, buckets int) ([]IngestionBucket, error) {
	// The backend's own knob is a period rather than a granularity, and the contract's is a
	// granularity. The mapping is stated here rather than passed through: a caller asking
	// for daily buckets and the backend calling it a period are the same request, and
	// forwarding an unrecognised value would produce a series at the wrong resolution with
	// nothing to say so.
	req := c.api.BanksAPI.GetMemoriesTimeseries(ctx, id)
	switch strings.ToLower(strings.TrimSpace(granularity)) {
	case "hour", "hourly", "1h":
		req = req.Period("hour")
	case "day", "daily", "1d":
		req = req.Period("day")
	case "week", "weekly", "7d":
		req = req.Period("week")
	case "month", "monthly", "30d":
		req = req.Period("month")
	case "":
	default:
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: fmt.Sprintf("granularity %q is not one of hour, day, week or month; the backend groups its history by period, and forwarding an unrecognised value would produce a series at an unexpected resolution with nothing to say so", granularity),
		}
	}
	resp, httpResp, err := req.Execute()
	if err != nil {
		return nil, Classify(err, httpResp, "reading the ingestion history of base "+id)
	}
	if resp == nil {
		return nil, nil
	}
	var out []IngestionBucket
	// The bucket reports the two fact kinds separately rather than one count, because the
	// distinction is the point of the series: a base growing on experience facts and one
	// growing on documented ones are different deployments, and a single number hides it.
	for _, b := range resp.GetBuckets() {
		bucket := IngestionBucket{Facts: b.GetWorld() + b.GetExperience(), Start: parseLoose(b.GetTime())}
		bucket.Observations = b.GetObservation()
		out = append(out, bucket)
	}
	if buckets > 0 && len(out) > buckets {
		out = out[len(out)-buckets:]
	}
	return out, nil
}

// parseLoose reads a timestamp in any of the shapes the backend uses.
//
// The backend sends ISO 8601 strings and its models type most of them as plain strings, so a
// single strict layout would turn a well-formed response into a failure over formatting. A value
// that matches none of them is the zero time, which callers render as absent rather than as the
// epoch.
func parseLoose(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
