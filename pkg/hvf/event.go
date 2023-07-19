package hvf

import (
	"github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/runtime"
)

// GetTopic converts an event from an interface type to the specific
// event topic id
func GetTopic(e interface{}) string {
	switch e.(type) {
	case *events.TaskCreate:
		return runtime.TaskCreateEventTopic
	case *events.TaskStart:
		return runtime.TaskStartEventTopic
	case *events.TaskOOM:
		return runtime.TaskOOMEventTopic
	case *events.TaskExit:
		return runtime.TaskExitEventTopic
	case *events.TaskDelete:
		return runtime.TaskDeleteEventTopic
	case *events.TaskExecAdded:
		return runtime.TaskExecAddedEventTopic
	case *events.TaskExecStarted:
		return runtime.TaskExecStartedEventTopic
	case *events.TaskPaused:
		return runtime.TaskPausedEventTopic
	case *events.TaskResumed:
		return runtime.TaskResumedEventTopic
	case *events.TaskCheckpointed:
		return runtime.TaskCheckpointedEventTopic
	default:
		log.L.Warnf("no topic for type %#v", e)
	}
	return runtime.TaskUnknownTopic
}
