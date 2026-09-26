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

// ConnectKind reads a failure that arrived over ConnectRPC as the kind it was
// classified as on the other side.
//
// It is the inverse of ConnectCode and it exists because a caller on this side of
// a transport frequently needs the classification rather than the code: a gateway
// answering a protocol of its own has to decide which code *that* protocol wants,
// and a subsystem's ConnectRPC code is not necessarily the right answer to a
// different protocol's question. "There is no such skill" is NotFound inside
// ConnectRPC and InvalidParams inside the MCP Skills extension, because there the
// URI is the parameter.
//
// A failure that carries no code is internal, which is what KindOf already says
// about an unclassified failure; this does not invent a classification to fill the
// gap. The codes that have no kind of their own — cancelled, and the two the SDK
// raises about a peer — are named rather than folded into internal, because a
// caller that treats a cancelled call as a server fault reports a fault that did
// not happen.
func ConnectKind(err error) ErrorKind {
	if err == nil {
		return ""
	}
	// The transport's code is read *before* KindOf, because KindOf answers "internal" for
	// anything it does not recognise — and a failure that crossed the wire is recognised by
	// its code even though it carries no classification. Asking KindOf first would replace a
	// precise answer with the framework's word for "nobody classified this".
	//
	// A classification this process set is not lost by that: ConnectError wraps the original
	// error, so a locally classified failure still unwraps to one and its kind is what the
	// code was derived from in the first place.
	var crossed *connect.Error
	if errors.As(err, &crossed) {
		switch crossed.Code() {
		case connect.CodeInvalidArgument:
			return KindInvalid
		case connect.CodeNotFound:
			return KindNotFound
		case connect.CodeFailedPrecondition:
			return KindFailedPrecondition
		case connect.CodePermissionDenied:
			return KindDenied
		case connect.CodeAlreadyExists:
			return KindAlreadyExists
		case connect.CodeUnimplemented:
			return KindUnsupported
		case connect.CodeUnavailable, connect.CodeResourceExhausted:
			return KindUnavailable
		case connect.CodeCanceled, connect.CodeDeadlineExceeded:
			// The peer stopped waiting. Neither is a failure of the operation, so neither
			// is reported as one.
			return ""
		default:
			return KindInternal
		}
	}
	return KindOf(err)
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
