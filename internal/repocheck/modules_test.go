package repocheck_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Manu343726/toolbox/internal/repocheck"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matrixEntry matches one entry of the independent job's module matrix.
var matrixEntry = regexp.MustCompile(`(?m)^\s{10}-\s+(subsystems/\S+|cmd/toolbox)\s*$`)

// workflowMatrix reads the module matrix out of the workflow.
//
// The list is parsed from the file rather than restated here, because a test that
// kept its own copy of the matrix would agree with the workflow whether or not the
// workflow were right — which is the whole thing being checked. Only the shape of
// the YAML is understood: the matrix is a block list of module paths, and finding
// those lines is enough.
//
// It skips when it is pointed at a tree with no workflow, rather than failing. The
// matrix is a property of the whole repository, so the check is skipped when
// REPOCHECK_ROOT narrows it to one module — which is what the CI matrix job does,
// and a check that fails because it was asked a narrower question than it answers
// is a check that gets ignored.
func workflowMatrix(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, ".github", "workflows", "ci.yml")
	source, err := os.ReadFile(path)
	if err != nil {
		if os.Getenv(EnvRoot) != "" {
			t.Skipf("%s is a property of the whole repository, and this tree is narrowed to %s",
				"the CI matrix", EnvRoot)
		}
		require.NoError(t, err, "the workflow is the thing being checked, so it has to be there")
	}

	entries := matrixEntry.FindAllStringSubmatch(string(source), -1)
	require.NotEmpty(t, entries, "no module matrix found in %s; the check is looking at the wrong shape", path)

	matrix := make([]string, 0, len(entries))
	for _, entry := range entries {
		// The regexp keeps the path in the first group and the surrounding
		// whitespace in the second.
		matrix = append(matrix, entry[1])
	}
	return matrix
}

// A subsystem that is not in the CI matrix is never tested on its own by anything,
// and nothing fails when that happens. The matrix runs, every module it names
// passes, and the subsystem that was just added is silently untested.
func TestEveryModuleIsInTheCIMatrix(t *testing.T) {
	root := repositoryRoot(t)
	matrix := workflowMatrix(t, root)

	uncovered, err := repocheck.Uncovered(root, matrix)
	require.NoError(t, err)
	for _, module := range uncovered {
		t.Error(repocheck.MatrixError(module).Error())
	}

	// The other way the list goes stale: a row naming nothing still reports green.
	unlisted, err := repocheck.Unlisted(root, matrix)
	require.NoError(t, err)
	for _, entry := range unlisted {
		t.Error(repocheck.UnlistedError(entry).Error())
	}
}

// Every subsystem is an independent project, which is the layout the architecture
// requires. A subsystem directory without a go.mod is a package of the root
// module, and the matrix cannot test it standalone because there is nothing to
// disable the workspace against.
//
// Like the matrix check, this is a property of the whole repository and is skipped
// when the tree has been narrowed to one module.
func TestEverySubsystemIsItsOwnModule(t *testing.T) {
	root := repositoryRoot(t)
	if _, err := os.Stat(filepath.Join(root, "subsystems")); err != nil {
		t.Skip("this tree is narrowed to one module, so the subsystem layout is not visible from it")
	}
	missing, err := repocheck.MissingModule(root)
	require.NoError(t, err)
	for _, dir := range missing {
		t.Error(repocheck.MissingModuleError(dir).Error())
	}
}

// The matrix must not name a module twice: a duplicate row spends a runner on a
// second identical job, and the cost is paid on every push.
func TestTheCIMatrixNamesEachModuleOnce(t *testing.T) {
	seen := map[string]int{}
	for _, entry := range workflowMatrix(t, repositoryRoot(t)) {
		seen[entry]++
	}
	for entry, count := range seen {
		if count > 1 {
			t.Errorf("%s is in the CI matrix %d times; each module needs one job", entry, count)
		}
	}
}

func TestUncoveredFindsAModuleTheMatrixOmits(t *testing.T) {
	root := write(t, map[string]string{
		"subsystems/one/go.mod": "module example.com/one\n",
		"subsystems/two/go.mod": "module example.com/two\n",
	})
	uncovered, err := repocheck.Uncovered(root, []string{"subsystems/one"})
	require.NoError(t, err)
	assert.Equal(t, []string{"subsystems/two"}, uncovered)

	// A trailing slash is how a person or a shell writes one, and it names the
	// same module.
	uncovered, err = repocheck.Uncovered(root, []string{"subsystems/one/"})
	require.NoError(t, err)
	assert.Equal(t, []string{"subsystems/two"}, uncovered)

	// Blank lines are ignored rather than treated as a module named "".
	uncovered, err = repocheck.Uncovered(root, []string{"  ", "subsystems/one", " subsystems/two "})
	require.NoError(t, err)
	assert.Empty(t, uncovered)
}

func TestUnlistedFindsARowThatTestsNothing(t *testing.T) {
	root := write(t, map[string]string{"subsystems/one/go.mod": "module example.com/one\n"})
	unlisted, err := repocheck.Unlisted(root, []string{"subsystems/one", "subsystems/gone", "cmd/toolbox"})
	require.NoError(t, err)
	assert.Equal(t, []string{"subsystems/gone"}, unlisted,
		"cmd/toolbox is a module but not a subsystem, so the matrix may name it")
}

func TestMissingModuleFindsASubsystemThatIsNotAModule(t *testing.T) {
	root := write(t, map[string]string{
		"subsystems/one/go.mod":  "module example.com/one\n",
		"subsystems/two/main.go": "package two\n",
	})
	missing, err := repocheck.MissingModule(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"subsystems/two"}, missing)
}

func TestAnAbsentSubsystemsDirectoryIsAnError(t *testing.T) {
	root := t.TempDir()
	_, err := repocheck.Modules(root)
	require.Error(t, err)
	_, err = repocheck.MissingModule(root)
	require.Error(t, err)
}

// The regexp the matrix is read with is the part that could quietly stop matching,
// and a check that finds nothing reports success. These pin the shape it needs.
func TestTheMatrixIsReadFromTheWorkflow(t *testing.T) {
	root := write(t, map[string]string{
		".github/workflows/ci.yml": "" +
			"jobs:\n" +
			"  independent:\n" +
			"    strategy:\n" +
			"      matrix:\n" +
			"        module:\n" +
			"          - subsystems/one\n" +
			"          - subsystems/two\n" +
			"          - cmd/toolbox\n",
		"subsystems/one/go.mod": "module example.com/one\n",
		"subsystems/two/go.mod": "module example.com/two\n",
	})
	matrix := workflowMatrix(t, root)
	require.Len(t, matrix, 3)
	assert.Equal(t, "subsystems/one", matrix[0])
	assert.Equal(t, "cmd/toolbox", matrix[2])

	uncovered, err := repocheck.Uncovered(root, matrix)
	require.NoError(t, err)
	assert.Empty(t, uncovered)
}

// The count of matrix entries is compared against the number of modules, so a
// change in either is visible without depending on the regexp still matching the
// same way it did when this was written.
func TestTheMatrixCoversExactlyTheModulesThatExist(t *testing.T) {
	root := repositoryRoot(t)
	if _, err := os.Stat(filepath.Join(root, "subsystems")); err != nil {
		t.Skip("this tree is narrowed to one module, so the module layout is not visible from it")
	}
	modules, err := repocheck.Modules(root)
	require.NoError(t, err)
	matrix := workflowMatrix(t, root)
	// One matrix entry beyond the subsystems: cmd/toolbox, the host.
	assert.Len(t, matrix, len(modules)+1,
		"the matrix has one entry per subsystem plus the host, and %d subsystems exist", len(modules))

	for _, module := range modules {
		assert.Contains(t, matrix, module, "module %s", module)
	}
}
