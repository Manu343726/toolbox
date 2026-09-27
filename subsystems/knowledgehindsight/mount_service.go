package knowledgehindsight

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Manu343726/toolbox/pkg/knowledge"
	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
	knowledgev1connect "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1/knowledgev1connect"
)

// mountService controls the optional read-only filesystem mount.
//
// Why this is an RPC and what it costs is stated on `MountStatus.mountpoint` and on the service in
// the contract, and it is worth repeating here because a reader of the code should not have to open
// the proto to find it: a mount lives in a filesystem namespace, and an RPC creates one in the
// namespace of the process that serves it. That is correct for a person auditing a base on the
// machine running it, and it is why `ExportWiki` remains the primary feature rather than this one.
type mountService struct {
	knowledgev1connect.UnimplementedMountServiceHandler
	p *Provider
}

func (p *Provider) mountHandler() *mountService { return &mountService{p: p} }

func (s *mountService) GetMountStatus(_ context.Context, req *connect.Request[knowledgev1.GetMountStatusRequest]) (*connect.Response[knowledgev1.GetMountStatusResponse], error) {
	msg := req.Msg
	out := &knowledgev1.GetMountStatusResponse{
		Mounts:         s.p.mounts.List(msg.GetMountId()),
		FuseAvailable:  fuseBuilt(),
		StalenessBound: durationMessage(maxDuration(DefaultAttrTimeout, DefaultEntryTimeout)),
	}
	if !fuseBuilt() {
		reason, _ := preflight()
		out.FuseUnavailableReason = reason
	} else if reason, _ := preflight(); reason != "" {
		// The build has FUSE and this host does not. Those are different facts with
		// different fixes, and a caller that is told only "unavailable" cannot tell
		// whether to rebuild or to fix the host.
		out.FuseAvailable = false
		out.FuseUnavailableReason = reason
	} else {
		out.NotificationCapabilities = notificationCapabilities()
	}
	return connect.NewResponse(out), nil
}

// EnableMount starts mounting a base's projection, and returns as soon as it is established.
func (s *mountService) EnableMount(ctx context.Context, req *connect.Request[knowledgev1.EnableMountRequest]) (*connect.Response[knowledgev1.EnableMountResponse], error) {
	// The request is cloned rather than copied: a generated message carries an internal mutex, and
	// the defaults are applied to a copy so that a caller inspecting its own request is not
	// surprised by a value it did not set.
	in := proto.Clone(req.Msg).(*knowledgev1.EnableMountRequest)
	if in.Spec != nil {
		in.Spec = normalizeMountSpec(in.Spec)
	}
	status, err := s.p.mounts.Enable(ctx, in)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.EnableMountResponse{Status: status}), nil
}

// DisableMount unmounts a mount and drops its record, never leaving a stale mountpoint behind.
func (s *mountService) DisableMount(ctx context.Context, req *connect.Request[knowledgev1.DisableMountRequest]) (*connect.Response[knowledgev1.DisableMountResponse], error) {
	res, err := s.p.mounts.Disable(ctx, req.Msg)
	if res == nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(res), nil
}

// projection builds the cache a mount serves, from the same reads ExportWiki uses.
func (p *Provider) projection(ctx context.Context, spec *knowledgev1.MountSpec) (*projectionSource, error) {
	baseID, err := p.resolveBase(ctx, spec.GetBaseId())
	if err != nil {
		return nil, err
	}
	corpus, err := p.bases.corpus(nil)
	if err != nil {
		return nil, err
	}
	return newProjectionSource(ctx, p, baseID, corpus, filterOrigin(spec.GetOrigin()), knowledge.NormalizePath(spec.GetSubtree()))
}

// durationMessage converts a Go duration to the contract's message, treating a non-positive value
// as absent rather than as zero. A zero timeout in a filesystem means "never expire", which is the
// opposite of what a caller omitting the field means, and the distinction is exactly why the
// defaults are applied before this is called.
func durationMessage(d time.Duration) *durationpb.Duration {
	if d <= 0 {
		return nil
	}
	return durationpb.New(d)
}

func timestampMessage(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t.UTC())
}

// maxDuration is the staleness bound a mount is read to within, which is the larger of its two
// cache timeouts. It is reported so a caller can state the guarantee rather than infer it.
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// notificationCapabilities reports what the running host negotiated, so a caller knows whether it
// will get inotify events for removals.
//
// The asymmetry is deliberate and is the honest version of "notified": a content change drops the
// kernel's cached copy but emits no inotify event, so `cat`, `rg` and an agent's file tools are
// correct immediately while a GUI editor with the file open is not told to reload. A removal does
// emit one. No amount of polling changes the first, which is why the staleness bound is a stated
// number rather than "eventually".
func notificationCapabilities() []string {
	return []string{
		"content-change: kernel cache invalidated; no inotify event (so `cat`, `rg` and file tools are correct at once, and a GUI editor is not told to reload)",
		"removal: kernel cache invalidated and an inotify event is emitted",
		"name-change: the next lookup is re-run; no inotify event",
	}
}
