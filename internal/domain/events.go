package domain

type DockerEventAction string

const (
	DockerEventActionCreate     DockerEventAction = "create"
	DockerEventActionStart      DockerEventAction = "start"
	DockerEventActionDie        DockerEventAction = "die"
	DockerEventActionStop       DockerEventAction = "stop"
	DockerEventActionKill       DockerEventAction = "kill"
	DockerEventActionExecCreate DockerEventAction = "exec_create"
	DockerEventActionExecStart  DockerEventAction = "exec_start"
	DockerEventActionPause      DockerEventAction = "pause"
	DockerEventActionUnpause    DockerEventAction = "unpause"
	DockerEventActionDestroy    DockerEventAction = "destroy"
	DockerEventActionPull       DockerEventAction = "pull"
	DockerEventActionUntag      DockerEventAction = "untag"
	DockerEventActionUpdate     DockerEventAction = "update"
)

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
	TaskCreateEventTopic      = "/tasks/create"
	TaskStartEventTopic       = "/tasks/start"
	TaskExitEventTopic        = "/tasks/exit"
	TaskDeleteEventTopic      = "/tasks/delete"
	TaskExecAddedEventTopic   = "/tasks/exec-added"
	TaskExecStartedEventTopic = "/tasks/exec-started"
	TaskPausedEventTopic      = "/tasks/paused"
	TaskResumedEventTopic     = "/tasks/resumed"
	TaskOOMEventTopic         = "/tasks/oom"
	ContainerCreateEventTopic = "/containers/create"
	ContainerUpdateEventTopic = "/containers/update"
	ContainerDeleteEventTopic = "/containers/delete"
	ImageCreateEventTopic     = "/images/create"
	ImageDeleteEventTopic     = "/images/delete"
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

func DockerActionForTopic(topic string) DockerEventAction { return containerdEventActions[topic] }

func LookupDockerAction(topic string) (DockerEventAction, bool) {
	action, ok := containerdEventActions[topic]
	return action, ok
}
