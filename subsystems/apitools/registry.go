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

// This file exposes the catalog as the framework's plain Go interfaces, so a
// composition that runs the catalog in the same process can use it without a
// ConnectRPC round trip to itself.
//
// The same catalog is reachable over ConnectRPC — that is what the subsystem's own
// command serves — and both paths run the same routing, the same policy, and the
// same store, so the two cannot disagree about what a deployment has registered.

// Registrar returns the catalog's write side, as the framework's interface.
//
// It is the counterpart of a Catalog, and it exists for compositions: a host that
// describes its own subsystems needs to register what it found, and a deployment
// that reads its catalog from a file needs to load it.
func (s *Service) Registrar() api.Registrar { return registrar{service: s} }

// Catalog returns the catalog's read side.
func (s *Service) Catalog() api.Catalog { return s.store.Catalog() }

// ExposureSource returns what the catalog has exposed, so a gateway can honour it
// instead of inventing its own opinion.
func (s *Service) ExposureSource() api.ExposureSource { return exposureSource{store: s.store} }

// Invoker returns an invoker that routes calls through the providers this
// deployment configured, after the catalog's own checks.
func (s *Service) Invoker() api.Invoker { return invoker{service: s} }

type registrar struct {
	service *Service
}

// RegisterServer records a server.
func (r registrar) RegisterServer(ctx context.Context, server api.Server) (api.Server, bool, error) {
	if r.service == nil {
		return api.Server{}, false, fmt.Errorf("no catalog is configured")
	}
	// A registered server is serving until something reports otherwise: the
	// catalog records what a deployment told it, and reachability is the
	// deployment's business.
	if server.Status == "" {
		server.Status = api.ServerStatusServing
	}
	return r.service.store.RegisterServer(server, false)
}

// DescribeAPI asks a parser provider to interpret a document, or to read a live
// endpoint that describes itself.
func (r registrar) DescribeAPI(ctx context.Context, request api.DescribeRequest) (api.DescribeResult, error) {
	if r.service == nil {
		return api.DescribeResult{}, fmt.Errorf("no catalog is configured")
	}
	if len(request.Document) == 0 {
		return api.DescribeResult{}, fmt.Errorf("a document or a base url is required")
	}
	described, providerID, warnings, formats, err := r.service.parse(
		ctx,
		request.Document,
		string(request.Format),
		string(request.Format),
		request.ParserID,
		&apiv1.ApiSource{Kind: request.Source.Kind, Location: request.Source.Location, Digest: request.Source.Digest},
		request.BaseURL,
		request.APIID,
		"",
	)
	if err != nil {
		return api.DescribeResult{}, err
	}
	return api.DescribeResult{API: described, ProviderID: providerID, Warnings: warnings, Formats: formats}, nil
}

// RegisterAPI stores a standard description, bound to a server.
func (r registrar) RegisterAPI(_ context.Context, described api.API, serverID string) (api.API, bool, error) {
	if r.service == nil {
		return api.API{}, false, fmt.Errorf("no catalog is configured")
	}
	return r.service.store.RegisterAPI(described, serverID, false)
}

// SetExposed records an exposure decision. Exposing is refused when the catalog's
// policy does not allow the operation, so a composition cannot route around it.
func (r registrar) SetExposed(_ context.Context, apiID, operationID string, exposed bool) (bool, error) {
	if r.service == nil {
		return false, fmt.Errorf("no catalog is configured")
	}
	return r.service.store.SetExposed(apiID, operationID, exposed)
}

type exposureSource struct {
	store *Memory
}

// Exposures implements api.ExposureSource.
func (e exposureSource) Exposures(_ context.Context, operationIDs []string) (map[string]api.Exposure, error) {
	if e.store == nil {
		return nil, nil
	}
	wanted := make(map[string]bool, len(operationIDs))
	for _, id := range operationIDs {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	result := make(map[string]api.Exposure, len(wanted))
	for _, record := range e.store.Exposure(ExposureFilter{}) {
		if len(wanted) > 0 && !wanted[record.Operation.ID] {
			continue
		}
		result[record.Operation.ID] = api.Exposure{
			OperationID: record.Operation.ID,
			Allowed:     record.Allowed,
			Exposed:     record.Exposed,
			Invokable:   record.Invokable,
		}
	}
	return result, nil
}

// callRequestFor builds the request the catalog's own call path reads, so the
// in-process invoker and the ConnectRPC handler run exactly the same checks.
func callRequestFor(call api.Call) *apitoolsv1.CallOperationRequest {
	return &apitoolsv1.CallOperationRequest{
		ApiId:         call.API.ID,
		OperationId:   call.Operation.ID,
		ArgumentsJson: append([]byte(nil), call.Arguments...),
	}
}

type invoker struct {
	service *Service
}

// Invoke implements api.Invoker by routing through the deployment's invoker
// providers, after the catalog's own validation.
func (i invoker) Invoke(ctx context.Context, call api.Call) (api.Result, error) {
	if i.service == nil {
		return api.Result{}, fmt.Errorf("no catalog is configured")
	}
	response, err := i.service.CallOperation(ctx, connect.NewRequest(callRequestFor(call)))
	if err != nil {
		return api.Result{}, err
	}
	result := api.Result{
		Status:      int(response.Msg.GetStatus()),
		ContentType: response.Msg.GetContentType(),
		Body:        response.Msg.GetBodyJson(),
	}
	if headers := response.Msg.GetHeaders(); len(headers) > 0 {
		result.Headers = make(map[string][]string, len(headers))
		for name, value := range headers {
			result.Headers[name] = []string{value}
		}
	}
	return result, nil
}
