package agent

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/fzakaria/tabernetes/internal/runtime"
	"github.com/fzakaria/tabernetes/internal/tab"
)

// The site's paths.
const (
	pathJoin    = "/join"
	pathSocket  = "/ws"
	pathCluster = "/api/cluster"
	pathQR      = "/qr.png"
	pathStatic  = "/static/"
	fileIndex   = "index.html"
	fileJoin    = "join.html"
)

// The tab's side of the conversation: how long a tab gets to say what
// it is, how much it may say, how often the agent checks that the tab
// still answers, and how long an answer may take.
const (
	helloTimeout = 10 * time.Second
	helloLimit   = 4 << 10
	pingEvery    = 5 * time.Second
	pongTimeout  = 10 * time.Second
	writeTimeout = 10 * time.Second
)

// messageBye is what a page sends when its tab is being closed.
const messageBye = "bye"

// qrSize is the QR code's width and height in pixels.
const qrSize = 512

// forwardedFor is the header the proxy in front of the agent puts each
// client's address in, after any the client sent itself.
const forwardedFor = "X-Forwarded-For"

// The headers of a page that frames a container's page. A container's
// page asks to be isolated from other origins, and a browser only shows
// such a page in a frame of a page that asks the same.
var isolationHeaders = map[string]string{
	"Cross-Origin-Opener-Policy":   "same-origin",
	"Cross-Origin-Embedder-Policy": "require-corp",
	"Cross-Origin-Resource-Policy": "same-origin",
}

// Site is what the agent serves besides the cluster: the pages, and the
// files every container's page loads.
type Site struct {
	// Pages are the site's own files (web/).
	Pages fs.FS
	// Share are the files of tabc's that every container's page loads
	// alike: the kernel, the SDK, the terminal.
	Share *Share
	// PublicURL is the site's address as a visitor types it, which is
	// what the QR code says.
	PublicURL string
}

// Handler returns the agent's HTTP handler.
func (a *Agent) Handler(site Site) http.Handler {
	mux := http.NewServeMux()
	pages := http.FileServerFS(site.Pages)
	if public, err := url.Parse(site.PublicURL); err == nil && public.Host != "" {
		a.origins = []string{public.Host}
	}

	mux.HandleFunc("GET "+pathSocket, a.serveSocket)
	mux.HandleFunc("GET "+pathCluster, a.serveCluster)
	mux.HandleFunc("GET "+pathQR, func(w http.ResponseWriter, r *http.Request) {
		image, err := qrcode.Encode(strings.TrimRight(site.PublicURL, "/")+pathJoin, qrcode.Medium, qrSize)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(image)
	})
	mux.Handle("GET "+pathStatic, site.Share)
	mux.HandleFunc(pathApps, a.serveApp)
	mux.HandleFunc(containerPrefix, func(w http.ResponseWriter, r *http.Request) {
		a.serveContainer(w, r, site.Share)
	})

	// The join page frames containers' pages, so it is isolated as they
	// are.
	mux.HandleFunc("GET "+pathJoin, func(w http.ResponseWriter, r *http.Request) {
		for name, value := range isolationHeaders {
			w.Header().Set(name, value)
		}
		http.ServeFileFS(w, r, site.Pages, fileJoin)
	})

	// Everything else is a page of the site's. The pattern names no
	// method, since one that did would overlap the containers' pattern
	// above without being more specific than it.
	mux.Handle("/", pages)
	return mux
}

// serveSocket is one tab's whole time as a node: the tab says what it
// is, becomes a node, is told which containers to show, and stops
// being a node when its connection ends.
func (a *Agent) serveSocket(w http.ResponseWriter, r *http.Request) {
	// A browser says which page opened the socket, and only the site's
	// own pages may: the proxy in front of the agent may hand the
	// request over under another host name than the visitor typed.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.origins})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(helloLimit)
	ctx := r.Context()

	// What the tab is.
	var hello tab.Hello
	helloCtx, cancel := context.WithTimeout(ctx, helloTimeout)
	err = wsjson.Read(helloCtx, conn, &hello)
	cancel()
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "no hello")
		return
	}

	node, err := a.Join(ctx, hello, clientAddress(r))
	if err != nil {
		reason := "the cluster could not take this tab; try again in a moment"
		if errors.Is(err, ErrFull) || errors.Is(err, ErrTooManyHere) {
			reason = err.Error()
		} else {
			a.log.Printf("join: %v", err)
		}
		write(ctx, conn, Message{Type: MessageRefused, Reason: reason})
		conn.Close(websocket.StatusTryAgainLater, "refused")
		return
	}
	defer a.Leave(node)
	if err := write(ctx, conn, Message{Type: MessageJoined, Node: node.Name}); err != nil {
		return
	}

	// The tab's own messages. There is one, the goodbye, and reading is
	// also what answers the tab's pings and hears the pongs.
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		for {
			var message Message
			if err := wsjson.Read(ctx, conn, &message); err != nil || message.Type == messageBye {
				return
			}
		}
	}()

	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	for {
		select {
		case message := <-node.Messages():
			if err := write(ctx, conn, message); err != nil {
				return
			}
		case <-ticker.C:
			// A tab that does not answer is gone, whatever its
			// connection says: a phone that locks keeps its sockets
			// open for a while and runs nothing.
			pingCtx, cancel := context.WithTimeout(ctx, pongTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
			nodes, pods := a.Counts()
			if err := write(ctx, conn, Message{Type: MessageCluster, Nodes: nodes, Pods: pods}); err != nil {
				return
			}
		case <-node.Lost():
			conn.Close(websocket.StatusGoingAway, "the node was removed")
			return
		case <-gone:
			return
		case <-a.ctx.Done():
			conn.Close(websocket.StatusGoingAway, "the agent is stopping")
			return
		}
	}
}

// write sends one message to a tab's page.
func write(ctx context.Context, conn *websocket.Conn, message Message) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, message)
}

// clientAddress returns where a request came from: the last address the
// proxy in front of the agent recorded, which is the one the proxy saw
// itself, or the connection's own when there is no proxy.
func clientAddress(r *http.Request) string {
	if forwarded := r.Header.Get(forwardedFor); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// The directories of a container's page that are the same for every
// container. A request for a file in one is sent to the site's own copy,
// which a browser keeps: the kernel is 11 MB, and a tab that runs a
// second pod should not fetch it a second time.
var sharedDirs = []string{"unkernel/", "xterm/"}

// serveContainer answers a tab's request for a container's page, or
// anything the page asks of tabc: the request goes to the container's
// tabc, inside the pod's sandbox.
func (a *Agent) serveContainer(w http.ResponseWriter, r *http.Request, share *Share) {
	id, rest, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, containerPrefix), "/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	running := a.containers[id]
	a.mu.Unlock()
	if running == nil {
		http.NotFound(w, r)
		return
	}
	proxy := running.page()
	if proxy == nil {
		http.Error(w, "the container is not up", http.StatusServiceUnavailable)
		return
	}

	for _, dir := range sharedDirs {
		if strings.HasPrefix(rest, dir) {
			http.Redirect(w, r, share.Path(rest), http.StatusFound)
			return
		}
	}
	proxy.ServeHTTP(w, r)
}

// newPageProxy returns what carries a tab's requests to a container's
// tabc: to tabc's page server, on the loopback of the pod's sandbox,
// with the container's secret in place of the container's id.
func newPageProxy(id string, link *url.URL, sandbox *runtime.Sandbox, carried func(down, up int64)) *httputil.ReverseProxy {
	prefix := containerPath(id)
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			target := *link
			target.Path = link.Path + strings.TrimPrefix(request.In.URL.Path, prefix)
			target.RawQuery = request.In.URL.RawQuery
			request.Out.URL = &target
			request.Out.Host = link.Host

			// tabc takes a WebSocket only from its own page, which it
			// knows by the origin the browser names. The tab loaded the
			// page from the agent's address, so the origin is put back
			// to the one tabc serves the page on. What stands in for
			// that check here is the container's id in the address,
			// which only the page's own tab was told.
			if request.Out.Header.Get(headerOrigin) != "" {
				request.Out.Header.Set(headerOrigin, link.Scheme+"://"+link.Host)
			}
		},
		// Connections are kept between requests: a guest reads its files
		// a request at a time, and each new connection is a trip into
		// the sandbox.
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				conn, err := sandbox.Dial(ctx, link.Host)
				if err != nil {
					return nil, err
				}
				return &countedConn{Conn: conn, carried: carried}, nil
			},
			MaxIdleConnsPerHost: pageIdleConnections,
			IdleConnTimeout:     pageIdleTimeout,
		},
		// The page reads the guest's files and the program's output as
		// they come.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "the container's page: "+err.Error(), http.StatusBadGateway)
		},
	}
}

// headerOrigin is the header a browser names the page that made a
// request in.
const headerOrigin = "Origin"

// How many idle connections to one container's tabc are kept, and for
// how long.
const (
	pageIdleConnections = 8
	pageIdleTimeout     = 30 * time.Second
)

// countedConn is a connection to a container's tabc that reports how
// much passes over it: what is read came from tabc and goes down to the
// tab, and what is written came up from the tab.
type countedConn struct {
	net.Conn
	carried func(down, up int64)
}

// Read reads from tabc and counts what was read as sent to the tab.
func (c *countedConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	if n > 0 {
		c.carried(int64(n), 0)
	}
	return n, err
}

// Write writes to tabc and counts what was written as come from the
// tab.
func (c *countedConn) Write(buffer []byte) (int, error) {
	n, err := c.Conn.Write(buffer)
	if n > 0 {
		c.carried(0, int64(n))
	}
	return n, err
}
