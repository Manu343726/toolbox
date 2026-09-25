package api

import (
	"fmt"
	"sort"
	"strings"
)

// Authorization is a name and a class, and nothing else. A contract says which
// operations exist and what invoking them does; a deployment says which of those
// it permits. Keeping the two apart is what lets a policy be coarse — "every read,
// no writes" — without enumerating anything, and it is why a capability, which was
// both a classification and a grant, is not needed here.
//
// A rule is a pattern over operation identifiers, a predicate over what invoking
// the operation does, and a decision. The first matching rule decides, and the
// rules are consulted most specific first, so a broad grant and a narrow refusal
// can both be written and neither depends on the order they were written in.

// Decision is what a rule concludes about a matching operation.
type Decision string

// The decisions a rule can make. Both are needed: a policy that grants
// everything readable still has to be able to refuse one operation inside that
// grant, which is the only way a grant can be narrowed without restating it.
const (
	// DecisionAllow permits an operation to be exposed.
	DecisionAllow Decision = "allow"
	// DecisionDeny refuses an operation.
	DecisionDeny Decision = "deny"
)

// ParseDecision reads a decision name.
func ParseDecision(value string) (Decision, error) {
	switch Decision(strings.ToLower(strings.TrimSpace(value))) {
	case DecisionAllow:
		return DecisionAllow, nil
	case DecisionDeny:
		return DecisionDeny, nil
	default:
		return "", fmt.Errorf("unknown decision %q: it must be allow or deny", value)
	}
}

// OperationFacts is what a policy is evaluated against: the operation's
// identifier and what invoking it declares it does.
//
// The identifier is the catalog's, which includes the API that owns the
// operation, because that is what makes it unique when several subsystems serve
// one contract.
type OperationFacts struct {
	// ID is the operation identifier, "<api>/<service>/<method>".
	ID string
	// SideEffects are the effects the contract declared. Empty means the contract
	// said nothing, which is not the same as a read.
	SideEffects []SideEffect
}

// Classes returns the classes this operation falls into.
func (f OperationFacts) Classes() []EffectClass { return EffectClasses(f.SideEffects) }

// Rule grants or refuses the operations it matches.
type Rule struct {
	// Pattern matches an operation identifier. "*" matches one segment and a
	// trailing "**" matches the rest, so "knowledge/**" is every operation of the
	// knowledge API and "*/toolbox.api.v1.ApiParserService/*" is that contract in
	// any API.
	Pattern string
	// Classes restricts the rule to operations in those classes. Empty matches
	// every class, including the unclassified one — a rule that says nothing about
	// what an operation does is a statement about every operation, which is how a
	// deployment deliberately trusts an unclassified one.
	Classes []EffectClass
	// Decision is what the rule concludes.
	Decision Decision
}

// Policy is an ordered set of rules, compiled once and consulted per operation.
//
// The zero value denies everything, which is the answer when a deployment has
// stated no policy. A policy that grants everything is written explicitly, with
// AllowAll, because "no policy" and "every policy" must never be the same value.
type Policy struct {
	rules []compiledRule
}

// compiledRule is a rule with its pattern compiled and its specificity measured.
type compiledRule struct {
	rule Rule
	// pattern is the rule's compiled pattern, so matching is the one implementation
	// rather than a second copy of it here.
	pattern Pattern
	order   int
}

// AllowAll returns a policy that permits every operation.
//
// It is for a deployment that has decided its whole surface is available, and it
// is never the default: an absent policy denies everything, so granting
// everything has to be something somebody wrote down.
func AllowAll() Policy {
	policy, err := NewPolicy(Rule{Pattern: wildcard, Decision: DecisionAllow})
	if err != nil {
		// The pattern is a constant and the decision is a constant, so this cannot
		// happen; returning an empty policy would deny everything, which is the safe
		// direction to fail in.
		return Policy{}
	}
	return policy
}

// DenyAll returns a policy that permits no operation. It is what a deployment
// with no stated policy gets, spelled out for a caller that wants to be explicit.
func DenyAll() Policy { return Policy{} }

// NewPolicy compiles a set of rules, most specific first.
//
// The order the rules were written in is preserved for rules of equal specificity,
// so two rules that match exactly the same operations are decided by the order a
// person wrote them, and a set of equally specific rules is still deterministic.
func NewPolicy(rules ...Rule) (Policy, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for index, rule := range rules {
		entry, err := compileRule(rule, index)
		if err != nil {
			return Policy{}, err
		}
		compiled = append(compiled, entry)
	}
	// Most specific first: a pattern that names more of the identifier exactly
	// matches fewer operations, so it is consulted first. That is what lets a broad
	// grant and a narrow refusal coexist without either depending on the order they
	// were written in.
	sort.SliceStable(compiled, func(i, j int) bool {
		if compiled[i].pattern.literal != compiled[j].pattern.literal {
			return compiled[i].pattern.literal > compiled[j].pattern.literal
		}
		if compiled[i].pattern.total != compiled[j].pattern.total {
			return compiled[i].pattern.total > compiled[j].pattern.total
		}
		return compiled[i].order < compiled[j].order
	})
	return Policy{rules: compiled}, nil
}

func compileRule(rule Rule, order int) (compiledRule, error) {
	decision, err := ParseDecision(string(rule.Decision))
	if err != nil {
		return compiledRule{}, err
	}
	pattern, err := CompilePattern(rule.Pattern)
	if err != nil {
		return compiledRule{}, err
	}
	classes := make([]EffectClass, 0, len(rule.Classes))
	for _, class := range rule.Classes {
		parsed, ok := ParseEffectClass(string(class))
		if !ok {
			return compiledRule{}, fmt.Errorf("unknown effect class %q in rule %q", class, rule.Pattern)
		}
		classes = append(classes, parsed)
	}
	return compiledRule{
		rule:    Rule{Pattern: rule.Pattern, Classes: classes, Decision: decision},
		pattern: pattern,
		order:   order,
	}, nil
}

// Allows reports whether a policy permits an operation.
//
// The first matching rule decides, and a policy with no matching rule refuses.
func (p Policy) Allows(facts OperationFacts) bool {
	return p.Decide(facts) == DecisionAllow
}

// Decide returns the decision a policy reaches for an operation, and whether any
// rule matched at all. An operation no rule matched is refused, so a caller can
// tell a refusal from a policy that said nothing.
func (p Policy) Decide(facts OperationFacts) Decision {
	if decision, matched := p.evaluate(facts); matched {
		return decision
	}
	return DecisionDeny
}

// Matched reports whether any rule matched an operation, whatever it decided.
func (p Policy) Matched(facts OperationFacts) bool {
	_, matched := p.evaluate(facts)
	return matched
}

func (p Policy) evaluate(facts OperationFacts) (Decision, bool) {
	identifier := strings.TrimSpace(facts.ID)
	if identifier == "" {
		return "", false
	}
	classes := facts.Classes()
	for _, entry := range p.rules {
		if !entry.pattern.Match(identifier) {
			continue
		}
		if entry.rule.matches(classes) {
			return entry.rule.Decision, true
		}
	}
	return "", false
}

// matches reports whether a rule's class predicate accepts an operation.
func (r Rule) matches(classes []EffectClass) bool {
	if len(r.Classes) == 0 {
		return true
	}
	for _, allowed := range r.Classes {
		for _, actual := range classes {
			if allowed == actual {
				return true
			}
		}
	}
	return false
}

// Rules returns the rules in the order they are consulted.
func (p Policy) Rules() []Rule {
	result := make([]Rule, 0, len(p.rules))
	for _, entry := range p.rules {
		result = append(result, entry.rule)
	}
	return result
}

// Len reports how many rules a policy holds.
func (p Policy) Len() int { return len(p.rules) }
