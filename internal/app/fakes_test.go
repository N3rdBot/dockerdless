package app

import (
	"context"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

type fakeRuntime struct {
	mu sync.Mutex

	createSpecs []ports.ContainerCreateSpec
	createID    domain.ContainerID
	createErr   error

	states    map[domain.ContainerID]domain.ContainerState
	statusErr error

	startIOErr  error
	startIOCall int
	keepState   bool

	stopCalls []stopCall
	stopErr   error

	removed   []domain.ContainerID
	removeErr error

	waitResult ports.ProcessExit
	waitErr    error

	execRequest *ports.ExecRequest
	execResult  ports.ExecResult
	execErr     error
	execRecords map[string]domain.ExecRecord
}

type stopCall struct {
	id      domain.ContainerID
	timeout time.Duration
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		createID:    "container-1",
		states:      map[domain.ContainerID]domain.ContainerState{},
		execRecords: map[string]domain.ExecRecord{},
	}
}

func (f *fakeRuntime) Create(ctx context.Context, spec domain.ContainerSpec) (domain.ContainerID, error) {
	return f.CreateContainer(ctx, ports.ContainerCreateSpec{Image: spec.Image, Command: spec.Command, Env: spec.Env})
}

func (f *fakeRuntime) CreateContainer(_ context.Context, spec ports.ContainerCreateSpec) (domain.ContainerID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createSpecs = append(f.createSpecs, spec)
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.createID, nil
}

func (f *fakeRuntime) Start(context.Context, domain.ContainerID) error { return nil }

func (f *fakeRuntime) Stop(_ context.Context, id domain.ContainerID, timeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, stopCall{id: id, timeout: timeout})
	return f.stopErr
}

func (f *fakeRuntime) Remove(_ context.Context, id domain.ContainerID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return f.removeErr
}

func (f *fakeRuntime) Kill(context.Context, domain.ContainerID, syscall.Signal) error { return nil }

func (f *fakeRuntime) Wait(context.Context, domain.ContainerID) (ports.ProcessExit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waitResult, f.waitErr
}

func (f *fakeRuntime) Status(_ context.Context, id domain.ContainerID) (domain.ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return domain.ContainerStateUnknown, f.statusErr
	}
	if state, ok := f.states[id]; ok {
		return state, nil
	}
	return domain.ContainerStateExited, nil
}

func (f *fakeRuntime) Resize(context.Context, domain.ContainerID, uint32, uint32) error { return nil }

func (f *fakeRuntime) StartWithIO(_ context.Context, id domain.ContainerID, _, _ io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startIOCall++
	if f.startIOErr != nil {
		return f.startIOErr
	}
	if !f.keepState {
		f.states[id] = domain.ContainerStateRunning
	}
	return nil
}

func (f *fakeRuntime) Exec(_ context.Context, _ domain.ContainerID, request ports.ExecRequest) (ports.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execRequest = &request
	if f.execErr != nil {
		return ports.ExecResult{}, f.execErr
	}
	return f.execResult, nil
}

func (f *fakeRuntime) ExecRecord(id string) (domain.ExecRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.execRecords[id]
	return record, ok
}

func (f *fakeRuntime) setState(id domain.ContainerID, state domain.ContainerState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[id] = state
}

func (f *fakeRuntime) lastExecRequest() ports.ExecRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execRequest == nil {
		return ports.ExecRequest{}
	}
	return *f.execRequest
}

type fakeImages struct {
	inspectDetail ports.ImageDetail
	inspectErr    error

	pulls     []string
	pullErr   error
	removed   []domain.ImageID
	removeErr error

	list    []ports.ImageDetail
	listErr error

	buildErr error
}

func (f *fakeImages) Pull(ctx context.Context, ref string) (domain.ImageID, error) {
	return f.PullImage(ctx, ref, ports.PullRequest{Reference: ref})
}

func (f *fakeImages) Remove(_ context.Context, id domain.ImageID) error {
	f.removed = append(f.removed, id)
	return f.removeErr
}

func (f *fakeImages) PullImage(_ context.Context, ref string, _ ports.PullRequest) (domain.ImageID, error) {
	f.pulls = append(f.pulls, ref)
	if f.pullErr != nil {
		return "", f.pullErr
	}
	return f.inspectDetail.ID, nil
}

func (f *fakeImages) Inspect(context.Context, string) (ports.ImageDetail, error) {
	if f.inspectErr != nil {
		return ports.ImageDetail{}, f.inspectErr
	}
	return f.inspectDetail, nil
}

func (f *fakeImages) List(context.Context) ([]ports.ImageDetail, error) {
	return f.list, f.listErr
}

func (f *fakeImages) Build(context.Context, ports.BuildRequest, io.Writer) error { return f.buildErr }

type fakeNetworks struct {
	ensureCalls int
	ensureErr   error

	resolveDetail ports.NetworkDetail
	resolveByName map[string]ports.NetworkDetail
	resolveErr    error

	connectRequests        []ports.NetworkConnectRequest
	connectResult          ports.NetworkAttachmentResult
	connectErr             error
	connectReservedRelease func(ports.NetworkConnectRequest)

	disconnectErrs    []error
	disconnectAllFunc func(context.Context, domain.ContainerID) []error

	createRequests []ports.NetworkCreateRequest
	createErr      error

	removed   []domain.NetworkID
	removeErr error

	list []ports.NetworkDetail
}

func newFakeNetworks() *fakeNetworks {
	return &fakeNetworks{
		resolveDetail: ports.NetworkDetail{ID: "network-bridge", Name: "bridge", Driver: "bridge", Mode: "network"},
		connectResult: ports.NetworkAttachmentResult{
			Attachment: domain.NetworkAttachment{NetworkID: "network-bridge", Name: "bridge", IPAddress: "10.88.0.2"},
		},
	}
}

func (f *fakeNetworks) Create(ctx context.Context, name string) (domain.NetworkID, error) {
	return f.CreateNetwork(ctx, ports.NetworkCreateRequest{Name: name})
}

func (f *fakeNetworks) Remove(_ context.Context, id domain.NetworkID) error {
	f.removed = append(f.removed, id)
	return f.removeErr
}

func (f *fakeNetworks) EnsureDefaultNetwork(context.Context) (domain.NetworkID, error) {
	f.ensureCalls++
	if f.ensureErr != nil {
		return "", f.ensureErr
	}
	return f.resolveDetail.ID, nil
}

func (f *fakeNetworks) Resolve(_ context.Context, ref string) (ports.NetworkDetail, error) {
	if f.resolveErr != nil {
		return ports.NetworkDetail{}, f.resolveErr
	}
	if detail, ok := f.resolveByName[ref]; ok {
		return detail, nil
	}
	return f.resolveDetail, nil
}

func (f *fakeNetworks) List(context.Context) ([]ports.NetworkDetail, error) { return f.list, nil }

func (f *fakeNetworks) CreateNetwork(_ context.Context, request ports.NetworkCreateRequest) (domain.NetworkID, error) {
	f.createRequests = append(f.createRequests, request)
	if f.createErr != nil {
		return "", f.createErr
	}
	return domain.NetworkID("network-" + request.Name), nil
}

func (f *fakeNetworks) Connect(_ context.Context, request ports.NetworkConnectRequest) (ports.NetworkAttachmentResult, error) {
	f.connectRequests = append(f.connectRequests, request)
	if f.connectErr != nil {
		return ports.NetworkAttachmentResult{}, f.connectErr
	}
	return f.connectResult, nil
}

func (f *fakeNetworks) ConnectReserved(ctx context.Context, request ports.NetworkConnectRequest) (ports.NetworkAttachmentResult, error) {
	if f.connectErr != nil && f.connectReservedRelease != nil {
		f.connectReservedRelease(request)
	}
	return f.Connect(ctx, request)
}

func (f *fakeNetworks) DisconnectAll(ctx context.Context, id domain.ContainerID) []error {
	if f.disconnectAllFunc != nil {
		return f.disconnectAllFunc(ctx, id)
	}
	return f.disconnectErrs
}

type fakeTasks struct {
	pid int
	err error
}

func (f *fakeTasks) TaskPID(context.Context, domain.ContainerID) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.pid, nil
}

type fakeImageConfigs struct {
	config          ports.ImageConfig
	err             error
	gotConfigDigest string
}

func (f *fakeImageConfigs) ImageConfig(_ context.Context, configDigest string) (ports.ImageConfig, error) {
	f.gotConfigDigest = configDigest
	if f.err != nil {
		return ports.ImageConfig{}, f.err
	}
	return f.config, nil
}
