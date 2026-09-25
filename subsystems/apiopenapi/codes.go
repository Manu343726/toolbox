package apiopenapi

import (
	"connectrpc.com/connect"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/openapi"
)

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

// CredentialSource supplies the credential a description's security scheme needs.
//
// It is re-exported from the implementation package so a deployment configures
// credentials without importing two packages that are the same thing.
type CredentialSource = openapi.CredentialSource

// CredentialSourceFunc adapts a function to CredentialSource.
type CredentialSourceFunc = openapi.CredentialSourceFunc

// StaticCredentials is a credential set keyed by scheme name.
type StaticCredentials = openapi.StaticCredentials
