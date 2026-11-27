package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fzakaria/tabernetes/internal/kube"
	"github.com/fzakaria/tabernetes/internal/runtime"
)

// bootTimeout is how long a tab gets to boot the guest and start the
// program, from when its page is told of the container.
const bootTimeout = 2 * time.Minute

// pollEvery is how often a run asks whether its container has started,
// and listingAge how old the shared `tabc list` may be before a run
// asks tabc again.
const (
	pollEvery  = 500 * time.Millisecond
	listingAge = 400 * time.Millisecond
)

// The wait before a program that ended is started again: the first
// wait, and the longest, with each wait double the one before.
const (
	restartDelayFirst = 2 * time.Second
	restartDelayMost  = time.Minute
)

// The reasons the agent gives for a container that is not running, as
// a kubelet words them, and the types of event.
const (
	reasonUnsupported = "UnsupportedPod"
	reasonImagePull   = "ErrImagePull"
	reasonSandbox     = "FailedCreatePodSandBox"
	reasonStart       = "StartError"
	reasonBoot        = "TabDidNotBoot"
	reasonBackOff     = "CrashLoopBackOff"
	reasonSkipped     = "PartlyUnsupported"
	reasonStarted     = "Started"
	eventNormal       = corev1.EventTypeNormal
	eventWarning      = corev1.EventTypeWarning
	exitFailed        = 1
)

// The files of a pod's directory.
const (
	podsDir     = "pods"
	logFile     = "log"
	podDirMode  = 0o755
	logFileMode = 0o644
)

// run is one pod being run on one node.
type run struct {
	pod  *corev1.Pod
	node *Node
	// id is the container's name in tabc, and the secret part of the
	// address the tab loads the container's page from.
	id string

	// cancel stops the run, and done is closed when nothing of the run
	// is left.
	cancel context.CancelFunc
	done   chan struct{}

	// proxy carries the tab's requests to the container's page, for as
	// long as there is a container.
	pageMu sync.Mutex
	proxy  *httputil.ReverseProxy
}

// pull is one image being fetched, or fetched.
type pull struct {
	done chan struct{}
	err  error
}

// listing is a `tabc list` and when it was taken.
type listing struct {
	mu         sync.Mutex
	taken      time.Time
	containers map[string]runtime.Listed
}

// PodChanged hears of a pod that is new or changed. A pod bound to one
// of the agent's nodes is started if it is not running, and stopped if
// it is being deleted.
func (a *Agent) PodChanged(pod *corev1.Pod) {
	a.mu.Lock()
	defer a.mu.Unlock()

	node := a.nodes[pod.Spec.NodeName]
	if node == nil {
		return
	}
	running := a.runs[pod.UID]

	switch {
	case pod.DeletionTimestamp != nil && running != nil:
		// The run removes the pod once its container is gone.
		running.cancel()
	case pod.DeletionTimestamp != nil:
		// Nothing of the pod is running. The API server keeps a deleted
		// pod until its node says the pod is gone.
		go a.config.Cluster.RemovePod(context.WithoutCancel(a.ctx), pod)
	case running != nil:
	case pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed:
	default:
		a.start(pod, node)
	}
}

// PodGone hears of a pod the API server no longer has.
func (a *Agent) PodGone(pod *corev1.Pod) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if running := a.runs[pod.UID]; running != nil {
		running.cancel()
	}
}

// start begins a pod's run. The caller holds the agent's lock.
func (a *Agent) start(pod *corev1.Pod, node *Node) {
	ctx, cancel := context.WithCancel(a.ctx)
	started := &run{
		pod:    pod.DeepCopy(),
		node:   node,
		id:     randomHex(containerIDBytes),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	a.runs[pod.UID] = started
	a.containers[started.id] = started
	go a.supervise(ctx, started)
}

// supervise runs a pod to its end and clears the run away. A run that
// was stopped, by the pod's deletion or the node's loss, ends with the
// pod removed from the API server.
func (a *Agent) supervise(ctx context.Context, r *run) {
	defer close(r.done)
	defer r.cancel()
	name := r.pod.Namespace + "/" + r.pod.Name
	a.log.Printf("pod %s: starting on %s as %s", name, r.node.Name, r.id)

	a.runPod(ctx, r)

	a.mu.Lock()
	delete(a.runs, r.pod.UID)
	delete(a.containers, r.id)
	a.mu.Unlock()

	if ctx.Err() == nil {
		a.log.Printf("pod %s: ended", name)
		return
	}
	background, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
	defer cancel()
	if err := a.config.Cluster.RemovePod(background, r.pod); err != nil {
		a.log.Printf("pod %s: removing it: %v", name, err)
	}
	a.log.Printf("pod %s: stopped", name)
}

// runPod takes a pod from scheduled to over: the image, the sandbox,
// then the container in the node's tab, started again as the pod's
// restart policy says, with the pod's status written at every step.
func (a *Agent) runPod(ctx context.Context, r *run) {
	container := r.pod.Spec.Containers[0]
	snapshot := kube.Snapshot{
		Container:   container.Name,
		Image:       container.Image,
		ContainerID: r.id,
		HostIP:      a.config.Node.Address,
		Scheduled:   metav1.Now(),
	}

	// Status and events are written with a context of their own: the
	// last ones are written after the run's context has ended.
	report := func() {
		background, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
		defer cancel()
		if err := a.config.Cluster.SetStatus(background, r.pod, kube.Status(snapshot)); err != nil {
			a.log.Printf("pod %s/%s: status: %v", r.pod.Namespace, r.pod.Name, err)
		}
	}
	event := func(kind, reason, message string) {
		background, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
		defer cancel()
		a.config.Cluster.Event(background, r.pod, kind, reason, message)
	}
	fail := func(reason string, err error) {
		snapshot.Stage = kube.StageExited
		snapshot.ExitCode = exitFailed
		snapshot.Reason = reason
		snapshot.Message = err.Error()
		snapshot.Finished = metav1.Now()
		report()
		event(eventWarning, reason, err.Error())
	}
	report()

	// What tabc is going to be told.
	podDir := filepath.Join(a.config.StateDir, podsDir, string(r.pod.UID))
	plan, err := runtime.PlanFor(r.pod, r.id, podDir, a.config.Limits)
	if err != nil {
		fail(reasonUnsupported, err)
		return
	}
	if len(plan.Skipped) > 0 {
		event(eventWarning, reasonSkipped, "a tab leaves out: "+strings.Join(plan.Skipped, ", "))
	}

	// The pod's directory, with its volumes and its log.
	defer os.RemoveAll(podDir)
	for _, dir := range append([]string{podDir}, plan.EmptyDirs...) {
		if err := os.MkdirAll(dir, podDirMode); err != nil {
			fail(reasonStart, err)
			return
		}
	}
	log, err := os.OpenFile(filepath.Join(podDir, logFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFileMode)
	if err != nil {
		fail(reasonStart, err)
		return
	}
	defer log.Close()

	if err := a.pullOnce(ctx, plan.Image); err != nil {
		if ctx.Err() == nil {
			fail(reasonImagePull, err)
		}
		return
	}

	// The sandbox, which gives the pod its address.
	sandbox, err := a.config.Network.Create(ctx, sandboxPrefix+r.id[:sandboxIDLength], r.pod.Namespace)
	if err != nil {
		if ctx.Err() == nil {
			fail(reasonSandbox, err)
		}
		return
	}
	defer func() {
		if err := sandbox.Destroy(context.WithoutCancel(ctx)); err != nil {
			a.log.Printf("pod %s/%s: %v", r.pod.Namespace, r.pod.Name, err)
		}
	}()
	snapshot.PodIP = sandbox.IP
	report()

	// The container, as many times as the restart policy asks.
	delay := restartDelayFirst
	for {
		exit, ran := a.runContainer(ctx, r, sandbox, plan, log, &snapshot, report, fail)
		if !ran || ctx.Err() != nil {
			return
		}

		policy := r.pod.Spec.RestartPolicy
		again := policy == corev1.RestartPolicyAlways || (policy == corev1.RestartPolicyOnFailure && exit != 0)
		snapshot.ExitCode = int32(exit)
		snapshot.Finished = metav1.Now()
		if !again {
			snapshot.Stage = kube.StageExited
			report()
			return
		}

		// Waiting to start again, for longer each time.
		snapshot.Stage = kube.StageCreating
		snapshot.Reason = reasonBackOff
		snapshot.Message = fmt.Sprintf("the program exited with status %d; starting it again in %s", exit, delay)
		snapshot.Restarts++
		report()
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
		delay = min(delay*2, restartDelayMost)
		snapshot.Reason, snapshot.Message = "", ""
	}
}

// runContainer runs the pod's program once: tabc in the sandbox, the
// page in the node's tab, and a wait for the program to end. It returns
// the program's exit status, and whether there is one: a container that
// could not be started, or was stopped, has none.
func (a *Agent) runContainer(ctx context.Context, r *run, sandbox *runtime.Sandbox, plan runtime.Plan, log *os.File,
	snapshot *kube.Snapshot, report func(), fail func(string, error)) (exit int, ran bool) {
	container, err := a.config.Tabc.Start(sandbox, plan.Args, log)
	if err != nil {
		fail(reasonStart, err)
		return 0, false
	}

	// Whatever happens next, the container and its page are cleared
	// away before this returns.
	r.setPage(container.Link, sandbox)
	defer func() {
		r.clearPage()
		r.node.tell(Message{Type: MessageStop, ID: r.id})
		container.Stop()
		a.removeContainer(context.WithoutCancel(ctx), r.id)
	}()

	// The tab loads the page, which boots the guest and starts the
	// program.
	r.node.tell(Message{
		Type:  MessageRun,
		ID:    r.id,
		Path:  containerPath(r.id),
		Pod:   r.pod.Namespace + "/" + r.pod.Name,
		Image: plan.Image,
	})
	if err := a.waitRunning(ctx, r.id, container); err != nil {
		if ctx.Err() == nil {
			fail(reasonBoot, err)
		}
		return 0, false
	}
	snapshot.Stage = kube.StageRunning
	snapshot.Started = metav1.Now()
	report()

	select {
	case <-container.Done():
		return container.Exit(), true
	case <-ctx.Done():
		return 0, false
	}
}

// waitRunning waits until tabc says the container's program runs.
func (a *Agent) waitRunning(ctx context.Context, id string, container *runtime.Container) error {
	deadline := time.After(bootTimeout)
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if a.listed(ctx)[id].Status == runtime.StatusRunning {
				return nil
			}
		case <-container.Done():
			// A program that ran and ended between two looks did run.
			if container.Exit() == 0 {
				return nil
			}
			return fmt.Errorf("the container ended with status %d before its program was seen running", container.Exit())
		case <-deadline:
			return fmt.Errorf("the tab did not start the program in %s", bootTimeout)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// listed returns tabc's containers by name, from a `tabc list` no older
// than listingAge. Every run that is waiting shares the one list.
func (a *Agent) listed(ctx context.Context) map[string]runtime.Listed {
	a.listing.mu.Lock()
	defer a.listing.mu.Unlock()
	if time.Since(a.listing.taken) < listingAge {
		return a.listing.containers
	}

	containers, err := a.config.Tabc.List(ctx)
	if err != nil {
		a.log.Printf("%v", err)
		return a.listing.containers
	}
	a.listing.containers = containers
	a.listing.taken = time.Now()
	return containers
}

// removeContainer deletes what tabc still has of a container. After a
// clean end there is nothing; after a kill there is a supervisor.
func (a *Agent) removeContainer(ctx context.Context, name string) {
	containers, err := a.config.Tabc.List(ctx)
	if err != nil {
		a.log.Printf("%v", err)
		return
	}
	container, has := containers[name]
	if !has {
		return
	}
	if err := a.config.Tabc.Remove(ctx, container.ID); err != nil {
		a.log.Printf("%v", err)
	}
}

// pullOnce fetches an image the first time a pod needs it. Pods that
// need the same image meanwhile wait for the one fetch, and a fetch
// that failed is tried again by the next pod.
func (a *Agent) pullOnce(ctx context.Context, image string) error {
	a.mu.Lock()
	fetch := a.pulls[image]
	if fetch == nil {
		fetch = &pull{done: make(chan struct{})}
		a.pulls[image] = fetch
		go func() {
			a.log.Printf("pulling %s", image)
			fetch.err = a.config.Tabc.Pull(context.WithoutCancel(ctx), image)
			if fetch.err != nil {
				a.mu.Lock()
				delete(a.pulls, image)
				a.mu.Unlock()
			}
			close(fetch.done)
		}()
	}
	a.mu.Unlock()

	select {
	case <-fetch.done:
		return fetch.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// setPage records where the run's container page is: the address tabc
// serves the page on, and the sandbox the address is reached from.
func (r *run) setPage(link *url.URL, sandbox *runtime.Sandbox) {
	proxy := newPageProxy(r.id, link, sandbox)
	r.pageMu.Lock()
	defer r.pageMu.Unlock()
	r.proxy = proxy
}

// clearPage records that the run has no container page, and closes the
// connections kept to the one it had.
func (r *run) clearPage() {
	r.pageMu.Lock()
	proxy := r.proxy
	r.proxy = nil
	r.pageMu.Unlock()

	if proxy == nil {
		return
	}
	if transport, ok := proxy.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

// page returns what carries requests to the run's container page, or
// nil when there is no container.
func (r *run) page() *httputil.ReverseProxy {
	r.pageMu.Lock()
	defer r.pageMu.Unlock()
	return r.proxy
}

// containerPrefix starts the path of every container's page.
const containerPrefix = "/c/"

// containerPath returns the path a tab loads a container's page from.
func containerPath(id string) string {
	return containerPrefix + id + "/"
}
