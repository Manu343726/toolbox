package hindsight

import (
	"strings"

	"github.com/Manu343726/toolbox/pkg/knowledge"
)

// The backend's page bundle states what each file is in its own YAML frontmatter, and this
// package reads that rather than guessing from a filename.
//
// The distinction matters once: the bundle's index is the backend's index of *pages*, and the
// whole-wiki view has one index of authored files and pages together. Using the backend's would
// produce a tree with two indexes, one of which lists a subset of the files the other does.
const (
	bundleKeyType = "type"
	bundleKeyID   = "id"

	bundleTypeIndex = "index"
	bundleTypeLog   = "log"
)

// bundleFrontmatter returns the frontmatter block of a bundle file's content, and whether the file
// has one.
//
// It is a deliberately shallow scan rather than a full parse: the bundle belongs to the backend,
// its shape is not this framework's contract, and a file whose frontmatter this package cannot
// parse is still a page that should appear in the tree. Reading a `type:` line and ignoring
// everything else degrades to "a page", which is the safe direction.
func bundleFrontmatter(content string) (map[string]string, bool) {
	text := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, false
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, false
	}
	fields := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" {
			continue
		}
		fields[key] = value
	}
	return fields, true
}

func bundleFileKind(content string) string {
	fields, ok := bundleFrontmatter(content)
	if !ok {
		return ""
	}
	return fields[bundleKeyType]
}

func bundleFileID(content string) (string, bool) {
	fields, ok := bundleFrontmatter(content)
	if !ok {
		return "", false
	}
	id := fields[bundleKeyID]
	return id, id != ""
}

// BundlePageID is the exported form of the bundle's page identifier, so a caller building a tree
// from a bundle it fetched itself identifies a file the same way this package does.
func BundlePageID(content string) (string, bool) { return bundleFileID(content) }

// BundleKind is the exported form of the bundle's declared file type: `index`, `log`, or empty for
// an ordinary page.
func BundleKind(content string) string { return bundleFileKind(content) }

// compile-time proof that the adapter satisfies the interface the reconcile is written against.
// Without it a signature change in the domain would surface as a runtime failure in a deployment
// rather than as a build failure here.
var _ knowledge.Engine = (*Client)(nil)
