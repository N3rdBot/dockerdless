package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReconcileMetadataWithoutTaskIsExitedAndStale(t *testing.T) {
	id := ContainerID("container-metadata-only")
	result, err := Reconcile([]Container{{ID: id, State: ContainerStateRunning}}, nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Containers[0].State != ContainerStateExited || len(result.Stale) != 1 || result.Stale[0] != id {
		t.Fatalf("expected exited stale container, got %#v", result)
	}
}

func TestReconcileDerivesTaskStatesAndExitData(t *testing.T) {
	tests := []struct {
		name   string
		status TaskState
		want   ContainerState
		dead   bool
	}{
		{"created", TaskStateCreated, ContainerStateCreated, false},
		{"running", TaskStateRunning, ContainerStateRunning, false},
		{"paused", TaskStatePaused, ContainerStatePaused, false},
		{"restarting", TaskStateRestarting, ContainerStateRestarting, false},
		{"stopped", TaskStateStopped, ContainerStateExited, false},
		{"exited", TaskStateExited, ContainerStateExited, false},
		{"dead", TaskStateDead, ContainerStateDead, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Reconcile([]Container{{ID: ContainerID(test.name)}}, []TaskSnapshot{{ContainerID: ContainerID(test.name), Status: test.status, ExitCode: 17}})
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			container := result.Containers[0]
			if container.State != test.want || container.Dead != test.dead || container.ExitCode != 17 {
				t.Fatalf("got state=%q dead=%t exit=%d", container.State, container.Dead, container.ExitCode)
			}
		})
	}
}

func TestReconcileCleansRuntimeOnlyTasksAndPreservesPinnedDigest(t *testing.T) {
	id := ContainerID("web")
	result, err := Reconcile([]Container{{ID: id, Name: "web", Spec: ContainerSpec{Image: ImageID("web:latest")}, ImageDigest: ImageID("sha256:pinned")}}, []TaskSnapshot{{ContainerID: id, Status: TaskStateRunning}, {ContainerID: "orphan", Status: TaskStateStopped}})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Containers[0].ImageDigest != ImageID("sha256:pinned") || len(result.Cleanup) != 1 || result.Cleanup[0] != "orphan" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestReconcileRecordedSnapshotJSON(t *testing.T) {
	result, err := Reconcile([]Container{{ID: "web"}, {ID: "db"}}, []TaskSnapshot{
		{ContainerID: "web", Status: TaskStateRunning, StartedAt: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)},
		{ContainerID: "db", Status: TaskStateStopped, ExitCode: 137, OOMKilled: true},
		{ContainerID: "orphan", Status: TaskStateStopped},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"State":"running"`) || !strings.Contains(string(encoded), `"State":"exited"`) {
		t.Fatalf("snapshot lacks recovered states: %s", encoded)
	}
}

func TestReconcileRejectsDuplicates(t *testing.T) {
	_, err := Reconcile([]Container{{ID: "same"}, {ID: "same"}}, nil)
	if err == nil {
		t.Fatal("expected duplicate metadata error")
	}
	_, err = Reconcile([]Container{{ID: "same"}}, []TaskSnapshot{{ContainerID: "same", Status: TaskStateRunning}, {ContainerID: "same", Status: TaskStateRunning}})
	if err == nil {
		t.Fatal("expected duplicate task error")
	}
}
