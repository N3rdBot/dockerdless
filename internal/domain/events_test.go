package domain

import "testing"

func TestDockerActionForTopic(t *testing.T) {
	tests := map[string]DockerEventAction{
		TaskCreateEventTopic: DockerEventActionCreate, TaskStartEventTopic: DockerEventActionStart,
		TaskExitEventTopic: DockerEventActionDie, TaskDeleteEventTopic: DockerEventActionStop,
		TaskExecAddedEventTopic: DockerEventActionExecCreate, TaskExecStartedEventTopic: DockerEventActionExecStart,
		TaskPausedEventTopic: DockerEventActionPause, TaskResumedEventTopic: DockerEventActionUnpause,
		TaskOOMEventTopic: DockerEventActionKill, ContainerCreateEventTopic: DockerEventActionCreate,
		ContainerUpdateEventTopic: DockerEventActionUpdate, ContainerDeleteEventTopic: DockerEventActionDestroy,
		ImageCreateEventTopic: DockerEventActionPull, ImageDeleteEventTopic: DockerEventActionUntag,
	}
	for topic, want := range tests {
		t.Run(topic, func(t *testing.T) {
			got, ok := LookupDockerAction(topic)
			if !ok || got != want || DockerActionForTopic(topic) != want {
				t.Fatalf("topic %q mapped to %q, present=%t", topic, got, ok)
			}
		})
	}
	if got, ok := LookupDockerAction("/unsupported/topic"); ok || got != "" {
		t.Fatalf("unknown topic mapped to %q, present=%t", got, ok)
	}
}
