package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/fzakaria/tabernetes/internal/kube"
)

// pathApps is where the site's visitors reach the exposed Services:
// /apps/<name>/<the rest of the path>.
const pathApps = "GET /apps/{name}/{rest...}"

// pathNodeApps is where a tab's page reaches the pod of an exposed
// Service that is on one node: /nodes/<node>/apps/<name>/<the rest of
// the path>. A tab asks its own pod this way, and sees the answer come
// from its own device.
const pathNodeApps = "GET /nodes/{node}/apps/{name}/{rest...}"

// What one visitor may ask of one Service: this many requests a
// second, with this many at once after a quiet spell. A request is
// answered by a pod in somebody's tab, on somebody's battery.
const (
	asksPerSecond = 2
	asksAtOnce    = 6
)

// askersKept is how many limits are remembered. Past that the
// table is emptied, which lets everyone start over: cheaper than
// tracking who was seen last, and no worse for a visitor than a full
// allowance.
const askersKept = 10000

// How long a request into the cluster may take in all, and how long
// its connection to the Service may take to open. A Service with no
// pod behind it refuses at once; one whose pod's tab has just gone
// hangs until this runs out.
const (
	askTimeout     = 10 * time.Second
	askDialTimeout = 3 * time.Second
)

// errTooManyAsks is what a visitor who asks too often is told.
const errTooManyAsks = "too many requests; the pods are on people's phones"

// asker is whose limit a limit is: a visitor's address, and the exposed
// Service the visitor asks, by the name the Service is exposed under.
type asker struct {
	name    string
	address string
}

// askers limits how often each visitor may ask each Service. The
// limits are apart so that a page that reads one Service every second
// leaves its visitor's allowance for another whole.
type askers struct {
	mu       sync.Mutex
	limiters map[asker]*rate.Limiter
}

// allow reports whether a visitor at an address may ask a Service now.
func (a *askers) allow(name, address string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.limiters == nil || len(a.limiters) >= askersKept {
		a.limiters = map[asker]*rate.Limiter{}
	}
	who := asker{name: name, address: address}
	limiter := a.limiters[who]
	if limiter == nil {
		limiter = rate.NewLimiter(asksPerSecond, asksAtOnce)
		a.limiters[who] = limiter
	}
	return limiter.Allow()
}

// serveApp carries a visitor's request to one of the exposed Services.
// The Service sends it on to one of its pods, which may be on anyone's
// phone: that is the point of asking.
//
// Only a GET is carried, only to a Service the agent was started with
// and for a path that Service was exposed with, and only so often for
// each visitor.
func (a *Agent) serveApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	exposed, has := a.config.Exposed[name]
	rest := "/" + r.PathValue("rest")
	if !has || !exposed.Carries(rest) {
		http.NotFound(w, r)
		return
	}
	if !a.askers.allow(name, clientAddress(r)) {
		http.Error(w, errTooManyAsks, http.StatusTooManyRequests)
		return
	}

	// Where the Service listens, from the last listing.
	service, found := a.service(exposed)
	if !found {
		http.Error(w, fmt.Sprintf("the cluster has no service %s/%s", exposed.Namespace, exposed.Service), http.StatusServiceUnavailable)
		return
	}
	a.ledger.asked(name)
	a.carry(w, r, net.JoinHostPort(service.ClusterIP, strconv.Itoa(exposed.Port)), rest)
}

// serveNodeApp carries a request to the pod of an exposed Service that
// is on one node, past the Service: the Service would pick any of its
// pods, and a tab that asks its own pod wants to see its own device
// answer. What may be asked, and how often, is as for serveApp.
func (a *Agent) serveNodeApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	exposed, has := a.config.Exposed[name]
	rest := "/" + r.PathValue("rest")
	if !has || !exposed.Carries(rest) {
		http.NotFound(w, r)
		return
	}
	if !a.askers.allow(name, clientAddress(r)) {
		http.Error(w, errTooManyAsks, http.StatusTooManyRequests)
		return
	}

	address, err := a.podAddress(exposed, r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	a.ledger.asked(name)
	a.carry(w, r, address, rest)
}

// service returns an exposed Service as the last listing had it.
func (a *Agent) service(exposed Exposed) (kube.Service, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, service := range a.services {
		if service.Namespace == exposed.Namespace && service.Name == exposed.Service {
			return service, true
		}
	}
	return kube.Service{}, false
}

// podAddress returns where the pod of an exposed Service that is on
// one node listens: the pod's own address and the port the Service
// sends on to.
func (a *Agent) podAddress(exposed Exposed, node string) (string, error) {
	service, found := a.service(exposed)
	if !found {
		return "", fmt.Errorf("the cluster has no service %s/%s", exposed.Namespace, exposed.Service)
	}
	port := service.Targets[int32(exposed.Port)]
	if port == 0 {
		return "", fmt.Errorf("service %s/%s names its pods' port, and the agent reads only numbers", exposed.Namespace, exposed.Service)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, running := range a.runs {
		if running.node.Name != node || !service.Selects(running.pod.Namespace, running.pod.Labels) {
			continue
		}
		ip, isRunning, _ := running.shown()
		if !isRunning {
			continue
		}
		return net.JoinHostPort(ip, strconv.Itoa(int(port))), nil
	}
	return "", fmt.Errorf("node %s runs no pod of service %s/%s", node, exposed.Namespace, exposed.Service)
}

// carry sends a visitor's request on to an address inside the cluster,
// for a path there, and the answer back.
func (a *Agent) carry(w http.ResponseWriter, r *http.Request, address, path string) {
	target := &url.URL{Scheme: "http", Host: address}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.URL.Path = path
			request.Out.URL.RawPath = ""
			request.Out.Host = target.Host

			// The visitor's own headers stay here: a pod on a stranger's
			// tab has no business with anyone's cookies.
			request.Out.Header = http.Header{}
		},
		Transport: &http.Transport{
			DialContext:       (&net.Dialer{Timeout: askDialTimeout}).DialContext,
			DisableKeepAlives: true,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "no pod answered: "+err.Error(), http.StatusBadGateway)
		},
	}
	ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	proxy.ServeHTTP(w, r.WithContext(ctx))
}
