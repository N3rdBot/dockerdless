package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

func TestRequestedMounts_translatesStructuredBindMount(t *testing.T) {
	// Given
	hostConfig := &container.HostConfig{Mounts: []mount.Mount{{
		Type:     mount.TypeBind,
		Source:   "/tmp",
		Target:   "/mnt",
		ReadOnly: true,
	}}}

	// When
	got := requestedMounts(hostConfig)

	// Then
	if len(got) != 1 {
		t.Fatalf("expected one mount, got %#v", got)
	}
	if got[0].Type != "bind" || got[0].Source != "/tmp" || got[0].Destination != "/mnt" || !got[0].ReadOnly {
		t.Fatalf("unexpected mount: %#v", got[0])
	}
}

func TestContainerInspectResponse_reportsStructuredBindMount(t *testing.T) {
	// Given
	mounts, err := json.Marshal([]ports.Mount{{
		Type:        "bind",
		Source:      "/tmp",
		Destination: "/mnt",
		ReadOnly:    true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	item := domain.Container{Labels: map[string]string{"io.dockerdless.mounts": string(mounts)}}

	// When
	response := containerInspectResponse(item)

	// Then
	if len(response.Mounts) != 1 {
		t.Fatalf("expected one inspect mount, got %#v", response.Mounts)
	}
	got := response.Mounts[0]
	if got.Type != mount.TypeBind || got.Source != "/tmp" || got.Destination != "/mnt" || got.RW {
		t.Fatalf("unexpected inspect mount: %#v", got)
	}
}

func TestValidateContainerCreate_rejectsPidModeHost(t *testing.T) {
	// Given
	payload := &container.CreateRequest{HostConfig: &container.HostConfig{PidMode: "host"}}

	// When
	got := validateContainerCreate(payload)

	// Then
	if got == nil || got.Status != http.StatusNotImplemented || got.Message != "HostConfig.PidMode is not supported" {
		t.Fatalf("unexpected validation error: %#v", got)
	}
}

func TestValidateContainerCreate_rejectsUnsupportedHostConfigFields(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*container.HostConfig)
		field string
	}{
		{name: "IpcMode", setup: func(hostConfig *container.HostConfig) { hostConfig.IpcMode = "host" }, field: "IpcMode"},
		{name: "UTSMode", setup: func(hostConfig *container.HostConfig) { hostConfig.UTSMode = "host" }, field: "UTSMode"},
		{name: "UsernsMode", setup: func(hostConfig *container.HostConfig) { hostConfig.UsernsMode = "host" }, field: "UsernsMode"},
		{name: "CgroupnsMode", setup: func(hostConfig *container.HostConfig) { hostConfig.CgroupnsMode = "host" }, field: "CgroupnsMode"},
		{name: "Sysctls", setup: func(hostConfig *container.HostConfig) {
			hostConfig.Sysctls = map[string]string{"net.ipv4.ip_forward": "1"}
		}, field: "Sysctls"},
		{name: "MaskedPaths", setup: func(hostConfig *container.HostConfig) { hostConfig.MaskedPaths = []string{"/proc/kcore"} }, field: "MaskedPaths"},
		{name: "ReadonlyPaths", setup: func(hostConfig *container.HostConfig) { hostConfig.ReadonlyPaths = []string{"/proc/asound"} }, field: "ReadonlyPaths"},
		{name: "Runtime", setup: func(hostConfig *container.HostConfig) { hostConfig.Runtime = "runsc" }, field: "Runtime"},
		{name: "VolumesFrom", setup: func(hostConfig *container.HostConfig) { hostConfig.VolumesFrom = []string{"other"} }, field: "VolumesFrom"},
		{name: "OomScoreAdj", setup: func(hostConfig *container.HostConfig) { hostConfig.OomScoreAdj = 1 }, field: "OomScoreAdj"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			hostConfig := &container.HostConfig{}
			testCase.setup(hostConfig)

			// When
			got := validateContainerCreate(&container.CreateRequest{HostConfig: hostConfig})

			// Then
			if got == nil || got.Status != http.StatusNotImplemented || got.Message != "HostConfig."+testCase.field+" is not supported" {
				t.Fatalf("unexpected validation error: %#v", got)
			}
		})
	}
}

func TestValidateContainerCreate_acceptsDaemonRuntime(t *testing.T) {
	// Given
	payload := &container.CreateRequest{HostConfig: &container.HostConfig{Runtime: defaultContainerRuntime}}

	// When
	got := validateContainerCreate(payload)

	// Then
	if got != nil {
		t.Fatalf("unexpected validation error: %#v", got)
	}
}
