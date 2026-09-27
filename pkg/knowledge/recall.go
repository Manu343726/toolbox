package knowledge

import "time"

// RecallResult is one retrieved item, in this framework's vocabulary rather than the backend's.
//
// A strategy's contribution is named rather than folded into one score, because a reader
// debugging a wrong answer needs to know *which* retrieval found it: an item that only keyword
// search found behaves differently from one the entity graph found, and a single blended score
// throws that away.
type RecallResult struct {
	ID       string
	Text     string
	FactType string
	// Origin is how this knowledge came to exist, so a reader can tell a documented claim
	// from an inference.
	Origin   Origin
	Location Location
	// DerivedFrom names the documents it was extracted from, which is what a citation
	// resolves to.
	DerivedFrom []string
	ObservedAt  time.Time
	Tags        []string
	Sources     []RecallSource
}

// RecallSource is one retrieval strategy's contribution to a result.
type RecallSource struct {
	// Strategy is from a fixed vocabulary this framework owns, rather than the backend's
	// name for the combination of strategies it runs.
	Strategy string
	// Score is normalised to 0..1 so that a caller comparing two results is not comparing
	// two different scales.
	Score float32
	// Excerpt is the text that matched, which is what makes a wrong answer diagnosable
	// rather than merely wrong.
	Excerpt string
}

// Citation is one item a reasoning answer relied on.
//
// A citation states its origin, because a citation that cannot tell a written decision from an
// inferred one is worth much less: a person reading "the retry count is three" needs to know
// whether a runbook says it or whether the system guessed.
type Citation struct {
	// ContentID is the content the claim traces to. It can be empty when the backend's
	// answer did not resolve, which is reported rather than filled in with a guess.
	ContentID  string
	Location   Location
	Origin     Origin
	Excerpt    string
	HasExcerpt bool
	// FactIDs are the facts that were cited. These resolve to a file only through the
	// reasoning trace, which is why the surface resolves them rather than a caller.
	FactIDs []string
}

// Reflection is a synthesised answer and what it relied on.
type Reflection struct {
	Text string
	// Citations is what the answer *cites*: the narrower, ordered, deduplicated set a
	// person wants.
	Citations []Citation
	// TraceDigest is a handle on what the answer *drew on*, for a reader debugging a wrong
	// answer. The trace itself is not returned: it is the agent's whole scratchpad, it is
	// large, and presenting it as the answer's sources would overstate what the answer used.
	TraceDigest string
	// BackingModelID is the synthesized document the backend keeps and rewrites as the base
	// changes. It is a projection and is not authority for anything.
	BackingModelID string
	// RawFactIDs are the cited fact identifiers as the backend returned them, kept when
	// citation resolution was switched off. They are identifiers without locations, and a
	// caller is told that rather than being handed a citation pointing at nothing.
	RawFactIDs []string
	// UnresolvedCitations counts the citations that could not be placed, because the
	// backend reported the fact without a document and the reasoning trace did not carry
	// one either. It is reported rather than hidden: a citation list that silently mixes
	// placed and unplaced entries is a list nobody can trust, and a reader who is told
	// "three of five could be traced" can act on that.
	UnresolvedCitations int
}

// ExtractionSettings is what a base's extraction is configured to do.
type ExtractionSettings struct {
	// Mode is one of the backend's extraction modes. The values exist only in its prose and
	// not in its schema, so this is a string and the adapter says where it came from rather
	// than pretending an enum is authoritative.
	Mode string
	// ResolveEntities is whether names are resolved against entities already in the base.
	// The corpus path turns it off.
	ResolveEntities bool
	// ObservationScope is `per_tag`, `combined` or `shared`.
	ObservationScope string
}

// PreviewFact is one fact a write would extract.
type PreviewFact struct {
	Text     string
	FactType string
	Entities []string
}

// Budget values, mirroring the contract's enum. They are declared here rather than imported so
// that `pkg/knowledge` does not depend on a subsystem's generated code, which is the direction the
// framework's own boundary rule points.
const (
	knowledgeBudgetLow  = "low"
	knowledgeBudgetHigh = "high"
)
