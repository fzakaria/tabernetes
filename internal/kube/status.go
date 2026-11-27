package kube

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Stage is where a pod's container is on the way from scheduled to
// over.
type Stage int

const (
	// StageCreating: the image is being fetched, or the tab has not
	// booted the guest yet.
	StageCreating Stage = iota
	// StageRunning: the program is running in the tab.
	StageRunning
	// StageExited: the program ended, and is not going to be started
	// again.
	StageExited
)

// The reasons a container's state gives, as a kubelet words them.
const (
	ReasonCreating  = "ContainerCreating"
	ReasonCompleted = "Completed"
	ReasonError     = "Error"
)

// containerIDPrefix makes a container's id the URL a kubelet reports:
// the runtime's name, then the id.
const containerIDPrefix = "tabc://"

// Snapshot is everything a pod's status is made from.
type Snapshot struct {
	// Container and Image are the pod's one container.
	Container string
	Image     string
	// ContainerID is the container's name in tabc.
	ContainerID string

	Stage Stage
	// Reason and Message say why the container is waiting or how the
	// container ended, when there is more to say than the stage.
	Reason  string
	Message string

	// PodIP and HostIP are the pod's address and the agent's machine's.
	PodIP  string
	HostIP string

	// Scheduled is when the agent first saw the pod, Started when the
	// program started and Finished when the program ended.
	Scheduled metav1.Time
	Started   metav1.Time
	Finished  metav1.Time
	ExitCode  int32
	// Restarts is how many times the program was started again.
	Restarts int32
}

// Status returns the PodStatus a kubelet would report for a pod whose
// one container is as the snapshot says.
func Status(s Snapshot) corev1.PodStatus {
	container := corev1.ContainerStatus{
		Name:         s.Container,
		Image:        s.Image,
		RestartCount: s.Restarts,
	}
	if s.ContainerID != "" {
		container.ContainerID = containerIDPrefix + s.ContainerID
	}

	// The phase and the container's state, by stage.
	phase := corev1.PodPending
	ready := corev1.ConditionFalse
	switch s.Stage {
	case StageCreating:
		reason := s.Reason
		if reason == "" {
			reason = ReasonCreating
		}
		container.State.Waiting = &corev1.ContainerStateWaiting{Reason: reason, Message: s.Message}
	case StageRunning:
		phase = corev1.PodRunning
		ready = corev1.ConditionTrue
		started := true
		container.Ready = true
		container.Started = &started
		container.State.Running = &corev1.ContainerStateRunning{StartedAt: s.Started}
	case StageExited:
		phase = corev1.PodFailed
		reason := ReasonError
		if s.ExitCode == 0 {
			phase = corev1.PodSucceeded
			reason = ReasonCompleted
		}
		if s.Reason != "" {
			reason = s.Reason
		}
		container.State.Terminated = &corev1.ContainerStateTerminated{
			ExitCode:    s.ExitCode,
			Reason:      reason,
			Message:     s.Message,
			StartedAt:   s.Started,
			FinishedAt:  s.Finished,
			ContainerID: container.ContainerID,
		}
	}

	condition := func(kind corev1.PodConditionType, status corev1.ConditionStatus) corev1.PodCondition {
		return corev1.PodCondition{Type: kind, Status: status, LastTransitionTime: s.Scheduled}
	}
	status := corev1.PodStatus{
		Phase:  phase,
		HostIP: s.HostIP,
		PodIP:  s.PodIP,
		Conditions: []corev1.PodCondition{
			condition(corev1.PodScheduled, corev1.ConditionTrue),
			condition(corev1.PodInitialized, corev1.ConditionTrue),
			condition(corev1.ContainersReady, ready),
			condition(corev1.PodReady, ready),
		},
		ContainerStatuses: []corev1.ContainerStatus{container},
	}
	if !s.Scheduled.IsZero() {
		status.StartTime = &s.Scheduled
	}
	if s.HostIP != "" {
		status.HostIPs = []corev1.HostIP{{IP: s.HostIP}}
	}
	if s.PodIP != "" {
		status.PodIPs = []corev1.PodIP{{IP: s.PodIP}}
	}
	if s.Stage == StageExited {
		status.Reason = container.State.Terminated.Reason
		status.Message = s.Message
	}
	return status
}
