package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// leaseNamespace is where every node's Lease lives.
const leaseNamespace = corev1.NamespaceNodeLease

// leaseSeconds is how long a Lease says a node is alive after its last
// renewal. The agent renews every node's Lease far more often than
// this, and removes a node itself the moment the node's tab is gone, so
// the Lease running out only ever means the agent died.
const leaseSeconds int32 = 60

// resyncPeriod is how often the pod watch hands every pod over again,
// which is what picks up a change the agent missed.
const resyncPeriod = 5 * time.Minute

// nodeSelector selects the nodes that are tabs.
const nodeSelector = LabelTab + "=" + LabelValueTab

// Cluster is the agent's connection to the API server.
type Cluster struct {
	client kubernetes.Interface
}

// Connect returns a Cluster from a kubeconfig file.
func Connect(kubeconfig string) (*Cluster, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", kubeconfig, err)
	}

	// Every tab's Lease is renewed from here, so the client's default
	// limit of five requests a second would hold a few hundred nodes'
	// heartbeats up.
	config.QPS = clientQPS
	config.Burst = clientBurst

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Cluster{client: client}, nil
}

// The client's own rate limit: requests a second, and how many may go
// at once.
const (
	clientQPS   = 200
	clientBurst = 400
)

// Version returns the API server's version, as "v1.37.2+k3s1".
func (c *Cluster) Version() (string, error) {
	info, err := c.client.Discovery().ServerVersion()
	if err != nil {
		return "", err
	}
	return info.GitVersion, nil
}

// AddNode creates a node and the node's Lease. A name that is taken is
// reported as such, for the caller to try another.
func (c *Cluster) AddNode(ctx context.Context, node *corev1.Node) error {
	created, err := c.client.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	// The API server drops a new object's status, so the status goes in
	// a second request: a patch, which cannot lose a race with the node
	// controller, which writes to every new node the moment it appears.
	// A node that cannot be finished is not left half made.
	patch, err := json.Marshal(map[string]any{"status": node.Status})
	if err == nil {
		_, err = c.client.CoreV1().Nodes().Patch(ctx, node.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}, "status")
	}
	if err != nil {
		c.RemoveNode(context.WithoutCancel(ctx), node.Name)
		return fmt.Errorf("node %s: status: %w", node.Name, err)
	}

	// The Lease is what the node controller reads as a heartbeat. The
	// node owns the Lease, so that deleting the node takes the Lease
	// along even if the agent is not there to do so.
	now := metav1.NowMicro()
	seconds := leaseSeconds
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: leaseNamespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "Node",
				Name:       created.Name,
				UID:        created.UID,
			}},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &created.Name,
			LeaseDurationSeconds: &seconds,
			RenewTime:            &now,
		},
	}
	_, err = c.client.CoordinationV1().Leases(leaseNamespace).Create(ctx, lease, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		c.RemoveNode(context.WithoutCancel(ctx), node.Name)
		return fmt.Errorf("node %s: lease: %w", node.Name, err)
	}
	return nil
}

// IsTaken reports whether an error from AddNode means the name is in
// use.
func IsTaken(err error) bool {
	return apierrors.IsAlreadyExists(err)
}

// RenewLease says a node is still alive.
func (c *Cluster) RenewLease(ctx context.Context, name string) error {
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{"renewTime": metav1.NowMicro()},
	})
	if err != nil {
		return err
	}
	_, err = c.client.CoordinationV1().Leases(leaseNamespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// RemoveNode deletes a node and the node's Lease. A node that is
// already gone is not an error.
func (c *Cluster) RemoveNode(ctx context.Context, name string) error {
	err := c.client.CoordinationV1().Leases(leaseNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	err = c.client.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// TabNodes returns the names of the nodes that are tabs. At start-up
// these are what an earlier agent left behind.
func (c *Cluster) TabNodes(ctx context.Context) ([]string, error) {
	list, err := c.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: nodeSelector})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Items))
	for _, node := range list.Items {
		names = append(names, node.Name)
	}
	return names, nil
}

// PodsOn returns the pods bound to a node.
func (c *Cluster) PodsOn(ctx context.Context, node string) ([]corev1.Pod, error) {
	list, err := c.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", node).String(),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// SetStatus writes a pod's status.
func (c *Cluster) SetStatus(ctx context.Context, pod *corev1.Pod, status corev1.PodStatus) error {
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"uid": pod.UID},
		"status":   status,
	})
	if err != nil {
		return err
	}
	_, err = c.client.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}, "status")
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// RemovePod deletes a pod at once, as a kubelet does when the pod's
// containers are gone. The API server keeps a deleted pod until
// whoever runs the pod says so, and for a tab's pod that is the agent.
func (c *Cluster) RemovePod(ctx context.Context, pod *corev1.Pod) error {
	var now int64
	err := c.client.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &now,
		Preconditions:      &metav1.Preconditions{UID: &pod.UID},
	})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// Event records something that happened to a pod, for `kubectl describe
// pod` to show.
func (c *Cluster) Event(ctx context.Context, pod *corev1.Pod, kind, reason, message string) {
	now := metav1.Now()
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pod.Name + ".",
			Namespace:    pod.Namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Namespace: pod.Namespace,
			Name:      pod.Name,
			UID:       pod.UID,
		},
		Type:           kind,
		Reason:         reason,
		Message:        message,
		Source:         corev1.EventSource{Component: eventSource, Host: pod.Spec.NodeName},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}

	// An event is a note. One that cannot be written is not worth
	// failing what the note is about.
	c.client.CoreV1().Events(pod.Namespace).Create(ctx, event, metav1.CreateOptions{})
}

// eventSource is who the agent's events say they are from.
const eventSource = "tabernetes"

// PodHandler hears of every pod that is bound to a node, as the pod
// appears and changes, and when the pod is gone from the API server.
type PodHandler interface {
	PodChanged(pod *corev1.Pod)
	PodGone(pod *corev1.Pod)
}

// WatchPods tells the handler about every scheduled pod until the
// context ends. The handler picks out the pods on its own nodes: a
// watch cannot select "bound to one of these nodes", and the cluster
// has few pods that are not.
func (c *Cluster) WatchPods(ctx context.Context, handler PodHandler) error {
	factory := informers.NewSharedInformerFactoryWithOptions(c.client, resyncPeriod,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fields.OneTermNotEqualSelector("spec.nodeName", "").String()
		}))
	informer := factory.Core().V1().Pods().Informer()

	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(object any) {
			if pod, ok := object.(*corev1.Pod); ok {
				handler.PodChanged(pod)
			}
		},
		UpdateFunc: func(_, object any) {
			if pod, ok := object.(*corev1.Pod); ok {
				handler.PodChanged(pod)
			}
		},
		DeleteFunc: func(object any) {
			// A delete the watch missed arrives as a tombstone holding
			// the last pod the watch saw.
			if tombstone, ok := object.(cache.DeletedFinalStateUnknown); ok {
				object = tombstone.Obj
			}
			if pod, ok := object.(*corev1.Pod); ok {
				handler.PodGone(pod)
			}
		},
	})
	if err != nil {
		return err
	}

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("the pod watch did not start")
	}
	return nil
}
