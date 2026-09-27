package knowledge

import (
	"strconv"
	"strings"
	"time"
)

// The tag namespace.
//
// Two vocabularies share one tag space and must not be confused.
//
// The structural dimensions are the ones a base already has to filter on, and
// they adopt the spelling the shipped Hindsight client established, so that a
// base written by that client and a base written by this one are filterable by
// the same expression:
//
//	vault:<name>              the corpus a file came from
//	folder:<ancestor>         one per ancestor folder, cumulative
//	created:<YYYY>            the year an event happened
//	created:<YYYY-MM>         the month
//	updated:<YYYY>            the year the file last changed
//	updated:<YYYY-MM>         the month
//
// The framework reserves the "toolbox:" prefix for itself and nothing else uses
// it, so a deployment cannot collide with it and a person can adopt any
// convention they like in their own files without this framework's filters
// changing meaning underneath them.
const (
	// TagNamespaceFramework is reserved for this framework's own dimensions.
	TagNamespaceFramework = "toolbox"
	// TagVault scopes a file to one corpus.
	TagVault = "vault"
	// TagFolder scopes a file to one folder, once per ancestor.
	TagFolder = "folder"
	// TagCreated and TagUpdated bucket a date.
	TagCreated = "created"
	TagUpdated = "updated"
)

// Date buckets are a workaround for a real limitation rather than a style
// choice: the backend has no date-range filter at all, so "last quarter" is not
// expressible. A date that is only a field is a date nobody can filter on, and
// the corpus is full of dates. Bucketing a date into a year and a year-month is
// what turns it into something the existing tag filter already does.
//
// timelessTimestamp is the one spelling the backend accepts for a retained item
// that has no event time.
//
// It is a string rather than an omitted field because the field is a timestamp
// and a zero timestamp is a real instant. No shipped client sends it: they all
// send a file's change time, which is a claim about when a person saved a file
// rather than about when the thing described happened.
const timelessTimestamp = "unset"

// UpdateModeReplace tells the backend to delete an existing document and
// re-extract it rather than appending to it.
//
// The schema carries no default for this field, so a generated client sees no
// default and the value must always be sent explicitly. Getting it wrong here
// is not a tuning question: an edited runbook must stop being retrievable by the
// sentence it used to contain, or an assistant answers from a superseded claim.
const UpdateModeReplace = "replace"

// contextValue names this subsystem on every item it retains, so that a base
// holding several writers can tell them apart.
const contextValue = "toolbox-knowledge"

// TagOptions configures tag derivation.
type TagOptions struct {
	// RootName is the corpus root's name, which scopes identifiers and becomes
	// the vault tag.
	RootName string
	// Owner is the identity written into the ownership marker, so that a prune
	// can tell this reconciler's documents from anybody else's in a shared base.
	Owner string
	// UseModTimeForDates permits falling back to a file's modification time when
	// the author declared no date. It is off by default.
	//
	// The fallback is an approximation wearing a precise label. A modification
	// time says when a person last saved a file, which is not when the thing the
	// file describes happened; and on Linux a file's "created" time is its inode
	// change time, which moves on every rewrite. A corpus that cares about dates
	// declares them, and a corpus that does not is better served by a timeless
	// ingest than by a wrong one.
	UseModTimeForDates bool
}

// TagsFor returns the tags for a corpus file.
//
// The author's own tags and aliases are carried through beside the framework's
// rather than folded into it, because a person's taxonomy and the framework's
// are different vocabularies and flattening them would make one of them
// unfilterable.
func TagsFor(f File, opts TagOptions) []string {
	tags := make([]string, 0, 16)

	if opts.RootName != "" {
		tags = append(tags, TagVault+":"+opts.RootName)
	}
	for _, ancestor := range PathAncestors(f.Path) {
		tags = append(tags, TagFolder+":"+ancestor)
	}

	// Dates, from the author's declaration when there is one.
	event, declared := f.Front.EventTime(f.ModTime)
	if declared || opts.UseModTimeForDates {
		if !event.IsZero() {
			tags = append(tags, dateBuckets(TagCreated, event)...)
		}
	}
	if !f.ModTime.IsZero() {
		tags = append(tags, dateBuckets(TagUpdated, f.ModTime)...)
	}

	// The framework's own dimensions.
	tags = append(tags, frameworkTag("origin", string(OriginAuthored)))
	if f.Front.Kind != KindUnspecified {
		tags = append(tags, frameworkTag("kind", string(f.Front.Kind)))
	}
	if f.Front.Status != StatusUnspecified {
		tags = append(tags, frameworkTag("status", string(f.Front.Status)))
	}
	authority := f.Front.Authority
	if authority == AuthorityUnspecified {
		// A corpus file with no declared authority is a file a person wrote; the
		// directory of reviewed markdown has no other origin. Stating it makes
		// the filter answerable rather than inferred.
		authority = AuthorityHuman
	}
	tags = append(tags, frameworkTag("authority", string(authority)))
	if f.Front.Source != "" {
		tags = append(tags, frameworkTag("source", f.Front.Source))
	}
	if f.Front.Owner != "" {
		tags = append(tags, frameworkTag("owner", f.Front.Owner))
	}
	for _, s := range f.Front.Supersedes {
		tags = append(tags, frameworkTag("supersedes", s))
	}
	if opts.Owner != "" {
		tags = append(tags, frameworkTag("owned-by", opts.Owner))
	}

	// The author's vocabulary, unflattened.
	tags = append(tags, f.Front.Tags...)
	tags = append(tags, f.Front.Aliases...)

	return SortedTags(tags)
}

func frameworkTag(dimension, value string) string {
	return TagNamespaceFramework + ":" + dimension + "=" + value
}

// dateBuckets returns the year and year-month tags for a date.
//
// Two buckets rather than one, because a year is the coarse filter people
// actually reach for and the month is the one that makes a range expressible:
// a filter for "since March" is a disjunction over the months from March on, and
// with only year buckets that query is not answerable at all.
func dateBuckets(dimension string, t time.Time) []string {
	t = t.UTC()
	return []string{
		dimension + ":" + strconv.Itoa(t.Year()),
		dimension + ":" + t.Format("2006-01"),
	}
}

// OwnedByTag returns the tag that marks content as owned by an identity, and
// whether the content carries it.
//
// This is the check a prune makes before it deletes anything: a document whose
// ownership marker names somebody else is not this reconciler's to remove, even
// if it sits in a base this reconciler writes to.
func OwnedByTag(tags []string, owner string) bool {
	if owner == "" {
		return false
	}
	want := frameworkTag("owned-by", owner)
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// OriginTag returns the framework tag naming an origin.
func OriginTag(o Origin) string { return frameworkTag("origin", string(o)) }

// IngestTimestamp returns the value to send as a retained item's event time.
//
// A file that declares a date is ingested with it. A file that declares none is
// ingested as timeless, because the alternative is asserting that something
// happened at the moment a person last pressed save, and an assistant that
// reasons about dates would believe it.
func IngestTimestamp(f File) string {
	if t, ok := f.Front.AuthoredAt(); ok {
		return t.UTC().Format(time.RFC3339)
	}
	return timelessTimestamp
}

// TimelessTimestamp is the value IngestTimestamp returns for content with no
// event time, exposed so a caller writing a test or a document can name it
// without repeating the literal.
func TimelessTimestamp() string { return timelessTimestamp }

// ParseTagGroup is a small reader for the framework's own dimensions, so that a
// caller can recover a value from a tag without parsing strings by hand.
//
// It only understands the reserved namespace. A person's own tags are opaque to
// this package and stay that way.
func ParseTagGroup(tag string) (dimension, value string, ok bool) {
	rest, found := strings.CutPrefix(tag, TagNamespaceFramework+":")
	if !found {
		return "", "", false
	}
	dimension, value, found = strings.Cut(rest, "=")
	if !found || dimension == "" || value == "" {
		return "", "", false
	}
	return dimension, value, true
}
