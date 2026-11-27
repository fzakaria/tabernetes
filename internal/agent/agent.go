// Package agent is the kubelet of a cluster whose nodes are browser
// tabs. One agent runs on one machine, beside the API server. A tab
// that opens the join page becomes a Node for as long as the tab stays
// open; a pod the scheduler binds to that Node is run by tabc on the
// agent's machine, with the tab as the computer the pod's program runs
// on.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/fzakaria/tabernetes/internal/geo"
	"github.com/fzakaria/tabernetes/internal/kube"
	"github.com/fzakaria/tabernetes/internal/machine"
	"github.com/fzakaria/tabernetes/internal/runtime"
	"github.com/fzakaria/tabernetes/internal/tab"
)

// renewEvery is how often every node's Lease is renewed. A node whose
// tab is gone is removed at once and not by its Lease running out, so
// the renewals only have to stay well inside the Lease's length.
const renewEvery = 20 * time.Second

// The random parts of names, in bytes: a node's suffix, which only has
// to tell apart devices of one model, and a container's id, which is
// also the secret in the address of the container's page.
const (
	containerIDBytes = 16
	hexPerByte       = 2
)

// nameAttempts is how many suffixes are tried for a node before giving
// up: a taken name is a coincidence, and five in a row are not.
const nameAttempts = 5

// requestTimeout bounds one call to the API server.
const requestTimeout = 15 * time.Second

// sandboxPrefix starts the name of every pod's network namespace, and
// sandboxIDLength is how much of the container's id follows.
const (
	sandboxPrefix   = "tab-"
	sandboxIDLength = 12
)

// Why a tab is turned away. A tab told the machine is busy asks again
// by itself; the other two are for a person to read.
var (
	ErrFull        = errors.New("the cluster has all the nodes its machine has room for; try again when someone leaves")
	ErrBusy        = errors.New("the cluster is busy; this tab asks again in a moment")
	ErrTooManyHere = errors.New("this address already has as many nodes as one address may")
)

// How often the machine is read, and how much of the newest reading
// goes into what the agent takes the machine's busyness to be: the
// rest is what it was taken to be before, so that one busy moment does
// not turn a tab away.
const (
	vitalsEvery = 2 * time.Second
	busyWeight  = 0.3
)

// Config is what an agent is made with.
type Config struct {
	Cluster *kube.Cluster
	Tabc    runtime.Tabc
	Network *runtime.Network
	// Node is what every tab's Node has in common.
	Node kube.NodeConfig
	// Limits is what a pod is allowed.
	Limits runtime.Limits
	// MaxNodes is the most tabs that may be nodes at once, whatever
	// room the machine has, and MaxNodesPerAddress how many of those
	// may be visitors from one address.
	MaxNodes           int
	MaxNodesPerAddress int
	// Proc is the directory the machine is read from, which is /proc.
	// Empty means the machine is not read, and MaxNodes is the only
	// limit.
	Proc string
	// ReserveMemory is how much of the machine's memory is kept back:
	// a tab is turned away while less than this could still be given
	// out. NodeCost is roughly what one node and its pod take of the
	// machine's memory, which is what the site's count of the places
	// left is worked out with.
	ReserveMemory int64
	NodeCost      int64
	// BusyAbove is the part of its CPUs' time the machine may spend
	// busy, from 0 to 1, before tabs are asked to wait. Zero means
	// tabs are never asked to wait.
	BusyAbove float64
	// StartsAtOnce is how many pods may be at the machine's part of
	// starting at once: the sandbox, and tabc. Zero means any number.
	StartsAtOnce int
	// Places says roughly where a visitor's address is, for the site's
	// map. Nil means nobody is looked up, and the map goes by each
	// browser's time zone.
	Places *geo.Book
	// RuntimeClass is the name of the RuntimeClass whose pods run in
	// tabs. A pod of that class bound to a node that is no tab of this
	// agent's is bound to a tab that has gone.
	RuntimeClass string
	// StateDir is where each pod's log and volumes are kept.
	StateDir string
	// Zone is the time zone the machine is in, which is where the
	// site's map puts the cluster itself.
	Zone string
	// Exposed are the Services the site's visitors may send requests
	// to, by the name each is exposed under.
	Exposed map[string]Exposed
	Log     *log.Logger
}

// Exposed is one Service the site's visitors may send requests to.
type Exposed struct {
	Namespace string
	Service   string
	Port      int
}

// Agent holds the tabs that are nodes and the pods that run on them.
type Agent struct {
	config Config
	log    *log.Logger

	// ctx ends when the agent is told to stop, and every pod's run with
	// it.
	ctx context.Context

	mu sync.Mutex
	// nodes are the tabs, by node name.
	nodes map[string]*Node
	// runs are the pods being run, by pod, and containers the same runs
	// by container id, which is how a page's request finds its run.
	runs       map[types.UID]*run
	containers map[string]*run
	// pulls are the images fetched or being fetched, by reference.
	pulls map[string]*pull
	// logs are where pods' logs are, by "namespace/name".
	logs map[string]string
	// services are the cluster's Services, as last listed.
	services []kube.Service

	// ledger is the record of what has happened, started is when this
	// agent began, and view is the cluster as the site was last told.
	ledger  *ledger
	started time.Time
	view    cachedView
	// askers limit how often each visitor may send a request into the
	// cluster.
	askers askers

	// origins are the hosts whose pages may open a tab's socket.
	origins []string

	// listing is the last `tabc list`, shared by every run that waits
	// for its container to start.
	listing listing

	// ready is closed once what an earlier agent left has been cleared
	// away. No tab is taken before then: see Join.
	ready chan struct{}

	// vitals is the machine as last read, and turns the places at the
	// machine's part of starting a pod, of which a run takes one.
	vitals vitals
	turns  chan struct{}
}

// vitals is the machine as the agent last read it.
type vitals struct {
	mu sync.Mutex
	// known is whether the machine has been read at all.
	known   bool
	reading machine.Reading
	// busy is the part of their time the CPUs have been busy lately,
	// from 0 to 1.
	busy float64
}

// Node is one tab.
type Node struct {
	// Name is the node's name in the cluster.
	Name string
	// Hello is what the tab said the tab is.
	Hello tab.Hello
	// Address is where the tab connected from.
	Address string
	// Joined is when the tab became a node.
	Joined time.Time
	// Place is roughly where the tab's address is, and Placed whether
	// that is known.
	Place  geo.Place
	Placed bool

	// outbox is what the agent has to tell the tab's page.
	outbox chan Message
	// lost is closed when the agent gives the tab up.
	lost     chan struct{}
	loseOnce sync.Once
}

// Message is one thing the agent tells a tab's page.
type Message struct {
	Type string `json:"type"`
	// Node is the tab's node name, in a "joined".
	Node string `json:"node,omitempty"`
	// ID, Path, Pod and Image describe a container to show, in a "run",
	// and ID alone the container to remove, in a "stop".
	ID    string `json:"id,omitempty"`
	Path  string `json:"path,omitempty"`
	Pod   string `json:"pod,omitempty"`
	Image string `json:"image,omitempty"`
	// Reason says why a tab is turned away, in a "refused", and Retry
	// that the tab may ask again by itself.
	Reason string `json:"reason,omitempty"`
	Retry  bool   `json:"retry,omitempty"`
	// Nodes and Pods count the cluster, in a "cluster".
	Nodes int `json:"nodes"`
	Pods  int `json:"pods"`
}

// The types of message.
const (
	MessageJoined  = "joined"
	MessageRefused = "refused"
	MessageRun     = "run"
	MessageStop    = "stop"
	MessageCluster = "cluster"
)

// outboxSize is how many messages may wait for a tab's page. A page
// that falls further behind than this has stopped reading.
const outboxSize = 32

// New returns an agent that has not started.
func New(config Config) *Agent {
	agent := &Agent{
		config:     config,
		log:        config.Log,
		nodes:      map[string]*Node{},
		runs:       map[types.UID]*run{},
		containers: map[string]*run{},
		pulls:      map[string]*pull{},
		ledger:     newLedger(config.StateDir, time.Now()),
		started:    time.Now(),
		ready:      make(chan struct{}),
	}
	if config.StartsAtOnce > 0 {
		agent.turns = make(chan struct{}, config.StartsAtOnce)
	}
	return agent
}

// Run clears away what an earlier agent left, then watches the cluster
// and keeps the nodes alive until the context ends, and removes every
// node on the way out.
func (a *Agent) Run(ctx context.Context) error {
	a.ctx = ctx
	if err := a.clearLeftovers(ctx); err != nil {
		return err
	}
	close(a.ready)
	if err := a.config.Cluster.WatchPods(ctx, a); err != nil {
		return err
	}
	a.log.Printf("watching for pods; up to %d nodes", a.config.MaxNodes)

	renewals := time.NewTicker(renewEvery)
	defer renewals.Stop()
	listings := time.NewTicker(servicesEvery)
	defer listings.Stop()
	saves := time.NewTicker(saveEvery)
	defer saves.Stop()
	readings := time.NewTicker(vitalsEvery)
	defer readings.Stop()
	a.listServices(ctx)
	a.readVitals()
	for {
		select {
		case <-readings.C:
			a.readVitals()
		case <-renewals.C:
			a.renew(ctx)
		case <-listings.C:
			a.listServices(ctx)
		case <-saves.C:
			a.saveLedger()
		case <-ctx.Done():
			a.removeAll()
			a.saveLedger()
			return nil
		}
	}
}

// How often the Services are listed again, for the site's picture of
// the cluster, and how often the ledger is written to its file.
const (
	servicesEvery = 5 * time.Second
	saveEvery     = 30 * time.Second
)

// listServices reads the cluster's Services.
func (a *Agent) listServices(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	services, err := a.config.Cluster.Services(ctx)
	if err != nil {
		a.log.Printf("listing the services: %v", err)
		return
	}
	a.mu.Lock()
	a.services = services
	a.mu.Unlock()
}

// readVitals reads the machine, and works the reading into what the
// agent takes the machine's busyness to be.
func (a *Agent) readVitals() {
	if a.config.Proc == "" {
		return
	}
	reading, err := machine.Read(a.config.Proc)
	if err != nil {
		a.log.Printf("reading the machine: %v", err)
		return
	}

	a.vitals.mu.Lock()
	defer a.vitals.mu.Unlock()
	if a.vitals.known {
		sample := reading.BusySince(a.vitals.reading)
		a.vitals.busy = a.vitals.busy*(1-busyWeight) + sample*busyWeight
	}
	a.vitals.reading = reading
	a.vitals.known = true
}

// machineNow returns the machine as last read, how busy the agent
// takes the machine to be, and whether the machine has been read.
func (a *Agent) machineNow() (machine.Reading, float64, bool) {
	a.vitals.mu.Lock()
	defer a.vitals.mu.Unlock()
	return a.vitals.reading, a.vitals.busy, a.vitals.known
}

// refusal returns why another tab may not become a node now, or nil:
// the cluster has the most nodes it is ever to have, the machine has
// too little memory left, or the machine is too busy. The caller holds
// the agent's lock.
func (a *Agent) refusal() error {
	if len(a.nodes) >= a.config.MaxNodes {
		return ErrFull
	}
	reading, busy, known := a.machineNow()
	if !known {
		return nil
	}
	if a.config.ReserveMemory > 0 && reading.MemoryAvailable < a.config.ReserveMemory {
		return ErrFull
	}
	if a.config.BusyAbove > 0 && busy > a.config.BusyAbove {
		return ErrBusy
	}
	return nil
}

// room returns how many more nodes the cluster would take now: up to
// the most it is ever to have, and no more than the machine's spare
// memory is enough for. The caller holds the agent's lock.
func (a *Agent) room() int {
	room := a.config.MaxNodes - len(a.nodes)
	reading, _, known := a.machineNow()
	if known && a.config.ReserveMemory > 0 && a.config.NodeCost > 0 {
		spare := reading.MemoryAvailable - a.config.ReserveMemory
		room = min(room, int(spare/a.config.NodeCost))
	}
	return max(0, room)
}

// saveLedger writes the ledger's totals to their file.
func (a *Agent) saveLedger() {
	if err := a.ledger.save(a.config.StateDir); err != nil {
		a.log.Printf("saving the ledger: %v", err)
	}
}

// clearLeftovers removes the nodes and the network namespaces of an
// agent that did not get to remove them. Their tabs are connected to
// nothing, and a tab that is still open will join again as a new node.
func (a *Agent) clearLeftovers(ctx context.Context) error {
	names, err := a.config.Cluster.TabNodes(ctx)
	if err != nil {
		return fmt.Errorf("listing the nodes: %w", err)
	}
	for _, name := range names {
		// A node this agent has is nobody's leftover. No tab is taken
		// while this runs, so there should be none; the look costs
		// nothing, and a live node removed here is a tab that never
		// gets a pod.
		a.mu.Lock()
		own := a.nodes[name] != nil
		a.mu.Unlock()
		if own {
			continue
		}
		a.log.Printf("removing node %s, left by an earlier agent", name)
		a.removeNodeAndPods(ctx, name)
	}

	// The containers of those nodes' pods, then the namespaces the
	// containers ran in.
	containers, err := a.config.Tabc.List(ctx)
	if err != nil {
		return err
	}
	for name, container := range containers {
		a.log.Printf("removing container %s, left by an earlier agent", name)
		if err := a.config.Tabc.Remove(ctx, container.ID); err != nil {
			a.log.Printf("%v", err)
		}
	}
	sandboxes, err := runtime.Leftovers(sandboxPrefix)
	if err != nil {
		return err
	}
	for _, name := range sandboxes {
		if err := a.config.Network.Adopt(name).Destroy(ctx); err != nil {
			a.log.Printf("removing namespace %s: %v", name, err)
		}
	}
	return nil
}

// removeNodeAndPods deletes a node and the pods bound to it. With the
// node gone the pods' controllers make new ones for other nodes.
func (a *Agent) removeNodeAndPods(ctx context.Context, name string) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	// The node first: see Leave.
	if err := a.config.Cluster.RemoveNode(ctx, name); err != nil {
		a.log.Printf("node %s: %v", name, err)
	}
	pods, err := a.config.Cluster.PodsOn(ctx, name)
	if err != nil {
		a.log.Printf("node %s: listing its pods: %v", name, err)
	}
	for index := range pods {
		if err := a.config.Cluster.RemovePod(ctx, &pods[index]); err != nil {
			a.log.Printf("node %s: removing pod %s: %v", name, pods[index].Name, err)
		}
	}
}

// Join makes a tab a node. The returned node's messages are for the
// tab's page, and Leave is to be called when the page is gone.
func (a *Agent) Join(ctx context.Context, hello tab.Hello, address string) (*Node, error) {
	// A tab that comes while the agent is clearing away what an earlier
	// agent left, or while the agent is stopping, is asked to come
	// back. A tab asks for the name it had before, and the clearing
	// would take the new Node for the old one and remove it; a Node
	// made by an agent that is stopping is removed by nobody.
	select {
	case <-a.ready:
	default:
		return nil, ErrBusy
	}
	if a.ctx.Err() != nil {
		return nil, ErrBusy
	}

	// A place is taken before the API server is asked, so that a crowd
	// arriving at once cannot overshoot the limits.
	a.mu.Lock()
	if refused := a.refusal(); refused != nil {
		a.mu.Unlock()
		return nil, refused
	}
	here := 0
	for _, node := range a.nodes {
		if node.Address == address {
			here++
		}
	}
	if here >= a.config.MaxNodesPerAddress {
		a.mu.Unlock()
		return nil, ErrTooManyHere
	}
	node := &Node{
		Hello:   hello,
		Address: address,
		Joined:  time.Now(),
		outbox:  make(chan Message, outboxSize),
		lost:    make(chan struct{}),
	}
	node.Place, node.Placed = a.config.Places.Lookup(address)

	// The place is held under a name nothing else has, until the node's
	// own name is known.
	placeholder := placeholderPrefix + randomHex(containerIDBytes)
	a.nodes[placeholder] = node
	a.mu.Unlock()

	err := a.register(ctx, node)

	a.mu.Lock()
	delete(a.nodes, placeholder)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	count := a.named()
	a.mu.Unlock()

	a.ledger.joined(node.Name, count, node.Joined)
	a.log.Printf("node %s joined from %s (%s); %d nodes", node.Name, address, hello.Describe(), count)
	return node, nil
}

// named returns how many nodes there are, leaving out the tabs whose
// Node is still being made. The caller holds the agent's lock.
func (a *Agent) named() int {
	count := 0
	for name := range a.nodes {
		if !strings.HasPrefix(name, placeholderPrefix) {
			count++
		}
	}
	return count
}

// placeholderPrefix starts the name a tab's place is held under while
// the tab's Node is being made. No node's name can start with it: a
// node's name ends with a dash and a suffix, and this has neither.
const placeholderPrefix = "joining:"

// register creates the tab's Node under a name that is free, and gives
// the node that name.
//
// The agent knows the node by the name before the API server does. The
// scheduler may bind a pod to a Node the moment the Node exists, and a
// pod bound to a node the agent does not know is taken for one whose
// tab has gone.
func (a *Agent) register(ctx context.Context, node *Node) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var err error
	for attempt := 0; attempt < nameAttempts; attempt++ {
		// The first try is for the name the tab had before, if the tab
		// says what that was, and every other try for a new one.
		suffix := node.Hello.PlainSuffix()
		if attempt > 0 || suffix == "" {
			suffix = randomHex(tab.SuffixLength / hexPerByte)
		}
		name := node.Hello.NodeName(suffix)
		a.mu.Lock()
		if a.nodes[name] != nil {
			a.mu.Unlock()
			continue
		}
		node.Name = name
		a.nodes[name] = node
		a.mu.Unlock()

		err = a.config.Cluster.AddNode(ctx, kube.Node(name, node.Hello, a.config.Node, metav1.Now()))
		if err == nil {
			return nil
		}
		a.mu.Lock()
		delete(a.nodes, name)
		node.Name = ""
		a.mu.Unlock()
		if !kube.IsTaken(err) {
			break
		}
	}
	return fmt.Errorf("registering the node: %w", err)
}

// Leave removes a tab's node, and the pods on it, from the cluster.
func (a *Agent) Leave(node *Node) {
	a.mu.Lock()
	if a.nodes[node.Name] != node {
		a.mu.Unlock()
		return
	}
	delete(a.nodes, node.Name)
	var running []*run
	var moved []string
	for _, candidate := range a.runs {
		if candidate.node != node {
			continue
		}
		running = append(running, candidate)
		if movesElsewhere(candidate.pod) {
			moved = append(moved, candidate.pod.Namespace+"/"+candidate.pod.Name)
		}
	}
	count := len(a.nodes)
	a.mu.Unlock()
	node.lose()

	now := time.Now()
	a.ledger.left(node.Name, now.Sub(node.Joined), moved, now)

	// The node goes first and its pods after. A pod's controller makes
	// a new pod the moment the old one is gone, and the scheduler must
	// not find this node still there to put the new pod on.
	a.removeNodeAndPods(context.WithoutCancel(a.ctx), node.Name)
	for _, candidate := range running {
		candidate.cancel()
	}
	a.log.Printf("node %s left; %d nodes", node.Name, count)
}

// kindDaemonSet is the kind of the controller whose pods are each for
// one node.
const kindDaemonSet = "DaemonSet"

// movesElsewhere reports whether a pod that loses its tab is made
// again for another node: a pod with a controller. A DaemonSet's pod
// is the exception, since that pod is for the one node and ends with
// the node.
func movesElsewhere(pod *corev1.Pod) bool {
	owner := metav1.GetControllerOf(pod)
	return owner != nil && owner.Kind != kindDaemonSet
}

// removeAll takes every node out of the cluster and waits for their
// pods' containers to be gone: the agent is stopping.
func (a *Agent) removeAll() {
	a.mu.Lock()
	nodes := make([]*Node, 0, len(a.nodes))
	for _, node := range a.nodes {
		nodes = append(nodes, node)
	}
	running := make([]*run, 0, len(a.runs))
	for _, candidate := range a.runs {
		running = append(running, candidate)
	}
	a.mu.Unlock()

	for _, node := range nodes {
		a.Leave(node)
	}
	for _, candidate := range running {
		<-candidate.done
	}
}

// renew says every node is alive.
func (a *Agent) renew(ctx context.Context) {
	a.mu.Lock()
	names := make([]string, 0, len(a.nodes))
	for name := range a.nodes {
		if !strings.HasPrefix(name, placeholderPrefix) {
			names = append(names, name)
		}
	}
	a.mu.Unlock()

	for _, name := range names {
		renewCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		if err := a.config.Cluster.RenewLease(renewCtx, name); err != nil {
			a.log.Printf("node %s: renewing its lease: %v", name, err)
		}
		cancel()
	}
}

// Messages are what the agent has to tell the node's page, and Lost is
// closed when the agent has given the node up.
func (n *Node) Messages() <-chan Message { return n.outbox }
func (n *Node) Lost() <-chan struct{}    { return n.lost }

// tell queues a message for the node's page. A page that has stopped
// reading is given up, not waited for.
func (n *Node) tell(message Message) {
	select {
	case n.outbox <- message:
	case <-n.lost:
	default:
		n.lose()
	}
}

// lose marks the node as given up.
func (n *Node) lose() {
	n.loseOnce.Do(func() { close(n.lost) })
}

// Counts returns how many nodes and pods there are.
func (a *Agent) Counts() (nodes, pods int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.named(), len(a.runs)
}

// randomHex returns that many random bytes, as hex.
func randomHex(bytes int) string {
	random := make([]byte, bytes)
	if _, err := rand.Read(random); err != nil {
		// The system's randomness failing is not something to run on
		// from.
		panic(err)
	}
	return hex.EncodeToString(random)
}
