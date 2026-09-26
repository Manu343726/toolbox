package skills

import (
	"strconv"
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
)

// LocalCatalog is the catalog every project has without declaring it: the `skills/`
// directory under the project's configuration directory.
//
// It needs no configuration entry to exist. A project with no skills directory has an empty
// local catalog, and an empty catalog is a project that offers nothing rather than a project
// that failed to start.
const LocalCatalog = "local"

// Reference is a skill's fully-qualified name: the catalog that holds it, and its own name.
//
// The catalog is part of the identity rather than decoration on it. Two catalogs may both
// hold a skill called `code-review`, and the MCP Skills extension is explicit that skill
// names are not unique and that a host must resolve them per origin — so the thing that
// identifies a skill in this framework is the pair. Dropping the catalog from a reference
// would reintroduce exactly the collision the qualification exists to prevent.
type Reference struct {
	// Catalog is the catalog holding the skill, the first segment of its URI.
	Catalog string
	// Name is the skill's own name, which is also its directory's.
	Name string
}

// String renders the reference as a project's configuration file writes it.
func (r Reference) String() string {
	if r.Catalog == "" {
		return r.Name
	}
	return r.Catalog + "." + r.Name
}

// URI is the resource URI of the skill's SKILL.md.
func (r Reference) URI() string { return SkillURI(r.Catalog, r.Name) }

// IsLocal reports whether the reference names a skill in the project's own catalog.
func (r Reference) IsLocal() bool { return r.Catalog == LocalCatalog }

// ParseReference reads a qualified reference as a project's configuration states it.
//
// The reference is split at the **last** dot, because a catalog identifier may itself contain
// one: `skills.sh.pull-request-review` is the `pull-request-review` skill of the `skills.sh`
// catalog, and splitting at the first dot would read it as a skill called `sh` in a catalog
// called `skills`.
func ParseReference(text string) (Reference, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Reference{}, api.Errorf(api.KindInvalid,
			"a skill reference is empty. A reference names the catalog a skill comes from and "+
				"the skill itself, as <catalog>.<name>")
	}
	separator := strings.LastIndex(trimmed, ".")
	if separator < 0 {
		return Reference{}, api.Errorf(api.KindInvalid,
			"%q is not a qualified skill reference; a reference is <catalog>.<name>, because "+
				"two catalogs may hold a skill of the same name", text)
	}
	reference := Reference{
		Catalog: strings.TrimSpace(trimmed[:separator]),
		Name:    strings.TrimSpace(trimmed[separator+1:]),
	}
	if reference.Catalog == "" || reference.Name == "" {
		return Reference{}, api.Errorf(api.KindInvalid,
			"%q is not a qualified skill reference; both halves are part of it, as "+
				"<catalog>.<name>", text)
	}
	// A second dot on the left of the last one is a catalog with a dot in it, which is
	// allowed; a dot on the right is not, and LastIndex guarantees there is none.
	if err := validateSkillName(reference.Name, "the skill named in "+strconv.Quote(text)); err != nil {
		return Reference{}, err
	}
	if err := validateCatalogID(reference.Catalog, text); err != nil {
		return Reference{}, err
	}
	return reference, nil
}

// validateCatalogID refuses a catalog identifier that could not be one segment of a URI.
//
// It is deliberately looser than a skill name: a catalog is a deployment's own label and a
// deployment may call its catalogs what it likes — `skills.sh` has a dot in it. What it may
// not do is carry whitespace or a slash, because the catalog is one segment of a URI and one
// field of a reference, and a segment cannot contain either.
func validateCatalogID(id, asWritten string) error {
	if strings.ContainsAny(id, " \t\n\r/") {
		return api.Errorf(api.KindInvalid,
			"the catalog named in %q is %q, which contains whitespace or a slash. A catalog is "+
				"one segment of a skill's URI, so it cannot", asWritten, id)
	}
	if strings.HasPrefix(id, ".") {
		return api.Errorf(api.KindInvalid,
			"the catalog named in %q is %q, which starts with a dot", asWritten, id)
	}
	return nil
}

// ParseReferences reads a project's whole list, reporting every entry that is not a reference
// rather than the first.
//
// A person fixing their configuration should see everything that is wrong in one go, because
// fixing one line at a time against a list that reappears on the next run is a worse
// experience than a list — and nothing here is ambiguous, so there is no reason to stop at the
// first.
func ParseReferences(values []string) ([]Reference, error) {
	references := make([]Reference, 0, len(values))
	var problems []string
	for _, value := range values {
		reference, err := ParseReference(value)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		references = append(references, reference)
	}
	if len(problems) > 0 {
		return nil, api.Errorf(api.KindInvalid,
			"the project's skills: list has %d %s that %s not a reference:\n  %s",
			len(problems), plural(len(problems), "entry", "entries"),
			map[bool]string{true: "is", false: "are"}[len(problems) == 1],
			strings.Join(problems, "\n  "))
	}
	return references, nil
}

// plural picks the noun that agrees with a count.
func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
