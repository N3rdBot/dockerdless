package containerd

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	containerdclient "github.com/containerd/containerd/v2/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const testImageRef = "docker.io/library/busybox:latest"

// dockerIDPattern matches Docker's 64-character lowercase hex container IDs.
var dockerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func newTestAdapter(t *testing.T) (*Adapter, *fakeClient) {
	t.Helper()
	fake := newFakeClient()
	fake.addImage(testImageRef, ocispec.Image{
		Config: ocispec.ImageConfig{
			Env:        []string{"PATH=/usr/bin:/bin"},
			Cmd:        []string{"sh"},
			WorkingDir: "/root",
		},
	})
	return NewWithClient(fake), fake
}

func mustCreateContainer(t *testing.T, adapter *Adapter, cfg Config) domain.ContainerID {
	t.Helper()
	if cfg.Image == "" {
		cfg.Image = domain.ImageID(testImageRef)
	}
	id, err := adapter.CreateContainer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	return id
}

func startTestContainer(t *testing.T, adapter *Adapter, fake *fakeClient, cfg Config) (domain.ContainerID, *fakeContainer, *fakeTask) {
	t.Helper()
	id := mustCreateContainer(t, adapter, cfg)
	if err := adapter.Start(context.Background(), id); err != nil {
		t.Fatalf("Start(%s): %v", id, err)
	}
	container := fake.container(string(id))
	if container == nil {
		t.Fatalf("container %q was not persisted", id)
	}
	task := container.fakeTask()
	if task == nil {
		t.Fatalf("container %q has no task after Start", id)
	}
	return id, container, task
}

func assertCallOrder(t *testing.T, calls []string, first, second string) {
	t.Helper()
	firstIndex, secondIndex := -1, -1
	for index, call := range calls {
		if firstIndex < 0 && strings.HasPrefix(call, first) {
			firstIndex = index
		}
		if secondIndex < 0 && strings.HasPrefix(call, second) {
			secondIndex = index
		}
	}
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("calls %v: want %q before %q", calls, first, second)
	}
}

func envContains(env []string, want string) bool {
	return slices.Contains(env, want)
}

func TestCreateReturnsStableDockerIDAndPersistsMetadata(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	ctx := context.Background()

	generatedA, err := adapter.Create(ctx, domain.ContainerSpec{Image: domain.ImageID(testImageRef)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	generatedB, err := adapter.Create(ctx, domain.ContainerSpec{Image: domain.ImageID(testImageRef)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !dockerIDPattern.MatchString(string(generatedA)) {
		t.Fatalf("generated id %q is not a 64-character hex Docker id", generatedA)
	}
	if generatedA == generatedB {
		t.Fatalf("generated ids must be unique, both were %q", generatedA)
	}

	explicitID := strings.Repeat("a", 64)
	id, err := adapter.CreateContainer(ctx, Config{
		ID:      explicitID,
		Name:    "web",
		Image:   domain.ImageID(testImageRef),
		Command: []string{"echo", "hi"},
		Env:     map[string]string{"FOO": "bar"},
		Labels:  map[string]string{"app": "web"},
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if string(id) != explicitID {
		t.Fatalf("CreateContainer id = %q, want stable explicit id %q", id, explicitID)
	}
	container := fake.container(explicitID)
	if container == nil {
		t.Fatalf("container %q was not persisted", explicitID)
	}
	if got := container.labels[labelName]; got != "web" {
		t.Fatalf("name label = %q, want web", got)
	}
	if container.snapshotKey != explicitID {
		t.Fatalf("snapshot key = %q, want %q", container.snapshotKey, explicitID)
	}
	spec := container.spec
	if spec == nil || spec.Process == nil {
		t.Fatal("persisted OCI spec is missing")
	}
	if !slices.Equal(spec.Process.Args, []string{"echo", "hi"}) {
		t.Fatalf("args = %v, want [echo hi]", spec.Process.Args)
	}
	if !envContains(spec.Process.Env, "FOO=bar") || !envContains(spec.Process.Env, "PATH=/usr/bin:/bin") {
		t.Fatalf("env = %v, want image PATH plus FOO=bar", spec.Process.Env)
	}
	if got := spec.Hostname; got != explicitID[:12] {
		t.Fatalf("hostname = %q, want %q", got, explicitID[:12])
	}
	if got := spec.Annotations["app"]; got != "web" {
		t.Fatalf("annotation app = %q, want web", got)
	}
}

func TestCreateRejectsDuplicateName(t *testing.T) {
	adapter, _ := newTestAdapter(t)
	ctx := context.Background()
	mustCreateContainer(t, adapter, Config{Name: "db"})
	_, err := adapter.CreateContainer(ctx, Config{Name: "db", Image: domain.ImageID(testImageRef)})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name error = %v, want ErrConflict", err)
	}
}

func TestCreateRejectsMissingImage(t *testing.T) {
	adapter, _ := newTestAdapter(t)
	_, err := adapter.Create(context.Background(), domain.ContainerSpec{Image: domain.ImageID("missing:latest")})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing image error = %v, want ErrNotFound", err)
	}
}

func TestStartOnMissingContainerDoesNotCreateTask(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	err := adapter.Start(context.Background(), domain.ContainerID("does-not-exist"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Start error = %v, want ErrNotFound", err)
	}
	for _, call := range fake.callLog() {
		if strings.HasPrefix(call, "NewTask") {
			t.Fatalf("Start created a task for a missing container: %s", call)
		}
	}
}

func TestStartCreatesTaskWaitBeforeStart(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})

	status, err := task.Status(context.Background())
	if err != nil {
		t.Fatalf("task status: %v", err)
	}
	if status.Status != containerdclient.Running {
		t.Fatalf("task status = %q, want running", status.Status)
	}
	assertCallOrder(t, fake.callLog(), "TaskWait(", "TaskStart("+string(id))
}

func TestStartRejectsAlreadyRunningContainer(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, _ := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	err := adapter.Start(context.Background(), id)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Start on running container error = %v, want ErrConflict", err)
	}
	if count := fake.countCalls("NewTask("); count != 1 {
		t.Fatalf("NewTask called %d times, want 1", count)
	}
}

func TestStartSupportsDetachedInteractiveStdinWithoutTTY(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id := mustCreateContainer(t, adapter, Config{
		Command:   []string{"sleep", "30"},
		OpenStdin: true,
		Terminal:  false,
	})
	if err := adapter.Start(context.Background(), id); err != nil {
		t.Fatalf("detached interactive stdin without TTY must be accepted: %v", err)
	}
	task := fake.container(string(id)).fakeTask()
	if task.streams.Stdin == nil {
		t.Fatal("task stdin was not kept open")
	}
	if task.streams.Terminal {
		t.Fatal("task must not allocate a TTY")
	}
	writer, ok := adapter.Stdin(id)
	if !ok || writer == nil {
		t.Fatal("detached stdin writer is not exposed")
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close detached stdin: %v", err)
	}
}

func TestStartWithOptionsAttachesCallerStreams(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id := mustCreateContainer(t, adapter, Config{Command: []string{"cat"}})
	var stdout strings.Builder
	if err := adapter.StartWithOptions(context.Background(), id, StartOptions{
		Stdin:  strings.NewReader("hello"),
		Stdout: &stdout,
	}); err != nil {
		t.Fatalf("StartWithOptions: %v", err)
	}
	task := fake.container(string(id)).fakeTask()
	if task.streams.Stdin == nil || task.streams.Stdout == nil {
		t.Fatal("caller streams were not attached")
	}
}

func TestStopSendsSIGTERMThenSIGKILLAfterTimeout(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	task.setTermExits(false)

	started := time.Now()
	if err := adapter.Stop(context.Background(), id, 25*time.Millisecond); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("Stop escalated after %v, before the timeout", elapsed)
	}
	want := []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}
	if got := task.signals(); !slices.Equal(got, want) {
		t.Fatalf("signals = %v, want %v", got, want)
	}
}

func TestStopReturnsAfterSIGTERMExit(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	task.setTermExits(true)

	if err := adapter.Stop(context.Background(), id, time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := []syscall.Signal{syscall.SIGTERM}
	if got := task.signals(); !slices.Equal(got, want) {
		t.Fatalf("signals = %v, want %v", got, want)
	}
}

func TestStopIsNoopForStoppedTask(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"true"}})
	task.exit(0)

	if err := adapter.Stop(context.Background(), id, time.Second); err != nil {
		t.Fatalf("Stop on stopped task: %v", err)
	}
	if got := task.signals(); len(got) != 0 {
		t.Fatalf("signals = %v, want none", got)
	}
}

func TestStopMissingContainerReturnsNotFound(t *testing.T) {
	adapter, _ := newTestAdapter(t)
	err := adapter.Stop(context.Background(), domain.ContainerID("missing"), time.Second)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stop error = %v, want ErrNotFound", err)
	}
}

func TestStopWithZeroTimeoutKillsImmediately(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})

	if err := adapter.Stop(context.Background(), id, 0); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := []syscall.Signal{syscall.SIGKILL}
	if got := task.signals(); !slices.Equal(got, want) {
		t.Fatalf("signals = %v, want %v", got, want)
	}
}

func TestResizeSendsSizeToRunningTask(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})

	if err := adapter.Resize(context.Background(), id, 120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	calls, width, height := task.resizeSnapshot()
	if calls != 1 {
		t.Fatalf("resize calls = %d, want 1", calls)
	}
	if width != 120 || height != 40 {
		t.Fatalf("resize = %dx%d, want 120x40", width, height)
	}
}

func TestKillSendsRequestedSignal(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	if err := adapter.Kill(context.Background(), id, syscall.SIGUSR1); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	want := []syscall.Signal{syscall.SIGUSR1}
	if got := task.signals(); !slices.Equal(got, want) {
		t.Fatalf("signals = %v, want %v", got, want)
	}
}

func TestKillOnStoppedTaskReturnsConflict(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"true"}})
	task.exit(0)
	err := adapter.Kill(context.Background(), id, syscall.SIGTERM)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Kill on stopped task error = %v, want ErrConflict", err)
	}
}

func TestKillOnMissingTaskReturnsNotFound(t *testing.T) {
	adapter, _ := newTestAdapter(t)
	id := mustCreateContainer(t, adapter, Config{Command: []string{"true"}})
	err := adapter.Kill(context.Background(), id, syscall.SIGTERM)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Kill without task error = %v, want ErrNotFound", err)
	}
}

func TestWaitReturnsExitCode(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sh", "-c", "exit 42"}})
	task.exit(42)

	result, err := adapter.Wait(context.Background(), id)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitCode != 42 {
		t.Fatalf("exit code = %d, want 42", result.ExitCode)
	}
	if result.ExitedAt.IsZero() {
		t.Fatal("exit time was not reported")
	}
}

func TestWaitMissingContainerReturnsNotFound(t *testing.T) {
	adapter, _ := newTestAdapter(t)
	if _, err := adapter.Wait(context.Background(), domain.ContainerID("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Wait error = %v, want ErrNotFound", err)
	}
}

func TestStatusDerivesDockerStateFromTask(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	ctx := context.Background()

	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	state, err := adapter.Status(ctx, id)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if state != domain.ContainerStateRunning {
		t.Fatalf("state = %q, want running", state)
	}
	task.exit(0)
	state, err = adapter.Status(ctx, id)
	if err != nil {
		t.Fatalf("Status after exit: %v", err)
	}
	if state != domain.ContainerStateExited {
		t.Fatalf("state = %q, want exited", state)
	}
}

func TestRemoveDeletesTaskContainerSnapshotInOrder(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"true"}})
	task.exit(0)

	if err := adapter.Remove(context.Background(), id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	calls := fake.callLog()
	assertCallOrder(t, calls, "TaskDelete(", "DeleteContainer(")
	assertCallOrder(t, calls, "DeleteContainer(", "RemoveSnapshot(")
	if got := fake.snapshotRemovals(); !slices.Equal(got, []string{string(id)}) {
		t.Fatalf("snapshot removals = %v, want [%s]", got, id)
	}
	if fake.container(string(id)) != nil {
		t.Fatalf("container %q still exists after Remove", id)
	}
}

func TestRemoveIsIdempotentForMissingContainer(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	if err := adapter.Remove(context.Background(), domain.ContainerID("missing")); err != nil {
		t.Fatalf("Remove missing container: %v", err)
	}
	if got := fake.snapshotRemovals(); !slices.Equal(got, []string{"missing"}) {
		t.Fatalf("snapshot removals = %v, want [missing]", got)
	}
}

func TestRemoveRunningContainerReturnsConflict(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, _ := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	err := adapter.Remove(context.Background(), id)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Remove running container error = %v, want ErrConflict", err)
	}
	if fake.container(string(id)) == nil {
		t.Fatal("container was removed despite the conflict")
	}
}

func TestExecCreatesStartsWaitsAndRecordsExitCode(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	ctx := context.Background()
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	task.setNextProcess(fakeProcessTemplate{exitOnStart: true, exitCode: 7})

	result, err := adapter.Exec(ctx, id, ExecConfig{Command: []string{"sh", "-c", "exit 7"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatalf("exit code = %v, want 7", result.ExitCode)
	}
	if !dockerIDPattern.MatchString(result.ID) {
		t.Fatalf("exec id %q is not a 64-character hex Docker id", result.ID)
	}
	record, ok := adapter.ExecRecord(result.ID)
	if !ok {
		t.Fatalf("exec %q was not recorded", result.ID)
	}
	if record.ExitCode == nil || *record.ExitCode != 7 {
		t.Fatalf("recorded exit code = %v, want 7", record.ExitCode)
	}
	if record.Running {
		t.Fatal("recorded exec still reports running")
	}
	if !slices.Equal(record.Command, []string{"sh", "-c", "exit 7"}) {
		t.Fatalf("recorded command = %v", record.Command)
	}
	process := task.taskProcess(result.ID)
	if process == nil {
		t.Fatalf("exec %q never reached the task", result.ID)
	}
	if !slices.Equal(process.spec.Args, []string{"sh", "-c", "exit 7"}) {
		t.Fatalf("process args = %v", process.spec.Args)
	}
	assertCallOrder(t, fake.callLog(), "ProcessWait(", "ProcessStart(")
}

func TestExecResizesTTYProcess(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	task.setNextProcess(fakeProcessTemplate{exitOnStart: true, exitCode: 0})

	result, err := adapter.Exec(context.Background(), id, ExecConfig{
		Command:  []string{"sh"},
		Terminal: true,
		Width:    80,
		Height:   24,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	process := task.taskProcess(result.ID)
	if process.resizeCalls != 1 {
		t.Fatalf("resize calls = %d, want 1", process.resizeCalls)
	}
	if process.lastWidth != 80 || process.lastHeight != 24 {
		t.Fatalf("resize = %dx%d, want 80x24", process.lastWidth, process.lastHeight)
	}
	if !process.spec.Terminal {
		t.Fatal("exec process did not request a terminal")
	}
}

func TestExecSkipsResizeWithoutTTY(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	task.setNextProcess(fakeProcessTemplate{exitOnStart: true, exitCode: 0})

	result, err := adapter.Exec(context.Background(), id, ExecConfig{
		Command: []string{"sh"},
		Width:   80,
		Height:  24,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	process := task.taskProcess(result.ID)
	if process.resizeCalls != 0 {
		t.Fatalf("resize calls = %d, want 0 without TTY", process.resizeCalls)
	}
}

func TestExecOnStoppedContainerReturnsConflict(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"true"}})
	task.exit(0)
	_, err := adapter.Exec(context.Background(), id, ExecConfig{Command: []string{"sh"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Exec on stopped container error = %v, want ErrConflict", err)
	}
}

func TestExecDetachedRecordsRunningProcess(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	task.setNextProcess(fakeProcessTemplate{exitOnStart: false})

	result, err := adapter.Exec(context.Background(), id, ExecConfig{
		Command: []string{"sleep", "30"},
		Detach:  true,
	})
	if err != nil {
		t.Fatalf("Exec detached: %v", err)
	}
	if !result.Detached {
		t.Fatal("result did not report detach")
	}
	if result.ExitCode != nil {
		t.Fatalf("detached exec exit code = %v, want nil", result.ExitCode)
	}
	record, ok := adapter.ExecRecord(result.ID)
	if !ok {
		t.Fatalf("exec %q was not recorded", result.ID)
	}
	if !record.Running {
		t.Fatal("detached exec record does not report running")
	}
	if record.CanRemove {
		t.Fatal("running exec record must not be removable")
	}
}

func TestExecMissingCommandReturnsInvalidArgument(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, _ := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	_, err := adapter.Exec(context.Background(), id, ExecConfig{})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Exec without command error = %v, want ErrInvalidArgument", err)
	}
}

func TestExecRecordsFiltersByContainer(t *testing.T) {
	adapter, fake := newTestAdapter(t)
	id, _, task := startTestContainer(t, adapter, fake, Config{Command: []string{"sleep", "30"}})
	task.setNextProcess(fakeProcessTemplate{exitOnStart: true, exitCode: 0})
	if _, err := adapter.Exec(context.Background(), id, ExecConfig{Command: []string{"sh"}}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if records := adapter.ExecRecords(id); len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records := adapter.ExecRecords(domain.ContainerID("other")); len(records) != 0 {
		t.Fatalf("records for other container = %d, want 0", len(records))
	}
}
