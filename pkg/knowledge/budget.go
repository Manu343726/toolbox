package knowledge

// Budget is how much work a retrieval is allowed to do.
//
// It is a three-value set rather than a number because the useful question is not "how many
// tokens" but "how long may this take before it is good enough" — and a person choosing between a
// fast answer and a thorough one should not be handed a token budget to express it.
//
// The two calls that accept it default differently, and that difference is stated rather than
// inherited: a recall defaults to mid and a reflect to low, because a reflect already does more
// work to produce its answer.
type Budget string

const (
	// BudgetUnspecified means the caller did not choose, and each call's own default
	// applies.
	BudgetUnspecified Budget = ""
	// BudgetLow suits an interactive turn.
	BudgetLow Budget = "low"
	// BudgetMid is the balanced default for a recall.
	BudgetMid Budget = "mid"
	// BudgetHigh suits a batch or an audit.
	BudgetHigh Budget = "high"
)

// DefaultFor returns the budget a call uses when the caller did not choose one.
//
// It is a function of the call rather than of the type, because the backend's own defaults differ
// and a surface that exposed one "depth" setting would be overriding a different default on each
// call without saying so.
func DefaultFor(reflecting bool) Budget {
	if reflecting {
		return BudgetLow
	}
	return BudgetMid
}

// Resolve returns the budget to use, substituting the call's default for an unset one.
func (b Budget) Resolve(reflecting bool) Budget {
	if b != BudgetUnspecified {
		return b
	}
	return DefaultFor(reflecting)
}
