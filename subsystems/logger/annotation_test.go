package logger_test

import (
	"testing"

	"github.com/Manu343726/toolbox/pkg/api"
	"github.com/Manu343726/toolbox/pkg/docs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// A side-effect annotation naming something outside the documented vocabulary is dropped
// rather than rejected, and the operation then falls to "unclassified" — which a policy naming
// read and write does not cover. So a typo does not fail; it quietly narrows what the default
// policy grants, and the only sign is an operation nobody can enable without naming
// "unclassified" by hand.
func TestEveryOperationDeclaresASideEffectTheFrameworkKnows(t *testing.T) {
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName("toolbox.logger.v1.LoggerService")
	require.NoError(t, err)
	service, ok := descriptor.(protoreflect.ServiceDescriptor)
	require.True(t, ok)

	documentation := docs.ExtractServiceDocumentation(service)
	require.NotNil(t, documentation)
	require.NotEmpty(t, documentation.Methods, "no methods were documented, so nothing is checked")

	for _, method := range documentation.Methods {
		effects, unknown := api.SideEffects(method.Annotations)
		assert.Empty(t, unknown, "%s declares a side effect outside the vocabulary: %v", method.Name, unknown)
		assert.NotEmpty(t, effects, "%s declares no side effect, so it is unclassified and a "+
			"policy naming read and write will not cover it", method.Name)
		for _, effect := range effects {
			assert.True(t, api.KnownSideEffect(effect), "%s declares the unknown effect %q", method.Name, effect)
		}
	}
}

func TestTheOperationsAreClassifiedTheWayTheirBehaviourSays(t *testing.T) {
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName("toolbox.logger.v1.LoggerService")
	require.NoError(t, err)
	service, ok := descriptor.(protoreflect.ServiceDescriptor)
	require.True(t, ok)
	documentation := docs.ExtractServiceDocumentation(service)
	require.NotNil(t, documentation)

	classes := map[string][]string{}
	for _, method := range documentation.Methods {
		effects, _ := api.SideEffects(method.Annotations)
		for _, class := range api.EffectClasses(effects) {
			classes[method.Name] = append(classes[method.Name], string(class))
		}
	}
	assert.Contains(t, classes["GetConfig"], "read", "GetConfig only reports the fanout")
	assert.Contains(t, classes["Log"], "write", "Log records an entry, which creates one")
	assert.Contains(t, classes["SetConfig"], "write", "SetConfig changes how entries are routed")
}
