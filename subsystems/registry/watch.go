package registry

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	registryv1 "github.com/Manu343726/toolbox/subsystems/registry/registryv1"
)

// WatchServices streams registry changes to a client.
//
// A registry a deployment can add to and remove from at runtime is only useful to
// something that learns about it while it runs. A resolver watching this stops
// resolving from a stale table; a subsystem watching it learns a peer is gone
// before its next call fails rather than after.
//
// The stream opens with a sync event carrying everything currently registered, so a
// client is useful immediately rather than after the next change. That is the
// difference between a change feed and a poll dressed up as one.
//
// Three endings, and each means something different to the client:
//
//   - the client cancels, or the registry shuts down: the stream ends cleanly and
//     the client may keep what it has;
//   - the client stops reading: it is disconnected rather than allowed to fall
//     behind, because a silently missed deregistration would leave it resolving an
//     endpoint that is gone. It must re-read the current set before trusting what
//     it holds again.
func (s *Service) WatchServices(
	ctx context.Context,
	req *connect.Request[registryv1.WatchServicesRequest],
	stream *connect.ServerStream[registryv1.RegistryEvent],
) error {
	if req == nil || req.Msg == nil {
		return connect.NewError(connect.CodeInvalidArgument, errWatchRequest)
	}
	name := strings.TrimSpace(req.Msg.GetSubsystemName())

	// The store's list sweeps lapsed leases first, so the sync event reflects what
	// is actually reachable rather than what was once registered.
	current := s.store.List(name, nil, false)
	if !req.Msg.GetSkipInitialSync() {
		if err := stream.Send(&registryv1.RegistryEvent{
			Type:     registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_SYNC,
			Services: current,
		}); err != nil {
			return err
		}
	}

	// The watch is opened after the sync, so a change that lands between the two is
	// delivered rather than missed. A change that lands before it appears in the
	// sync event instead, which is the other half of the same guarantee.
	events := s.store.Watch(watchBuffer)
	defer s.store.CloseWatch(events)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, open := <-events:
			if !open {
				// The watcher fell behind and was disconnected. Say so, because a
				// client that treats a clean end as "nothing changed" would go on
				// believing a deregistration it never saw.
				return connect.NewError(connect.CodeAborted, errWatchLagged)
			}
			message, keep := watchEventFor(event, name)
			if !keep {
				continue
			}
			if err := stream.Send(message); err != nil {
				return err
			}
		}
	}
}

// watchBuffer is how much change a stream may fall behind by before it is
// disconnected. It is generous because the events are small and a client that
// stalls briefly is normal; a client that stalls indefinitely should be told, not
// accommodated.
const watchBuffer = 64

// watchEventFor converts a store event into the message a client sees, and
// reports whether the client asked for it.
func watchEventFor(event Event, name string) (*registryv1.RegistryEvent, bool) {
	if event.Descriptor == nil {
		return nil, false
	}
	// A heartbeat carries no change in reachability, so a client watching one
	// subsystem is not told about another's lease renewal.
	if event.Type == EventHeartbeat && name != event.Descriptor.GetSubsystemName() {
		return nil, false
	}
	if name != "" && event.Descriptor.GetSubsystemName() != name {
		return nil, false
	}
	return &registryv1.RegistryEvent{
		Type:        eventTypeToProto(event.Type),
		Descriptor_: event.Descriptor,
	}, true
}

func eventTypeToProto(eventType EventType) registryv1.RegistryEventType {
	switch eventType {
	case EventRegistered:
		return registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_REGISTERED
	case EventUpdated:
		return registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_UPDATED
	case EventHeartbeat:
		return registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_HEARTBEAT
	case EventDeregistered:
		return registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_DEREGISTERED
	default:
		return registryv1.RegistryEventType_REGISTRY_EVENT_TYPE_UNSPECIFIED
	}
}

var (
	errWatchRequest = errors.New("watch request is required")
	errWatchLagged  = errors.New("this watch fell behind and was closed; read the current services again before trusting them")
)
