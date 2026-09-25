package api

import (
	"fmt"
	"strings"
)

// A policy is stored as text so one document can live in a file, in the policy
// subsystem's own storage, and in a test fixture, and be read the same way in all
// three. The format is one rule per line and nothing else:
//
//	allow knowledge/**                          read
//	deny  registry/toolbox.registry.v1.RegistryService/Register
//	allow *                                     unclassified
//
// A line is a decision, a pattern, and optionally the effect classes it applies
// to. The decision comes first so a document reads as a list of statements, the
// classes come last so a rule that names none is visibly a rule about everything,
// and a rule's shape is the same whether it is written by a person or generated.

// ParsePolicy reads a policy document.
//
// A blank line and a line whose first non-space character is "#" are ignored. Every
// other line is a rule, and a line that cannot be read is an error naming the line
// number: a policy that silently skipped half its rules would authorize less than
// its author believes, which is the direction that gets noticed last.
func ParsePolicy(document string) (Policy, error) {
	rules := make([]Rule, 0, 8)
	for number, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		rule, err := parsePolicyLine(trimmed)
		if err != nil {
			return Policy{}, fmt.Errorf("line %d: %w", number+1, err)
		}
		rules = append(rules, rule)
	}
	return NewPolicy(rules...)
}

func parsePolicyLine(line string) (Rule, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return Rule{}, fmt.Errorf("a rule needs a decision and a pattern")
	}
	decision, err := ParseDecision(fields[0])
	if err != nil {
		return Rule{}, err
	}
	rule := Rule{Pattern: fields[1], Decision: decision}
	for _, name := range fields[2:] {
		class, ok := ParseEffectClass(name)
		if !ok {
			return Rule{}, fmt.Errorf("unknown effect class %q: it must be read, write, or unclassified", name)
		}
		rule.Classes = append(rule.Classes, class)
	}
	return rule, nil
}

// Format renders a policy back to the document form.
//
// The rules come back in the order they are consulted rather than the order they
// were written, because that is the order that decides an operation. A person
// reading it back is reading the policy as it behaves.
func (p Policy) Format() string {
	builder := &strings.Builder{}
	for _, entry := range p.rules {
		builder.WriteString(string(entry.rule.Decision))
		builder.WriteString(" ")
		builder.WriteString(entry.rule.Pattern)
		for _, class := range entry.rule.Classes {
			builder.WriteString(" ")
			builder.WriteString(string(class))
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

// String renders a policy as its document form.
func (p Policy) String() string { return p.Format() }
