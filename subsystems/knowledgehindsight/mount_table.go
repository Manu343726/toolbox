package knowledgehindsight

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Manu343726/toolbox/pkg/api"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// mountTable holds the running mounts and supervises them.
//
// It exists on every build, FUSE or not, so that `GetMountStatus` can answer accurately and the
// mutating methods can refuse by name. A nil table would turn "this build cannot mount" into a
// crash, which is the worst possible answer to a question a caller asked on purpose.
type mountTable struct {
	mu sync.Mutex
	p  *Provider

	// live holds the running mounts, by identifier.
	live map[string]*mount
	// supervisors are the polling loops, one per mount, cancelled when the mount is
	// disabled.
	supervisors map[string]context.CancelFunc
}

func newMountTable(p *Provider) *mountTable {
	return &mountTable{p: p, live: map[string]*mount{}, supervisors: map[string]context.CancelFunc{}}
}

// mount is one running or attempted mount.
type mount struct {
	// spec is what this mount was asked to serve.
	//
	// A pointer rather than a value, and not for brevity: a generated protobuf message carries an
	// internal mutex, so copying one by value copies a lock. The framework's own vet flags it, and
	// it is the kind of copy that only misbehaves under concurrency — which is exactly when a mount
	// is being refreshed.
	spec *knowledgev1.MountSpec
	// mountpoint is remembered separately so the busy-mount message can name it without
	// reaching through the spec, which a caller may have mutated.
	mountpoint string
	// mountID is unique within the deployment.
	mountID string
	// state is where it got to.
	state knowledgev1.MountState
	// failure names the missing piece when the state is FAILED. Each has a different fix, so
	// collapsing them into one "unavailable" would throw the diagnosis away.
	failure string
	started time.Time
	// revision is the projection revision currently being served.
	revision    string
	refreshedAt time.Time
	files       int
	refreshes   int
	// raw is the FUSE handle, nil on a build without FUSE support.
	raw mountHandle
}

// mountHandle is what a build with FUSE support puts here.
//
// It is an interface rather than a concrete type so that the table, the service and their tests do
// not all gain a build tag, and so that a test can drive the whole lifecycle with a handle that
// records what it was asked to do.
type mountHandle interface {
	// Close unmounts. It must be safe to call more than once.
	Close() error
	// Files is how many files the mount is currently serving.
	Files() int
	// Revision is the projection revision it is serving.
	Revision() string
	// Refreshed is when it last re-fetched.
	Refreshed() time.Time
	// Refreshes is how many times it re-fetched.
	Refreshes() int
	// Stop blocks until the mount's context is cancelled, then unmounts.
	Stop(ctx context.Context) error
}

// status renders one mount.
func (m *mount) status() *knowledgev1.MountStatus {
	s := &knowledgev1.MountStatus{
		MountId:         m.mountID,
		BaseId:          m.spec.GetBaseId(),
		State:           m.state,
		Mountpoint:      m.spec.GetMountpoint(),
		Failure:         m.failure,
		PollInterval:    durationMessage(m.spec.GetPollInterval().AsDuration()),
		AttrTimeout:     durationMessage(m.spec.GetAttrTimeout().AsDuration()),
		EntryTimeout:    durationMessage(m.spec.GetEntryTimeout().AsDuration()),
		ServingRevision: m.revision,
		Files:           int32(m.files),
		ReadOnly:        true,
	}
	if !m.refreshedAt.IsZero() {
		s.RefreshedAt = timestampMessage(m.refreshedAt)
	}
	if !m.started.IsZero() {
		s.StartedAt = timestampMessage(m.started)
	}
	if m.raw != nil {
		s.ServingRevision = m.raw.Revision()
		s.Files = int32(m.raw.Files())
		s.Refreshes = int32(m.raw.Refreshes())
		if r := m.raw.Refreshed(); !r.IsZero() {
			s.RefreshedAt = timestampMessage(r)
		}
	}
	return s
}

// Enable starts a mount, or reports precisely why it could not start.
func (t *mountTable) Enable(ctx context.Context, req *knowledgev1.EnableMountRequest) (*knowledgev1.MountStatus, error) {
	spec := req.GetSpec()
	if spec == nil {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a mount specification is required"}
	}
	base := spec.GetBaseId()
	if _, err := t.p.resolveBaseForRead(ctx, base); err != nil {
		return nil, err
	}
	mountpoint := spec.GetMountpoint()
	if mountpoint == "" {
		return nil, &api.Error{
			Kind:    api.KindInvalid,
			Message: "a mountpoint is required: this surface will not choose one, because a command that invents a directory is a directory somebody has to find and clean up afterwards",
		}
	}
	if err := checkMountpoint(mountpoint); err != nil {
		return nil, err
	}

	id := req.GetMountId()
	if id == "" {
		id = derivedMountID(base, mountpoint)
	}

	t.mu.Lock()
	if existing, ok := t.live[id]; ok && existing.state == knowledgev1.MountState_MOUNT_STATE_MOUNTED {
		if !req.GetReplaceExisting() {
			t.mu.Unlock()
			return existing.status(), &api.Error{
				Kind: api.KindAlreadyExists,
				Message: fmt.Sprintf("a mount is already serving %s; replacing a live mount would make a reader's open files point at a different projection with no indication, so ask for it explicitly",
					existing.mountpoint),
			}
		}
		// Replace is explicit, so the old mount is torn down first and its record dropped
		// rather than left behind as a stale entry.
		if cancel, ok := t.supervisors[id]; ok {
			cancel()
		}
		if existing.raw != nil {
			_ = existing.raw.Close()
		}
		delete(t.live, id)
	}
	t.mu.Unlock()

	// Preflight before anything is created, so a failure leaves nothing behind. Each cause has
	// a different fix and they are reported separately for exactly that reason.
	if reason, err := preflightFuse(); err != nil {
		return nil, &api.Error{
			Kind:    api.KindUnavailable,
			Message: fmt.Sprintf("this host cannot mount a filesystem: %s", reason),
			Err:     err,
		}
	} else if reason != "" {
		m := &mount{spec: spec, mountpoint: mountpoint, mountID: id, state: knowledgev1.MountState_MOUNT_STATE_FAILED, failure: reason}
		t.mu.Lock()
		t.live[id] = m
		t.mu.Unlock()
		return m.status(), nil
	}

	handle, err := startFuse(ctx, t.p, spec, id, spec.GetPollInterval().AsDuration())
	if err != nil {
		return nil, &api.Error{
			Kind:    api.KindUnavailable,
			Message: fmt.Sprintf("mounting %s at %s: %s", base, mountpoint, err),
			Err:     err,
		}
	}

	m := &mount{spec: spec, mountpoint: mountpoint, mountID: id, state: knowledgev1.MountState_MOUNT_STATE_MOUNTED,
		started: time.Now(), raw: handle, revision: handle.Revision(), files: handle.Files()}

	t.mu.Lock()
	t.live[id] = m
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t.supervisors[id] = cancel
	t.mu.Unlock()

	go t.supervise(runCtx, m)
	return m.status(), nil
}

// supervise polls for a changed projection and re-fetches when one moves.
//
// The loop is the whole of the change notification. There is no server-side feed, and the mount
// therefore reports a revision and re-reads when it changes rather than being told. It exits when
// the mount is disabled or the provider stops.
func (t *mountTable) supervise(ctx context.Context, m *mount) {
	interval := m.spec.GetPollInterval().AsDuration()
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh, err := refreshFuse(ctx, m.raw, m.spec.GetBaseId())
			if err != nil {
				t.p.log.Warn("refreshing a knowledge mount", slog.String("mount_id", m.mountID), slog.String("error", err.Error()))
				continue
			}
			if !refresh {
				continue
			}
			// The handle owns the counters, because the handle is what actually knows: it
			// holds the cache and the cache counts. A second copy here would drift from it.
			m.refreshes++
			m.revision = m.raw.Revision()
			m.files = m.raw.Files()
			if r := m.raw.Refreshed(); !r.IsZero() {
				m.refreshedAt = r
			}
		}
	}
}

// Disable stops a mount and removes its record.
func (t *mountTable) Disable(_ context.Context, req *knowledgev1.DisableMountRequest) (*knowledgev1.DisableMountResponse, error) {
	id := req.GetMountId()
	if id == "" {
		return nil, &api.Error{Kind: api.KindInvalid, Message: "a mount identifier is required"}
	}
	t.mu.Lock()
	m, ok := t.live[id]
	if !ok {
		t.mu.Unlock()
		return nil, &api.Error{
			Kind:    api.KindNotFound,
			Message: fmt.Sprintf("no mount is called %q; call GetMountStatus to see what is mounted", id),
		}
	}
	if cancel, ok := t.supervisors[id]; ok {
		cancel()
		delete(t.supervisors, id)
	}
	delete(t.live, id)
	t.mu.Unlock()

	var busy []string
	if m.raw != nil {
		if err := m.raw.Stop(context.Background()); err != nil && !req.GetForce() {
			// A busy mount is reported rather than detached underneath an open file. The
			// caller can force it, and forcing says so on the record.
			return &knowledgev1.DisableMountResponse{Status: m.status()}, &api.Error{
				Kind:    api.KindFailedPrecondition,
				Message: fmt.Sprintf("the mount at %s is still busy, so it was not detached: %v. Pass force to detach it anyway — files a reader already has open will keep reading the old projection", m.spec.GetMountpoint(), err),
				Err:     err,
			}
		}
		_ = m.raw.Close()
	}
	m.state = knowledgev1.MountState_MOUNT_STATE_STOPPED
	return &knowledgev1.DisableMountResponse{Status: m.status(), BusyPaths: busy}, nil
}

// List renders every mount, or one.
func (t *mountTable) List(id string) []*knowledgev1.MountStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*knowledgev1.MountStatus, 0, len(t.live))
	for _, m := range t.live {
		if id != "" && m.mountID != id {
			continue
		}
		out = append(out, m.status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountId < out[j].MountId })
	return out
}

// serve runs the mount supervisors for as long as the provider is up.
func (t *mountTable) serve(ctx context.Context) error {
	<-ctx.Done()
	t.mu.Lock()
	for id, cancel := range t.supervisors {
		cancel()
		delete(t.supervisors, id)
	}
	mounts := make([]*mount, 0, len(t.live))
	for id, m := range t.live {
		mounts = append(mounts, m)
		delete(t.live, id)
	}
	t.mu.Unlock()
	// Every mount is torn down on the way out, so a restart does not find a mountpoint with
	// nothing behind it and an operator does not have to clean one up by hand.
	for _, m := range mounts {
		if m.raw != nil {
			_ = m.raw.Close()
		}
	}
	return nil
}

// derivedMountID names a mount deterministically from what it serves.
//
// It is a function of the base and the mountpoint because that is what makes a mount *the same
// mount* across two calls, which is what lets a caller enable one twice without a second one
// appearing. Two mounts of one base at two paths get different identifiers, because the path is
// part of it.
func derivedMountID(base, mountpoint string) string {
	return base + "@" + mountpoint
}
