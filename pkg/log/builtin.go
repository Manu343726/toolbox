package log

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// stderr is the backend a deployment has before it configures anything, and the one a
// handler that cannot write falls back to reporting itself.
//
// It is a provider rather than a subsystem because a deployment must be able to log before any
// subsystem has started, and because a provider that writes to a process's own standard error
// is not worth a module.
type stderrProvider struct{}

// NewStderr returns the provider for a deployment's own standard error.
func NewStderr() Provider { return stderrProvider{} }

func (stderrProvider) ProviderID() string { return "stderr" }

func (stderrProvider) NewHandler(options map[string]any) (Handler, error) {
	out := io.Writer(os.Stderr)
	if configured, ok := options["path"]; ok {
		path := fmt.Sprint(configured)
		if path != "" && path != "-" {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", path, err)
			}
			return &writerHandler{name: "stderr", out: file, closer: file}, nil
		}
	}
	return &writerHandler{name: "stderr", out: out}, nil
}

// nullProvider discards everything. It exists so a route can send entries nowhere on purpose —
// a deployment that wants a handler switched off without deleting its configuration.
type nullProvider struct{}

// NewNull returns the provider that discards entries.
func NewNull() Provider { return nullProvider{} }

func (nullProvider) ProviderID() string { return "null" }

func (nullProvider) NewHandler(map[string]any) (Handler, error) {
	return &writerHandler{name: "null", out: io.Discard}, nil
}

// writerHandler writes one line per entry.
//
// The line format is the one every log reader already parses — a timestamp, a level, the
// logger, the message, then key=value pairs — rather than something this framework invented.
// A backend that had its own format would need its own reader, and the point of writing to a
// file or a terminal is that something else can read it.
type writerHandler struct {
	name   string
	out    io.Writer
	closer io.Closer
	// now is injectable so a test can assert an exact line rather than a shape.
	now func() Time
	mu  sync.Mutex
}

func (h *writerHandler) Name() string { return h.name }

func (h *writerHandler) Handle(entry Entry) error {
	line := FormatEntry(entry, h.clock())
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintln(h.out, line)
	return err
}

func (h *writerHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closer == nil {
		return nil
	}
	return h.closer.Close()
}

func (h *writerHandler) clock() func() Time {
	if h.now != nil {
		return h.now
	}
	return TimeNow
}

// FormatEntry renders an entry as one line.
//
// The layout is timestamp, level, logger, message, then attributes sorted by key. Sorted
// because a line whose attribute order changed between two runs of the same program is a line
// a reader cannot diff, and a log is mostly read by comparing two of them.
func FormatEntry(entry Entry, now func() Time) string {
	when := entry.Time
	if when.IsZero() && now != nil {
		when = now()
	}
	line := fmt.Sprintf("%s %-5s %-16s %s",
		when.Format("2006-01-02T15:04:05.000Z07:00"),
		strings.ToUpper(entry.Level.String()),
		truncate(entry.Logger, 16),
		entry.Message)
	if entry.Workspace != "" {
		line += " " + WorkspaceKey + "=" + entry.Workspace
	}
	if source := entry.Source.String(); source != "" {
		line += " source=" + source
	}
	keys := make([]string, 0, len(entry.Attributes))
	for key := range entry.Attributes {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		value := entry.Attributes[key]
		if value == "" {
			continue
		}
		if needsQuoting(value) {
			value = quote(value)
		}
		line += " " + key + "=" + value
	}
	return line
}

func truncate(value string, width int) string {
	if len(value) <= width {
		return value
	}
	return value[:width]
}

func needsQuoting(value string) bool {
	return strings.ContainsAny(value, " \t\"=")
}

func quote(value string) string {
	return fmt.Sprintf("%q", value)
}
