package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
	"github.com/N3rdBot/dockerdless/internal/streams"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/storage"
	"go.uber.org/zap"
)

func (h *handlers) containerCreate(w http.ResponseWriter, r *http.Request) {
	var payload container.CreateRequest
	if !decodeJSONBody(w, r, h.bodyLimit(), &payload) {
		return
	}
	if payload.Config == nil {
		WriteDockerError(w, NewInvalidParameter("Config is required"))
		return
	}
	if apiErr := validateContainerCreate(&payload); apiErr != nil {
		WriteDockerError(w, apiErr)
		return
	}
	portBindings, apiErr := requestedPortBindings(payload.HostConfig)
	if apiErr != nil {
		WriteDockerError(w, apiErr)
		return
	}
	result, err := h.service.ContainerCreate(r.Context(), ports.ContainerCreateRequest{
		Name:         r.URL.Query().Get("name"),
		Image:        payload.Image,
		Command:      payload.Cmd,
		Entrypoint:   payload.Entrypoint,
		Env:          envMap(payload.Env),
		WorkingDir:   payload.WorkingDir,
		User:         payload.User,
		Hostname:     payload.Hostname,
		Labels:       payload.Labels,
		TTY:          payload.Tty,
		OpenStdin:    payload.OpenStdin,
		NetworkMode:  requestedNetworkMode(&payload),
		PortBindings: portBindings,
		Mounts:       requestedMounts(payload.HostConfig),
	})
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, container.CreateResponse{
		ID:       string(result.ID),
		Warnings: result.Warnings,
	})
}

func validateContainerCreate(payload *container.CreateRequest) *DockerError {
	if payload == nil || payload.HostConfig == nil {
		return nil
	}
	hostConfig := payload.HostConfig
	switch {
	case hostConfig.Privileged:
		return NewNotImplemented("HostConfig.Privileged is not supported")
	case len(hostConfig.CapAdd) > 0:
		return NewNotImplemented("HostConfig.CapAdd is not supported")
	case len(hostConfig.CapDrop) > 0:
		return NewNotImplemented("HostConfig.CapDrop is not supported")
	case len(hostConfig.Devices) > 0:
		return NewNotImplemented("HostConfig.Devices is not supported")
	case hostConfig.ReadonlyRootfs:
		return NewNotImplemented("HostConfig.ReadonlyRootfs is not supported")
	case !reflect.DeepEqual(hostConfig.Resources, container.Resources{}):
		return NewNotImplemented("HostConfig.Resources is not supported")
	case hostConfig.RestartPolicy.Name != "" || hostConfig.RestartPolicy.MaximumRetryCount != 0:
		return NewNotImplemented("HostConfig.RestartPolicy is not supported")
	case len(hostConfig.SecurityOpt) > 0:
		return NewNotImplemented("HostConfig.SecurityOpt is not supported")
	}
	return nil
}

func (h *handlers) containerList(w http.ResponseWriter, r *http.Request) {
	all := boolQuery(r, "all", false)
	containers, err := h.service.ContainerList(r.Context(), all)
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	summaries := make([]container.Summary, 0, len(containers))
	for _, item := range containers {
		if !matchesContainerFilters(r, item) {
			continue
		}
		summaries = append(summaries, containerSummaryResponse(item))
	}
	writeJSON(w, http.StatusOK, summaries)
}

func (h *handlers) containerInspect(w http.ResponseWriter, r *http.Request) {
	id := pathParameter(r, "/containers/", "/json")
	item, err := h.service.ContainerInspect(r.Context(), id)
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, containerInspectResponse(item))
}

func (h *handlers) containerStart(w http.ResponseWriter, r *http.Request) {
	id := pathParameter(r, "/containers/", "/start")
	if err := h.service.ContainerStart(r.Context(), id); err != nil {
		WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) containerStop(w http.ResponseWriter, r *http.Request) {
	id := pathParameter(r, "/containers/", "/stop")
	if err := h.service.ContainerStop(r.Context(), id, timeoutQuery(r)); err != nil {
		WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) containerRemove(w http.ResponseWriter, r *http.Request) {
	id := pathParameter(r, "/containers/", "")
	force := boolQuery(r, "force", false)
	if err := h.service.ContainerRemove(r.Context(), id, force); err != nil {
		WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) containerLogs(w http.ResponseWriter, r *http.Request) {
	id := pathParameter(r, "/containers/", "/logs")
	item, err := h.service.ContainerInspect(r.Context(), id)
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	options := ports.LogRequest{
		Follow:     boolQuery(r, "follow", false),
		Tail:       tailQuery(r),
		Since:      timeQuery(r, "since"),
		Until:      timeQuery(r, "until"),
		Timestamps: boolQuery(r, "timestamps", false),
		Stdout:     boolQuery(r, "stdout", true),
		Stderr:     boolQuery(r, "stderr", true),
	}
	if options.Follow {
		if err := h.limiter.acquire(r.Context()); err != nil {
			WriteServiceError(w, err)
			return
		}
		defer h.limiter.release()
	}

	tty := item.Labels[ports.LabelTTY] == "true"
	contentType := streams.MediaTypeMultiplexedStream
	if tty {
		contentType = streams.MediaTypeRawStream
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)

	flushing := newFlushWriter(w)
	var stdout, stderr io.Writer = flushing, flushing
	if !tty {
		stdout = streams.NewWriter(flushing, streams.Stdout)
		stderr = streams.NewWriter(flushing, streams.Stderr)
	}
	if err := h.service.ContainerLogs(r.Context(), id, options, stdout, stderr); err != nil {
		h.logger.Debug("container log stream ended with error",
			zap.Error(err),
			zap.String("container_id", string(item.ID)))
	}
}

func requestedNetworkMode(payload *container.CreateRequest) string {
	if payload.HostConfig != nil && strings.TrimSpace(string(payload.HostConfig.NetworkMode)) != "" {
		return string(payload.HostConfig.NetworkMode)
	}
	if payload.NetworkingConfig != nil {
		names := make([]string, 0, len(payload.NetworkingConfig.EndpointsConfig))
		for name := range payload.NetworkingConfig.EndpointsConfig {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) > 0 {
			return names[0]
		}
	}
	return ""
}

func requestedPortBindings(hostConfig *container.HostConfig) ([]domain.PortBinding, *DockerError) {
	if hostConfig == nil || len(hostConfig.PortBindings) == 0 {
		return nil, nil
	}
	bindings := make([]domain.PortBinding, 0, len(hostConfig.PortBindings))
	for port, list := range hostConfig.PortBindings {
		for _, binding := range list {
			hostPort := 0
			if binding.HostPort != "" {
				parsed, err := strconv.Atoi(binding.HostPort)
				if err != nil || parsed < 0 || parsed > 65535 {
					return nil, NewInvalidParameter("HostConfig.PortBindings contains an invalid HostPort")
				}
				hostPort = parsed
			}
			hostIP := ""
			if binding.HostIP.IsValid() {
				hostIP = binding.HostIP.String()
			}
			bindings = append(bindings, domain.PortBinding{
				ContainerPort: port.Num(),
				Protocol:      strings.ToLower(string(port.Proto())),
				HostIP:        hostIP,
				HostPort:      uint16(hostPort),
			})
		}
	}
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].ContainerPort != bindings[j].ContainerPort {
			return bindings[i].ContainerPort < bindings[j].ContainerPort
		}
		return bindings[i].HostPort < bindings[j].HostPort
	})
	return bindings, nil
}

func requestedMounts(hostConfig *container.HostConfig) []ports.Mount {
	if hostConfig == nil || len(hostConfig.Binds) == 0 {
		return nil
	}
	mounts := make([]ports.Mount, 0, len(hostConfig.Binds))
	for _, bind := range hostConfig.Binds {
		parts := strings.Split(bind, ":")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		mount := ports.Mount{Type: "bind", Source: parts[0], Destination: parts[1]}
		if len(parts) >= 3 {
			for option := range strings.SplitSeq(parts[2], ",") {
				switch strings.ToLower(strings.TrimSpace(option)) {
				case "ro":
					mount.ReadOnly = true
				case "":
				default:
					mount.Options = append(mount.Options, option)
				}
			}
		}
		mounts = append(mounts, mount)
	}
	return mounts
}

func containerInspectResponse(item domain.Container) container.InspectResponse {
	portMap := portMapFromBindings(item.PortBindings)
	tty := item.Labels[ports.LabelTTY] == "true"
	execIDs := make([]string, 0, len(item.Execs))
	for _, record := range item.Execs {
		execIDs = append(execIDs, record.ID)
	}
	return container.InspectResponse{
		ID:      string(item.ID),
		Created: formatTimestamp(item.CreatedAt),
		Args:    append([]string(nil), item.Spec.Command...),
		State: &container.State{
			Status:     container.ContainerState(item.State),
			Running:    item.State == domain.ContainerStateRunning,
			Paused:     item.State == domain.ContainerStatePaused,
			Restarting: item.State == domain.ContainerStateRestarting,
			OOMKilled:  item.OOMKilled,
			Dead:       item.Dead,
			ExitCode:   item.ExitCode,
			Error:      item.Error,
			StartedAt:  formatTimestamp(item.StartedAt),
			FinishedAt: formatTimestamp(item.FinishedAt),
		},
		Image:    imageIdentity(item),
		Name:     "/" + item.Name,
		Driver:   "containerd",
		Platform: "linux",
		ExecIDs:  execIDs,
		HostConfig: &container.HostConfig{
			PortBindings: portMap,
			NetworkMode:  container.NetworkMode(containerNetworkMode(item)),
		},
		GraphDriver: &storage.DriverData{Name: "overlayfs"},
		Config: &container.Config{
			Hostname:   shortIdentity(item.ID),
			User:       "",
			Tty:        tty,
			Image:      string(item.ImageReference),
			Labels:     publicLabels(item.Labels),
			Cmd:        append([]string(nil), item.Spec.Command...),
			Env:        envSlice(item.Spec.Env),
			WorkingDir: "",
		},
		Mounts: []container.MountPoint{},
		NetworkSettings: &container.NetworkSettings{
			Ports:    portMap,
			Networks: endpointSettingsMap(item.Networks),
		},
	}
}

func containerSummaryResponse(item domain.Container) container.Summary {
	portMap := portMapFromBindings(item.PortBindings)
	summary := container.Summary{
		ID:              string(item.ID),
		Names:           []string{"/" + item.Name},
		Image:           string(item.ImageReference),
		ImageID:         string(item.ImageDigest),
		Command:         strings.Join(item.Spec.Command, " "),
		Created:         item.CreatedAt.Unix(),
		Ports:           portSummaries(portMap),
		Labels:          publicLabels(item.Labels),
		State:           container.ContainerState(item.State),
		Status:          containerStatusText(item, time.Now()),
		NetworkSettings: &container.NetworkSettingsSummary{Networks: endpointSettingsMap(item.Networks)},
	}
	summary.HostConfig.NetworkMode = containerNetworkMode(item)
	return summary
}

func matchesContainerFilters(r *http.Request, item domain.Container) bool {
	raw := strings.TrimSpace(r.URL.Query().Get("filters"))
	if raw == "" {
		return true
	}
	filters := decodeContainerFilters(raw)
	for _, wanted := range filters["label"] {
		key, value, hasValue := strings.Cut(wanted, "=")
		got, ok := item.Labels[key]
		if !ok {
			return false
		}
		if hasValue && got != value {
			return false
		}
	}
	for _, name := range filters["name"] {
		if !matchesNameFilter(name, item) {
			return false
		}
	}
	for _, status := range filters["status"] {
		if string(item.State) != status {
			return false
		}
	}
	for _, id := range filters["id"] {
		if !strings.HasPrefix(string(item.ID), id) {
			return false
		}
	}
	return true
}

// matchesNameFilter applies Docker's regexp name-filter semantics while still
// accepting unanchored plain substrings.
func matchesNameFilter(pattern string, item domain.Container) bool {
	targets := []string{item.Name, "/" + item.Name, string(item.ID)}
	if compiled, err := regexp.Compile(pattern); err == nil {
		return slices.ContainsFunc(targets, compiled.MatchString)
	}
	for _, target := range targets {
		if strings.Contains(target, pattern) {
			return true
		}
	}
	return false
}

func decodeContainerFilters(raw string) map[string][]string {
	var filters map[string][]string
	if err := json.Unmarshal([]byte(raw), &filters); err == nil {
		return filters
	}
	var legacy map[string]map[string]bool
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return nil
	}
	filters = make(map[string][]string, len(legacy))
	for key, values := range legacy {
		for value, enabled := range values {
			if enabled {
				filters[key] = append(filters[key], value)
			}
		}
	}
	return filters
}

func portMapFromBindings(bindings []domain.PortBinding) network.PortMap {
	if len(bindings) == 0 {
		return nil
	}
	portMap := make(network.PortMap, len(bindings))
	for _, binding := range bindings {
		if binding.HostPort == 0 || binding.ContainerPort == 0 {
			continue
		}
		port, ok := network.PortFrom(binding.ContainerPort, network.IPProtocol(normalizeBindingProtocol(binding.Protocol)))
		if !ok {
			continue
		}
		entry := network.PortBinding{HostPort: strconv.Itoa(int(binding.HostPort))}
		if binding.HostIP != "" {
			if addr, err := netip.ParseAddr(binding.HostIP); err == nil {
				entry.HostIP = addr
			}
		}
		portMap[port] = append(portMap[port], entry)
	}
	if len(portMap) == 0 {
		return nil
	}
	return portMap
}

func portSummaries(portMap network.PortMap) []container.PortSummary {
	if len(portMap) == 0 {
		return []container.PortSummary{}
	}
	summaries := make([]container.PortSummary, 0, len(portMap))
	for port, bindings := range portMap {
		summary := container.PortSummary{
			PrivatePort: port.Num(),
			Type:        string(port.Proto()),
		}
		if len(bindings) > 0 {
			summary.IP = bindings[0].HostIP
			if parsed, err := strconv.Atoi(bindings[0].HostPort); err == nil {
				summary.PublicPort = uint16(parsed)
			}
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].PrivatePort < summaries[j].PrivatePort })
	return summaries
}

func endpointSettingsMap(attachments []domain.NetworkAttachment) map[string]*network.EndpointSettings {
	if len(attachments) == 0 {
		return nil
	}
	settings := make(map[string]*network.EndpointSettings, len(attachments))
	for _, attachment := range attachments {
		if attachment.Name == "" {
			continue
		}
		endpoint := &network.EndpointSettings{
			NetworkID:           string(attachment.NetworkID),
			Aliases:             append([]string(nil), attachment.Aliases...),
			IPPrefixLen:         attachment.IPPrefixLen,
			GlobalIPv6PrefixLen: attachment.GlobalIPv6PrefixLen,
		}
		setAddr(&endpoint.IPAddress, attachment.IPAddress)
		setAddr(&endpoint.Gateway, attachment.Gateway)
		setAddr(&endpoint.GlobalIPv6Address, attachment.GlobalIPv6Address)
		if attachment.MACAddress != "" {
			if hardware, err := net.ParseMAC(attachment.MACAddress); err == nil {
				endpoint.MacAddress = network.HardwareAddr(hardware)
			}
		}
		settings[attachment.Name] = endpoint
	}
	if len(settings) == 0 {
		return nil
	}
	return settings
}

func setAddr(target *netip.Addr, value string) {
	if value == "" {
		return
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		*target = addr
	}
}

func normalizeBindingProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "", "tcp":
		return "tcp"
	case "udp":
		return "udp"
	default:
		return strings.ToLower(strings.TrimSpace(protocol))
	}
}

func containerNetworkMode(item domain.Container) string {
	if len(item.Networks) == 0 {
		return "default"
	}
	name := item.Networks[0].Name
	if name == "" {
		return "default"
	}
	return name
}

func imageIdentity(item domain.Container) string {
	if item.ImageDigest != "" {
		return string(item.ImageDigest)
	}
	return string(item.ImageReference)
}

func shortIdentity(id domain.ContainerID) string {
	value := string(id)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func publicLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return map[string]string{}
	}
	public := make(map[string]string, len(labels))
	for key, value := range labels {
		if strings.HasPrefix(key, "io.dockerdless.") {
			continue
		}
		public[key] = value
	}
	return public
}

func formatTimestamp(value time.Time) string {
	return value.Format(time.RFC3339Nano)
}

func containerStatusText(item domain.Container, now time.Time) string {
	switch item.State {
	case domain.ContainerStateRunning:
		return "Up " + humanDuration(now.Sub(item.StartedAt))
	case domain.ContainerStateExited:
		return fmt.Sprintf("Exited (%d) %s ago", item.ExitCode, humanDuration(now.Sub(item.FinishedAt)))
	case domain.ContainerStateCreated:
		return "Created"
	case domain.ContainerStatePaused:
		return "Up " + humanDuration(now.Sub(item.StartedAt)) + " (Paused)"
	default:
		return string(item.State)
	}
}

func humanDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	switch {
	case duration < time.Minute:
		return fmt.Sprintf("%d seconds", int(duration.Seconds()))
	case duration < time.Hour:
		return fmt.Sprintf("%d minutes", int(duration.Minutes()))
	case duration < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(duration.Hours()))
	default:
		return fmt.Sprintf("%d days", int(duration.Hours()/24))
	}
}

// flushWriter flushes the HTTP response after every write so long-lived
// streams reach the client promptly. ResponseController unwraps the
// observability status recorder.
type flushWriter struct {
	writer  http.ResponseWriter
	control *http.ResponseController
}

func newFlushWriter(w http.ResponseWriter) *flushWriter {
	return &flushWriter{writer: w, control: http.NewResponseController(w)}
}

func (f *flushWriter) Write(p []byte) (int, error) {
	written, err := f.writer.Write(p)
	_ = f.control.Flush()
	return written, err
}
