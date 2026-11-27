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
)

// pathApps is where the site's visitors reach the exposed Services:
// /apps/<name>/<the rest of the path>.
const pathApps = "GET /apps/{name}/{rest...}"

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
		http.Error(w, "too many requests; the pods are on people's phones", http.StatusTooManyRequests)
		return
	}

	// Where the Service listens, from the last listing.
	var address string
	a.mu.Lock()
	for _, service := range a.services {
		if service.Namespace == exposed.Namespace && service.Name == exposed.Service {
			address = service.ClusterIP
		}
	}
	a.mu.Unlock()
	if address == "" {
		http.Error(w, fmt.Sprintf("the cluster has no service %s/%s", exposed.Namespace, exposed.Service), http.StatusServiceUnavailable)
		return
	}
	a.ledger.asked(name)

	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(address, strconv.Itoa(exposed.Port))}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.URL.Path = rest
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
