package agent

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fzakaria/tabernetes/internal/tab"
)

// viewAge is how long the cluster's description is served from memory
// before it is put together again. Every visitor's page asks for it
// every few seconds, and a crowd of them should cost the agent one
// description a second, not one a request.
const viewAge = time.Second

// appLabel is the label a pod's workload is read from, for the colour
// the site gives the pod.
const appLabel = "app"

// PodView is a pod as the site shows one.
type PodView struct {
	// Name is "namespace/name".
	Name string `json:"name"`
	// App is the pod's workload: its "app" label, or its own name.
	App string `json:"app"`
	// Running is whether the pod's program is running in the tab, and
	// Boot how many seconds the tab took to start it.
	Running bool    `json:"running"`
	Boot    float64 `json:"boot"`
	IP      string  `json:"ip"`
	// Down and Up are the bytes sent to the tab and received from it.
	Down int64 `json:"down"`
	Up   int64 `json:"up"`
}

// NodeView is a node as the site shows one.
type NodeView struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Browser  string `json:"browser"`
	Model    string `json:"model"`
	// Zone is the tab's time zone, which is how the site's map places
	// the node.
	Zone    string    `json:"zone"`
	Threads int       `json:"threads"`
	Joined  time.Time `json:"joined"`
	Pods    []PodView `json:"pods"`
}

// ServiceView is a Service as the site shows one: its name, as
// "namespace/name", and the pods on tabs it is in front of.
type ServiceView struct {
	Name string   `json:"name"`
	Pods []string `json:"pods"`
}

// MachineView is the cluster's own machine as the site shows it.
type MachineView struct {
	Zone    string `json:"zone"`
	Version string `json:"version"`
	Runtime string `json:"runtime"`
}

// View is the cluster as the site shows it.
type View struct {
	// Now is the agent's clock, so that a page works out ages without
	// trusting its own.
	Now time.Time `json:"now"`
	// Started is when this agent started.
	Started  time.Time     `json:"started"`
	Machine  MachineView   `json:"machine"`
	MaxNodes int           `json:"maxNodes"`
	Totals   Totals        `json:"totals"`
	Nodes    []NodeView    `json:"nodes"`
	Services []ServiceView `json:"services"`
	Events   []Event       `json:"events"`
	Boots    []Boot        `json:"boots"`
	// Exposed are the names of the Services a visitor may send a
	// request to.
	Exposed []string `json:"exposed"`
}

// Snapshot returns the cluster as it is now: the nodes, oldest first,
// with the pods on each, the Services in front of those pods, and the
// ledger.
func (a *Agent) Snapshot() View {
	totals, events, boots := a.ledger.view()
	view := View{
		Now:     time.Now(),
		Started: a.started,
		Machine: MachineView{
			Zone:    a.config.Zone,
			Version: a.config.Node.Version,
			Runtime: a.config.Node.RuntimeVersion,
		},
		MaxNodes: a.config.MaxNodes,
		Totals:   totals,
		Nodes:    []NodeView{},
		Services: []ServiceView{},
		Events:   events,
		Boots:    boots,
		Exposed:  []string{},
	}
	for name := range a.config.Exposed {
		view.Exposed = append(view.Exposed, name)
	}
	sort.Strings(view.Exposed)

	a.mu.Lock()
	defer a.mu.Unlock()

	// The nodes, each with its pods.
	for name, node := range a.nodes {
		if strings.HasPrefix(name, placeholderPrefix) {
			continue
		}
		entry := NodeView{
			Name:     node.Name,
			Platform: tab.LabelValue(node.Hello.Platform),
			Browser:  tab.LabelValue(node.Hello.Browser),
			Model:    tab.LabelValue(node.Hello.Model),
			Zone:     node.Hello.PlainZone(),
			Threads:  node.Hello.PlainThreads(),
			Joined:   node.Joined,
			Pods:     []PodView{},
		}
		for _, running := range a.runs {
			if running.node != node {
				continue
			}
			ip, isRunning, boot := running.shown()
			app := running.pod.Labels[appLabel]
			if app == "" {
				app = running.pod.Name
			}
			entry.Pods = append(entry.Pods, PodView{
				Name:    running.pod.Namespace + "/" + running.pod.Name,
				App:     app,
				Running: isRunning,
				Boot:    boot.Seconds(),
				IP:      ip,
				Down:    running.down.Load(),
				Up:      running.up.Load(),
			})
		}
		view.Nodes = append(view.Nodes, entry)
	}
	sort.Slice(view.Nodes, func(i, j int) bool { return view.Nodes[i].Joined.Before(view.Nodes[j].Joined) })

	// The Services that are in front of at least one pod on a tab.
	for _, service := range a.services {
		entry := ServiceView{Name: service.Namespace + "/" + service.Name}
		for _, running := range a.runs {
			if service.Selects(running.pod.Namespace, running.pod.Labels) {
				entry.Pods = append(entry.Pods, running.pod.Namespace+"/"+running.pod.Name)
			}
		}
		if len(entry.Pods) == 0 {
			continue
		}
		sort.Strings(entry.Pods)
		view.Services = append(view.Services, entry)
	}
	sort.Slice(view.Services, func(i, j int) bool { return view.Services[i].Name < view.Services[j].Name })
	return view
}

// cachedView is the cluster's description as last put together, as
// JSON and as that JSON gzipped.
type cachedView struct {
	mu      sync.Mutex
	made    time.Time
	plain   []byte
	gzipped []byte
}

// serveCluster answers with the cluster's description, from memory
// when the one there is fresh.
func (a *Agent) serveCluster(w http.ResponseWriter, r *http.Request) {
	a.view.mu.Lock()
	if time.Since(a.view.made) >= viewAge {
		plain, err := json.Marshal(a.Snapshot())
		if err != nil {
			a.view.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var zipped bytes.Buffer
		writer := gzip.NewWriter(&zipped)
		writer.Write(plain)
		writer.Close()
		a.view.plain, a.view.gzipped, a.view.made = plain, zipped.Bytes(), time.Now()
	}
	plain, zipped := a.view.plain, a.view.gzipped
	a.view.mu.Unlock()

	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("Cache-Control", "no-store")
	header.Set("Vary", "Accept-Encoding")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		header.Set("Content-Encoding", "gzip")
		w.Write(zipped)
		return
	}
	w.Write(plain)
}
