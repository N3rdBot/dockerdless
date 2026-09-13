package api

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/moby/moby/api/types/network"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

func (h *handlers) networkList(w http.ResponseWriter, r *http.Request) {
	details, err := h.service.NetworkList(r.Context())
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	summaries := make([]network.Summary, 0, len(details))
	for _, detail := range details {
		summaries = append(summaries, networkSummaryResponse(detail))
	}
	writeJSON(w, http.StatusOK, summaries)
}

func (h *handlers) networkInspect(w http.ResponseWriter, r *http.Request) {
	ref := pathParameter(r, "/networks/", "")
	detail, err := h.service.NetworkInspect(r.Context(), ref)
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	response := networkSummaryResponse(detail)
	inspect := network.Inspect{
		Network:    response.Network,
		Containers: map[string]network.EndpointResource{},
	}
	writeJSON(w, http.StatusOK, inspect)
}

func (h *handlers) networkCreate(w http.ResponseWriter, r *http.Request) {
	var payload network.CreateRequest
	if !decodeJSONBody(w, r, h.bodyLimit(), &payload) {
		return
	}
	enableIPv6 := payload.EnableIPv6 != nil && *payload.EnableIPv6
	result, err := h.service.NetworkCreate(r.Context(), ports.NetworkCreateRequest{
		Name:       payload.Name,
		Driver:     payload.Driver,
		EnableIPv6: enableIPv6,
		Labels:     payload.Labels,
	})
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, network.CreateResponse{
		ID:      string(result.ID),
		Warning: result.Warning,
	})
}

func (h *handlers) networkConnect(w http.ResponseWriter, r *http.Request) {
	ref := pathParameter(r, "/networks/", "/connect")
	var payload network.ConnectRequest
	if !decodeJSONBody(w, r, h.bodyLimit(), &payload) {
		return
	}
	var aliases []string
	if payload.EndpointConfig != nil {
		aliases = payload.EndpointConfig.Aliases
	}
	if err := h.service.NetworkConnect(r.Context(), ports.NetworkConnectRequest{
		Network:   ref,
		Container: domain.ContainerID(payload.Container),
		Aliases:   aliases,
	}); err != nil {
		WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) networkRemove(w http.ResponseWriter, r *http.Request) {
	ref := pathParameter(r, "/networks/", "")
	if err := h.service.NetworkRemove(r.Context(), ref); err != nil {
		WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func networkSummaryResponse(detail ports.NetworkDetail) network.Summary {
	driver := detail.Driver
	if detail.Mode == "none" {
		driver = "null"
	}
	ipam := network.IPAM{Driver: "default", Config: []network.IPAMConfig{}}
	if detail.Subnet != "" {
		config := network.IPAMConfig{}
		if prefix, err := netip.ParsePrefix(detail.Subnet); err == nil {
			config.Subnet = prefix
		}
		if detail.Gateway != "" {
			if addr, err := netip.ParseAddr(detail.Gateway); err == nil {
				config.Gateway = addr
			}
		}
		ipam.Config = append(ipam.Config, config)
	}
	return network.Summary{
		Name:       detail.Name,
		ID:         string(detail.ID),
		Created:    time.Time{},
		Scope:      "local",
		Driver:     driver,
		EnableIPv4: true,
		EnableIPv6: false,
		IPAM:       ipam,
		Options:    map[string]string{},
		Labels:     detail.Labels}
}
