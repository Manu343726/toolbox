// Package contractcheck holds the checks that run against a contract — what a subsystem declares
// — rather than against the shape of a file.
//
// It is a separate package from `internal/repocheck` for one reason, and the reason is a
// constraint rather than a preference. `repocheck` reads files and uses nothing but the standard
// library, so it runs on a bare checkout: the CI job that asserts no Go file is one the
// toolchain skips runs it before anything is generated, and that job is only cheap because the
// check needs no build. A check that reads a contract's annotations needs `pkg/docs` to compile
// the contract and `pkg/api` to interpret what it found, and both of those need generated
// `.pb.go` — so importing them from `repocheck` would have made a job named *formatting and staged
// files* depend on a full protobuf toolchain, and it would have failed on a package that has
// nothing to do with formatting.
//
// The dividing line is the subject. `repocheck` asks whether the files in this tree are the files
// they claim to be. This package asks whether a contract says what it means.
package contractcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	apimodel "github.com/Manu343726/toolbox/pkg/api"
	shareddocs "github.com/Manu343726/toolbox/pkg/docs"

	// Linked so its generated descriptor is in the binary, which is how a contract that
	// imports `toolbox/skills/v1/skill.proto` resolves below. The resolver falls back to the
	// descriptors already linked into the process, so a subsystem does not have to embed a file
	// it does not own — and the check compiles contracts the way the framework does rather than
	// the way that is convenient here.
	_ "github.com/Manu343726/toolbox/pkg/skills"
)

// The checks in this file are about a contract that exists and is not what it says it is.
//
// Both failures here are silent, and that is what makes them worth a check rather than a review
// note. Neither produces an error, neither fails a build, and neither is visible in a deployment's
// output:
//
//   - A subsystem that declares a contract and does not embed its source serves operations that
//     nobody classified. An unclassified operation is refused, which is the safe direction, so a
//     deployment withholding it looks exactly like a deployment whose policy is working.
//   - A method that declares no side effects is unclassified for the same reason and with the
//     same consequence.
//
// Together they are how a read stops being callable. The first was found in `subsystems/skillgit`,
// whose every method is annotated and all seven of which reached the policy layer unclassified
// because the subsystem had no `docs_embed.go`. The annotation above `SyncCatalog` then turned out
// to say `read_only` for an operation that runs `git pull`, and that misdeclaration had never been
// exercised because the classification never arrived — so one missing file hid a second defect
// that would have granted an agent the ability to change what a catalog contains.
//
// The checks are therefore narrow: they assert that the classification *reached* the framework,
// and they read it through `pkg/docs` and `pkg/api` rather than by re-parsing the source. A check
// that agreed with the runtime by reimplementing it would be a second translation of one rule, and
// two translations stop agreeing the first time one of them changes.

// contractFile is one .proto this repository declares.
type contractFile struct {
	// Subsystem is the directory holding it, relative to the tree checked:
	// `subsystems/skillgit`, or `pkg/skills`.
	Subsystem string
	// Path is the file, relative to the same tree, and is the name the source is registered
	// under — so it is also what an `import` in another contract has to say.
	Path string
	// Source is the file's text, which is where the annotations are.
	Source []byte
}

// UnembeddedContract is one subsystem that declares a contract and does not embed its source.
type UnembeddedContract struct {
	// Subsystem is the directory holding the contract, relative to the tree checked.
	Subsystem string
	// Proto is the contract, relative to the same tree.
	Proto string
}

// Error names the contract, the file that should have embedded it, and what the omission costs,
// because "the build passes" is not a reason anyone would otherwise look.
func (u UnembeddedContract) Error() string {
	return fmt.Sprintf(
		"%s declares %s but nothing embeds its source, so nothing reads the comments in it. "+
			"The @toolbox.side-effects annotations live in those comments, so every method "+
			"reaches the policy layer unclassified and is refused — which is indistinguishable "+
			"from a policy doing its job. Add a docs_embed.go: a //go:embed on the proto, "+
			"passed to shareddocs.RegisterEmbeddedProtoSource from an init, panicking if the "+
			"contract cannot be read",
		u.Subsystem, u.Proto)
}

// UnembeddedContracts returns the subsystems that declare a contract and do not embed its source.
//
// The directive has to name *this* contract, not merely exist: a subsystem that embeds a file it
// has since replaced keeps a `docs_embed.go` that documents nothing, and a check that only looked
// for the file would pass it.
func UnembeddedContracts(root string) ([]UnembeddedContract, error) {
	contracts, err := contractFiles(root)
	if err != nil {
		return nil, err
	}
	var missing []UnembeddedContract
	for _, contract := range contracts {
		relative := strings.TrimPrefix(contract.Path, contract.Subsystem+"/")
		embed := filepath.Join(root, filepath.FromSlash(contract.Subsystem), "docs_embed.go")
		content, err := os.ReadFile(embed)
		if err != nil || !strings.Contains(string(content), "//go:embed "+relative) {
			missing = append(missing, UnembeddedContract{
				Subsystem: contract.Subsystem,
				Proto:     contract.Path,
			})
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Proto < missing[j].Proto })
	return missing, nil
}

// UndeclaredEffect is one method whose contract does not state a side effect this framework
// recognises.
type UndeclaredEffect struct {
	// Subsystem is the directory holding the contract, relative to the tree checked.
	Subsystem string
	// Service is the fully qualified service the method belongs to.
	Service string
	// Method is the method's name.
	Method string
	// Proto is the contract declaring it, relative to the tree checked.
	Proto string
	// Declared is what the method's comment did write, which is empty when it wrote nothing
	// and is a near-miss when the key or a value was misspelled.
	Declared []string
}

// Error names the method, what its comment wrote instead, and what the policy can then do with it.
func (u UndeclaredEffect) Error() string {
	subject := u.Service + "/" + u.Method
	if len(u.Declared) == 0 {
		return fmt.Sprintf(
			"%s declares no %s, so %s is unclassified. Unclassified is not a read: the default "+
				"policy grants reads and withholds writes, and it withholds this too, so an "+
				"operation the subsystem serves cannot be called at all. State what invoking it "+
				"does",
			u.Proto, apimodel.AnnotationSideEffects, subject)
	}
	return fmt.Sprintf(
		"%s annotates %s with %s, which this framework does not recognise, so it is "+
			"unclassified exactly as a method that declares nothing is. Accepted values: %s",
		u.Proto, subject, strings.Join(u.Declared, " "), strings.Join(knownEffects(), ", "))
}

// UndeclaredEffects returns the methods whose contract states no side effect this framework
// recognises.
//
// A value it does not recognise is reported next to a method that declared nothing, because the
// two reach the policy layer identically. `sideeffect` without the hyphen, or `read-only` instead
// of `read_only`, costs exactly as much as an absent line — and it is the typo that is harder to
// see, because an absent annotation looks like an oversight and a misspelled one looks deliberate.
func UndeclaredEffects(root string) ([]UndeclaredEffect, error) {
	methods, err := contractMethods(root)
	if err != nil {
		return nil, err
	}
	var undeclared []UndeclaredEffect
	for _, method := range methods {
		effects, _ := apimodel.SideEffects(method.Annotations)
		if len(effects) > 0 {
			continue
		}
		undeclared = append(undeclared, UndeclaredEffect{
			Subsystem: method.Subsystem,
			Service:   method.Service,
			Method:    method.Name,
			Proto:     method.Proto,
			Declared:  sideEffectLikeValues(method.Annotations),
		})
	}
	sort.Slice(undeclared, func(i, j int) bool {
		if undeclared[i].Service != undeclared[j].Service {
			return undeclared[i].Service < undeclared[j].Service
		}
		return undeclared[i].Method < undeclared[j].Method
	})
	return undeclared, nil
}

// UndocumentedOperation is one operation a feature document describes in prose without naming.
type UndocumentedOperation struct {
	// Subsystem is the directory holding the contract, relative to the tree checked.
	Subsystem string
	// Document is the document that describes the subsystem.
	Document string
	// Service is the fully qualified service the operation belongs to.
	Service string
	// Method is the operation's name.
	Method string
}

// Error names the operation and says what a reader cannot do without it.
func (u UndocumentedOperation) Error() string {
	return fmt.Sprintf(
		"%s describes %s in prose but never names %s, so a reader who has read the prose has "+
			"no way to find the method. A method name is the only handle on a protobuf "+
			"operation, and prose describing what it does is not it",
		u.Document, u.Service, u.Method)
}

// featureDocument names the document that describes one subsystem, for the subsystems that have
// one.
//
// **The table is the check, as much as the rule is.** A subsystem is in it because a document in
// this repository claims to describe that feature, and a claim like that is checkable: the prose
// is there to be read, and a reader who follows it has to be able to arrive at the method. A
// subsystem that is *not* in it is not failing anything — no document claims to describe it, and
// its contract is self-documenting, because protoc-gen-go writes every comment into the generated
// Go and `pkg/docs` serves them as CLI help and as MCP tool descriptions. Demanding a prose
// mention of `apitools`' nineteen methods in `docs/subsystems.md` would mean writing a document
// to satisfy the check rather than because a reader needed one.
//
// So the rule is deliberately about the documents that exist, and the requirement that a row name
// a real file and a real subsystem is what stops the table from being a place to hide a subsystem.
var featureDocuments = []struct {
	// Subsystem is the directory holding the contract, relative to the repository root.
	Subsystem string
	// Document is the file describing it, relative to the same tree.
	Document string
}{
	{Subsystem: "subsystems/skill", Document: "docs/skills.md"},
	{Subsystem: "subsystems/skillgit", Document: "docs/skills.md"},
	{Subsystem: "subsystems/policy", Document: "docs/policy.md"},
	{Subsystem: "subsystems/logger", Document: "docs/logging.md"},
}

// MisplacedFeatureDocument is a row of the feature-document table that does not describe anything.
type MisplacedFeatureDocument struct {
	// Subsystem is the directory the row names, relative to the repository root.
	Subsystem string
	// Document is the file the row names, relative to the same tree.
	Document string
	// Reason says what is wrong with it.
	Reason string
}

// Error names the row and what is wrong with it.
func (m MisplacedFeatureDocument) Error() string {
	return fmt.Sprintf(
		"the feature-document table names %s as described by %s, and %s. A row that describes "+
			"nothing exempts a subsystem from being described, so a stale one is a hole rather "+
			"than a redundancy",
		m.Subsystem, m.Document, m.Reason)
}

// UndocumentedOperations returns the operations a feature document describes without naming.
//
// **One direction only, and the other is a mistake.** A document naming something the contracts
// do not declare is not a defect here. A specification is supposed to name what it proposes:
// `docs/knowledge.md` and ADR-0014 name forty-nine and five things respectively that nothing
// declares yet, and that is them doing their job — a specification that hides its open questions
// reads as more finished than it is. An investigation quotes the API of the system it surveyed,
// and those names belong to somebody else's release. Requiring every backticked name under `docs/`
// to be declared would flag both, and the exemptions it needs are a hand-written list that goes
// stale the way hand-written lists of exceptions always do.
//
// The direction with no false positives is the other one. A document describes a feature in prose,
// and the prose has to reach the method.
func UndocumentedOperations(root string) ([]UndocumentedOperation, error) {
	methods, err := contractMethods(root)
	if err != nil {
		return nil, err
	}
	var undocumented []UndocumentedOperation
	for _, row := range featureDocuments {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(row.Document)))
		if err != nil {
			return nil, fmt.Errorf("the feature document %s named for %s cannot be read: %w",
				row.Document, row.Subsystem, err)
		}
		document := string(content)
		for _, method := range methods {
			if method.Subsystem != row.Subsystem {
				continue
			}
			if strings.Contains(document, method.Name) {
				continue
			}
			undocumented = append(undocumented, UndocumentedOperation{
				Subsystem: method.Subsystem,
				Document:  row.Document,
				Service:   method.Service,
				Method:    method.Name,
			})
		}
	}
	sort.Slice(undocumented, func(i, j int) bool {
		if undocumented[i].Document != undocumented[j].Document {
			return undocumented[i].Document < undocumented[j].Document
		}
		if undocumented[i].Service != undocumented[j].Service {
			return undocumented[i].Service < undocumented[j].Service
		}
		return undocumented[i].Method < undocumented[j].Method
	})
	return undocumented, nil
}

// MisplacedFeatureDocuments returns the rows of the feature-document table that name a subsystem
// with no contract or a document that is not there.
//
// A table that can hold a stale row is a table that goes stale, and a stale row is worse than no
// row: it reads as coverage, so the subsystem it names is exempted from being described by a
// document that does not exist. It is the same failure as a CI matrix row naming no module, and it
// is checked for the same reason.
func MisplacedFeatureDocuments(root string) ([]MisplacedFeatureDocument, error) {
	contracts, err := contractFiles(root)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, contract := range contracts {
		known[contract.Subsystem] = true
	}
	var misplaced []MisplacedFeatureDocument
	for _, row := range featureDocuments {
		if !known[row.Subsystem] {
			misplaced = append(misplaced, MisplacedFeatureDocument{
				Subsystem: row.Subsystem,
				Document:  row.Document,
				Reason:    "no contract is declared there",
			})
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(row.Document))); err != nil {
			misplaced = append(misplaced, MisplacedFeatureDocument{
				Subsystem: row.Subsystem,
				Document:  row.Document,
				Reason:    "the document does not exist",
			})
		}
	}
	sort.Slice(misplaced, func(i, j int) bool { return misplaced[i].Subsystem < misplaced[j].Subsystem })
	return misplaced, nil
}

// documentedMethod is one method of one contract, with the annotations its comment declared.
type documentedMethod struct {
	// Subsystem is the directory holding the contract, relative to the tree checked.
	Subsystem string
	// Proto is the contract declaring it, relative to the tree checked.
	Proto string
	// Service is the fully qualified service it belongs to.
	Service string
	// Name is the method's name.
	Name string
	// Annotations are the machine-readable lines its comment declared.
	Annotations shareddocs.AnnotationSet
}

// contractMethods compiles every contract and returns every method, annotated as the framework
// reads it.
//
// Compilation goes through `shareddocs` on purpose. The check needs the same answer the runtime
// gets, including how annotations are found in a comment, and a second parser would agree with it
// right up until one of them changed.
func contractMethods(root string) ([]documentedMethod, error) {
	contracts, err := contractFiles(root)
	if err != nil {
		return nil, err
	}
	if err := registerSources(contracts); err != nil {
		return nil, err
	}
	var methods []documentedMethod
	for _, contract := range contracts {
		file, err := shareddocs.CompileProtoSource(contract.Path)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", contract.Path, err)
		}
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			service := services.Get(i)
			documented := shareddocs.ExtractServiceDocumentation(service)
			for _, method := range documented.Methods {
				methods = append(methods, documentedMethod{
					Subsystem:   contract.Subsystem,
					Proto:       contract.Path,
					Service:     string(service.FullName()),
					Name:        method.Name,
					Annotations: method.Annotations,
				})
			}
		}
	}
	return methods, nil
}

// knownEffects are the values `@toolbox.side-effects` accepts, for a message that has to say what
// is accepted — a refusal a reader cannot act on is a refusal they route around.
func knownEffects() []string {
	return []string{
		string(apimodel.SideEffectReadOnly),
		string(apimodel.SideEffectCreate),
		string(apimodel.SideEffectUpdate),
		string(apimodel.SideEffectDelete),
		string(apimodel.SideEffectExternal),
	}
}

// sideEffectLikeValues returns what a comment wrote where a side-effect declaration was expected.
//
// It matches on the shape of the word rather than on the exact key, because the case worth
// reporting is the near-miss: `sideeffect`, `side_effects`, `read-only`. Returning them lets the
// message say what was written instead of only that nothing was recognised, which is the
// difference between a fix and a search.
func sideEffectLikeValues(annotations shareddocs.AnnotationSet) []string {
	var values []string
	for _, annotation := range annotations {
		compact := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(annotation.Key))
		if !strings.Contains(compact, "sideeffect") {
			continue
		}
		if len(annotation.Values) == 0 {
			values = append(values, annotation.Key)
			continue
		}
		values = append(values, append([]string{annotation.Key}, annotation.Values...)...)
	}
	return values
}

// contractFiles returns every .proto this repository declares.
//
// Found by walking for `proto/` rather than from a list, so a subsystem added tomorrow is checked
// without anything here being edited — which is the property that makes this worth having over a
// table, and the same reason the CI matrix check exists for modules.
func contractFiles(root string) ([]contractFile, error) {
	var contracts []contractFile
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if shouldSkipTree(relative) {
			return filepath.SkipDir
		}
		if filepath.Base(path) != "proto" {
			return nil
		}
		// The owning directory is the one holding `proto/`, which is where a
		// `docs_embed.go` lives. Reporting the `proto/` directory itself would look for
		// the file in a directory that has never held one.
		owner := filepath.ToSlash(filepath.Dir(relative))
		found, walkErr := protosUnder(root, path, owner)
		if walkErr != nil {
			return walkErr
		}
		contracts = append(contracts, found...)
		// A `proto/` holds contracts and nothing that can hold another, so the walk stops
		// here rather than counting the same file a second time.
		return filepath.SkipDir
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].Path < contracts[j].Path })
	return contracts, nil
}

// protosUnder collects the contracts in one `proto/` directory.
func protosUnder(root, protoDir, subsystem string) ([]contractFile, error) {
	var contracts []contractFile
	err := filepath.WalkDir(protoDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".proto" {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contracts = append(contracts, contractFile{
			Subsystem: subsystem,
			Path:      filepath.ToSlash(relative),
			Source:    source,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return contracts, nil
}

// registerSources makes every contract readable by `shareddocs`.
//
// A source that is already registered is left alone: a subsystem's own `init` registers it, and
// this file deliberately imports nothing from a subsystem so the check can run against a tree that
// does not compile.
func registerSources(contracts []contractFile) error {
	for _, contract := range contracts {
		err := shareddocs.RegisterProtoSource(contract.Path, contract.Source)
		if err == nil || strings.Contains(err.Error(), "already registered") {
			continue
		}
		return fmt.Errorf("register %s: %w", contract.Path, err)
	}
	return nil
}

// shouldSkipTree reports whether a directory is not part of the repository's own source.
func shouldSkipTree(relative string) bool {
	relative = filepath.ToSlash(relative)
	if relative == "" || relative == "." {
		return false
	}
	for _, part := range strings.Split(relative, "/") {
		switch part {
		case "node_modules", "bin", "vendor", ".git":
			return true
		}
	}
	return false
}

// EnvRoot names the directory the checks should run against, relative to the repository root or
// absolute. It exists for the same reason `repocheck` has one: the CI matrix tests every
// subsystem on its own, and a subsystem whose contract is not classified should fail in the job
// that asserts it stands alone rather than only in the workspace job.
const EnvRoot = "REPOCHECK_ROOT"

// narrowed reports whether the checks were pointed at one module rather than the repository.
//
// The two whole-repository judgements are skipped when they are. A row of the feature-document
// table naming a subsystem that is not under the root being checked is not a defect — it is the
// table being read from the wrong place — and a document describing a subsystem is not visible
// from inside one module at all. Skipping is the honest answer; running them narrowed would report
// green for the wrong reason, which is worse than reporting nothing.
func narrowed() bool { return os.Getenv(EnvRoot) != "" }
