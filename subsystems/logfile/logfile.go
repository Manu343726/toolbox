// Package logfile is a logging provider that writes to a rotating file.
//
// The rotation is lumberjack's, which is the whole reason this package is a provider and not
// a subsystem implementation: the standard library knows how to call a handler, lumberjack
// knows how to run out of disk space gracefully, and neither fact belongs here. What belongs
// here is turning a configuration's settings into a lumberjack writer and a standard library
// handler over it.
//
// A project wants its own output somewhere it can read, tail and archive without an
// aggregator, and this is also the backend that can be tested honestly: a file's contents are
// observable, where a network sink's are not.
package logfile

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/Manu343726/toolbox/pkg/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

// The subsystem's identity, and the identifier a configuration names to select this backend.
const (
	Name        = "logfile"
	Version     = "0.1.0"
	ProviderID  = "logfile"
	Description = "Rotating-file logging provider for a Toolbox deployment"
)

// Provider is the backend, as the framework's logging providers declare themselves.
//
// It is a log.ProviderFunc because that is all a backend is: an identifier and a function
// from a configuration's options to a slog.Handler. There is no Handler type, no rotation
// policy and no buffering here for a caller to manage, because all of that is lumberjack's
// and the standard library's.
var Provider = log.ProviderFunc{
	ID:      ProviderID,
	BuildFn: NewHandler,
}

// NewHandler builds a rotating-file sink from a configuration's options.
//
// The defaults are chosen so that a deployment which says nothing gets a file that does not
// fill a disk: a logging backend that can exhaust the filesystem it logs to is a backend that
// takes the deployment down with it.
func NewHandler(options map[string]any) (slog.Handler, error) {
	if err := refuseUnknown(options); err != nil {
		return nil, err
	}
	format := text(options, "format", "json")
	switch format {
	case "json", "text":
	default:
		return nil, fmt.Errorf("format %q is not one this provider writes: json, text", format)
	}
	writer := NewWriter(options)
	// The rotation settings have been read, so they are removed before the standard library's
	// handler sees the map: that handler refuses a setting it does not know, and refusing
	// lumberjack's would be this package failing to clean up after itself.
	settings := map[string]any{}
	for key, value := range options {
		switch key {
		case "path", "max_size", "max_backups", "max_age_days", "compress", "local_time", "format":
		default:
			settings[key] = value
		}
	}
	return log.NewStdlib(settings, writer, format == "json")
}

// NewWriter builds the rotating file itself, for a caller that wants a bounded writer rather
// than a handler.
//
// It is here so the rotation settings are configured in one place, and so a test can reach the
// writer and close it. lumberjack compresses a rotated file on a background goroutine, so a
// test that watches for a compressed backup has to be able to flush the mill and cannot
// pretend the write completed it.
func NewWriter(options map[string]any) *lumberjack.Logger {
	return &lumberjack.Logger{
		Filename:   text(options, "path", "toolbox.log"),
		MaxSize:    megabytes(options, "max_size", 100),
		MaxBackups: count(options, "max_backups", 3),
		MaxAge:     count(options, "max_age_days", 28),
		Compress:   flag(options, "compress", true),
		LocalTime:  flag(options, "local_time", true),
	}
}

// rotationSettings are what this provider adds to the standard library's own.
var rotationSettings = []string{
	"path", "format", "max_size", "max_backups", "max_age_days", "compress", "local_time",
}

// stdlibSettings are the standard handler's, listed here so the message a deployment sees
// names everything a logfile handler accepts rather than only what the standard handler does.
var stdlibSettings = []string{"level", "source", "replace_attr"}

// refuseUnknown reports a setting this provider does not understand, naming it and listing
// what it accepts.
//
// A misspelled rotation setting that was ignored would leave a deployment believing its file
// rotated when it did not, and finding out when the disk filled.
func refuseUnknown(options map[string]any) error {
	accepted := make(map[string]bool, len(rotationSettings)+len(stdlibSettings))
	for _, key := range rotationSettings {
		accepted[key] = true
	}
	for _, key := range stdlibSettings {
		accepted[key] = true
	}
	var unknown []string
	for key := range options {
		if !accepted[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	keys := append(append([]string(nil), rotationSettings...), stdlibSettings...)
	sort.Strings(keys)
	quoted := make([]string, 0, len(unknown))
	for _, key := range unknown {
		quoted = append(quoted, strconv.Quote(key))
	}
	return fmt.Errorf("the %s provider has no setting %s; it takes: %s",
		ProviderID, strings.Join(quoted, ", "), strings.Join(keys, ", "))
}

// text reads a string setting, falling back to a default.
func text(options map[string]any, key, fallback string) string {
	if value, ok := options[key]; ok {
		if written, isString := value.(string); isString && written != "" {
			return written
		}
	}
	return fallback
}

// count reads a whole-number setting, falling back to a default.
//
// A number that arrived as a string is accepted, because a configuration read from an
// environment or a flag is strings all the way down, and refusing it there would make the
// same configuration work in a file and fail from a flag.
func count(options map[string]any, key string, fallback int) int {
	value, ok := options[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

// megabytes reads a size setting. Rotation is measured in megabytes because that is how
// lumberjack measures it, and a setting the backend cannot express is better refused than
// quietly rounded.
func megabytes(options map[string]any, key string, fallback int) int {
	value := count(options, key, fallback)
	if value < 1 {
		return fallback
	}
	return value
}

func flag(options map[string]any, key string, fallback bool) bool {
	value, ok := options[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "true" || typed == "yes" || typed == "1"
	default:
		return fallback
	}
}
