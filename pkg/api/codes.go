package api

import (
	"context"
	"errors"
	"net"
	"syscall"

	"connectrpc.com/connect"
)

// This file is the one place a classified failure becomes a ConnectRPC code.
//
// The classifications are the framework's own and the codes are ConnectRPC's, and
// the mapping between them is a fact about the framework rather than about any
// subsystem. It was written out in each provider subsystem that needed it, which
// meant a new ErrorKind could be mapped in one copy and not another, and a caller
// would see two different codes for the same failure depending on which provider
// produced it.

// ConnectCode maps a classified failure onto the canonical ConnectRPC code.
//
// Every kind has a code, including the ones with no obvious counterpart: a request
// that cannot be satisfied in the current state is a failed precondition, and an
// unrecognised kind is an internal failure rather than a success or a silent
// success-shaped error.
func ConnectCode(kind ErrorKind) connect.Code {
	switch kind {
	case KindInvalid:
		return connect.CodeInvalidArgument
	case KindNotFound:
		return connect.CodeNotFound
	case KindFailedPrecondition:
		return connect.CodeFailedPrecondition
	case KindDenied:
		return connect.CodePermissionDenied
	case KindAlreadyExists:
		return connect.CodeAlreadyExists
	case KindUnsupported:
		return connect.CodeUnimplemented
	case KindUnavailable:
		return connect.CodeUnavailable
	case KindInternal:
		return connect.CodeInternal
	default:
		return connect.CodeInternal
	}
}

// ConnectError maps a failure onto a ConnectRPC error carrying the code its kind
// calls for, so the classification survives the transport instead of being
// re-derived from a message.
//
// A failure that is already a ConnectRPC error keeps its code. That matters because
// a provider reached over ConnectRPC has already mapped its own failure, and
// re-deriving a code from a kind that was never set would replace a precise
// answer with a guess. A nil error is nil, because a mapping that invented a
// failure would make every caller of it check.
func ConnectError(err error) error {
	if err == nil {
		return nil
	}
	var already *connect.Error
	if errors.As(err, &already) {
		return err
	}
	// A context that ended is not a failure of the operation: the caller stopped
	// waiting, or the deadline passed. Reporting either as internal would tell a
	// caller its request was broken when in fact it stopped being interesting.
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	// A provider that failed because the machine it reaches is gone is
	// unavailable, and saying so is what tells a caller to retry rather than to
	// look for a different request.
	if isConnectionFailure(err) {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewError(ConnectCode(KindOf(err)), err)
}

// isConnectionFailure reports whether a failure is the network rather than the
// request, for an error that arrived from outside this process and therefore
// carries no classification of its own.
//
// The check is for the connection errors Go itself defines, plus the two every
// dial produces on a Unix socket, because a provider reached over ConnectRPC
// reports a refused or reset connection as a syscall error with nothing on it to
// say what happened.
func isConnectionFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	// A DNS or dial failure that reached the transport unwrapped.
	var dnsError *net.DNSError
	return errors.As(err, &dnsError)
}
