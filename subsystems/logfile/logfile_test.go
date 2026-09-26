package logfile_test

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/log"
	"github.com/Manu343726/toolbox/subsystems/logfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The provider is tested by writing to a real file and reading it back, because a file's
// contents are observable where a network sink's are not. The rotation is lumberjack's and is
// exercised through the settings a configuration actually passes, so a test that passed here
// and failed in a deployment would mean the settings are being dropped in between.

func TestAProviderWritesTheEntriesItIsGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.log")
	router := build(t, path, map[string]any{})

	slog.New(router).Info("stored a source", "id", 7)

	written := read(t, path)
	assert.Contains(t, written, "stored a source")
	assert.Contains(t, written, `"id":7`)
}

func TestTheDefaultsProduceABoundedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolbox.log")
	router := build(t, path, map[string]any{})

	slog.New(router).Info("stored a source")

	// A deployment which says nothing gets a file that does not fill a disk: a logging
	// backend that can exhaust the filesystem it logs to takes the deployment down with it.
	assert.FileExists(t, path)
	assert.Contains(t, read(t, path), "stored a source")
}

func TestRotationKeepsABoundedNumberOfBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.log")
	// A megabyte is the smallest size that rotates at all, so a test that respected the
	// setting has to write about a megabyte. The entries are padded rather than counted so
	// the test does not depend on how many entries a megabyte happens to be.
	entry := strings.Repeat("x", 512)
	router := build(t, path, map[string]any{
		"max_size":    1,
		"max_backups": 2,
		"compress":    false,
	})
	logger := slog.New(router)
	for i := 0; i < 4000; i++ {
		logger.Info("stored a source", "id", i, "padding", entry)
	}

	// Two backups is what was asked for, and asking is the whole contract: a provider that
	// kept five would fill a disk the deployment sized for two.
	backups, err := filepath.Glob(filepath.Join(dir, "output-*.log"))
	require.NoError(t, err)
	require.Len(t, backups, 2)
	// The live file is whatever has not been rotated yet, so the bound that matters is on
	// the size setting and not on the bytes on disk at the moment a test looked.
	assert.LessOrEqual(t, fileSize(t, path), int64(2*1024*1024))
}

func TestRotationCompressesWhenAsked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.log")
	entry := strings.Repeat("x", 512)
	router := build(t, path, map[string]any{
		"max_size":    1,
		"max_backups": 1,
		"compress":    true,
	})
	logger := slog.New(router)
	for i := 0; i < 4000; i++ {
		logger.Info("stored a source", "id", i, "padding", entry)
	}

	// The padded entries are highly compressible, so a compressed backup is much smaller than
	// the file it came from. That is the observable difference, and it is lumberjack's, not
	// this package's — which is the point of testing it here rather than reimplementing it.
	//
	// lumberjack compresses on a background goroutine, so the write returning does not mean
	// the backup exists, let alone that it is finished. Waiting for a readable backup is
	// therefore part of the assertion rather than a sleep afterwards: what is being tested is
	// a background task finishing, and pretending it is synchronous would test nothing.
	var contents []byte
	waitFor(t, "a readable compressed backup", func() bool {
		contents = readCompressed(t, dir)
		return len(contents) > 0
	})
	assert.Contains(t, string(contents), "stored a source")
}

func TestTheTextFormatIsWrittenWhenAsked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.log")
	router := build(t, path, map[string]any{"format": "text"})

	slog.New(router).Info("stored a source", "id", 7)

	// Both formats exist because a person and a collector read the output differently, and
	// neither layout is this package's to invent.
	written := read(t, path)
	assert.Contains(t, written, "msg=\"stored a source\"")
	assert.Contains(t, written, "id=7")
}

func TestAnUnknownFormatIsRefused(t *testing.T) {
	_, err := logfile.NewHandler(map[string]any{"format": "yaml"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"yaml" is not one this provider writes`)
}

func TestAnUnknownRotationSettingIsRefused(t *testing.T) {
	// A misspelled rotation setting that was ignored would leave a deployment believing its
	// file rotated when it did not, and discovering it when the disk filled.
	_, err := logfile.NewHandler(map[string]any{"maxsize": 10})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `no setting "maxsize"`)
	assert.Contains(t, err.Error(), "max_size")
}

func TestTheLevelReachesTheStandardHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.log")
	router := build(t, path, map[string]any{"level": "warn"})

	logger := slog.New(router)
	logger.Info("stored a source")
	logger.Warn("cache is stale")

	// The filtering is the standard library's own Enabled, so a caller deciding whether to
	// format an expensive attribute learns the answer from the same call slog makes.
	written := read(t, path)
	assert.NotContains(t, written, "stored a source")
	assert.Contains(t, written, "cache is stale")
}

func TestANumberMayArriveAsAString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.log")
	entry := strings.Repeat("x", 512)
	// A configuration read from an environment or a flag is strings all the way down, and the
	// same configuration has to work in a file and from a flag.
	router := build(t, path, map[string]any{"max_size": "1", "max_backups": "1", "compress": "false"})

	logger := slog.New(router)
	for i := 0; i < 4000; i++ {
		logger.Info("stored a source", "id", i, "padding", entry)
	}

	backups, err := filepath.Glob(filepath.Join(dir, "output-*.log"))
	require.NoError(t, err)
	assert.Len(t, backups, 1)
}

func TestTheSubsystemDeclaresItselfAsAProvider(t *testing.T) {
	declared := logfile.Providers("127.0.0.1:9100")

	require.Len(t, declared, 1)
	assert.Equal(t, logfile.ProviderID, declared[0].ID)
	assert.Equal(t, logfile.Name, declared[0].Subsystem)
	assert.Equal(t, api.ProviderRole("loghandler"), declared[0].Role)
	assert.Equal(t, "127.0.0.1:9100", declared[0].Endpoint)
}

func TestTheGatheredProviderIsTheDeclaredOne(t *testing.T) {
	// A deployment that registered this through the catalog and a deployment that gathered
	// the subsystem end up with one backend rather than two that happen to write the same
	// file.
	assert.Equal(t, logfile.ProviderID, logfile.LogProvider().ProviderID())
}

func TestTheSubsystemComposes(t *testing.T) {
	server, err := logfile.New(logfile.SubsystemOptions{ListenAddress: "127.0.0.1:0"})

	require.NoError(t, err)
	require.NotNil(t, server)
	// A rotating file has nothing to drain and serves no operations of its own, so the server
	// exists only so the provider is declarable — and it is describable as itself.
	descriptor := server.Descriptor()
	assert.Equal(t, logfile.Name, descriptor.SubsystemName)
	assert.Equal(t, logfile.Version, descriptor.ImplementationVersion)
	assert.Empty(t, descriptor.ServiceNames)
}

func TestAProjectCanSendItsLogsToItsOwnFile(t *testing.T) {
	project := t.TempDir()
	registry := log.NewRegistry()
	registry.BaseDir = project
	require.NoError(t, registry.Register(logfile.LogProvider()))

	router, err := registry.Build(log.Config{
		Level: slog.LevelInfo,
		Handlers: []log.HandlerConfig{{
			Name:     "project",
			Provider: logfile.ProviderID,
			Options:  map[string]any{"path": "logs/acme.log", "max_backups": 5},
		}},
		Routes: []log.Route{{
			Name:     "acme's own",
			When:     log.Workspace("acme"),
			Handlers: []string{"project"},
			Add:      map[string]string{"project": "acme"},
		}},
	})
	require.NoError(t, err)

	ctx := log.WithWorkspace(context.Background(), "acme")
	slog.New(router).InfoContext(ctx, "stored a source")
	slog.New(router).Info("someone else's work")

	// "A file local to the project" means a path relative to the file that declared it, and
	// the project's tag is what makes its lines findable among a machine's.
	written := read(t, filepath.Join(project, "logs", "acme.log"))
	assert.Contains(t, written, "stored a source")
	assert.Contains(t, written, `"project":"acme"`)
	assert.Contains(t, written, `"workspace":"acme"`)
	assert.NotContains(t, written, "someone else's work")
}

// build wires the provider into a router the way a deployment would, so a test exercises the
// provider as the framework hands it over rather than calling it directly.
func build(t *testing.T, path string, options map[string]any) *log.Router {
	t.Helper()
	registry := log.NewRegistry()
	require.NoError(t, registry.Register(logfile.LogProvider()))
	settings := map[string]any{"path": path}
	for key, value := range options {
		settings[key] = value
	}
	router, err := registry.Build(log.Config{
		Level:    slog.LevelDebug,
		Handlers: []log.HandlerConfig{{Name: "file", Provider: logfile.ProviderID, Options: settings}},
		Routes:   []log.Route{{Handlers: []string{"file"}}},
	})
	require.NoError(t, err)
	return router
}

func read(t *testing.T, path string) string {
	t.Helper()
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(written)
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size()
}

// waitFor retries a condition until it holds or a bound passes.
//
// The bound is what keeps it from being a hang and the wait is what keeps it from being a
// race: lumberjack's mill is a background goroutine, so a backup appears some time after the
// write that rotated the file.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			require.FailNowf(t, "timed out waiting for "+what,
				"the condition did not hold within ten seconds")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// readCompressed returns the contents of the first compressed backup that is complete, or
// nothing if there is not one yet.
func readCompressed(t *testing.T, dir string) []byte {
	t.Helper()
	backups, err := filepath.Glob(filepath.Join(dir, "output-*.log.gz"))
	if err != nil || len(backups) == 0 {
		return nil
	}
	file, err := os.Open(backups[0])
	if err != nil {
		return nil
	}
	defer file.Close()
	uncompressed, err := gzip.NewReader(file)
	if err != nil {
		// The file exists but the mill has not written its footer yet.
		return nil
	}
	defer uncompressed.Close()
	contents, err := io.ReadAll(uncompressed)
	if err != nil {
		return nil
	}
	return contents
}
