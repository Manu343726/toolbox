package skill

import (
	"strings"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/skills"
	skillv1 "github.com/Manu343726/toolbox/pkg/skills/skillv1"
)

// validateEntry checks a skill a catalog served, by the same reader that checks one from a
// directory.
//
// A catalog is a provider and a provider is not this framework: it may be a subsystem in another
// deployment, or a user's own code, and it may hold a skill this framework cannot read. The
// check is therefore run here rather than assumed of the catalog — and the entry is read back
// into this framework's own form first, so a catalog that served a skill with no name, or a
// manifest that is not complete, is caught by the code that would otherwise have served it.
//
// It is what makes `Validate` worth having: a deployment would otherwise serve a broken skill to
// the first model that asked for it, and the person who could have fixed it would be looking at
// a transcript rather than at a startup failure.
func validateEntry(entry *skillv1.SkillEntry) error {
	if entry == nil {
		return api.Errorf(api.KindInvalid, "a catalog answered with no skill")
	}
	reference := skills.Reference{
		Catalog: entry.GetRef().GetCatalog(),
		Name:    entry.GetRef().GetName(),
	}
	if reference.Catalog == "" || reference.Name == "" {
		return api.Errorf(api.KindInvalid,
			"a catalog served a skill with no reference; a skill is identified by the catalog "+
				"that holds it and its own name, because two catalogs may hold a skill of the "+
				"same name")
	}
	// The served document is rendered here rather than taken from the entry, and the
	// manifest is **recomputed from it** rather than read from the entry. A digest in a
	// manifest a catalog asserted is worth exactly as much as the catalog asserted it, so
	// taking the manifest at face value would make this check agree with whatever it was
	// handed — which is the one thing a check must not do.
	document := entry.GetFrontmatter().AsMap()
	served := skills.NewFile(skills.SkillFileName,
		skills.RenderSkillMarkdown(document, entry.GetBody()))

	if err := verifyManifest(served, entry.GetResources(), reference); err != nil {
		return err
	}
	// The manifest becomes what the content actually is, so the checks below run against the
	// skill rather than against a description of it.
	manifest := fromManifest(entry.GetResources())
	for index := range manifest {
		if manifest[index].Path == skills.SkillFileName {
			manifest[index] = served
		}
	}
	template, err := skills.Parse(reference.Catalog, reference.Name,
		skills.RenderSkillMarkdown(document, entry.GetBody()), manifest)
	if err != nil {
		return err
	}
	return template.Validate()
}

// verifyManifest reports a digest or size in a served manifest that its content does not match.
//
// The specification has a host verify every file against the entry it was fetched under, and a
// host that cannot is a host that cannot tell corruption from a server that rewrote both. This
// is that check run before the entry leaves, where a mismatch can still be named as the
// catalog's rather than discovered by a model.
func verifyManifest(served skills.File, published []*skillv1.SkillFile, reference skills.Reference) error {
	if len(published) == 0 {
		return api.Errorf(api.KindInvalid,
			"the catalog served %s with no manifest. A skill's manifest is complete by "+
				"construction — a catalog that cannot enumerate a skill's files does not serve it",
			reference)
	}
	// Only the skill's own document can be verified here, because it is the only file whose
	// content travels with the entry. A catalog's other files are not in the entry, so their
	// digests cannot be recomputed from it and this framework does not pretend otherwise: it
	// checks the shape of the claim — each file once, a well-formed digest, a real size, and
	// the skill's own file present — and the *other* files are verified when they are read,
	// which is where their content is in hand. See verifyFile.
	seen := make(map[string]bool, len(published))
	for _, entry := range published {
		path := entry.GetPath()
		if seen[path] {
			return api.Errorf(api.KindInvalid,
				"the catalog served %s with %q in its manifest twice; a manifest lists each "+
					"file exactly once", reference, path)
		}
		seen[path] = true
		if entry.GetSize() < 0 {
			return api.Errorf(api.KindInvalid,
				"the catalog served %s with a manifest saying %q is %d bytes, which is not a "+
					"size", reference, path, entry.GetSize())
		}
		if !strings.HasPrefix(entry.GetDigest(), skills.DigestPrefix) ||
			len(entry.GetDigest()) != len(skills.DigestPrefix)+64 {
			return api.Errorf(api.KindInvalid,
				"the catalog served %s with a manifest saying %q hashes to %q, which is not a "+
					"%s digest", reference, path, entry.GetDigest(), skills.DigestPrefix)
		}
	}
	own, present := seen[skills.SkillFileName]
	if !present {
		return api.Errorf(api.KindInvalid,
			"the catalog served %s with a manifest that does not list its own %s",
			reference, skills.SkillFileName)
	}
	_ = own
	if served.Digest != digestOfPublished(published, skills.SkillFileName) {
		return api.Errorf(api.KindInvalid,
			"the catalog served %s with a manifest saying its %s hashes to %s, and the "+
				"document it served hashes to %s. A digest in a manifest a server published "+
				"cannot establish trust, so this framework checks it rather than passing it on",
			reference, skills.SkillFileName,
			digestOfPublished(published, skills.SkillFileName), served.Digest)
	}
	if served.Size != sizeOfPublished(published, skills.SkillFileName) {
		return api.Errorf(api.KindInvalid,
			"the catalog served %s with a manifest saying its %s is %d bytes, and the document "+
				"it served is %d", reference, skills.SkillFileName,
			sizeOfPublished(published, skills.SkillFileName), served.Size)
	}
	return nil
}

func digestOfPublished(published []*skillv1.SkillFile, path string) string {
	for _, file := range published {
		if file.GetPath() == path {
			return file.GetDigest()
		}
	}
	return ""
}

func sizeOfPublished(published []*skillv1.SkillFile, path string) int64 {
	for _, file := range published {
		if file.GetPath() == path {
			return file.GetSize()
		}
	}
	return 0
}

// verifyFile checks content against the manifest entry it was fetched under.
//
// The specification has a *host* verify every file against the entry it fetched, and a mismatch
// means the content is not what the entry described. A server that verifies first can refuse to
// serve at all, which is strictly better: a host is told the content is wrong, and this
// deployment is told its own catalog is wrong.
//
// What a digest is not is a trust anchor, and this does not make it one. It confirms that the
// entry and the bytes agree, and it cannot establish that a skill is safe, cannot defend
// against the catalog itself, and cannot detect an intermediary that rewrote both together.
func verifyFile(reference skills.Reference, entry skills.File, content []byte) error {
	actual := skills.NewFile(entry.Path, content)
	if actual.Digest != entry.Digest {
		return api.Errorf(api.KindInvalid,
			"the file %s of %s is %d bytes hashing to %s, and its manifest says %d bytes "+
				"hashing to %s. The content is not what the manifest described, so it is not "+
				"served; a digest confirms that an entry and its content agree, and says "+
				"nothing about whether either is safe",
			entry.Path, reference, actual.Size, actual.Digest, entry.Size, entry.Digest)
	}
	if actual.Size != entry.Size {
		return api.Errorf(api.KindInvalid,
			"the file %s of %s is %d bytes and its manifest says %d",
			entry.Path, reference, actual.Size, entry.Size)
	}
	return nil
}
