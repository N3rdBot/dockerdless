package domain

// DockerEventAction is the action exposed by Docker's events API.
type DockerEventAction string

const (
	// DockerEventActionCreate records resource creation.
	DockerEventActionCreate DockerEventAction = "create"
	// DockerEventActionStart records a task start.
	DockerEventActionStart DockerEventAction = "start"
	// DockerEventActionDie records a task exit.
	DockerEventActionDie DockerEventAction = "die"
	// DockerEventActionStop records task deletion after a stop.
	DockerEventActionStop DockerEventAction = "stop"
	// DockerEventActionKill records an out-of-memory task termination.
	DockerEventActionKill DockerEventAction = "kill"
	// DockerEventActionExecCreate records an exec process being added.
	DockerEventActionExecCreate DockerEventAction = "exec_create"
	// DockerEventActionExecStart records an exec process start.
	DockerEventActionExecStart DockerEventAction = "exec_start"
	// DockerEventActionPause records a task pause.
	DockerEventActionPause DockerEventAction = "pause"
	// DockerEventActionUnpause records a task resume.
	DockerEventActionUnpause DockerEventAction = "unpause"
	// DockerEventActionDestroy records container deletion.
	DockerEventActionDestroy DockerEventAction = "destroy"
	// DockerEventActionPull records image creation or pulling.
	DockerEventActionPull DockerEventAction = "pull"
	// DockerEventActionUntag records image deletion or untagging.
	DockerEventActionUntag DockerEventAction = "untag"
	// DockerEventActionUpdate records container metadata updates.
	DockerEventActionUpdate DockerEventAction = "update"
)

// EventAction is retained as a short name for Docker event actions.
type EventAction = DockerEventAction

const (
	ActionCreate     = DockerEventActionCreate
	ActionStart      = DockerEventActionStart
	ActionDie        = DockerEventActionDie
	ActionStop       = DockerEventActionStop
	ActionKill       = DockerEventActionKill
	ActionExecCreate = DockerEventActionExecCreate
	ActionExecStart  = DockerEventActionExecStart
	ActionPause      = DockerEventActionPause
	ActionUnpause    = DockerEventActionUnpause
	ActionDestroy    = DockerEventActionDestroy
	ActionPull       = DockerEventActionPull
	ActionUntag      = DockerEventActionUntag
	ActionUpdate     = DockerEventActionUpdate
)

const (
	// TaskCreateEventTopic is containerd's task-create topic.
	TaskCreateEventTopic = "/tasks/create"
	// TaskStartEventTopic is containerd's task-start topic.
	TaskStartEventTopic = "/tasks/start"
	// TaskExitEventTopic is containerd's task-exit topic.
	TaskExitEventTopic = "/tasks/exit"
	// TaskDeleteEventTopic is containerd's task-delete topic.
	TaskDeleteEventTopic = "/tasks/delete"
	// TaskExecAddedEventTopic is containerd's exec-create topic.
	TaskExecAddedEventTopic = "/tasks/exec-added"
	// TaskExecStartedEventTopic is containerd's exec-start topic.
	TaskExecStartedEventTopic = "/tasks/exec-started"
	// TaskPausedEventTopic is containerd's task-pause topic.
	TaskPausedEventTopic = "/tasks/paused"
	// TaskResumedEventTopic is containerd's task-resume topic.
	TaskResumedEventTopic = "/tasks/resumed"
	// TaskOOMEventTopic is containerd's out-of-memory task topic.
	TaskOOMEventTopic = "/tasks/oom"
	// ContainerCreateEventTopic is containerd's container-create topic.
	ContainerCreateEventTopic = "/containers/create"
	// ContainerUpdateEventTopic is containerd's container-update topic.
	ContainerUpdateEventTopic = "/containers/update"
	// ContainerDeleteEventTopic is containerd's container-delete topic.
	ContainerDeleteEventTopic = "/containers/delete"
	// ImageCreateEventTopic is containerd's image-create topic.
	ImageCreateEventTopic = "/images/create"
	// ImageDeleteEventTopic is containerd's image-delete topic.
	ImageDeleteEventTopic = "/images/delete"
)

var containerdEventActions = map[string]DockerEventAction{
	TaskCreateEventTopic:      DockerEventActionCreate,
	TaskStartEventTopic:       DockerEventActionStart,
	TaskExitEventTopic:        DockerEventActionDie,
	TaskDeleteEventTopic:      DockerEventActionStop,
	TaskExecAddedEventTopic:   DockerEventActionExecCreate,
	TaskExecStartedEventTopic: DockerEventActionExecStart,
	TaskPausedEventTopic:      DockerEventActionPause,
	TaskResumedEventTopic:     DockerEventActionUnpause,
	TaskOOMEventTopic:         DockerEventActionKill,
	ContainerCreateEventTopic: DockerEventActionCreate,
	ContainerUpdateEventTopic: DockerEventActionUpdate,
	ContainerDeleteEventTopic: DockerEventActionDestroy,
	ImageCreateEventTopic:     DockerEventActionPull,
	ImageDeleteEventTopic:     DockerEventActionUntag,
}

// DockerActionForTopic returns the Docker action for a containerd event topic.
// An empty action means that the topic is not part of the bridge contract.
func DockerActionForTopic(topic string) DockerEventAction {
	return containerdEventActions[topic]
}

// LookupDockerAction returns the Docker action and whether the topic is known.
func LookupDockerAction(topic string) (DockerEventAction, bool) {
	action, ok := containerdEventActions[topic]
	return action, ok
}
