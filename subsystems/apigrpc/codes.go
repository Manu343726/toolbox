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
// classification is the single source of truth and so is the mapping, which lives
// in the framework rather than in each provider that needs it.
func connectCode(kind api.ErrorKind) connect.Code {
	return api.ConnectCode(kind)
}

// Descriptor is the in-process contract reader this subsystem serves, so a host
// or a test can describe a protobuf contract without going through ConnectRPC.
func Descriptor(options protocontract.Descriptor) *protocontract.Descriptor {
	return protocontract.NewDescriptor(options)
}
