package kube

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fzakaria/tabernetes/internal/tab"
)

// TestNode checks the Node a tab becomes: ready, an arm64 Linux machine
// whatever the device, labelled and tainted as a tab, offering what the
// agent was configured with, and described by what the browser said.
func TestNode(t *testing.T) {
	hello := tab.Hello{Model: "Pixel 9", Platform: "Android", PlatformVersion: "16", Browser: "Chrome", BrowserVersion: "142"}
	config := NodeConfig{
		Address:        "10.0.0.1",
		KubeletPort:    10260,
		Version:        "v1.37.2+k3s1",
		RuntimeVersion: "tabc://1.0.0",
		KernelVersion:  "7.3.4-unkernel",
		CPU:            resource.MustParse("1"),
		Memory:         resource.MustParse("256Mi"),
		Pods:           resource.MustParse("1"),
	}
	node := Node("pixel-9-3fa2", hello, config, metav1.NewTime(time.Unix(1, 0)))

	if node.Name != "pixel-9-3fa2" {
		t.Errorf("name %q", node.Name)
	}
	wantLabels := map[string]string{
		corev1.LabelArchStable: "arm64",
		corev1.LabelOSStable:   "linux",
		LabelTab:               "true",
		LabelPlatform:          "Android",
		LabelBrowser:           "Chrome",
		LabelModel:             "Pixel-9",
	}
	for key, want := range wantLabels {
		if got := node.Labels[key]; got != want {
			t.Errorf("label %s = %q, want %q", key, got, want)
		}
	}
	if len(node.Spec.Taints) != 1 || node.Spec.Taints[0].Key != TaintTab || node.Spec.Taints[0].Effect != corev1.TaintEffectNoSchedule {
		t.Errorf("taints %+v", node.Spec.Taints)
	}

	ready := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		t.Error("the node is not ready")
	}
	if got := node.Status.Allocatable.Pods().Value(); got != 1 {
		t.Errorf("allocatable pods %d", got)
	}
	info := node.Status.NodeInfo
	if info.OSImage != "Chrome 142 on Android 16" || info.Architecture != "arm64" || info.ContainerRuntimeVersion != "tabc://1.0.0" {
		t.Errorf("node info %+v", info)
	}
	if node.Status.DaemonEndpoints.KubeletEndpoint.Port != 10260 {
		t.Errorf("kubelet port %d", node.Status.DaemonEndpoints.KubeletEndpoint.Port)
	}
}

// TestStatus checks the phase and the container's state at each stage:
// pending and waiting while the container is made, running and ready
// once the program runs, and succeeded or failed by the exit status.
func TestStatus(t *testing.T) {
	base := Snapshot{Container: "main", Image: "alpine", ContainerID: "abc", PodIP: "10.244.128.2", HostIP: "10.0.0.1"}

	creating := Status(base)
	if creating.Phase != corev1.PodPending || creating.ContainerStatuses[0].State.Waiting == nil {
		t.Errorf("creating: %+v", creating)
	}
	if creating.ContainerStatuses[0].State.Waiting.Reason != ReasonCreating {
		t.Errorf("creating reason %q", creating.ContainerStatuses[0].State.Waiting.Reason)
	}

	running := base
	running.Stage = StageRunning
	status := Status(running)
	if status.Phase != corev1.PodRunning || !status.ContainerStatuses[0].Ready || status.PodIP != "10.244.128.2" {
		t.Errorf("running: %+v", status)
	}
	if status.ContainerStatuses[0].ContainerID != "tabc://abc" {
		t.Errorf("container id %q", status.ContainerStatuses[0].ContainerID)
	}
	for _, condition := range status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status != corev1.ConditionTrue {
			t.Error("a running pod is not ready")
		}
	}

	succeeded := base
	succeeded.Stage = StageExited
	if got := Status(succeeded); got.Phase != corev1.PodSucceeded || got.ContainerStatuses[0].State.Terminated.Reason != ReasonCompleted {
		t.Errorf("exit 0: %+v", got)
	}

	failed := base
	failed.Stage = StageExited
	failed.ExitCode = 3
	if got := Status(failed); got.Phase != corev1.PodFailed || got.ContainerStatuses[0].State.Terminated.ExitCode != 3 {
		t.Errorf("exit 3: %+v", got)
	}
}
