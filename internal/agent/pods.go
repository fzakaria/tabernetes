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
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fzakaria/tabernetes/internal/kube"
	"github.com/fzakaria/tabernetes/internal/runtime"
	"github.com/fzakaria/tabernetes/internal/tab"
)

// bootTimeout is how long a tab gets to boot the guest and start the
// program, from when its page is told of the container.
const bootTimeout = 2 * time.Minute

// pollEvery is how often a run asks whether its container has started,
// and listingAge how old the shared `tabc list` may be before a run
// asks tabc again. A boot's time is measured no finer than this.
const (
	pollEvery  = 250 * time.Millisecond
	listingAge = 200 * time.Millisecond
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

// Where pods' directories are kept under the agent's state directory,
// and the modes of what the agent makes there.
const (
	podsDir     = "pods"
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

	// What the site shows of the run: the pod's address, whether its
	// program is running, and how long the tab took to start it.
	ip      string
	running bool
	boot    time.Duration

	// down and up count the bytes that have gone to the tab and come
	// from it, over every container of the run, and carried is what
	// the page's connections report those bytes to.
	down    atomic.Int64
	up      atomic.Int64
	carried func(down, up int64)
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
		// A pod for a tab, on a node that is no tab the agent has: the
		// tab went as the scheduler chose it, or while no agent was
		// running. Nothing will ever run the pod, so the pod is removed
		// for its controller to make another.
		if class := pod.Spec.RuntimeClassName; class != nil && *class == a.config.RuntimeClass {
			go a.config.Cluster.RemovePod(context.WithoutCancel(a.ctx), pod)
		}
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

// PodGone hears of a pod the API server no longer has. Whatever of the
// pod is still running is stopped, and the pod's log goes with it.
func (a *Agent) PodGone(pod *corev1.Pod) {
	a.mu.Lock()
	running := a.runs[pod.UID]
	a.mu.Unlock()
	if running != nil {
		running.cancel()
	}
	a.forgetLog(pod.Namespace, pod.Name, string(pod.UID))
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
	started.carried = func(down, up int64) {
		started.down.Add(down)
		started.up.Add(up)
		a.ledger.carried(down + up)
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

// failure is why a container is not running, in a kubelet's words and
// in the agent's.
type failure struct {
	reason string
	err    error
}

// runPod takes a pod from scheduled to over: the image, the sandbox,
// then the container in the node's tab, with the pod's status written
// at every step.
//
// What happens when a step fails, or the program ends, is the pod's
// restart policy's to say, as with a kubelet. Under Always, and under
// OnFailure after a failure, the pod stays where it is and the step is
// tried again after a wait that doubles each time; the pod is never
// marked failed, since a controller answers a failed pod with a new
// one, and a pod that cannot start would be replaced without end. Only
// a pod that asks for no restarts ends at its first failure.
func (a *Agent) runPod(ctx context.Context, r *run) {
	container := r.pod.Spec.Containers[0]
	policy := r.pod.Spec.RestartPolicy
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
	report()

	// The pod's directory, with its volumes and its log, and the
	// sandbox, which is made on the first try that gets that far and
	// kept for the tries after.
	podDir := filepath.Join(a.config.StateDir, podsDir, string(r.pod.UID))
	defer os.RemoveAll(podDir)
	var sandbox *runtime.Sandbox
	defer func() {
		if sandbox == nil {
			return
		}
		if err := sandbox.Destroy(context.WithoutCancel(ctx)); err != nil {
			a.log.Printf("pod %s/%s: %v", r.pod.Namespace, r.pod.Name, err)
		}
	}()

	delay := restartDelayFirst
	for {
		exit, failed := a.attempt(ctx, r, podDir, &sandbox, &snapshot, report, event)
		if ctx.Err() != nil {
			return
		}

		// Over, or to be tried again.
		again := policy == corev1.RestartPolicyAlways ||
			(policy == corev1.RestartPolicyOnFailure && (failed != nil || exit != 0))
		snapshot.Finished = metav1.Now()
		snapshot.ExitCode = int32(exit)
		snapshot.Reason, snapshot.Message = "", ""
		if failed != nil {
			snapshot.ExitCode = exitFailed
			snapshot.Reason = failed.reason
			snapshot.Message = failed.err.Error()
			event(eventWarning, failed.reason, failed.err.Error())
		}
		if !again {
			snapshot.Stage = kube.StageExited
			report()
			return
		}

		// Waiting, for longer each time. A program that ran and ended
		// is a restart; a container that never started is not.
		snapshot.Stage = kube.StageCreating
		if failed == nil {
			snapshot.Restarts++
			snapshot.Reason = reasonBackOff
			snapshot.Message = fmt.Sprintf("the program exited with status %d; starting it again in %s", exit, delay)
		}
		report()
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
		delay = min(delay*2, restartDelayMost)
	}
}

// attempt tries once to run the pod's program to its end: the plan, the
// image, the sandbox if there is none yet, then the container. It
// returns the program's exit status, or why there was no program to
// have one.
func (a *Agent) attempt(ctx context.Context, r *run, podDir string, sandbox **runtime.Sandbox,
	snapshot *kube.Snapshot, report func(), event func(kind, reason, message string)) (int, *failure) {
	// What tabc is going to be told.
	plan, err := runtime.PlanFor(r.pod, r.id, podDir, a.config.Limits)
	if err != nil {
		return 0, &failure{reasonUnsupported, err}
	}
	if len(plan.Skipped) > 0 && snapshot.Restarts == 0 && snapshot.Reason == "" {
		event(eventWarning, reasonSkipped, "a tab leaves out: "+strings.Join(plan.Skipped, ", "))
	}

	// The directories of the pod's volumes, and its log.
	for _, dir := range append([]string{podDir}, plan.Dirs...) {
		if err := os.MkdirAll(dir, podDirMode); err != nil {
			return 0, &failure{reasonStart, err}
		}
	}
	log, err := a.openLog(r.pod.Namespace, r.pod.Name, string(r.pod.UID))
	if err != nil {
		return 0, &failure{reasonStart, err}
	}
	defer log.Close()

	if err := a.pullOnce(ctx, plan.Image); err != nil {
		return 0, &failure{reasonImagePull, err}
	}

	// The sandbox, which gives the pod its address.
	if *sandbox == nil {
		made, err := a.config.Network.Create(ctx, sandboxPrefix+r.id[:sandboxIDLength], r.pod.Namespace)
		if err != nil {
			return 0, &failure{reasonSandbox, err}
		}
		*sandbox = made
		snapshot.PodIP = made.IP
		report()
	}
	return a.runContainer(ctx, r, *sandbox, plan, log, snapshot, report)
}

// runContainer runs the pod's program once: tabc in the sandbox, the
// page in the node's tab, and a wait for the program to end.
func (a *Agent) runContainer(ctx context.Context, r *run, sandbox *runtime.Sandbox, plan runtime.Plan, log *os.File,
	snapshot *kube.Snapshot, report func()) (int, *failure) {
	container, err := a.config.Tabc.Start(sandbox, plan.Args, log)
	if err != nil {
		return 0, &failure{reasonStart, err}
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
	told := time.Now()
	if err := a.waitRunning(ctx, r.id, container); err != nil {
		return 0, &failure{reasonBoot, err}
	}
	snapshot.Stage = kube.StageRunning
	snapshot.Started = metav1.Now()
	snapshot.Reason, snapshot.Message = "", ""

	// A pod with a readiness probe is running and not yet ready, and
	// the probe says when it is.
	probe, probed := runtime.ProbeFor(r.pod.Spec.Containers[0])
	snapshot.Unready = probed
	report()
	if probed {
		// The watch is over before this returns: the snapshot is the
		// run's again from then on.
		watching, stop := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			a.watchReadiness(watching, probe, sandbox.IP, snapshot, report)
		}()
		defer func() {
			stop()
			<-stopped
		}()
	}

	// For the site: the program runs, after a boot of this length.
	boot := time.Since(told)
	r.show(sandbox.IP, true, boot)
	defer r.show(sandbox.IP, false, boot)
	a.ledger.started(r.node.Name, r.pod.Namespace+"/"+r.pod.Name,
		tab.LabelValue(r.node.Hello.Platform), tab.LabelValue(r.node.Hello.Model), boot, time.Now())

	select {
	case <-container.Done():
		return container.Exit(), nil
	case <-ctx.Done():
		return 0, nil
	}
}

// watchReadiness makes a pod's readiness probe over and over until the
// context ends, and writes the pod's status each time the answer
// changes. The snapshot is the run's own, which the run does not touch
// again until this has been stopped.
func (a *Agent) watchReadiness(ctx context.Context, probe runtime.Probe, address string, snapshot *kube.Snapshot, report func()) {
	ticker := time.NewTicker(probe.Period)
	defer ticker.Stop()
	for {
		unready := probe.Passes(ctx, address) != nil
		if ctx.Err() != nil {
			return
		}
		if unready != snapshot.Unready {
			snapshot.Unready = unready
			report()
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
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

// show records what the site shows of the run.
func (r *run) show(ip string, running bool, boot time.Duration) {
	r.pageMu.Lock()
	defer r.pageMu.Unlock()
	r.ip, r.running, r.boot = ip, running, boot
}

// shown returns what the site shows of the run.
func (r *run) shown() (ip string, running bool, boot time.Duration) {
	r.pageMu.Lock()
	defer r.pageMu.Unlock()
	return r.ip, r.running, r.boot
}

// setPage records where the run's container page is: the address tabc
// serves the page on, and the sandbox the address is reached from.
func (r *run) setPage(link *url.URL, sandbox *runtime.Sandbox) {
	proxy := newPageProxy(r.id, link, sandbox, r.carried)
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
