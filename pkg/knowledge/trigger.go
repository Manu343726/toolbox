package knowledge

// Trigger states when a derived document is rebuilt and from what.
//
// Every field is a filter on what the document is built from, which is what makes a derived document
// a projection rather than a document: there is nothing in it that somebody wrote, and everything in
// it that somebody would want to say is decided here.
type Trigger struct {
	// Mode is `delta` or `all`. `delta` rewrites what changed; `all` regenerates from
	// scratch. A first build has nothing to edit, so it is a full generation whichever is
	// asked for.
	Mode string
	// FactTypes restricts which facts feed the document. Empty means all of them.
	FactTypes []string
	// ExcludeMentalModels keeps other derived documents out of this one's inputs, which is
	// what stops a projection from becoming an input to the next projection.
	ExcludeMentalModels bool
	// RefreshAfterConsolidation rebuilds whenever consolidation produces anything new.
	RefreshAfterConsolidation bool
	// Tags restricts which memories feed it, and TagsMatch is how they combine.
	//
	// This is the field that bites hardest when it is wrong: a page tagged with descriptive
	// terms it was not given matches nothing, and the page stays empty however many times it
	// is refreshed.
	Tags      []string
	TagsMatch string
	// TagGroups is the same filter as a boolean expression, and it is what a compound trigger
	// uses. `Tags` is the shorthand for a single leaf of this tree.
	//
	// The two coexist because a flat list is what a caller writes almost always, and the tree
	// is what the backend's own algebra actually is. Setting both is a refusal rather than a
	// merge: two filters over the same input with no rule for combining them is a filter whose
	// meaning depends on which one a reader happened to look at.
	TagGroups []TagFilter
	// Cron schedules a refresh on a fixed interval. Mutually exclusive with refreshing after
	// consolidation: a document refreshes either after consolidation or on a schedule, not both.
	Cron string
}

// DefaultTrigger is what a derived document gets when nobody says otherwise.
//
// The defaults are stated rather than inherited because they are not nothing, and the second one in
// particular is load-bearing: a page whose trigger matches no facts will never be wrong, because it
// will be empty.
func DefaultTrigger() Trigger {
	return Trigger{
		Mode:                      "delta",
		FactTypes:                 []string{"observation"},
		ExcludeMentalModels:       true,
		RefreshAfterConsolidation: true,
		TagsMatch:                 "all_strict",
	}
}
