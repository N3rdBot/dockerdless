package domain

import "testing"

func TestDockerActionForTopic(t *testing.T) {
	tests := []struct {
		name  string
		topic string
		want  DockerEventAction
	}{
		{name: "task create", topic: TaskCreateEventTopic, want: DockerEventActionCreate},
		{name: "task start", topic: TaskStartEventTopic, want: DockerEventActionStart},
		{name: "task exit", topic: TaskExitEventTopic, want: DockerEventActionDie},
		{name: "task delete", topic: TaskDeleteEventTopic, want: DockerEventActionStop},
		{name: "task exec added", topic: TaskExecAddedEventTopic, want: DockerEventActionExecCreate},
		{name: "task exec started", topic: TaskExecStartedEventTopic, want: DockerEventActionExecStart},
		{name: "task paused", topic: TaskPausedEventTopic, want: DockerEventActionPause},
		{name: "task resumed", topic: TaskResumedEventTopic, want: DockerEventActionUnpause},
		{name: "container create", topic: ContainerCreateEventTopic, want: DockerEventActionCreate},
		{name: "container update", topic: ContainerUpdateEventTopic, want: DockerEventActionUpdate},
		{name: "container delete", topic: ContainerDeleteEventTopic, want: DockerEventActionDestroy},
		{name: "image create", topic: ImageCreateEventTopic, want: DockerEventActionPull},
		{name: "image delete", topic: ImageDeleteEventTopic, want: DockerEventActionUntag},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := LookupDockerAction(tt.topic)
			if !ok {
				t.Fatalf("topic %q was not mapped", tt.topic)
			}
			if got != tt.want {
				t.Fatalf("expected action %q for %q, got %q", tt.want, tt.topic, got)
			}
			if got := DockerActionForTopic(tt.topic); got != tt.want {
				t.Fatalf("single-result lookup returned %q for %q", got, tt.topic)
			}
		})
	}
}

func TestDockerActionForTopicMapsKillAndRejectsUnknown(t *testing.T) {
	if got := DockerActionForTopic(TaskOOMEventTopic); got != DockerEventActionKill {
		t.Fatalf("expected OOM topic to map to kill, got %q", got)
	}
	if got, ok := LookupDockerAction("/unsupported/topic"); ok || got != "" {
		t.Fatalf("expected unsupported topic to be absent, got %q, %t", got, ok)
	}
}
