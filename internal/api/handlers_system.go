package api

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/system"
)

const (
	daemonVersion = "0.0.0-dev"
	daemonName    = "dockerdless"
)

func pingHandler(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	header.Set("Pragma", "no-cache")
	header.Set("Docker-Experimental", "false")
	header.Set("Swarm", "inactive")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, system.VersionResponse{
		Version:       daemonVersion,
		APIVersion:    AdvertisedAPIVersion,
		MinAPIVersion: MinimumAPIVersion,
		Os:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		GoVersion:     runtime.Version(),
		KernelVersion: kernelVersion(),
		Platform:      system.PlatformInfo{Name: daemonName},
		Components: []system.ComponentVersion{{
			Name:    "Engine",
			Version: daemonVersion,
			Details: map[string]string{
				"ApiVersion":    AdvertisedAPIVersion,
				"Arch":          runtime.GOARCH,
				"GoVersion":     runtime.Version(),
				"KernelVersion": kernelVersion(),
				"MinAPIVersion": MinimumAPIVersion,
				"Os":            runtime.GOOS,
			},
		}},
	})
}

func (h *handlers) info(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.Info(r.Context())
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, system.Info{
		Containers:         status.Containers,
		ContainersRunning:  status.ContainersRunning,
		ContainersPaused:   status.ContainersPaused,
		ContainersStopped:  status.ContainersStopped,
		Images:             status.Images,
		Driver:             "containerd",
		OSType:             runtime.GOOS,
		Architecture:       runtime.GOARCH,
		OperatingSystem:    daemonName,
		ServerVersion:      daemonVersion,
		KernelVersion:      kernelVersion(),
		NCPU:               runtime.NumCPU(),
		MemTotal:           memTotal(),
		LoggingDriver:      "cri",
		IndexServerAddress: "https://index.docker.io/v1/",
		Runtimes:           map[string]system.RuntimeWithStatus{"io.containerd.runc.v2": {}},
		DefaultRuntime:     "io.containerd.runc.v2",
		Containerd: &system.ContainerdInfo{
			Address:    status.ContainerdSocket,
			Namespaces: system.ContainerdNamespaces{Containers: status.Namespace},
		},
		Warnings: status.Warnings,
	})
}

func kernelVersion() string {
	raw, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func memTotal() int64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(raw)) {
		value, ok := strings.CutPrefix(line, "MemTotal:")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return 0
		}
		kilobytes, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return kilobytes * 1024
	}
	return 0
}
