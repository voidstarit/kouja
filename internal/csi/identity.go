package csi

import (
	"context"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type Identity struct {
	csipb.UnimplementedIdentityServer

	Name       string
	Version    string
	Controller bool
	Ready      func() bool
}

func (identity *Identity) GetPluginInfo(context.Context, *csipb.GetPluginInfoRequest) (*csipb.GetPluginInfoResponse, error) {
	return &csipb.GetPluginInfoResponse{Name: identity.Name, VendorVersion: identity.Version}, nil
}

func (identity *Identity) GetPluginCapabilities(context.Context, *csipb.GetPluginCapabilitiesRequest) (*csipb.GetPluginCapabilitiesResponse, error) {
	response := &csipb.GetPluginCapabilitiesResponse{}
	if identity.Controller {
		response.Capabilities = append(response.Capabilities, &csipb.PluginCapability{
			Type: &csipb.PluginCapability_Service_{Service: &csipb.PluginCapability_Service{
				Type: csipb.PluginCapability_Service_CONTROLLER_SERVICE,
			}},
		})
	}
	return response, nil
}

func (identity *Identity) Probe(context.Context, *csipb.ProbeRequest) (*csipb.ProbeResponse, error) {
	ready := identity.Ready == nil || identity.Ready()
	return &csipb.ProbeResponse{Ready: wrapperspb.Bool(ready)}, nil
}
