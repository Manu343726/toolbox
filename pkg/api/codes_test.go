package api_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every subsystem that reaches a provider maps its failures the same way, and
// this is the mapping. A kind with no code here would be reported as a different
// failure depending on which subsystem produced it, which is the outcome the
// single shared mapping exists to prevent.
func TestEveryKindHasACode(t *testing.T) {
	for _, testCase := range []struct {
		kind api.ErrorKind
		want connect.Code
	}{
		{api.KindInvalid, connect.CodeInvalidArgument},
		{api.KindNotFound, connect.CodeNotFound},
		{api.KindFailedPrecondition, connect.CodeFailedPrecondition},
		{api.KindDenied, connect.CodePermissionDenied},
		{api.KindAlreadyExists, connect.CodeAlreadyExists},
		{api.KindUnsupported, connect.CodeUnimplemented},
		{api.KindUnavailable, connect.CodeUnavailable},
		{api.KindInternal, connect.CodeInternal},
	} {
		t.Run(string(testCase.kind), func(t *testing.T) {
			assert.Equal(t, testCase.want, api.ConnectCode(testCase.kind))
		})
	}

	// An unrecognised kind is a failure this framework does not understand, and
	// internal is the honest answer: reporting it as anything else would claim a
	// specificity nobody has.
	assert.Equal(t, connect.CodeInternal, api.ConnectCode("something_new"))
	assert.Equal(t, connect.CodeInternal, api.ConnectCode(""))
}

func TestConnectErrorCarriesTheKindsCode(t *testing.T) {
	mapped := api.ConnectError(api.Errorf(api.KindNotFound, "no such model"))
	require.Error(t, mapped)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(mapped))
	assert.Contains(t, mapped.Error(), "no such model", "the message is the one the failure carried")
}

// A provider reached over ConnectRPC has already mapped its own failure. Mapping
// it again from a kind that was never set would replace a precise answer with a
// guess, so an existing code is kept exactly as it arrived.
func TestConnectErrorKeepsACodeTheProviderAlreadyChose(t *testing.T) {
	original := connect.NewError(connect.CodeResourceExhausted, errors.New("slow down"))
	mapped := api.ConnectError(original)
	require.Error(t, mapped)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(mapped))

	// The same holds when the code is wrapped in something else on the way here.
	wrapped := fmt.Errorf("calling the provider: %w", original)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(api.ConnectError(wrapped)))
}

// A caller that stopped waiting did not have a broken request, and telling it the
// request was internal tells it to look for a bug rather than to stop.
func TestConnectErrorReportsAContextThatEndedAsItself(t *testing.T) {
	cancelled := api.ConnectError(api.WrapError(api.KindInternal, context.Canceled, "generating"))
	assert.Equal(t, connect.CodeCanceled, connect.CodeOf(cancelled))

	overdue := api.ConnectError(api.WrapError(api.KindInternal, context.DeadlineExceeded, "generating"))
	assert.Equal(t, connect.CodeDeadlineExceeded, connect.CodeOf(overdue))

	// Bare, because a provider may return one without classifying it.
	assert.Equal(t, connect.CodeCanceled, connect.CodeOf(api.ConnectError(context.Canceled)))
	assert.Equal(t, connect.CodeDeadlineExceeded, connect.CodeOf(api.ConnectError(context.DeadlineExceeded)))
}

// A provider whose endpoint is gone is unavailable, and that is what tells a
// caller to retry rather than to change its request. The failure arrives from
// outside this process and carries no classification of its own.
func TestConnectErrorReportsAnUnreachableProviderAsUnavailable(t *testing.T) {
	for _, failure := range []error{
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.EHOSTUNREACH,
		syscall.ENETUNREACH,
		&net.DNSError{Err: "no such host", Name: "provider.test"},
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
		fmt.Errorf("invoke: %w", syscall.ECONNREFUSED),
	} {
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(api.ConnectError(failure)),
			"%v is a network failure, not a request the caller got wrong", failure)
	}

	// A failure that is not one of those keeps whatever the kind says, so this
	// does not turn every error into "try again".
	assert.Equal(t, connect.CodeNotFound,
		connect.CodeOf(api.ConnectError(api.Errorf(api.KindNotFound, "no such model"))))
}

// A mapping that invented a failure would make every caller of it check for nil.
func TestConnectErrorOfNothingIsNothing(t *testing.T) {
	assert.NoError(t, api.ConnectError(nil))
}
