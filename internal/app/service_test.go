package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/adapters/buildkit"
	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

func staticProbe() cni.PortProbe {
	var next atomic.Uint32
	next.Store(40000)
	return func(_, _ string, requested uint16) (uint16, error) {
		if requested != 0 {
			return requested, nil
		}
		//nolint:gosec // G115: the static test allocator starts at 40000.
		return uint16(next.Add(1)), nil
	}
}

func testService(t *testing.T, runtime *fakeRuntime, images *fakeImages, networks *fakeNetworks, tasks *fakeTasks) *Service {
	t.Helper()
	service, err := New(Config{
		Runtime:        runtime,
		Images:         images,
		Networks:       networks,
		Registry:       domain.NewRegistry(),
		Tasks:          tasks,
		Allocator:      NewPortAllocator(cni.NewPortAllocator(cni.WithPortProbe(staticProbe()))),
		StopTimeout:    time.Second,
		RequestTimeout: 5 * time.Second,
		LogDir:         t.TempDir(),
		Namespace:      "default",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(service.CloseLogSinks)
	return service
}

func seedContainer(t *testing.T, service *Service, id domain.ContainerID, name string, state domain.ContainerState, bindings []domain.PortBinding, attachments []domain.NetworkAttachment) domain.Container {
	t.Helper()
	container := domain.Container{
		ID:             id,
		Name:           name,
		Spec:           domain.ContainerSpec{Image: "alpine:latest"},
		ImageReference: "alpine:latest",
		ImageDigest:    "sha256:deadbeef",
		State:          state,
		CreatedAt:      time.Now().Add(-time.Minute),
		PortBindings:   bindings,
		Networks:       attachments,
	}
	if err := service.registry.Save(context.Background(), container); err != nil {
		t.Fatalf("seed container: %v", err)
	}
	return container
}

func dockerMessage(err error) string {
	var messenger interface{ DockerMessage() string }
	if errors.As(err, &messenger) {
		return messenger.DockerMessage()
	}
	return err.Error()
}

func TestContainerCreate_unknownImageReturnsDocker404(t *testing.T) {
	runtime := newFakeRuntime()
	service := testService(t, runtime, &fakeImages{inspectErr: buildkit.ErrNotFound}, newFakeNetworks(), &fakeTasks{pid: 1})

	_, err := service.ContainerCreate(context.Background(), ports.ContainerCreateRequest{Image: "missing:latest"})

	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if got := dockerMessage(err); got != "No such image: missing:latest" {
		t.Fatalf("expected Docker image message, got %q", got)
	}
	if len(runtime.createSpecs) != 0 {
		t.Fatalf("expected no container creation on unknown image, got %d", len(runtime.createSpecs))
	}
}

func TestContainerList_zeroStartedAtUsesCreatedAtForStatus(t *testing.T) {
	runtime := newFakeRuntime()
	service := testService(t, runtime, &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})
	createdAt := time.Now().Add(-time.Minute)
	container := domain.Container{
		ID:        "running-zero-start",
		Name:      "web",
		Spec:      domain.ContainerSpec{Image: "alpine:latest"},
		State:     domain.ContainerStateRunning,
		CreatedAt: createdAt,
	}
	if err := service.registry.Save(context.Background(), container); err != nil {
		t.Fatalf("seed container: %v", err)
	}
	runtime.states[container.ID] = domain.ContainerStateRunning

	listed, err := service.ContainerList(context.Background(), true)
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected one container, got %d", len(listed))
	}
	if !listed[0].StartedAt.Equal(createdAt) {
		t.Fatalf("expected zero StartedAt to fall back to CreatedAt %v, got %v", createdAt, listed[0].StartedAt)
	}
	if err := service.registry.Save(context.Background(), container); err != nil {
		t.Fatalf("reset container: %v", err)
	}
	inspected, err := service.ContainerInspect(context.Background(), string(container.ID))
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if !inspected.StartedAt.Equal(createdAt) {
		t.Fatalf("expected inspect zero StartedAt to fall back to CreatedAt %v, got %v", createdAt, inspected.StartedAt)
	}
}

func TestContainerCreate_allocatesHostPortsAndPersistsIdentity(t *testing.T) {
	runtime := newFakeRuntime()
	images := &fakeImages{inspectDetail: ports.ImageDetail{
		ID:       "sha256:cafebabe",
		RepoTags: []string{"alpine:latest"},
	}}
	networks := newFakeNetworks()
	service := testService(t, runtime, images, networks, &fakeTasks{pid: 1})
	ctx := context.Background()

	result, err := service.ContainerCreate(ctx, ports.ContainerCreateRequest{
		Name:         "web",
		Image:        "alpine:latest",
		Command:      []string{"/bin/sh", "-c", "echo hello"},
		PortBindings: []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp"}},
	})
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	if result.ID != runtime.createID {
		t.Fatalf("expected created id %q, got %q", runtime.createID, result.ID)
	}
	if networks.ensureCalls != 1 {
		t.Fatalf("expected default network to be ensured once, got %d", networks.ensureCalls)
	}
	if len(runtime.createSpecs) != 1 || runtime.createSpecs[0].Name != "web" {
		t.Fatalf("expected runtime create with name web, got %+v", runtime.createSpecs)
	}
	if got := runtime.createSpecs[0].Labels[containerImageDigestLabel]; got != "sha256:cafebabe" {
		t.Fatalf("expected digest label, got %q", got)
	}
	if got := runtime.createSpecs[0].Labels[containerCommandLabel]; got != `["/bin/sh","-c","echo hello"]` {
		t.Fatalf("expected command label, got %q", got)
	}
	container, err := service.registry.Get(ctx, result.ID)
	if err != nil {
		t.Fatalf("registry get: %v", err)
	}
	if container.ImageDigest != "sha256:cafebabe" {
		t.Fatalf("expected pinned digest, got %q", container.ImageDigest)
	}
	if container.State != domain.ContainerStateCreated {
		t.Fatalf("expected created state, got %q", container.State)
	}
	if len(container.PortBindings) != 1 || container.PortBindings[0].HostPort == 0 {
		t.Fatalf("expected concrete nonzero ports, got %+v", container.PortBindings)
	}
	if len(container.Networks) != 1 || container.Networks[0].Name != "bridge" {
		t.Fatalf("expected bridge network attachment, got %+v", container.Networks)
	}
}

func TestContainerCreate_usesCanonicalRuntimeImageReference(t *testing.T) {
	runtime := newFakeRuntime()
	images := &fakeImages{inspectDetail: ports.ImageDetail{
		ID:          "sha256:x",
		RepoTags:    []string{"alpine:latest"},
		RepoDigests: []string{"alpine@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
	}}
	service := testService(t, runtime, images, newFakeNetworks(), &fakeTasks{pid: 1})

	if _, err := service.ContainerCreate(context.Background(), ports.ContainerCreateRequest{Name: "web", Image: "alpine:latest"}); err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}

	if len(runtime.createSpecs) != 1 {
		t.Fatalf("expected one create, got %d", len(runtime.createSpecs))
	}
	if got := string(runtime.createSpecs[0].Image); got != "docker.io/library/alpine:latest" {
		t.Fatalf("expected canonical runtime reference, got %q", got)
	}
}

func TestContainerCreate_duplicateNameConflicts(t *testing.T) {
	runtime := newFakeRuntime()
	service := testService(t, runtime, &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:x"}}, newFakeNetworks(), &fakeTasks{pid: 1})
	seedContainer(t, service, "existing-id", "web", domain.ContainerStateExited, nil, nil)

	_, err := service.ContainerCreate(context.Background(), ports.ContainerCreateRequest{Name: "web", Image: "alpine:latest"})

	if !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if !strings.Contains(dockerMessage(err), "already in use") {
		t.Fatalf("expected Docker name conflict message, got %q", dockerMessage(err))
	}
}

func TestContainerCreate_publishedPortsWithHostModeRejected(t *testing.T) {
	service := testService(t, newFakeRuntime(), &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:x"}}, newFakeNetworks(), &fakeTasks{pid: 1})

	_, err := service.ContainerCreate(context.Background(), ports.ContainerCreateRequest{
		Name:         "web",
		Image:        "alpine:latest",
		NetworkMode:  "host",
		PortBindings: []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 1234}},
	})

	if !errors.Is(err, ports.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestContainerStart_joinsNetworkWithNetnsAndConcretePorts(t *testing.T) {
	runtime := newFakeRuntime()
	networks := newFakeNetworks()
	service := testService(t, runtime, &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:x"}}, networks, &fakeTasks{pid: 4242})
	ctx := context.Background()
	seedContainer(t, service, "container-1", "web", domain.ContainerStateCreated,
		[]domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 40001}},
		[]domain.NetworkAttachment{{NetworkID: "network-bridge", Name: "bridge"}})
	networks.connectResult = ports.NetworkAttachmentResult{
		Attachment: domain.NetworkAttachment{NetworkID: "network-bridge", Name: "bridge", IPAddress: "10.88.0.7"},
		Ports:      []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 40001}},
	}

	if err := service.ContainerStart(ctx, "web"); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}

	if len(networks.connectRequests) != 1 {
		t.Fatalf("expected one CNI connect, got %d", len(networks.connectRequests))
	}
	connect := networks.connectRequests[0]
	if connect.NetNS != "/proc/4242/ns/net" {
		t.Fatalf("expected netns from task pid, got %q", connect.NetNS)
	}
	if len(connect.Ports) != 1 || connect.Ports[0].HostPort == 0 {
		t.Fatalf("expected concrete published ports, got %+v", connect.Ports)
	}
	container, err := service.registry.Get(ctx, "container-1")
	if err != nil {
		t.Fatalf("registry get: %v", err)
	}
	if container.State != domain.ContainerStateRunning {
		t.Fatalf("expected running state, got %q", container.State)
	}
	if len(container.PortBindings) != 1 || container.PortBindings[0].HostPort != 40001 {
		t.Fatalf("expected inspect-visible allocated ports, got %+v", container.PortBindings)
	}
	if len(container.Networks) != 1 || container.Networks[0].IPAddress != "10.88.0.7" {
		t.Fatalf("expected attached endpoint, got %+v", container.Networks)
	}
	if runtime.startIOCall != 1 {
		t.Fatalf("expected task start with captured IO, got %d calls", runtime.startIOCall)
	}
}

func TestContainerStart_networkFailureStopsContainer(t *testing.T) {
	runtime := newFakeRuntime()
	networks := newFakeNetworks()
	networks.connectErr = errors.New("cni plugin failed")
	service := testService(t, runtime, &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:x"}}, networks, &fakeTasks{pid: 7})
	seedContainer(t, service, "container-1", "web", domain.ContainerStateCreated, nil,
		[]domain.NetworkAttachment{{NetworkID: "network-bridge", Name: "bridge"}})

	if err := service.ContainerStart(context.Background(), "web"); err == nil {
		t.Fatal("expected start to fail when CNI connect fails")
	}
	if len(runtime.stopCalls) != 1 {
		t.Fatalf("expected container cleanup stop, got %+v", runtime.stopCalls)
	}
}

func TestContainerStart_toleratesFastExitingContainerWithoutNetns(t *testing.T) {
	runtime := newFakeRuntime()
	runtime.keepState = true
	networks := newFakeNetworks()
	networks.connectErr = errors.New(`failed to open netns "/proc/1/ns/net": no such file or directory`)
	service := testService(t, runtime, &fakeImages{}, networks, &fakeTasks{pid: 1})
	seedContainer(t, service, "container-1", "web", domain.ContainerStateCreated, nil,
		[]domain.NetworkAttachment{{NetworkID: "network-bridge", Name: "bridge"}})
	runtime.setState("container-1", domain.ContainerStateExited)
	runtime.waitResult = ports.ProcessExit{ExitCode: 0, ExitedAt: time.Now()}

	if err := service.ContainerStart(context.Background(), "web"); err != nil {
		t.Fatalf("expected fast-exiting container to start successfully, got %v", err)
	}

	container, err := service.registry.Get(context.Background(), "container-1")
	if err != nil {
		t.Fatalf("registry get: %v", err)
	}
	if container.State != domain.ContainerStateExited {
		t.Fatalf("expected exited state, got %q", container.State)
	}
	if len(runtime.stopCalls) != 0 {
		t.Fatalf("expected no cleanup stop for a finished container, got %+v", runtime.stopCalls)
	}
}

func TestContainerStop_recordsExitCodeAndState(t *testing.T) {
	runtime := newFakeRuntime()
	service := testService(t, runtime, &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})
	seedContainer(t, service, "container-1", "web", domain.ContainerStateRunning, nil, nil)
	runtime.setState("container-1", domain.ContainerStateExited)
	runtime.waitResult = ports.ProcessExit{ExitCode: 3, ExitedAt: time.Now()}

	if err := service.ContainerStop(context.Background(), "web", nil); err != nil {
		t.Fatalf("ContainerStop: %v", err)
	}

	container, err := service.registry.Get(context.Background(), "container-1")
	if err != nil {
		t.Fatalf("registry get: %v", err)
	}
	if container.State != domain.ContainerStateExited || container.ExitCode != 3 {
		t.Fatalf("expected exited exit-code 3, got state=%q exit=%d", container.State, container.ExitCode)
	}
	if len(runtime.stopCalls) != 1 {
		t.Fatalf("expected one stop call, got %+v", runtime.stopCalls)
	}
}

func TestContainerRemove_runningRequiresForce(t *testing.T) {
	runtime := newFakeRuntime()
	service := testService(t, runtime, &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})
	seedContainer(t, service, "container-1", "web", domain.ContainerStateRunning, nil, nil)
	runtime.setState("container-1", domain.ContainerStateRunning)
	ctx := context.Background()

	if err := service.ContainerRemove(ctx, "web", false); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("expected conflict without force, got %v", err)
	}
	if err := service.ContainerRemove(ctx, "web", true); err != nil {
		t.Fatalf("forced remove: %v", err)
	}
	if len(runtime.removed) != 1 {
		t.Fatalf("expected runtime removal, got %+v", runtime.removed)
	}
	if _, err := service.registry.Get(ctx, "container-1"); !errors.Is(err, domain.ErrContainerNotFound) {
		t.Fatalf("expected registry removal, got %v", err)
	}
}

func TestContainerLogs_streamsCRILinesWithTail(t *testing.T) {
	service := testService(t, newFakeRuntime(), &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})
	seedContainer(t, service, "container-1", "web", domain.ContainerStateExited, nil, nil)
	logContent := "2026-01-01T00:00:00.000000000Z stdout F first\n" +
		"2026-01-01T00:00:01.000000000Z stdout F second\n"
	if err := os.WriteFile(service.logPath("container-1"), []byte(logContent), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	var stdout bytes.Buffer

	if err := service.ContainerLogs(context.Background(), "web", ports.LogRequest{Tail: 1, Stdout: true, Stderr: true}, &stdout, &stdout); err != nil {
		t.Fatalf("ContainerLogs: %v", err)
	}

	if got := stdout.String(); got != "second\n" {
		t.Fatalf("expected tailed log line, got %q", got)
	}
}

func TestContainerList_closesLogSinkAfterContainerExit(t *testing.T) {
	runtime := newFakeRuntime()
	service := testService(t, runtime, &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})
	container := seedContainer(t, service, "container-1", "web", domain.ContainerStateRunning, nil, nil)
	sink, err := service.openLogSink(container.ID)
	if err != nil {
		t.Fatalf("openLogSink: %v", err)
	}
	if _, err := sink.file.WriteString("log survives sink close\n"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	runtime.setState(container.ID, domain.ContainerStateExited)
	runtime.waitResult = ports.ProcessExit{ExitCode: 0, ExitedAt: time.Now()}

	if _, err := service.ContainerList(context.Background(), true); err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	if _, err := sink.file.WriteString("must fail after close\n"); err == nil {
		t.Fatal("log sink remained writable after container exit")
	}
	if _, err := os.Stat(service.logPath(container.ID)); err != nil {
		t.Fatalf("log file disappeared after sink close: %v", err)
	}
}

func TestContainerRemove_doesNotReleaseCNIOwnedReservationTwice(t *testing.T) {
	runtime := newFakeRuntime()
	networks := newFakeNetworks()
	allocator := cni.NewPortAllocator(cni.WithPortProbe(func(_, _ string, requested uint16) (uint16, error) {
		return requested, nil
	}))
	service := testService(t, runtime, &fakeImages{}, networks, &fakeTasks{pid: 1})
	service.alloc = NewPortAllocator(allocator)
	container := seedContainer(t, service, "container-1", "web", domain.ContainerStateExited,
		[]domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 45000}},
		[]domain.NetworkAttachment{{Name: "bridge"}})
	reserved, err := allocator.Allocate("tcp", "", 45000)
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	liveReservation := make(chan error, 1)
	networks.disconnectAllFunc = func(context.Context, domain.ContainerID) []error {
		allocator.Release(reserved)
		_, allocateErr := allocator.Allocate("tcp", "", 45000)
		liveReservation <- allocateErr
		return nil
	}

	if err := service.ContainerRemove(context.Background(), string(container.ID), false); err != nil {
		t.Fatalf("ContainerRemove: %v", err)
	}
	if err := <-liveReservation; err != nil {
		t.Fatalf("live reservation could not be acquired during disconnect: %v", err)
	}
	if !allocator.IsUsed("tcp", "", 45000) {
		t.Fatal("live reservation was released a second time by app removal")
	}
}

func TestContainerStart_retryAfterReservedCNIAddFailureKeepsReservation(t *testing.T) {
	runtime := newFakeRuntime()
	networks := newFakeNetworks()
	allocator := cni.NewPortAllocator(cni.WithPortProbe(func(_, _ string, requested uint16) (uint16, error) {
		return requested, nil
	}))
	service := testService(t, runtime, &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:image"}}, networks, &fakeTasks{pid: 7})
	service.alloc = NewPortAllocator(allocator)

	created, err := service.ContainerCreate(context.Background(), ports.ContainerCreateRequest{
		Name:         "web",
		Image:        "alpine:latest",
		PortBindings: []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 45000}},
	})
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	networks.connectErr = errors.New("bridge plugin exploded")
	networks.connectReservedRelease = func(request ports.NetworkConnectRequest) {
		allocator.Release(cni.PortAllocation{Protocol: request.Ports[0].Protocol, HostIP: request.Ports[0].HostIP, HostPort: request.Ports[0].HostPort})
	}
	if err := service.ContainerStart(context.Background(), string(created.ID)); err == nil {
		t.Fatal("ContainerStart succeeded with failing CNI ADD")
	}
	if !allocator.IsUsed("tcp", "", 45000) {
		t.Fatal("reservation was lost after failed CNI ADD")
	}

	networks.connectErr = nil
	if err := service.ContainerStart(context.Background(), string(created.ID)); err != nil {
		t.Fatalf("ContainerStart retry: %v", err)
	}
	runtime.setState(created.ID, domain.ContainerStateExited)
	networks.disconnectAllFunc = func(context.Context, domain.ContainerID) []error {
		allocator.Release(cni.PortAllocation{Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 45000})
		return nil
	}
	if err := service.ContainerRemove(context.Background(), string(created.ID), false); err != nil {
		t.Fatalf("ContainerRemove: %v", err)
	}
	if allocator.IsUsed("tcp", "", 45000) {
		t.Fatal("reservation was not released by removal")
	}
}

func TestExecLifecycle_createStartInspect(t *testing.T) {
	runtime := newFakeRuntime()
	service := testService(t, runtime, &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})
	seedContainer(t, service, "container-1", "web", domain.ContainerStateRunning, nil, nil)
	runtime.setState("container-1", domain.ContainerStateRunning)
	ctx := context.Background()

	execID, err := service.ExecCreate(ctx, "web", ports.ExecCreateRequest{Command: []string{"sh", "-c", "exit 7"}, AttachStdout: true})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	exitCode := 7
	runtime.execResult = ports.ExecResult{ID: execID, ExitCode: &exitCode}
	runtime.execRecords[execID] = domain.ExecRecord{ID: execID, ContainerID: "container-1", ExitCode: &exitCode, Command: []string{"sh", "-c", "exit 7"}}

	code, err := service.ExecStart(ctx, execID, ports.ExecStartRequest{})
	if err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	if code != 7 {
		t.Fatalf("expected exit code 7, got %d", code)
	}
	request := runtime.lastExecRequest()
	if request.ID != execID || len(request.Command) == 0 {
		t.Fatalf("expected exec request to carry identity and command, got %+v", request)
	}
	record, err := service.ExecInspect(ctx, execID)
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if record.ExitCode == nil || *record.ExitCode != 7 {
		t.Fatalf("expected recorded exit code 7, got %+v", record)
	}
}

func TestExecCreate_requiresRunningContainer(t *testing.T) {
	service := testService(t, newFakeRuntime(), &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})
	seedContainer(t, service, "container-1", "web", domain.ContainerStateExited, nil, nil)

	_, err := service.ExecCreate(context.Background(), "web", ports.ExecCreateRequest{Command: []string{"true"}})

	if !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestNetworkList_includesPredefinedNetworks(t *testing.T) {
	networks := newFakeNetworks()
	networks.resolveByName = map[string]ports.NetworkDetail{
		"bridge": {ID: "bridge-id", Name: "bridge", Driver: "bridge", Mode: "network"},
		"host":   {ID: "host-id", Name: "host", Driver: "host", Mode: "host"},
		"none":   {ID: "none-id", Name: "none", Driver: "null", Mode: "none"},
	}
	service := testService(t, newFakeRuntime(), &fakeImages{}, networks, &fakeTasks{pid: 1})

	details, err := service.NetworkList(context.Background())
	if err != nil {
		t.Fatalf("NetworkList: %v", err)
	}

	names := make([]string, 0, len(details))
	for _, detail := range details {
		names = append(names, detail.Name)
	}
	if strings.Join(names, ",") != "bridge,host,none" {
		t.Fatalf("expected bridge,host,none, got %v", names)
	}
}

func TestImagePull_streamsStatusAndReportsFailure(t *testing.T) {
	images := &fakeImages{pullErr: buildkit.ErrNotFound}
	service := testService(t, newFakeRuntime(), images, newFakeNetworks(), &fakeTasks{pid: 1})
	var out bytes.Buffer

	err := service.ImagePull(context.Background(), ports.PullRequest{Reference: "alpine:latest"}, &out)

	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one progress line, got %q", out.String())
	}
	var message map[string]string
	if jsonErr := json.Unmarshal([]byte(lines[0]), &message); jsonErr != nil {
		t.Fatalf("decode progress line: %v", jsonErr)
	}
	if message["status"] != "Pulling from alpine:latest" {
		t.Fatalf("expected pulling status, got %q", message["status"])
	}
}

func TestContainerList_refreshesRuntimeState(t *testing.T) {
	runtime := newFakeRuntime()
	service := testService(t, runtime, &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})
	seedContainer(t, service, "container-1", "web", domain.ContainerStateRunning, nil, nil)
	runtime.setState("container-1", domain.ContainerStateExited)
	runtime.waitResult = ports.ProcessExit{ExitCode: 0, ExitedAt: time.Now()}

	containers, err := service.ContainerList(context.Background(), true)
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	if len(containers) != 1 || containers[0].State != domain.ContainerStateExited {
		t.Fatalf("expected refreshed exited state, got %+v", containers)
	}
	running, err := service.ContainerList(context.Background(), false)
	if err != nil {
		t.Fatalf("ContainerList all=false: %v", err)
	}
	if len(running) != 0 {
		t.Fatalf("expected no running containers, got %+v", running)
	}
}

func TestContainerInspect_notFoundMessage(t *testing.T) {
	service := testService(t, newFakeRuntime(), &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})

	_, err := service.ContainerInspect(context.Background(), "nope")

	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if got := dockerMessage(err); got != "No such container: nope" {
		t.Fatalf("expected Docker container message, got %q", got)
	}
}

func TestNetworkInspect_resolvesAndReportsMissing(t *testing.T) {
	networks := newFakeNetworks()
	networks.resolveByName = map[string]ports.NetworkDetail{
		"bridge": {ID: "bridge-id", Name: "bridge", Driver: "bridge", Mode: "network"},
	}
	service := testService(t, newFakeRuntime(), &fakeImages{}, networks, &fakeTasks{pid: 1})

	detail, err := service.NetworkInspect(context.Background(), "bridge")
	if err != nil {
		t.Fatalf("NetworkInspect: %v", err)
	}
	if detail.Name != "bridge" || detail.ID != "bridge-id" {
		t.Fatalf("unexpected network detail %+v", detail)
	}

	networks.resolveErr = cni.ErrNetworkNotFound
	if _, err := service.NetworkInspect(context.Background(), "missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNetworkCreate_duplicateConflicts(t *testing.T) {
	networks := newFakeNetworks()
	networks.createErr = cni.ErrNetworkExists
	service := testService(t, newFakeRuntime(), &fakeImages{}, networks, &fakeTasks{pid: 1})

	_, err := service.NetworkCreate(context.Background(), ports.NetworkCreateRequest{Name: "frontend"})

	if !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if got := dockerMessage(err); !strings.Contains(got, "already exists") {
		t.Fatalf("expected Docker network conflict message, got %q", got)
	}
}

func TestInfo_countsContainerStates(t *testing.T) {
	runtime := newFakeRuntime()
	service := testService(t, runtime, &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})
	seedContainer(t, service, "running-id", "running", domain.ContainerStateRunning, nil, nil)
	seedContainer(t, service, "exited-id", "exited", domain.ContainerStateExited, nil, nil)
	runtime.setState("running-id", domain.ContainerStateRunning)
	runtime.setState("exited-id", domain.ContainerStateExited)

	status, err := service.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if status.Containers != 2 || status.ContainersRunning != 1 || status.ContainersStopped != 1 {
		t.Fatalf("unexpected counters: %+v", status)
	}
}

func TestNew_rejectsMissingDependencies(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected New to reject a missing runtime port")
	}
}

func TestExecStart_unknownExecNotFound(t *testing.T) {
	service := testService(t, newFakeRuntime(), &fakeImages{}, newFakeNetworks(), &fakeTasks{pid: 1})

	_, err := service.ExecStart(context.Background(), "missing-exec", ports.ExecStartRequest{Detach: true})

	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if got := dockerMessage(err); got != "No such exec instance: missing-exec" {
		t.Fatalf("expected Docker exec message, got %q", got)
	}
}

func TestTranslateError_mapsAdapterSentinels(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		target error
	}{
		{"buildkit not found", fmt.Errorf("wrap: %w", buildkit.ErrNotFound), ports.ErrNotFound},
		{"cni port in use", fmt.Errorf("wrap: %w", cni.ErrPortInUse), ports.ErrConflict},
		{"cni unknown mode", fmt.Errorf("wrap: %w", cni.ErrUnknownNetworkMode), ports.ErrInvalidArgument},
		{"buildkit no solver", fmt.Errorf("wrap: %w", buildkit.ErrNoSolver), ports.ErrNotImplemented},
		{"unknown", errors.New("weird"), ports.ErrServerError},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			mapped := translateError(test.err)
			if !errors.Is(mapped, test.target) {
				t.Fatalf("expected %v, got %v", test.target, mapped)
			}
		})
	}
}
