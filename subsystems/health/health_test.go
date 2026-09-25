package health

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	healthv1 "github.com/Manu343726/toolbox/subsystems/health/healthv1"
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
