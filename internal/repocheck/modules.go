package repocheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Modules returns the module directories under a repository's subsystems
// directory, by name and in order.
//
// A directory is a module when it holds a go.mod. Every subsystem is one — that
// is the layout the architecture requires — so a subsystem directory without one
// is not a module and is not reported here; ModuleLayout reports it instead.
func Modules(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "subsystems"))
	if err != nil {
		return nil, err
	}
	modules := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "subsystems", entry.Name(), "go.mod")); err != nil {
			continue
		}
		modules = append(modules, "subsystems/"+entry.Name())
	}
	sort.Strings(modules)
	return modules, nil
}

// Uncovered reports the modules that are not named in a CI matrix.
//
// The matrix in the workflow is a hand-written list, and a hand-written list is
// one a new subsystem does not appear in. Nothing fails when that happens: the
// matrix runs, every module it names passes, and the subsystem that was just
// added is never tested on its own by anything. That is the same shape of failure
// as the checks in this package — a mistake that produces no error at all — so it
// is a check too rather than a convention.
func Uncovered(root string, matrix []string) ([]string, error) {
	modules, err := Modules(root)
	if err != nil {
		return nil, err
	}
	named := make(map[string]bool, len(matrix))
	for _, entry := range matrix {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// The matrix may name a module with a trailing slash, because a shell
		// expands one and a person writes one.
		named[strings.TrimSuffix(entry, "/")] = true
	}
	uncovered := make([]string, 0)
	for _, module := range modules {
		if !named[module] {
			uncovered = append(uncovered, module)
		}
	}
	return uncovered, nil
}

// Unlisted reports matrix entries that name no module, which is the other way the
// list goes stale: a subsystem that was renamed or removed leaves a row that
// tests nothing and still reports green.
func Unlisted(root string, matrix []string) ([]string, error) {
	modules, err := Modules(root)
	if err != nil {
		return nil, err
	}
	existing := make(map[string]bool, len(modules))
	for _, module := range modules {
		existing[module] = true
	}
	// cmd/toolbox is the host, which is a module but not a subsystem, so the
	// matrix legitimately names it too.
	existing["cmd/toolbox"] = true
	unlisted := make([]string, 0)
	for _, entry := range matrix {
		entry = strings.TrimSuffix(strings.TrimSpace(entry), "/")
		if entry == "" {
			continue
		}
		if !existing[entry] {
			unlisted = append(unlisted, entry)
		}
	}
	sort.Strings(unlisted)
	return unlisted, nil
}

// MissingModule reports a directory under subsystems that holds no go.mod, which
// makes it a package of the root module rather than the independent project the
// architecture says every subsystem is.
func MissingModule(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "subsystems"))
	if err != nil {
		return nil, err
	}
	missing := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "subsystems", entry.Name(), "go.mod")); err == nil {
			continue
		}
		missing = append(missing, "subsystems/"+entry.Name())
	}
	sort.Strings(missing)
	return missing, nil
}

// MatrixError names a module the CI matrix does not test, and says what to do.
func MatrixError(module string) error {
	return fmt.Errorf(
		"%s is a module and is not in the CI matrix, so nothing tests it on its own. "+
			"Add it under independent.matrix.module in .github/workflows/ci.yml", module)
}

// UnlistedError names a matrix entry that tests nothing.
func UnlistedError(entry string) error {
	return fmt.Errorf(
		"%s is in the CI matrix and is not a module, so that job tests nothing and still "+
			"reports green. Remove it, or restore the module it names", entry)
}

// MissingModuleError names a subsystem directory that is not a module.
func MissingModuleError(dir string) error {
	return fmt.Errorf(
		"%s has no go.mod, so it is a package of the root module rather than the independent "+
			"project every subsystem is. AGENTS.md rule 1 gives each subsystem its own module", dir)
}
