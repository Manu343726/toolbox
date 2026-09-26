package apiopenapi

import (
	"connectrpc.com/connect"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/openapi"
)

// connectCode maps a classified failure onto the canonical ConnectRPC code. The
// classification is the single source of truth and so is the mapping, which lives
// in the framework rather than in each provider that needs it.
func connectCode(kind api.ErrorKind) connect.Code {
	return api.ConnectCode(kind)
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
