package containerd

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"

	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// fakeClient is an in-memory Client implementation. It records every call so
// tests can assert both behavior and ordering without a containerd daemon.
type fakeClient struct {
	mu                sync.Mutex // guards images, containers, snapshots, errors
	images            map[string]*fakeImage
	containers        map[string]*fakeContainer
	removedSnapshots  []string
	removeSnapshotErr error

	callMu sync.Mutex
	calls  []string
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		images:     make(map[string]*fakeImage),
		containers: make(map[string]*fakeContainer),
	}
}

func (f *fakeClient) record(format string, args ...any) {
	f.callMu.Lock()
	defer f.callMu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeClient) callLog() []string {
	f.callMu.Lock()
	defer f.callMu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeClient) countCalls(prefix string) int {
	count := 0
	for _, call := range f.callLog() {
		if len(call) >= len(prefix) && call[:len(prefix)] == prefix {
			count++
		}
	}
	return count
}

func (f *fakeClient) addImage(ref string, spec ocispec.Image) *fakeImage {
	image := &fakeImage{name: ref, spec: spec}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[ref] = image
	return image
}

func (f *fakeClient) container(id string) *fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containers[id]
}

func (f *fakeClient) snapshotRemovals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removedSnapshots...)
}

func (f *fakeClient) GetImage(_ context.Context, ref string) (Image, error) {
	f.record("GetImage(%s)", ref)
	f.mu.Lock()
	defer f.mu.Unlock()
	image, ok := f.images[ref]
	if !ok {
		return nil, fmt.Errorf("image %q: %w", ref, errdefs.ErrNotFound)
	}
	return image, nil
}

func (f *fakeClient) NewContainer(_ context.Context, req NewContainerRequest) (Container, error) {
	f.record("NewContainer(%s)", req.ID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.containers[req.ID]; exists {
		return nil, fmt.Errorf("container %q: %w", req.ID, errdefs.ErrAlreadyExists)
	}
	snapshotKey := req.SnapshotID
	if snapshotKey == "" {
		snapshotKey = req.ID
	}
	container := &fakeContainer{
		client:      f,
		id:          req.ID,
		labels:      cloneStringMap(req.Labels),
		spec:        req.Spec,
		snapshotter: req.Snapshotter,
		snapshotKey: snapshotKey,
	}
	f.containers[req.ID] = container
	return container, nil
}

func (f *fakeClient) LoadContainer(_ context.Context, id string) (Container, error) {
	f.record("LoadContainer(%s)", id)
	f.mu.Lock()
	defer f.mu.Unlock()
	container, ok := f.containers[id]
	if !ok {
		return nil, fmt.Errorf("container %q: %w", id, errdefs.ErrNotFound)
	}
	return container, nil
}

func (f *fakeClient) FindContainerByName(_ context.Context, name string) (Container, error) {
	f.record("FindContainerByName(%s)", name)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, container := range f.containers {
		if container.labels[labelName] == name {
			return container, nil
		}
	}
	return nil, fmt.Errorf("container name %q: %w", name, errdefs.ErrNotFound)
}

func (f *fakeClient) RemoveSnapshot(_ context.Context, snapshotter, key string) error {
	f.record("RemoveSnapshot(%s,%s)", snapshotter, key)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedSnapshots = append(f.removedSnapshots, key)
	if f.removeSnapshotErr != nil {
		return f.removeSnapshotErr
	}
	return nil
}

// fakeImage implements Image.
type fakeImage struct {
	name string
	spec ocispec.Image

	mu          sync.Mutex
	unpacked    bool
	unpackCalls int
}

func (i *fakeImage) Name() string {
	return i.name
}

func (i *fakeImage) Spec(_ context.Context) (ocispec.Image, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.spec, nil
}

func (i *fakeImage) IsUnpacked(_ context.Context, _ string) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.unpacked, nil
}

func (i *fakeImage) Unpack(_ context.Context, _ string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.unpacked = true
	i.unpackCalls++
	return nil
}

// fakeContainer implements Container.
type fakeContainer struct {
	client      *fakeClient
	id          string
	labels      map[string]string
	spec        *oci.Spec
	snapshotter string
	snapshotKey string

	mu         sync.Mutex
	task       *fakeTask
	deleted    bool
	newTaskErr error
	taskErr    error
}

func (c *fakeContainer) ID() string {
	return c.id
}

func (c *fakeContainer) Labels(_ context.Context) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deleted {
		return nil, fmt.Errorf("container %q: %w", c.id, errdefs.ErrNotFound)
	}
	return cloneStringMap(c.labels), nil
}

func (c *fakeContainer) Spec(_ context.Context) (*oci.Spec, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spec == nil {
		return nil, fmt.Errorf("container %q has no spec: %w", c.id, errdefs.ErrNotFound)
	}
	return c.spec, nil
}

func (c *fakeContainer) Snapshot() (string, string) {
	return c.snapshotter, c.snapshotKey
}

func (c *fakeContainer) NewTask(_ context.Context, streams TaskIO) (Task, error) {
	c.client.record("NewTask(%s,terminal=%t,stdin=%t)", c.id, streams.Terminal, streams.Stdin != nil)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.newTaskErr != nil {
		return nil, c.newTaskErr
	}
	if c.task != nil && !c.task.isStopped() {
		return nil, fmt.Errorf("container %q already has a task: %w", c.id, errdefs.ErrAlreadyExists)
	}
	task := newFakeTask(c.id, streams, c.client)
	c.task = task
	return task, nil
}

func (c *fakeContainer) Task(_ context.Context) (Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.taskErr != nil {
		return nil, c.taskErr
	}
	if c.task == nil {
		return nil, fmt.Errorf("container %q: %w", c.id, errdefs.ErrNotFound)
	}
	return c.task, nil
}

func (c *fakeContainer) Delete(_ context.Context) error {
	c.client.record("DeleteContainer(%s)", c.id)
	c.mu.Lock()
	running := c.task != nil && !c.task.isStopped()
	c.mu.Unlock()
	if running {
		return fmt.Errorf("task must be stopped: %w", errdefs.ErrFailedPrecondition)
	}
	c.client.mu.Lock()
	delete(c.client.containers, c.id)
	c.client.mu.Unlock()
	c.mu.Lock()
	c.deleted = true
	c.mu.Unlock()
	return nil
}

func (c *fakeContainer) fakeTask() *fakeTask {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.task
}

// fakeProcessTemplate preconfigures the next exec process.
type fakeProcessTemplate struct {
	exitOnStart bool
	exitCode    uint32
}

// fakeTask implements Task.
type fakeTask struct {
	client  *fakeClient
	id      string
	streams TaskIO

	mu              sync.Mutex
	status          containerdclient.ProcessStatus
	exitCode        uint32
	exitTime        time.Time
	exitCh          chan containerdclient.ExitStatus
	exited          bool
	termExits       bool
	killExits       bool
	waitCalls       int
	startCalls      int
	resizeCalls     int
	closeIOCalls    int
	deleteCalls     int
	lastWidth       uint32
	lastHeight      uint32
	kills           []syscall.Signal
	processTemplate fakeProcessTemplate
	processes       map[string]*fakeProcess
	execErr         error
}

func newFakeTask(id string, streams TaskIO, client *fakeClient) *fakeTask {
	return &fakeTask{
		client:    client,
		id:        id,
		streams:   streams,
		status:    containerdclient.Created,
		exitCh:    make(chan containerdclient.ExitStatus, 1),
		killExits: true,
		processes: make(map[string]*fakeProcess),
	}
}

func (t *fakeTask) ID() string {
	return t.id
}

func (t *fakeTask) Pid() uint32 {
	return 4242
}

func (t *fakeTask) Status(_ context.Context) (containerdclient.Status, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return containerdclient.Status{Status: t.status, ExitStatus: t.exitCode, ExitTime: t.exitTime}, nil
}

func (t *fakeTask) Start(_ context.Context) error {
	t.client.record("TaskStart(%s)", t.id)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.startCalls++
	t.status = containerdclient.Running
	return nil
}

func (t *fakeTask) Wait(_ context.Context) (<-chan containerdclient.ExitStatus, error) {
	t.client.record("TaskWait(%s)", t.id)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.waitCalls++
	return t.exitCh, nil
}

func (t *fakeTask) Kill(_ context.Context, signal syscall.Signal) error {
	t.client.record("TaskKill(%s,%d)", t.id, signal)
	t.mu.Lock()
	t.kills = append(t.kills, signal)
	exit := (signal == syscall.SIGTERM && t.termExits) || (signal == syscall.SIGKILL && t.killExits)
	t.mu.Unlock()
	if exit {
		t.exit(128 + uint32(signal))
	}
	return nil
}

func (t *fakeTask) Resize(_ context.Context, width, height uint32) error {
	t.client.record("TaskResize(%s)", t.id)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resizeCalls++
	t.lastWidth = width
	t.lastHeight = height
	return nil
}

func (t *fakeTask) CloseIO(_ context.Context) error {
	t.client.record("TaskCloseIO(%s)", t.id)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeIOCalls++
	return nil
}

func (t *fakeTask) Delete(_ context.Context) (*containerdclient.ExitStatus, error) {
	t.client.record("TaskDelete(%s)", t.id)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deleteCalls++
	if !t.exited && t.status == containerdclient.Running {
		return nil, fmt.Errorf("task must be stopped: %w", errdefs.ErrFailedPrecondition)
	}
	return containerdclient.NewExitStatus(t.exitCode, t.exitTime, nil), nil
}

func (t *fakeTask) Exec(_ context.Context, id string, spec *specs.Process, streams TaskIO) (Process, error) {
	t.client.record("TaskExec(%s,%s)", t.id, id)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.execErr != nil {
		return nil, t.execErr
	}
	process := newFakeProcess(id, spec, streams, t.client)
	process.exitOnStart = t.processTemplate.exitOnStart
	process.exitCode = t.processTemplate.exitCode
	t.processes[id] = process
	return process, nil
}

func (t *fakeTask) exit(code uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exited {
		return
	}
	t.exited = true
	t.status = containerdclient.Stopped
	t.exitCode = code
	t.exitTime = time.Now()
	t.exitCh <- *containerdclient.NewExitStatus(code, t.exitTime, nil)
}

func (t *fakeTask) isStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exited || t.status == containerdclient.Stopped
}

func (t *fakeTask) setTermExits(exit bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.termExits = exit
}

func (t *fakeTask) setNextProcess(template fakeProcessTemplate) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.processTemplate = template
}

func (t *fakeTask) signals() []syscall.Signal {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]syscall.Signal(nil), t.kills...)
}

func (t *fakeTask) resizeSnapshot() (calls int, width, height uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resizeCalls, t.lastWidth, t.lastHeight
}

func (t *fakeTask) taskProcess(id string) *fakeProcess {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.processes[id]
}

// fakeProcess implements Process.
type fakeProcess struct {
	client  *fakeClient
	id      string
	spec    *specs.Process
	streams TaskIO

	mu           sync.Mutex
	status       containerdclient.ProcessStatus
	exitCode     uint32
	exitTime     time.Time
	exitCh       chan containerdclient.ExitStatus
	exited       bool
	exitOnStart  bool
	waitCalls    int
	startCalls   int
	resizeCalls  int
	closeIOCalls int
	deleteCalls  int
	lastWidth    uint32
	lastHeight   uint32
}

func newFakeProcess(id string, spec *specs.Process, streams TaskIO, client *fakeClient) *fakeProcess {
	return &fakeProcess{
		client:  client,
		id:      id,
		spec:    spec,
		streams: streams,
		status:  containerdclient.Created,
		exitCh:  make(chan containerdclient.ExitStatus, 1),
	}
}

func (p *fakeProcess) ID() string {
	return p.id
}

func (p *fakeProcess) Pid() uint32 {
	return 9001
}

func (p *fakeProcess) Start(_ context.Context) error {
	p.client.record("ProcessStart(%s)", p.id)
	p.mu.Lock()
	p.startCalls++
	p.status = containerdclient.Running
	exit, code := p.exitOnStart, p.exitCode
	p.mu.Unlock()
	if exit {
		p.exit(code)
	}
	return nil
}

func (p *fakeProcess) Wait(_ context.Context) (<-chan containerdclient.ExitStatus, error) {
	p.client.record("ProcessWait(%s)", p.id)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waitCalls++
	return p.exitCh, nil
}

func (p *fakeProcess) Kill(_ context.Context, signal syscall.Signal) error {
	p.client.record("ProcessKill(%s,%d)", p.id, signal)
	return nil
}

func (p *fakeProcess) Resize(_ context.Context, width, height uint32) error {
	p.client.record("ProcessResize(%s)", p.id)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizeCalls++
	p.lastWidth = width
	p.lastHeight = height
	return nil
}

func (p *fakeProcess) CloseIO(_ context.Context) error {
	p.client.record("ProcessCloseIO(%s)", p.id)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeIOCalls++
	return nil
}

func (p *fakeProcess) Delete(_ context.Context) (*containerdclient.ExitStatus, error) {
	p.client.record("ProcessDelete(%s)", p.id)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deleteCalls++
	return containerdclient.NewExitStatus(p.exitCode, p.exitTime, nil), nil
}

func (p *fakeProcess) Status(_ context.Context) (containerdclient.Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return containerdclient.Status{Status: p.status, ExitStatus: p.exitCode, ExitTime: p.exitTime}, nil
}

func (p *fakeProcess) exit(code uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		return
	}
	p.exited = true
	p.status = containerdclient.Stopped
	p.exitCode = code
	p.exitTime = time.Now()
	p.exitCh <- *containerdclient.NewExitStatus(code, p.exitTime, nil)
}
