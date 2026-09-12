package domain

import "testing"

func TestContainerStateDockerStrings(t *testing.T) {
	tests := []struct {
		name  string
		state ContainerState
		want  string
	}{
		{name: "created", state: ContainerStateCreated, want: "created"},
		{name: "running", state: ContainerStateRunning, want: "running"},
		{name: "paused", state: ContainerStatePaused, want: "paused"},
		{name: "restarting", state: ContainerStateRestarting, want: "restarting"},
		{name: "removing", state: ContainerStateRemoving, want: "removing"},
		{name: "exited", state: ContainerStateExited, want: "exited"},
		{name: "dead", state: ContainerStateDead, want: "dead"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.state); got != tt.want {
				t.Fatalf("expected Docker state %q, got %q", tt.want, got)
			}
			if !tt.state.IsDockerState() {
				t.Fatalf("expected %q to be recognized as a Docker state", tt.state)
			}
		})
	}
}
