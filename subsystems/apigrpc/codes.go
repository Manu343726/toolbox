package apigrpc

import (
	"connectrpc.com/connect"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/Manu343726/toolbox/pkg/protocontract"
)

// discoveryClientAlias keeps the invoker option's type name readable while
// sharing the implementation package's client.
type discoveryClientAlias = discovery.Client

// connectCode maps a classified failure onto the canonical ConnectRPC code. The
// classification is the single source of truth, so the codes a caller sees mean
// the same thing whichever provider produced them.
func connectCode(kind api.ErrorKind) connect.Code {
	switch kind {
	case api.KindInvalid:
		return connect.CodeInvalidArgument
	case api.KindNotFound:
		return connect.CodeNotFound
	case api.KindFailedPrecondition:
		return connect.CodeFailedPrecondition
	case api.KindDenied:
		return connect.CodePermissionDenied
	case api.KindAlreadyExists:
		return connect.CodeAlreadyExists
	case api.KindUnsupported:
		return connect.CodeUnimplemented
	case api.KindUnavailable:
		return connect.CodeUnavailable
	case api.KindInternal:
		return connect.CodeInternal
	default:
		return connect.CodeInternal
	}
}

// Descriptor is the in-process contract reader this subsystem serves, so a host
// or a test can describe a protobuf contract without going through ConnectRPC.
func Descriptor(options protocontract.Descriptor) *protocontract.Descriptor {
	return protocontract.NewDescriptor(options)
}
