package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrorKind is the canonical classification of a failure in the API layer.
//
// The standard model's implementations live in plain Go packages that know
// nothing about any transport, so they report what went wrong rather than which
// status code it maps to. A transport layer — a ConnectRPC handler, an HTTP
// gateway — maps the kind to its own codes, and a caller that speaks neither
// transport can still read the kind.
type ErrorKind string

const (
	// KindInvalid is a malformed or contradictory request.
	KindInvalid ErrorKind = "invalid_argument"
	// KindNotFound is a request for something that does not exist.
	KindNotFound ErrorKind = "not_found"
	// KindFailedPrecondition is a request that cannot be satisfied in the current
	// state, such as describing an API whose parser is not deployed.
	KindFailedPrecondition ErrorKind = "failed_precondition"
	// KindDenied is a request the policy refuses.
	KindDenied ErrorKind = "permission_denied"
	// KindAlreadyExists is a request for a name that is already taken, such as
	// serving an API that is already being served.
	KindAlreadyExists ErrorKind = "already_exists"
	// KindUnsupported is a capability the implementation does not have, such as
	// invoking a streaming operation over a unary-only path.
	KindUnsupported ErrorKind = "unimplemented"
	// KindUnavailable is a failure to reach something that should be there.
	KindUnavailable ErrorKind = "unavailable"
	// KindInternal is a failure the caller cannot fix.
	KindInternal ErrorKind = "internal"
)

// Error is a classified failure.
type Error struct {
	// Kind is what went wrong.
	Kind ErrorKind
	// Message explains it. It must not carry secrets or whole request payloads.
	Message string
	// Err is the underlying cause, if any.
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	switch {
	case e.Message != "" && e.Err != nil:
		return e.Message + ": " + e.Err.Error()
	case e.Message != "":
		return e.Message
	case e.Err != nil:
		return e.Err.Error()
	default:
		return string(e.Kind)
	}
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Errorf builds a classified error.
func Errorf(kind ErrorKind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// WrapError builds a classified error around a cause.
func WrapError(kind ErrorKind, err error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...), Err: err}
}

// KindOf reports the classification of an error, walking its chain. An error with
// no classification is internal, because an unclassified failure is a bug rather
// than a request the caller should change. A cancelled or expired context keeps
// its own kind, so a caller can tell "too slow" from "wrong".
func KindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var classified *Error
	if errors.As(err, &classified) && classified.Kind != "" {
		return classified.Kind
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return KindUnavailable
	case errors.Is(err, context.Canceled):
		return KindUnavailable
	default:
		return KindInternal
	}
}

// IsKind reports whether an error carries a classification.
func IsKind(err error, kind ErrorKind) bool {
	return err != nil && KindOf(err) == kind
}

// NormalizeKind trims and lowercases a classification, so a caller that
// configured one by hand is not defeated by whitespace or case.
func NormalizeKind(kind string) ErrorKind {
	return ErrorKind(strings.ToLower(strings.TrimSpace(kind)))
}
