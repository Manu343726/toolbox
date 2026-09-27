package knowledge

import (
	"fmt"
	"sort"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Frontmatter is the optional metadata block at the top of a corpus file.
//
// It is a contract where it is present rather than a suggestion: it decides the
// file's identity, and it is the only place a person can say what kind of
// document they wrote and whether it is still current. A file with no
// frontmatter is reconciled all the same, and the path alone scopes it.
//
// Unknown keys are preserved rather than refused, because a corpus is a body of
// documentation that people extend with their own taxonomy and this package is
// not the owner of it. A known key with an unusable *value* is refused, because
// those become retrieval filters and a typo in a filter is a silently empty
// result that nobody notices until they need it.
type Frontmatter struct {
	// ID is the stable identity of the file, unique within the base. It is what
	// makes a move a move rather than a delete and a create.
	ID string `yaml:"id,omitempty"`
	// Title is a display title where the filename is a poor one.
	Title string `yaml:"title,omitempty"`
	// Kind is what sort of document this is, and becomes a tag.
	Kind DocKind `yaml:"kind,omitempty"`
	// Status is whether it still holds, and becomes a tag.
	Status DocStatus `yaml:"status,omitempty"`
	// Authority is who stands behind it. Human is the default and the only value
	// a person should write; it exists so a file states what it is rather than
	// the deployment assuming it.
	Authority Authority `yaml:"authority,omitempty"`
	// Source names which corpus or repository this came from, when a base
	// reconciles more than one.
	Source string `yaml:"source,omitempty"`
	// Supersedes names identifiers this document replaces. It is how an authored
	// conflict stays authored: the corpus states which decision replaced which,
	// in a reviewed file, rather than a synchroniser guessing.
	Supersedes []string `yaml:"supersedes,omitempty"`
	// Owner, Reviewed and Date are for a reader. Nothing depends on them.
	Owner    string     `yaml:"owner,omitempty"`
	Reviewed *time.Time `yaml:"reviewed,omitempty"`
	Date     *time.Time `yaml:"date,omitempty"`
	// Created is the document's own creation date, preferred over Date and over
	// the filesystem's when deciding what timestamp to ingest with.
	Created *time.Time `yaml:"created,omitempty"`
	// Tags and Aliases are the author's own vocabulary, carried through beside
	// the framework's rather than flattened into it.
	Tags    []string `yaml:"tags,omitempty"`
	Aliases []string `yaml:"aliases,omitempty"`

	// Extra holds every key this package does not recognise, so that a caller
	// reading a file can see what the author put there. It is never sent to
	// retrieval as a filter: this package does not know what any of it means.
	Extra map[string]any `yaml:",inline"`
}

// DocKind is what sort of document a file is. It is an enum rather than a free
// string because retrieval filters on it.
type DocKind string

// The document kinds. A file with no kind is a file whose sort nobody has
// declared, and is reconciled without one rather than guessed at.
const (
	KindUnspecified    DocKind = ""
	KindArchitecture   DocKind = "architecture"
	KindPolicy         DocKind = "policy"
	KindDecision       DocKind = "decision"
	KindProcedure      DocKind = "procedure"
	KindReference      DocKind = "reference"
	KindRunbook        DocKind = "runbook"
	KindGlossary       DocKind = "glossary"
	KindIndexGenerated DocKind = "index"
	KindLogGenerated   DocKind = "log"
)

// Valid reports whether k is a declared kind.
func (k DocKind) Valid() bool {
	switch k {
	case KindArchitecture, KindPolicy, KindDecision, KindProcedure, KindReference,
		KindRunbook, KindGlossary, KindIndexGenerated, KindLogGenerated:
		return true
	default:
		return false
	}
}

// DocStatus is whether a document still holds. It is what lets a consumer
// discount a superseded document without the deployment resolving anything on
// its behalf.
type DocStatus string

const (
	StatusUnspecified DocStatus = ""
	// StatusActive is a document that holds.
	StatusActive DocStatus = "active"
	// StatusDeprecated is one that was replaced. It stays retrievable, because
	// somebody will still ask why it used to be done this way.
	StatusDeprecated DocStatus = "deprecated"
	// StatusDraft is one somebody is still writing.
	StatusDraft DocStatus = "draft"
)

// Valid reports whether s is a declared status.
func (s DocStatus) Valid() bool {
	switch s {
	case StatusActive, StatusDeprecated, StatusDraft:
		return true
	default:
		return false
	}
}

// Authority states who stands behind a document.
type Authority string

const (
	// AuthorityUnspecified means the file said nothing, which the reconciler
	// reads as human: a corpus is a directory of files people wrote.
	AuthorityUnspecified Authority = ""
	// AuthorityHuman is a person-written document, reviewed through version
	// control. It is the default and the only value a person should write.
	AuthorityHuman Authority = "human"
)

// Valid reports whether a is a declared authority.
func (a Authority) Valid() bool { return a == AuthorityHuman || a == AuthorityUnspecified }

// frontmatterDelimiter is the YAML block fence. A file has frontmatter when its
// first line is exactly this.
const frontmatterDelimiter = "---"

// ParsedFile is a corpus file after the frontmatter block has been separated
// from the body. Body is the markdown a person actually wrote, with no fence in
// it, and is what a projection copies verbatim.
type ParsedFile struct {
	Front Frontmatter
	Body  string
	// HadFrontmatter reports whether a frontmatter block was present at all,
	// which is different from it being present and empty.
	HadFrontmatter bool
}

// ParseFile separates a corpus file's frontmatter from its body.
//
// A block that is never closed is not an error: the whole file is the body. That
// is the same rule the shipped Hindsight client applies, and for the same reason
// — a file with a stray "---" under its title must not be silently reduced to
// nothing, and a parse failure that returned no body would lose a person's
// writing.
//
// The returned error is reserved for a block that opens and does parse but
// carries a value this package cannot use, such as an unknown document kind.
func ParseFile(raw []byte) (ParsedFile, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	// A body-only file, and an empty one, are both trivially body.
	if !strings.HasPrefix(text, frontmatterDelimiter+"\n") && text != frontmatterDelimiter {
		return ParsedFile{Body: text}, nil
	}
	if text == frontmatterDelimiter {
		return ParsedFile{Body: "", HadFrontmatter: true}, nil
	}

	rest := text[len(frontmatterDelimiter)+1:]
	end := strings.Index(rest, "\n"+frontmatterDelimiter)
	if end < 0 {
		// Unterminated. Treat the entire file as body rather than losing it.
		return ParsedFile{Body: text}, nil
	}
	block := rest[:end]
	body := rest[end+1:]
	// Consume the closing fence, the end of its line, and then the single blank
	// line conventional markdown puts between frontmatter and the document. One,
	// and only one: a body that genuinely starts with blank lines keeps them,
	// because a parser that tidies whitespace is a parser that eventually tidies
	// something a person wrote.
	body = strings.TrimPrefix(body, frontmatterDelimiter)
	body = strings.TrimPrefix(body, "\n")
	body = strings.TrimPrefix(body, "\n")

	var fm Frontmatter
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return ParsedFile{}, fmt.Errorf("knowledge: frontmatter is not valid YAML: %w", err)
	}
	fm.normalise()
	if err := fm.validate(); err != nil {
		return ParsedFile{}, err
	}
	return ParsedFile{Front: fm, Body: body, HadFrontmatter: true}, nil
}

// normalise trims and canonicalises what the author wrote, so that two files
// saying the same thing compare equal and filter identically.
func (f *Frontmatter) normalise() {
	f.ID = strings.TrimSpace(f.ID)
	f.Title = strings.TrimSpace(f.Title)
	f.Source = strings.TrimSpace(f.Source)
	f.Owner = strings.TrimSpace(f.Owner)
	f.Kind = DocKind(strings.ToLower(strings.TrimSpace(string(f.Kind))))
	f.Status = DocStatus(strings.ToLower(strings.TrimSpace(string(f.Status))))
	f.Authority = Authority(strings.ToLower(strings.TrimSpace(string(f.Authority))))
	f.Supersedes = dedupeNonEmpty(f.Supersedes)
	f.Tags = dedupeNonEmpty(f.Tags)
	f.Aliases = dedupeNonEmpty(f.Aliases)
	for k := range f.Extra {
		if isReservedFrontmatterKey(k) {
			// A known key the inline map also captured, which happens when a
			// document is rewritten with a different YAML shape. Drop it: the
			// typed field is the one this package acts on.
			delete(f.Extra, k)
		}
	}
	if len(f.Extra) == 0 {
		f.Extra = nil
	}
}

// validate refuses a file whose declared values cannot be used, naming the file
// and the field. It is the difference between a typo being a message and a typo
// being a filter that matches nothing.
func (f Frontmatter) validate() error {
	if f.Kind != KindUnspecified && !f.Kind.Valid() {
		return fmt.Errorf("knowledge: frontmatter kind %q is not one of %s", f.Kind, joinDocKinds())
	}
	if f.Status != StatusUnspecified && !f.Status.Valid() {
		return fmt.Errorf("knowledge: frontmatter status %q is not one of active, deprecated, draft", f.Status)
	}
	if !f.Authority.Valid() {
		return fmt.Errorf("knowledge: frontmatter authority %q is not one of human", f.Authority)
	}
	return nil
}

func isReservedFrontmatterKey(k string) bool {
	switch k {
	case "id", "title", "kind", "status", "authority", "source", "supersedes",
		"owner", "reviewed", "date", "created", "tags", "aliases":
		return true
	default:
		return false
	}
}

func joinDocKinds() string {
	all := []DocKind{KindArchitecture, KindPolicy, KindDecision, KindProcedure,
		KindReference, KindRunbook, KindGlossary, KindIndexGenerated, KindLogGenerated}
	parts := make([]string, len(all))
	for i, k := range all {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}

func dedupeNonEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AuthoredAt returns the time to ingest the document with, and whether one is
// known at all.
//
// created wins over date, first declared wins, and the filesystem's own
// timestamps are not consulted here: on Linux a file's "created" time is really
// its inode change time, which changes on every rewrite and is reported as zero
// by some filesystems, so a derived date is an approximation wearing a precise
// label. A caller that wants an approximation passes the modification time
// explicitly.
func (f Frontmatter) AuthoredAt() (time.Time, bool) {
	if f.Created != nil {
		return f.Created.UTC(), true
	}
	if f.Date != nil {
		return f.Date.UTC(), true
	}
	return time.Time{}, false
}

// EventTime returns the time to record as the content's event time, falling back
// to modTime when the author declared none. The bool reports whether the
// fallback was used, so a caller can choose to send a timeless ingest instead.
func (f Frontmatter) EventTime(modTime time.Time) (t time.Time, declared bool) {
	if declaredAt, ok := f.AuthoredAt(); ok {
		return declaredAt, true
	}
	if modTime.IsZero() {
		return time.Time{}, false
	}
	return modTime.UTC(), false
}

// Render writes the frontmatter back out as a YAML block, including the fences.
//
// It is used by the projection to state a file's origin in its own header, and by
// nothing that writes a corpus file: the corpus is written by people, and a tool
// writing one behind their editor would fight their review process. The rendered
// form is therefore only ever something a reader or a derived artifact sees.
func (f Frontmatter) Render() ([]byte, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("knowledge: rendering frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("knowledge: rendering frontmatter: %w", err)
	}
	return []byte(frontmatterDelimiter + "\n" + sb.String() + frontmatterDelimiter + "\n"), nil
}

// SortedExtraKeys returns the unrecognised keys, in a stable order, so that a
// projection or a plan does not vary between runs.
func (f Frontmatter) SortedExtraKeys() []string {
	if len(f.Extra) == 0 {
		return nil
	}
	keys := make([]string, 0, len(f.Extra))
	for k := range f.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
