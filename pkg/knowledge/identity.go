package knowledge

import "strings"

// IdentityFor resolves a corpus file's identifier, and reports whether the file
// declared it.
//
// The two cases differ in exactly one way, and the difference is the reason the
// declared form is recommended on anything that might move:
//
//	declared "wiki:auth"      a move is a move. The identifier is unchanged, the
//	                          facts extracted from the file survive, and
//	                          everything that referenced the old path stays valid.
//	derived  "docs/old/a.md"  a move is a delete and a create. The content is
//	                          re-extracted under a new identifier and every fact
//	                          that referenced the old one is orphaned.
//
// Both are computable from the file alone, which is what keeps a reconcile
// stateless. The alternative — an index file mapping paths to identities, which
// is what a synchroniser for a different corpus keeps outside the tree it syncs —
// introduces a second thing that can be lost and two indexers that can disagree.
// Reading identity from the file means a lost record costs nothing: the next plan
// recomputes it and a digest comparison says what is unchanged.
func IdentityFor(declared, rootName, relPath string) (id string, declaredIdentity bool) {
	if d := strings.TrimSpace(declared); d != "" {
		return d, true
	}
	return DocumentID(rootName, relPath), false
}
