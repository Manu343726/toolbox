package apitools

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/Manu343726/toolbox/pkg/api"
	apiv1 "github.com/Manu343726/toolbox/pkg/api/apiv1"
	apitoolsv1 "github.com/Manu343726/toolbox/subsystems/apitools/apitoolsv1"
)

// ServeApi asks an adapter provider to serve a registered API in a target
// format and registers the served surface as a server of its own.
//
// Registering the surface matters: a served API is an API an agent can reach, and
// everything the catalog does — listing, exposure, invocation — has to work for
// it the same way it does for a server someone registered by hand.
func (s *Service) ServeApi(ctx context.Context, req *connect.Request[apitoolsv1.ServeApiRequest]) (*connect.Response[apitoolsv1.ServeApiResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("serve request is required"))
	}
	apiID := strings.TrimSpace(req.Msg.GetApiId())
	if apiID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("api_id is required"))
	}
	target := strings.TrimSpace(req.Msg.GetTarget())
	if target == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("target is required"))
	}
	described, err := s.store.GetAPI(apiID)
	if err != nil {
		return nil, catalogError(err)
	}
	if len(described.ServerIDs) == 0 {
		return nil, connect.NewError(
			connect.CodeFailedPrecondition,
			fmt.Errorf("api %q is not bound to a server; an adapted surface has to forward somewhere", apiID),
		)
	}
	// The provider is given the server as well as the description: the surface it
	// serves is defined by the description, and the work happens at the server.
	original, err := s.store.GetServer(described.ServerIDs[0])
	if err != nil {
		return nil, catalogError(err)
	}
	if s.directory == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no provider directory is configured"))
	}
	provider, err := s.selectProvider(ctx, req.Msg.GetAdapterId(), api.ProviderAdapter, "adapter_id", target)
	if err != nil {
		return nil, err
	}
	client, err := s.directory.Adapter(ctx, provider.ID)
	if err != nil {
		return nil, catalogError(err)
	}
	response, err := client.ServeApi(ctx, connect.NewRequest(&apiv1.ServeApiRequest{
		Api:           described.ToProto(),
		Server:        original.ToProto(),
		Target:        target,
		ListenAddress: req.Msg.GetListenAddress(),
		BasePath:      req.Msg.GetBasePath(),
		Options:       req.Msg.GetOptions(),
	}))
	if err != nil {
		return nil, providerError(err)
	}
	instanceID := response.Msg.GetInstanceId()
	if instanceID == "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("the provider started a surface without identifying it"))
	}
	// The served surface is registered as a server so it can be listed, bound to,
	// and invoked like any other, and so a second call reports the conflict.
	servedID := strings.TrimSpace(req.Msg.GetServedServerId())
	if servedID == "" {
		servedID = instanceID
	}
	served, _, err := s.store.RegisterServer(api.Server{
		ID:          servedID,
		Name:        fmt.Sprintf("%s served as %s", described.ID, target),
		BaseURL:     response.Msg.GetEndpoint(),
		Format:      described.Format,
		Transport:   api.Transport("http"),
		Description: fmt.Sprintf("Adapted surface of %s rendered into %s by %s.", described.ID, target, provider.ID),
	}, false)
	if err != nil {
		// The surface is already running, so it has to be stopped again rather
		// than left behind with nothing recording it.
		_, _ = client.StopApi(ctx, connect.NewRequest(&apiv1.StopApiRequest{InstanceId: instanceID}))
		return nil, catalogError(err)
	}
	return connect.NewResponse(&apitoolsv1.ServeApiResponse{
		Target:                response.Msg.GetTarget(),
		AdapterId:             provider.ID,
		InstanceId:            instanceID,
		Server:                served.ToProto(),
		Endpoint:              response.Msg.GetEndpoint(),
		BasePath:              response.Msg.GetBasePath(),
		Paths:                 append([]string(nil), response.Msg.GetPaths()...),
		DocumentationEndpoint: response.Msg.GetDocumentationEndpoint(),
		SchemaEndpoint:        response.Msg.GetSchemaEndpoint(),
		Warnings:              append([]string(nil), response.Msg.GetWarnings()...),
	}), nil
}

// StopApi stops one adapted surface and, when asked, removes the server that was
// registered for it.
func (s *Service) StopApi(ctx context.Context, req *connect.Request[apitoolsv1.StopApiRequest]) (*connect.Response[apitoolsv1.StopApiResponse], error) {
	if req == nil || req.Msg == nil || strings.TrimSpace(req.Msg.GetInstanceId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance_id is required"))
	}
	instanceID := strings.TrimSpace(req.Msg.GetInstanceId())
	if s.directory == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no provider directory is configured"))
	}
	adapterID := strings.TrimSpace(req.Msg.GetAdapterId())
	var lastErr error
	stopped := false
	if adapterID != "" {
		stopped, lastErr = s.stopOne(ctx, adapterID, instanceID)
	} else {
		// The instance is stopped through whichever provider claims it, so a
		// caller does not have to remember which adapter it asked.
		adapters, err := s.providersForRole(ctx, api.ProviderAdapter)
		if err != nil {
			return nil, err
		}
		for _, provider := range adapters {
			ok, err := s.stopOne(ctx, provider.ID, instanceID)
			if err != nil {
				lastErr = err
				continue
			}
			if ok {
				stopped = true
				break
			}
		}
	}
	if !stopped && lastErr != nil {
		return nil, providerError(lastErr)
	}
	serverRemoved := false
	if req.Msg.GetRemoveServer() {
		if removed, _, err := s.store.DeleteServer(instanceID, true); err == nil {
			serverRemoved = removed
		}
	}
	return connect.NewResponse(&apitoolsv1.StopApiResponse{Stopped: stopped, ServerRemoved: serverRemoved}), nil
}

func (s *Service) stopOne(ctx context.Context, providerID, instanceID string) (bool, error) {
	client, err := s.directory.Adapter(ctx, providerID)
	if err != nil {
		return false, err
	}
	response, err := client.StopApi(ctx, connect.NewRequest(&apiv1.StopApiRequest{InstanceId: instanceID}))
	if err != nil {
		return false, err
	}
	return response.Msg.GetStopped(), nil
}
