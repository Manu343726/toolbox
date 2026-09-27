package knowledgehindsight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// The FUSE half of the mount, behind a build tag.
//
// The tag is what keeps M-6 true while the mount is also an RPC. A provider whose module required
// go-fuse unconditionally would drag a FUSE implementation into every binary that serves the
// contract, so a host with no FUSE could not build the subsystem at all — and "the mount failing is
// never the subsystem failing" would be a claim about a build nobody can produce.
//
// So the implementation is behind `fuse`, and the stubs below answer every question honestly when
// it is absent: `GetMountStatus` reports `fuse_available: false` and names the tag, and the
// mutating methods refuse with `Unimplemented` saying which tag to use. A caller can therefore tell
// "not supported in this build" from "tried to mount and the host said no" without attempting it.
//
// Build it with:
//
//	go build -tags fuse ./subsystems/knowledgehindsight
//
// The logic above the FUSE wiring — which files the projection contains, when it changed, which
// subset of it a filter selects — is in the root package and is tested there over a plain io/fs,
// with no FUSE present at all. Per this repository's own testing rule, no test requires a mount to
// work.
const fuseBuildNote = "go build -tags fuse"

// FuseBuilt reports whether this binary has FUSE support compiled in.
func FuseBuilt() bool { return fuseBuilt() }

// preflightFuse reports why this host cannot mount, or the empty string when it can.
//
// Each cause is separate because each has a different fix and a caller who is told "mounting is
// unavailable" learns nothing they can act on.
func preflightFuse() (string, error) {
	return preflight()
}

// startFuse begins serving a projection at a mountpoint.
func startFuse(ctx context.Context, p *Provider, spec *knowledgev1.MountSpec, id string, poll time.Duration) (mountHandle, error) {
	return start(ctx, p, spec, id, poll)
}

// refreshFuse re-fetches a mount's projection if the revision moved, and reports whether it did.
func refreshFuse(ctx context.Context, handle mountHandle, baseID string) (bool, error) {
	if handle == nil {
		return false, nil
	}
	return refresh(ctx, handle, baseID)
}

// checkMountpoint refuses a path that is not an existing empty directory.
//
// Two rules, and both are about leaving nothing behind. The directory must already exist, because a
// mount command that creates its own mountpoint leaves a directory behind when it fails, and an empty
// directory that looks like a mount is worse than no mount. And it must be empty, because mounting
// over somebody's files hides them.
func checkMountpoint(path string) error {
	if !filepath.IsAbs(path) {
		return &api.Error{
			Kind:    api.KindInvalid,
			Message: fmt.Sprintf("the mountpoint %q must be an absolute path: a relative one is resolved against whatever directory the process happens to be in, which is not a thing anybody can reason about later", path),
		}
	}
	if strings.Contains(path, "..") {
		return &api.Error{Kind: api.KindInvalid, Message: fmt.Sprintf("the mountpoint %q contains `..`; resolve it before calling this rather than leaving path traversal to the mount", path)}
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &api.Error{
				Kind:    api.KindInvalid,
				Message: fmt.Sprintf("the mountpoint %s does not exist; create it first, so that a mount that fails leaves nothing behind for you to clean up", path),
			}
		}
		return &api.Error{Kind: api.KindInvalid, Message: "checking the mountpoint " + path, Err: err}
	}
	if !info.IsDir() {
		return &api.Error{Kind: api.KindInvalid, Message: fmt.Sprintf("the mountpoint %s is not a directory", path)}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return &api.Error{Kind: api.KindInvalid, Message: "reading the mountpoint " + path, Err: err}
	}
	if len(entries) > 0 {
		return &api.Error{
			Kind:    api.KindFailedPrecondition,
			Message: fmt.Sprintf("the mountpoint %s is not empty (%d entries); mounting over somebody's files would hide them, and this surface will not do that", path, len(entries)),
		}
	}
	return nil
}

// defaults the contract leaves open, stated here so the mount's behaviour is one place.
const (
	// DefaultPollInterval is how often a mount asks whether the projection moved.
	//
	// Two seconds is short enough that a reader does not notice a change arriving and long
	// enough that polling is not the load. It is a poll rather than a subscription because
	// there is no server-side change feed: the mount is a client of two calls, and adding a
	// stream would be the only streaming contract in the framework to serve a feature a two
	// second poll serves.
	DefaultPollInterval = 2 * time.Second
	// DefaultAttrTimeout and DefaultEntryTimeout are the kernel cache bounds.
	//
	// These are the bound on staleness where change notification is unavailable, and they
	// are a number rather than "eventually". Content changes drop the kernel's cached copy
	// immediately, so `cat`, `rg` and an agent's file tools are correct at once; a content
	// notification emits no inotify event, so a GUI editor with the file open is not told to
	// reload, and no amount of polling changes that. A reader that re-reads on focus — most
	// of them — sees the new content within this bound.
	DefaultAttrTimeout  = time.Second
	DefaultEntryTimeout = time.Second
)

// normalizeMountSpec fills in the defaults the contract leaves open.
//
// It mutates the message it is given rather than copying it, because a generated protobuf message
// carries an internal mutex and copying one by value copies a lock. The caller passes a message this
// package owns — a clone of the request — so mutating it is safe, and a copy would be neither safe
// nor shorter.
//
// A non-positive timeout is filled in rather than passed through, because zero to a filesystem means
// "never expire" and the opposite of what a caller omitting the field means. That is the whole reason
// the defaults are applied here and not at the call site.
func normalizeMountSpec(spec *knowledgev1.MountSpec) *knowledgev1.MountSpec {
	if spec.PollInterval == nil || spec.PollInterval.AsDuration() <= 0 {
		spec.PollInterval = durationMessage(DefaultPollInterval)
	}
	if spec.AttrTimeout == nil || spec.AttrTimeout.AsDuration() <= 0 {
		spec.AttrTimeout = durationMessage(DefaultAttrTimeout)
	}
	if spec.EntryTimeout == nil || spec.EntryTimeout.AsDuration() <= 0 {
		spec.EntryTimeout = durationMessage(DefaultEntryTimeout)
	}
	return spec
}
