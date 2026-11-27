// Package kube is the cluster's side of a tab: the Node a tab becomes,
// the Lease that says the node is alive, the status of the pods the node
// runs, and the calls to the API server that write each of them.
package kube

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fzakaria/tabernetes/internal/tab"
)

// The labels and the taint every tab's node carries. The RuntimeClass
// in deploy/ selects the label and tolerates the taint, so only pods
// that ask for a tab are scheduled onto one.
const (
	LabelTab      = "tabernetes.dev/tab"
	LabelPlatform = "tabernetes.dev/platform"
	LabelBrowser  = "tabernetes.dev/browser"
	LabelModel    = "tabernetes.dev/model"
	LabelResident = "tabernetes.dev/resident"
	LabelValueTab = "true"
	TaintTab      = "tabernetes.dev/tab"
)

// What the guest in a tab is: unkernel emulates an aarch64 machine and
// runs Linux on it, whatever the device under the browser.
const (
	guestArch = "arm64"
	guestOS   = "linux"
)

// The reasons a node's conditions give, as a kubelet's are worded.
const (
	reasonTabOpen       = "TabOpen"
	reasonNoPressure    = "TabHasNoPressure"
	messageTabOpen      = "the tab is open and answering"
	messageNoPressure   = "a tab reports no pressure"
	conditionStatusTrue = corev1.ConditionTrue
)

// NodeConfig is what every node of this agent has in common.
type NodeConfig struct {
	// Address is where the API server reaches the agent for a pod's
	// logs: the machine the agent runs on.
	Address string
	// KubeletPort is the port the agent answers such requests on.
	KubeletPort int32
	// Version is what the node gives as its kubelet's version: the API
	// server's own, since other components read the field as a
	// Kubernetes version.
	Version string
	// RuntimeVersion names the container runtime, as "tabc://1.0.0".
	RuntimeVersion string
	// KernelVersion is the guest kernel's version.
	KernelVersion string
	// CPU, Memory and Pods are what one tab offers the scheduler.
	CPU    resource.Quantity
	Memory resource.Quantity
	Pods   resource.Quantity
}

// Node returns the Node a tab becomes: named, labelled with what the
// browser said the tab runs on and with being a resident if the tab is
// one, tainted so that only pods asking for a tab land on the node, and
// ready.
func Node(name string, hello tab.Hello, origin tab.Origin, config NodeConfig, now metav1.Time) *corev1.Node {
	resources := corev1.ResourceList{
		corev1.ResourceCPU:    config.CPU,
		corev1.ResourceMemory: config.Memory,
		corev1.ResourcePods:   config.Pods,
	}

	// The conditions a kubelet reports, so that the node reads like any
	// other in `kubectl describe node`.
	condition := func(kind corev1.NodeConditionType, status corev1.ConditionStatus, reason, message string) corev1.NodeCondition {
		return corev1.NodeCondition{
			Type:               kind,
			Status:             status,
			Reason:             reason,
			Message:            message,
			LastHeartbeatTime:  now,
			LastTransitionTime: now,
		}
	}

	labels := map[string]string{
		corev1.LabelHostname:   name,
		corev1.LabelOSStable:   guestOS,
		corev1.LabelArchStable: guestArch,
		LabelTab:               LabelValueTab,
		LabelPlatform:          tab.LabelValue(hello.Platform),
		LabelBrowser:           tab.LabelValue(hello.Browser),
		LabelModel:             tab.LabelValue(hello.Model),
	}
	if origin == tab.Resident {
		labels[LabelResident] = LabelValueTab
	}

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{{
				Key:    TaintTab,
				Value:  LabelValueTab,
				Effect: corev1.TaintEffectNoSchedule,
			}},
		},
		Status: corev1.NodeStatus{
			Capacity:    resources,
			Allocatable: resources,
			Conditions: []corev1.NodeCondition{
				condition(corev1.NodeReady, conditionStatusTrue, reasonTabOpen, messageTabOpen),
				condition(corev1.NodeMemoryPressure, corev1.ConditionFalse, reasonNoPressure, messageNoPressure),
				condition(corev1.NodeDiskPressure, corev1.ConditionFalse, reasonNoPressure, messageNoPressure),
				condition(corev1.NodePIDPressure, corev1.ConditionFalse, reasonNoPressure, messageNoPressure),
			},
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: config.Address},
				{Type: corev1.NodeHostName, Address: name},
			},
			DaemonEndpoints: corev1.NodeDaemonEndpoints{
				KubeletEndpoint: corev1.DaemonEndpoint{Port: config.KubeletPort},
			},
			NodeInfo: corev1.NodeSystemInfo{
				Architecture:            guestArch,
				OperatingSystem:         guestOS,
				OSImage:                 hello.Describe(),
				KernelVersion:           config.KernelVersion,
				ContainerRuntimeVersion: config.RuntimeVersion,
				KubeletVersion:          config.Version,
				KubeProxyVersion:        config.Version,
			},
		},
	}
}
