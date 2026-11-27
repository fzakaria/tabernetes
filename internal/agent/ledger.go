package agent

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// How much of what happened the ledger keeps to show: the latest
// events, and the latest boots and the latest asks, which the site's
// charts are drawn from.
const (
	eventsKept = 60
	bootsKept  = 240
	asksKept   = 240
)

// The ledger's file under the agent's state directory, and its mode.
const (
	ledgerFile     = "ledger.json"
	ledgerFileMode = 0o644
)

// The kinds of event.
const (
	// EventJoin: a tab became a node.
	EventJoin = "join"
	// EventLeave: a node's tab went away.
	EventLeave = "leave"
	// EventStart: a pod's program started in a tab.
	EventStart = "start"
	// EventMove: a pod lost its tab, and its controller is making
	// another for a different node.
	EventMove = "move"
)

// Totals are what the cluster has done since it was first started. They
// are kept in a file, so that a restart of the agent does not start the
// count again.
type Totals struct {
	// Since is when the cluster was first started.
	Since time.Time `json:"since"`
	// Joins is how many times a tab became a node.
	Joins int64 `json:"joins"`
	// NodeSeconds is how long tabs have been nodes, added up, counting
	// only the nodes that have left.
	NodeSeconds float64 `json:"nodeSeconds"`
	// PodsStarted is how many times a pod's program started in a tab,
	// and PodsMoved how many times a pod lost its tab.
	PodsStarted int64 `json:"podsStarted"`
	PodsMoved   int64 `json:"podsMoved"`
	// BootSeconds is every start's time from the tab being told of the
	// container to the program running, added up.
	BootSeconds float64 `json:"bootSeconds"`
	// Bytes is how much has passed between tabs and the machine.
	Bytes int64 `json:"bytes"`
	// Asked is how many requests the site's visitors sent into the
	// cluster, and AskedOf how many of them went to each exposed
	// Service, by the name the Service is exposed under. A count kept
	// from before the Services were told apart has no share for any.
	Asked   int64            `json:"asked"`
	AskedOf map[string]int64 `json:"askedOf,omitempty"`
	// PeakNodes is the most nodes there have been at once.
	PeakNodes int `json:"peakNodes"`
	// LongestName and LongestSeconds are the node that lasted longest.
	LongestName    string  `json:"longestName"`
	LongestSeconds float64 `json:"longestSeconds"`
}

// Event is one thing that happened, as the site's feed shows it.
type Event struct {
	// ID counts events from the agent's start, so a page can tell which
	// it has shown.
	ID   int64     `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Node string    `json:"node"`
	// Pod is the pod of a start or a move, as "namespace/name".
	Pod string `json:"pod,omitempty"`
	// Seconds is a start's boot time, or how long a node that left had
	// lasted.
	Seconds float64 `json:"seconds,omitempty"`
	// Pods is how many pods a node that left was running.
	Pods int `json:"pods,omitempty"`
}

// Boot is one start of a pod's program, as the site's chart counts it.
type Boot struct {
	At      time.Time `json:"at"`
	Seconds float64   `json:"seconds"`
	// Platform and Model are the node's, as label values.
	Platform string `json:"platform"`
	Model    string `json:"model"`
}

// Ask is one request a visitor sent into the cluster, as the site's
// chart counts it.
type Ask struct {
	At time.Time `json:"at"`
	// App is the name the Service that was asked is exposed by.
	App string `json:"app"`
	// Seconds is how long the agent waited: from sending the request
	// on to the Service until the last of the answer had come back.
	Seconds float64 `json:"seconds"`
	// Status is the HTTP status the visitor was answered with: the
	// pod's own, or bad gateway when no pod answered.
	Status int `json:"status"`
}

// ledger is the agent's record of what happened: the totals, the latest
// events, the latest boots and the latest asks.
type ledger struct {
	mu     sync.Mutex
	totals Totals
	events []Event
	boots  []Boot
	asks   []Ask
	nextID int64
}

// saved is the ledger as its file holds it: the totals, and the latest
// boots and asks, so that the site's charts of them are not empty
// after every restart of the agent. The feed is not kept: what
// happened before a restart is not happening now.
type saved struct {
	Totals Totals `json:"totals"`
	Boots  []Boot `json:"boots"`
	Asks   []Ask  `json:"asks"`
}

// newLedger returns a ledger with the totals, the boots and the asks
// an earlier agent saved under a directory, or one that starts now.
func newLedger(dir string, now time.Time) *ledger {
	book := &ledger{totals: Totals{Since: now}}
	contents, err := os.ReadFile(filepath.Join(dir, ledgerFile))
	if err != nil {
		return book
	}
	var kept saved
	if err := json.Unmarshal(contents, &kept); err != nil {
		return book
	}
	if !kept.Totals.Since.IsZero() {
		book.totals, book.boots, book.asks = kept.Totals, kept.Boots, kept.Asks
		return book
	}

	// A file from before boots were saved is the totals alone.
	var totals Totals
	if err := json.Unmarshal(contents, &totals); err != nil || totals.Since.IsZero() {
		return book
	}
	book.totals = totals
	return book
}

// save writes the totals, the boots and the asks under a directory, by
// way of a second file so that a crash mid-write leaves the old ones
// whole.
func (l *ledger) save(dir string) error {
	l.mu.Lock()
	contents, err := json.Marshal(saved{Totals: l.totals, Boots: l.boots, Asks: l.asks})
	l.mu.Unlock()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, podDirMode); err != nil {
		return err
	}
	path := filepath.Join(dir, ledgerFile)
	partial := path + ".partial"
	if err := os.WriteFile(partial, contents, ledgerFileMode); err != nil {
		return err
	}
	return os.Rename(partial, path)
}

// record adds an event to the feed, dropping the oldest when the feed
// is full. The caller holds the ledger's lock.
func (l *ledger) record(event Event) {
	l.nextID++
	event.ID = l.nextID
	l.events = append(l.events, event)
	if len(l.events) > eventsKept {
		l.events = l.events[len(l.events)-eventsKept:]
	}
}

// joined records that a tab became a node, with how many nodes there
// are now.
func (l *ledger) joined(node string, nodes int, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.totals.Joins++
	if nodes > l.totals.PeakNodes {
		l.totals.PeakNodes = nodes
	}
	l.record(Event{At: now, Kind: EventJoin, Node: node})
}

// left records that a node's tab went away after a lifetime, taking the
// named pods with it.
func (l *ledger) left(node string, lifetime time.Duration, pods []string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	seconds := lifetime.Seconds()
	l.totals.NodeSeconds += seconds
	if seconds > l.totals.LongestSeconds {
		l.totals.LongestSeconds = seconds
		l.totals.LongestName = node
	}
	l.totals.PodsMoved += int64(len(pods))
	l.record(Event{At: now, Kind: EventLeave, Node: node, Seconds: seconds, Pods: len(pods)})
	for _, pod := range pods {
		l.record(Event{At: now, Kind: EventMove, Node: node, Pod: pod})
	}
}

// started records that a pod's program started on a node, after a boot
// of some length.
func (l *ledger) started(node, pod, platform, model string, boot time.Duration, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	seconds := boot.Seconds()
	l.totals.PodsStarted++
	l.totals.BootSeconds += seconds
	l.boots = append(l.boots, Boot{At: now, Seconds: seconds, Platform: platform, Model: model})
	if len(l.boots) > bootsKept {
		l.boots = l.boots[len(l.boots)-bootsKept:]
	}
	l.record(Event{At: now, Kind: EventStart, Node: node, Pod: pod, Seconds: seconds})
}

// carried adds to the bytes passed between tabs and the machine.
func (l *ledger) carried(bytes int64) {
	l.mu.Lock()
	l.totals.Bytes += bytes
	l.mu.Unlock()
}

// asked records one request a visitor sent to an exposed Service: the
// name the Service is exposed under, how long the answer took, and the
// status the visitor was answered with.
func (l *ledger) asked(name string, took time.Duration, status int, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.totals.Asked++
	if l.totals.AskedOf == nil {
		l.totals.AskedOf = map[string]int64{}
	}
	l.totals.AskedOf[name]++
	l.asks = append(l.asks, Ask{At: now, App: name, Seconds: took.Seconds(), Status: status})
	if len(l.asks) > asksKept {
		l.asks = l.asks[len(l.asks)-asksKept:]
	}
}

// view returns the totals, the events, the boots and the asks as they
// are now. All four are the caller's own.
func (l *ledger) view() (Totals, []Event, []Boot, []Ask) {
	l.mu.Lock()
	defer l.mu.Unlock()
	totals := l.totals
	totals.AskedOf = maps.Clone(l.totals.AskedOf)
	return totals, append([]Event{}, l.events...), append([]Boot{}, l.boots...), append([]Ask{}, l.asks...)
}
