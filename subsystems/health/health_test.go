package health

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	healthv1 "github.com/Manu343726/toolbox/subsystems/health/healthv1"
	"github.com/Manu343726/toolbox/subsystems/health/healthv1/healthv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthHandler(t *testing.T) {
	handler := NewHandler(Options{ComponentName: "worker", Version: "test"})
	response, err := handler.Check(context.Background(), connect.NewRequest(&healthv1.CheckRequest{}))
	require.NoError(t, err)
	assert.Equal(t, healthv1.HealthStatus_HEALTH_STATUS_SERVING, response.Msg.GetStatus())
	assert.Equal(t, "test", response.Msg.GetVersion())

	_, err = handler.Check(context.Background(), connect.NewRequest(&healthv1.CheckRequest{ComponentName: "other"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestHealthHandlerApplicationFailure(t *testing.T) {
	handler := NewHandler(Options{Check: func(context.Context) error { return errors.New("not ready") }})
	response, err := handler.Check(context.Background(), connect.NewRequest(&healthv1.CheckRequest{}))
	require.NoError(t, err)
	assert.Equal(t, healthv1.HealthStatus_HEALTH_STATUS_NOT_SERVING, response.Msg.GetStatus())
	assert.Equal(t, "not ready", response.Msg.GetMessage())
}

func TestHealthNewServer(t *testing.T) {
	server, err := New(Options{})
	require.NoError(t, err)
	assert.Equal(t, Name, server.Descriptor().SubsystemName)
}

// An unhealthy component is a successful answer about an unhealthy component, not
// a failed call. A probe that could not tell the two apart reports a deployment
// that is up and refusing work as one that is not answering at all, and the two
// need different responses.
func TestAFailingCheckIsAnAnswerNotAnError(t *testing.T) {
	handler := NewHandler(Options{
		ComponentName: "worker",
		Version:       "2.1.0",
		Check:         func(context.Context) error { return errors.New("the database is unreachable") },
	})

	response, err := handler.Check(context.Background(), connect.NewRequest(&healthv1.CheckRequest{}))
	require.NoError(t, err, "a failing readiness check is still an answered probe")
	assert.Equal(t, healthv1.HealthStatus_HEALTH_STATUS_NOT_SERVING, response.Msg.GetStatus())
	assert.Equal(t, "the database is unreachable", response.Msg.GetMessage(),
		"the operator reads this message, so it is the check's own")
	assert.Equal(t, "2.1.0", response.Msg.GetVersion())
}

// The contract says an empty component name checks the server itself, and a name
// of only whitespace says the same thing to whoever sent it.
func TestAnAbsentOrBlankComponentNameChecksTheServerItself(t *testing.T) {
	asked := 0
	handler := NewHandler(Options{
		ComponentName: "worker",
		Check:         func(context.Context) error { asked++; return nil },
	})
	for _, request := range []*connect.Request[healthv1.CheckRequest]{
		nil,
		connect.NewRequest(&healthv1.CheckRequest{}),
		connect.NewRequest(&healthv1.CheckRequest{ComponentName: ""}),
		connect.NewRequest(&healthv1.CheckRequest{ComponentName: "  "}),
		connect.NewRequest(&healthv1.CheckRequest{ComponentName: "\t\n"}),
	} {
		response, err := handler.Check(context.Background(), request)
		require.NoError(t, err)
		assert.Equal(t, healthv1.HealthStatus_HEALTH_STATUS_SERVING, response.Msg.GetStatus())
	}
	assert.Equal(t, 5, asked, "each of those ran the check")
}

// A name this subsystem does not host is a not-found naming both, because the
// caller's next move is to ask the deployment which names it does host.
func TestAnAbsentComponentIsANotFoundNamingBothNames(t *testing.T) {
	handler := NewHandler(Options{ComponentName: "worker"})

	_, err := handler.Check(context.Background(), connect.NewRequest(&healthv1.CheckRequest{ComponentName: "somebody-else"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "somebody-else", "the error names what was asked for")
	assert.Contains(t, err.Error(), "worker", "and what is here instead")

	// Asking for the name this subsystem does host is answered.
	response, err := handler.Check(context.Background(), connect.NewRequest(&healthv1.CheckRequest{ComponentName: "worker"}))
	require.NoError(t, err)
	assert.Equal(t, healthv1.HealthStatus_HEALTH_STATUS_SERVING, response.Msg.GetStatus())
}

// A check that blocks forever must be released by the caller's own context, or a
// probe that times out at the transport cannot stop waiting for this one.
func TestACheckIsGivenTheCallersContext(t *testing.T) {
	observed := make(chan error, 1)
	handler := NewHandler(Options{
		Check: func(ctx context.Context) error {
			<-ctx.Done()
			observed <- ctx.Err()
			return ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := handler.Check(ctx, connect.NewRequest(&healthv1.CheckRequest{}))
		require.NoError(t, err)
		assert.Equal(t, healthv1.HealthStatus_HEALTH_STATUS_NOT_SERVING, response.Msg.GetStatus())
	}()
	cancel()
	<-done
	select {
	case err := <-observed:
		assert.ErrorIs(t, err, context.Canceled)
	default:
		t.Error("the check finished without its context ending")
	}
}

// A subsystem with no check is serving, and says so without inventing a reason.
func TestASubsystemWithNoCheckReportsItselfServing(t *testing.T) {
	response, err := NewHandler(Options{}).Check(context.Background(), connect.NewRequest(&healthv1.CheckRequest{}))
	require.NoError(t, err)
	assert.Equal(t, healthv1.HealthStatus_HEALTH_STATUS_SERVING, response.Msg.GetStatus())
	assert.Equal(t, "subsystem is serving", response.Msg.GetMessage())
	assert.Equal(t, Version, response.Msg.GetVersion(), "the default version is reported when none is configured")
}

// A name configured with padding is reachable under the name it was configured
// with, because a deployment wrote that name and expects to be able to ask for it.
func TestAConfiguredComponentNameIsTrimmed(t *testing.T) {
	handler := NewHandler(Options{ComponentName: "  worker  "})
	response, err := handler.Check(context.Background(), connect.NewRequest(&healthv1.CheckRequest{ComponentName: "worker"}))
	require.NoError(t, err)
	assert.Equal(t, healthv1.HealthStatus_HEALTH_STATUS_SERVING, response.Msg.GetStatus())
}

// What a deployment launches is part of the subsystem's behaviour, and the
// readiness check a deployment configured has to reach the framework's own health
// reporting as well as the contract's.
func TestNewDeclaresItsContractAndWiresTheCheck(t *testing.T) {
	failing := errors.New("not ready")
	server, err := New(Options{ComponentName: "worker", Version: "2.1.0", Check: func(context.Context) error { return failing }})
	require.NoError(t, err)

	descriptor := server.Descriptor()
	assert.Equal(t, Name, descriptor.SubsystemName)
	assert.Equal(t, "2.1.0", descriptor.ImplementationVersion)
	assert.Equal(t, []string{healthv1connect.HealthServiceName}, descriptor.ServiceNames)

	require.NoError(t, server.Start(t.Context()))
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	// Over the real transport, with the real client: a check that works in process
	// and is not registered is a subsystem nothing can probe.
	client := healthv1connect.NewHealthServiceClient(http.DefaultClient, server.Endpoint())
	response, err := client.Check(t.Context(), connect.NewRequest(&healthv1.CheckRequest{}))
	require.NoError(t, err)
	assert.Equal(t, healthv1.HealthStatus_HEALTH_STATUS_NOT_SERVING, response.Msg.GetStatus())
	assert.Equal(t, "not ready", response.Msg.GetMessage())

	_, err = client.Check(t.Context(), connect.NewRequest(&healthv1.CheckRequest{ComponentName: "somebody-else"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
