package knowledgehindsight

import (
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/knowledge"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// corpusMetaMessage converts a file's declared frontmatter to the contract.
//
// An undeclared kind or status is sent as UNSPECIFIED rather than as a guessed value, because a
// filter on a guessed value returns a silently wrong answer and a filter on an absent one returns
// nothing — and "nothing" is the answer a person can notice.
func corpusMetaMessage(fm knowledge.Frontmatter) *knowledgev1.CorpusFileMeta {
	return &knowledgev1.CorpusFileMeta{
		DeclaredId: fm.ID,
		Kind:       docKindMessage(fm.Kind),
		Status:     docStatusMessage(fm.Status),
		Authority:  string(fm.Authority),
		Source:     fm.Source,
		Supersedes: fm.Supersedes,
		Owner:      fm.Owner,
		AuthorTags: fm.Tags,
		Reviewed:   timeMessage(fm.Reviewed),
		Date:       timeMessage(fm.Date),
	}
}

func docKindMessage(k knowledge.DocKind) knowledgev1.DocKind {
	switch k {
	case knowledge.KindArchitecture:
		return knowledgev1.DocKind_DOC_KIND_ARCHITECTURE
	case knowledge.KindPolicy:
		return knowledgev1.DocKind_DOC_KIND_POLICY
	case knowledge.KindDecision:
		return knowledgev1.DocKind_DOC_KIND_DECISION
	case knowledge.KindProcedure:
		return knowledgev1.DocKind_DOC_KIND_PROCEDURE
	case knowledge.KindReference:
		return knowledgev1.DocKind_DOC_KIND_REFERENCE
	case knowledge.KindRunbook:
		return knowledgev1.DocKind_DOC_KIND_RUNBOOK
	case knowledge.KindGlossary:
		return knowledgev1.DocKind_DOC_KIND_GLOSSARY
	case knowledge.KindIndexGenerated:
		return knowledgev1.DocKind_DOC_KIND_INDEX
	case knowledge.KindLogGenerated:
		return knowledgev1.DocKind_DOC_KIND_LOG
	default:
		return knowledgev1.DocKind_DOC_KIND_UNSPECIFIED
	}
}

func docStatusMessage(s knowledge.DocStatus) knowledgev1.DocStatus {
	switch s {
	case knowledge.StatusActive:
		return knowledgev1.DocStatus_DOC_STATUS_ACTIVE
	case knowledge.StatusDeprecated:
		return knowledgev1.DocStatus_DOC_STATUS_DEPRECATED
	case knowledge.StatusDraft:
		return knowledgev1.DocStatus_DOC_STATUS_DRAFT
	default:
		return knowledgev1.DocStatus_DOC_STATUS_UNSPECIFIED
	}
}

func timeMessage(t *time.Time) *timestamppb.Timestamp {
	if t == nil || t.IsZero() {
		return nil
	}
	return timestamppb.New(*t)
}

// budget maps the contract's depth enum onto the domain's, substituting the call's own default
// for an unset one.
//
// The default is a function of the call rather than of the type, because the backend's defaults
// differ — a recall is mid, a reflect is low — and a surface exposing one "depth" setting would
// otherwise be overriding a different default on each call without saying so.
func budget(b knowledgev1.Budget, reflecting bool) knowledge.Budget {
	switch b {
	case knowledgev1.Budget_BUDGET_LOW:
		return knowledge.BudgetLow
	case knowledgev1.Budget_BUDGET_MID:
		return knowledge.BudgetMid
	case knowledgev1.Budget_BUDGET_HIGH:
		return knowledge.BudgetHigh
	default:
		return knowledge.DefaultFor(reflecting)
	}
}

func scopeOrigins(origins []knowledgev1.Origin) ([]knowledgev1.Origin, error) {
	if len(origins) == 0 {
		return nil, nil
	}
	out := make([]knowledgev1.Origin, 0, len(origins))
	for _, o := range origins {
		if o == knowledgev1.Origin_ORIGIN_UNSPECIFIED {
			return nil, &api.Error{
				Kind:    api.KindInvalid,
				Message: "an unspecified origin in a filter matches nothing; omit it to mean every origin, or name the ones you want",
			}
		}
		out = append(out, o)
	}
	return out, nil
}

func originMessage(o knowledge.Origin) knowledgev1.Origin {
	switch o {
	case knowledge.OriginAuthored:
		return knowledgev1.Origin_ORIGIN_AUTHORED
	case knowledge.OriginRetained:
		return knowledgev1.Origin_ORIGIN_RETAINED
	case knowledge.OriginDerived:
		return knowledgev1.Origin_ORIGIN_DERIVED
	default:
		return knowledgev1.Origin_ORIGIN_UNSPECIFIED
	}
}

// mutabilityMessage is the declared answer to "may this be changed", derived from the origin
// rather than stored.
//
// It is a total function precisely so that a caller is told the rule instead of discovering it by
// having a write silently overwritten. An assistant told a page is regenerated can say so to the
// user; one told a runbook is edited at its source goes and edits the file.
func mutabilityMessage(o knowledge.Origin) knowledgev1.Mutability {
	switch knowledge.MutabilityFor(o) {
	case knowledge.MutabilityNone:
		return knowledgev1.Mutability_MUTABILITY_NONE
	case knowledge.MutabilityCurated:
		return knowledgev1.Mutability_MUTABILITY_CURATED
	case knowledge.MutabilityRegenerated:
		return knowledgev1.Mutability_MUTABILITY_REGENERATED
	default:
		return knowledgev1.Mutability_MUTABILITY_UNSPECIFIED
	}
}

// refuseOriginWrite is the answer to a write against content whose origin does not accept one.
//
// It is `FailedPrecondition` and not `InvalidArgument`, and the distinction is the whole point:
// the request was well formed, the content just does not accept that kind of change. An assistant
// is told what to do instead — edit the file, or accept that a page is regenerated — rather than
// being handed a validation error about a field it sent correctly.
func refuseOriginWrite(id string, o knowledgev1.Origin) error {
	switch o {
	case knowledgev1.Origin_ORIGIN_AUTHORED:
		return &api.Error{
			Kind: api.KindFailedPrecondition,
			Message: "authored content is not written through this API: it lives in a reviewed file in a repository, " +
				"and a write here would be a second source of truth that the next reconcile deletes. " +
				"Edit the file and run ApplyReconcile; if you hold the identifier " + id + ", its path is in the content's `location`",
		}
	case knowledgev1.Origin_ORIGIN_DERIVED:
		return &api.Error{
			Kind: api.KindFailedPrecondition,
			Message: "derived content is regenerated: it is what the system currently believes, it is rewritten whenever its inputs change, " +
				"and an edit would be discarded rather than applied. To make a claim stick, write it in the corpus and reconcile — that is the way a person's version becomes the one that holds",
		}
	case knowledgev1.Origin_ORIGIN_UNSPECIFIED:
		return &api.Error{
			Kind:    api.KindInvalid,
			Message: "an origin is required on a write; it is never chosen by the caller, and an absent one cannot be inferred — only retained content is written here",
		}
	}
	return nil
}

// notImplemented is what a capability this build does not carry returns.
//
// It names what to do, because "unimplemented" on its own sends an operator looking for a bug
// rather than for a build tag.
func notImplemented(what, how string) error {
	return &api.Error{
		Kind:    api.KindUnsupported,
		Message: what + " is not available in this build: " + how,
	}
}

func requireBase(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", &api.Error{Kind: api.KindInvalid, Message: "a base name or identifier is required"}
	}
	return strings.TrimSpace(name), nil
}

// pageWindow resolves a page size and token into the limit and offset the backend takes.
//
// The token is a decimal offset, and that is worth saying rather than disguising: a caller that
// stores one and changes the page size under it gets a different slice, which is a normal
// consequence of offset paging and not something a token can prevent. An unparseable token is a
// caller's mistake and is refused, because silently treating it as the first page would make a
// caller believe it had read the whole thing.
func pageWindow(size int32, token string) (int32, int32) {
	limit := size
	if limit <= 0 {
		limit = 50
	}
	if token == "" {
		return limit, 0
	}
	// The token has already been checked by `checkToken`, so this cannot fail; a token that
	// was not checked reads as the first page rather than as a panic.
	offset, err := strconv.ParseInt(token, 10, 32)
	if err != nil || offset < 0 {
		return limit, 0
	}
	return limit, int32(offset)
}

// checkToken refuses a page token that is not an offset.
func checkToken(token string) error {
	if token == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(token, 10, 32)
	if err != nil {
		return &api.Error{
			Kind:    api.KindInvalid,
			Message: "the page token " + strconv.Quote(token) + " is not a page token; tokens are decimal offsets, and an unreadable one is refused rather than treated as the first page, which would make a caller believe it had read everything",
		}
	}
	if parsed < 0 {
		return &api.Error{
			Kind:    api.KindInvalid,
			Message: "a page token cannot be negative; " + strconv.Quote(token) + " would read from before the first item",
		}
	}
	return nil
}

// nextToken encodes where the next page starts, or the empty string when this was the last one.
//
// It returns the empty string when the reported total is unknown rather than when a page came back
// short. A short page with no total is a page that happens to be the end *or* a page that was cut
// short, and continuing to ask costs one call and losing the tail costs a fact nobody can find.
func nextToken(offset, returned, total int32) string {
	if total <= 0 {
		if returned == 0 {
			return ""
		}
		// There is more to try: a full-or-short page from a source that does not count.
		return strconv.Itoa(int(offset + returned))
	}
	if int(offset+returned) >= int(total) {
		return ""
	}
	return strconv.Itoa(int(offset + returned))
}
