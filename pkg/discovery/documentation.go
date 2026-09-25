package discovery

import (
	"context"
	"fmt"

	"github.com/Manu343726/toolsbox/pkg/docs"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

const documentationServiceName = "toolsbox.documentation.v1.DocumentationService"

// GetDocumentation loads documentation for targetService from the standard
// DocumentationService exposed at this endpoint. If the process has embedded
// descriptor documentation, it is preferred without an RPC.
func (c *Client) GetDocumentation(ctx context.Context, targetService string) (*docs.Service, error) {
	if local, err := docs.DefaultCatalog().Get(targetService); err == nil {
		return &local, nil
	}
	schema, err := c.DescribeService(ctx, documentationServiceName)
	if err != nil {
		return nil, err
	}
	method := findMethod(schema, "GetDocumentation")
	if method == nil {
		return nil, fmt.Errorf("documentation service has no GetDocumentation method")
	}
	request := dynamicpb.NewMessage(method.Input)
	requestField := method.Input.Fields().ByName("service_name")
	if requestField == nil {
		return nil, fmt.Errorf("documentation request has no service_name field")
	}
	request.Set(requestField, protoreflect.ValueOfString(targetService))
	response, err := c.Invoke(ctx, documentationServiceName, "GetDocumentation", request)
	if err != nil {
		return nil, err
	}
	responseMessage, ok := response.(protoreflect.Message)
	if !ok {
		return nil, fmt.Errorf("documentation response is not a protobuf message")
	}
	documentationField := responseMessage.Descriptor().Fields().ByName("documentation")
	if documentationField == nil {
		return nil, fmt.Errorf("documentation response has no documentation field")
	}
	return documentationFromMessage(responseMessage.Get(documentationField).Message())
}

func documentationFromMessage(message protoreflect.Message) (*docs.Service, error) {
	if !message.IsValid() {
		return nil, fmt.Errorf("documentation response is empty")
	}
	result := &docs.Service{
		Name:        stringField(message, "name"),
		Description: stringField(message, "description"),
	}
	methods := message.Get(message.Descriptor().Fields().ByName("methods")).List()
	for i := 0; i < methods.Len(); i++ {
		methodMessage := methods.Get(i).Message()
		method := docs.Method{
			Name:            stringField(methodMessage, "name"),
			Description:     stringField(methodMessage, "description"),
			InputType:       stringField(methodMessage, "input_type"),
			OutputType:      stringField(methodMessage, "output_type"),
			ClientStreaming: boolField(methodMessage, "client_streaming"),
			ServerStreaming: boolField(methodMessage, "server_streaming"),
		}
		parametersField := methodMessage.Descriptor().Fields().ByName("parameters")
		if parametersField != nil {
			parameters := methodMessage.Get(parametersField).List()
			for j := 0; j < parameters.Len(); j++ {
				parameter, err := parameterFromMessage(parameters.Get(j).Message())
				if err != nil {
					return nil, err
				}
				method.Parameters = append(method.Parameters, parameter)
			}
		}
		result.Methods = append(result.Methods, method)
	}
	return result, nil
}

func parameterFromMessage(message protoreflect.Message) (docs.Parameter, error) {
	result := docs.Parameter{
		Name: stringField(message, "name"), Description: stringField(message, "description"),
		TypeName: stringField(message, "type_name"), DefaultValue: stringField(message, "default_value"),
		Repeated: boolField(message, "repeated"), Required: boolField(message, "required"),
	}
	fields := message.Descriptor().Fields().ByName("fields")
	if fields != nil {
		list := message.Get(fields).List()
		for i := 0; i < list.Len(); i++ {
			parameter, err := parameterFromMessage(list.Get(i).Message())
			if err != nil {
				return docs.Parameter{}, err
			}
			result.Fields = append(result.Fields, parameter)
		}
	}
	return result, nil
}

func stringField(message protoreflect.Message, name string) string {
	field := message.Descriptor().Fields().ByName(protoreflect.Name(name))
	if field == nil {
		return ""
	}
	return message.Get(field).String()
}

func boolField(message protoreflect.Message, name string) bool {
	field := message.Descriptor().Fields().ByName(protoreflect.Name(name))
	if field == nil {
		return false
	}
	return message.Get(field).Bool()
}
