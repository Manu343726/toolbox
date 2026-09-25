package discovery_test

import (
	"testing"

	"github.com/Manu343726/toolbox/pkg/discovery"
	"github.com/stretchr/testify/assert"
)

func TestNewNormalizesEndpoint(t *testing.T) {
	client := discovery.New(" http://127.0.0.1:9000/ ")
	assert.Equal(t, "http://127.0.0.1:9000", client.Endpoint())
}

func TestReflectionServiceFilter(t *testing.T) {
	assert.True(t, discovery.IsReflectionService("grpc.reflection.v1.ServerReflection"))
	assert.True(t, discovery.IsReflectionService("grpc.reflection.v1alpha.ServerReflection"))
	assert.False(t, discovery.IsReflectionService("toolbox.testecho.v1.EchoService"))
}
