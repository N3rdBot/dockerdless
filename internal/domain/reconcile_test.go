package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReconcileClassifiesMetadataWithoutTaskAsExitedStale(t *testing.T) {
	// Given a container metadata record with no corresponding runtime task.
	id, err := NewContainerID("container-metadata-only")
	if err != nil {
		t.Fatalf("construct container id: %v", err)
	}
	metadata := []Container{{
		ID:    id,
		Name:  "metadata-only",
		State: ContainerStateCreated,
	}}

	// When the split containerd metadata and task views are reconciled.
	result, err := Reconcile(metadata, nil)
	if err != nil {
		t.Fatalf("reconcile metadata-only container: %v", err)
	}

	// Then metadata alone must never make the container look running.
	if len(result.Containers) != 1 {
		t.Fatalf("expected one reconciled container, got %d", len(result.Containers))
	}
	if result.Containers[0].State != ContainerStateExited {
		t.Fatalf("expected metadata-only container to be exited, got %q", result.Containers[0].State)
	}
	if len(result.Stale) != 1 || result.Stale[0] != id {
		t.Fatalf("expected metadata-only container to be marked stale, got %#v", result.Stale)
	}
	if result.Containers[0].State == ContainerStateRunning {
		t.Fatal("metadata-only container must not be classified as running")
	}
}

func TestReconcileDerivesDockerStateFromTaskStatus(t *testing.T) {
	tests := []struct {
		name      string
		taskState TaskState
		wantState ContainerState
		wantDead  bool
	}{
		{name: "created", taskState: TaskStateCreated, wantState: ContainerStateCreated},
		{name: "running", taskState: TaskStateRunning, wantState: ContainerStateRunning},
		{name: "paused", taskState: TaskStatePaused, wantState: ContainerStatePaused},
		{name: "restarting", taskState: TaskStateRestarting, wantState: ContainerStateRestarting},
		{name: "stopped", taskState: TaskStateStopped, wantState: ContainerStateExited},
		{name: "exited", taskState: TaskStateExited, wantState: ContainerStateExited},
		{name: "dead", taskState: TaskStateDead, wantState: ContainerStateDead, wantDead: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := ContainerID("task-" + tt.name)
			result, err := Reconcile(
				[]Container{{ID: id, State: ContainerStateCreated}},
				[]TaskSnapshot{{ContainerID: id, Status: tt.taskState, ExitCode: 17}},
			)
			if err != nil {
				t.Fatalf("reconcile task state: %v", err)
			}
			if got := result.Containers[0].State; got != tt.wantState {
				t.Fatalf("expected state %q, got %q", tt.wantState, got)
			}
			if result.Containers[0].Dead != tt.wantDead {
				t.Fatalf("expected dead=%t, got %t", tt.wantDead, result.Containers[0].Dead)
			}
			if result.Containers[0].ExitCode != 17 {
				t.Fatalf("expected exit code 17, got %d", result.Containers[0].ExitCode)
			}
		})
	}
}

func TestReconcileMarksRuntimeOnlyTasksForCleanup(t *testing.T) {
	metadataID := ContainerID("metadata")
	orphanID := ContainerID("orphan")
	result, err := Reconcile(
		[]Container{{ID: metadataID}},
		[]TaskSnapshot{{ContainerID: orphanID, Status: TaskStateRunning}},
	)
	if err != nil {
		t.Fatalf("reconcile orphan task: %v", err)
	}
	if len(result.Cleanup) != 1 || result.Cleanup[0] != orphanID {
		t.Fatalf("expected orphan task cleanup, got %#v", result.Cleanup)
	}
}

func TestReconcileManualQA_printsRecordedSequenceJSON(t *testing.T) {
	webID := ContainerID("web")
	dbID := ContainerID("db")
	orphanID := ContainerID("orphan-task")
	recordedEvents := []Event{
		{Type: EventContainerCreated, ContainerID: webID, Topic: ContainerCreateEventTopic, Action: DockerActionForTopic(ContainerCreateEventTopic)},
		{Type: EventContainerStarted, ContainerID: webID, Topic: TaskStartEventTopic, Action: DockerActionForTopic(TaskStartEventTopic)},
		{Type: EventContainerCreated, ContainerID: dbID, Topic: ContainerCreateEventTopic, Action: DockerActionForTopic(ContainerCreateEventTopic)},
		{Type: EventContainerExited, ContainerID: dbID, Topic: TaskExitEventTopic, Action: DockerActionForTopic(TaskExitEventTopic)},
	}
	for _, event := range recordedEvents {
		if event.Action == "" {
			t.Fatalf("recorded event %q has no mapped action", event.Topic)
		}
	}

	metadata := []Container{
		{ID: webID, Name: "web", Spec: ContainerSpec{Image: ImageID("web:latest")}, ImageDigest: ImageID("sha256:web")},
		{ID: dbID, Name: "db", Spec: ContainerSpec{Image: ImageID("db:latest")}, ImageDigest: ImageID("sha256:db")},
		{ID: ContainerID("stale"), Name: "stale", State: ContainerStateRunning},
	}
	tasks := []TaskSnapshot{
		{ContainerID: webID, Status: TaskStateRunning, StartedAt: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)},
		{ContainerID: dbID, Status: TaskStateStopped, ExitCode: 137, OOMKilled: true, FinishedAt: time.Date(2026, 9, 13, 12, 1, 0, 0, time.UTC)},
		{ContainerID: orphanID, Status: TaskStateStopped, ExitCode: 1},
	}
	result, err := Reconcile(metadata, tasks)
	if err != nil {
		t.Fatalf("manual QA reconcile: %v", err)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal reconcile result: %v", err)
	}
	output := string(encoded)
	fmt.Println(output)
	if !strings.Contains(output, `"State": "running"`) {
		t.Fatalf("manual JSON did not show running status:\n%s", output)
	}
	if !strings.Contains(output, `"State": "exited"`) {
		t.Fatalf("manual JSON did not show exited status:\n%s", output)
	}
	if !strings.Contains(output, `"Cleanup": [`) || !strings.Contains(output, `orphan-task`) {
		t.Fatalf("manual JSON did not show cleanup classification:\n%s", output)
	}
}
