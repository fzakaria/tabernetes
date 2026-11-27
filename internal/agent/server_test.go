package agent

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// testSite returns an agent's handler over a share with one kernel file
// and a site with a front page and a join page. The agent has no
// cluster: the routes under test ask nothing of one.
func testSite(t *testing.T) (http.Handler, *Share) {
	t.Helper()
	pages := fstest.MapFS{
		"index.html": {Data: []byte("front page")},
		"join.html":  {Data: []byte("join page")},
	}
	return testSiteOf(t, pages, Site{PublicURL: "https://cluster.example"})
}

// testSiteOf returns an agent's handler over the same share as
// testSite, with the pages and the site's settings the test gives.
func testSiteOf(t *testing.T, pages fstest.MapFS, site Site) (http.Handler, *Share) {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range map[string]string{
		"unkernel/unkernel.wasm": strings.Repeat("kernel ", 1000),
		"xterm/xterm.js":         "terminal",
		"page/guest.js":          "not shared",
	} {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	share, err := OpenShare(dir)
	if err != nil {
		t.Fatal(err)
	}

	site.Pages, site.Share = pages, share
	agent := New(Config{Log: log.New(io.Discard, "", 0), MaxNodes: 3, StateDir: t.TempDir()})
	handler, err := agent.Handler(site)
	if err != nil {
		t.Fatal(err)
	}
	return handler, share
}

// get asks the handler for a path.
func get(handler http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for name, value := range header {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// TestPagesAreFilledIn checks that a page is served with the site's
// public address and the build's name written where the page asks for
// them, which is what a link preview and the footer are made of: by
// serving a front page and a join page that ask for both, from a site
// whose address was given with a slash at its end.
func TestPagesAreFilledIn(t *testing.T) {
	pages := fstest.MapFS{
		"index.html": {Data: []byte(`<meta content="{{.PublicURL}}/card.png" /><code>{{.Build}}</code>`)},
		"join.html":  {Data: []byte(`<a href="{{.PublicURL}}/">{{.Build}}</a>`)},
	}
	handler, _ := testSiteOf(t, pages, Site{PublicURL: "https://cluster.example/", Build: "/nix/store/abc-tabernetes"})

	front := get(handler, "/", nil)
	want := `<meta content="https://cluster.example/card.png" /><code>/nix/store/abc-tabernetes</code>`
	if front.Code != http.StatusOK || front.Body.String() != want {
		t.Errorf("/: %d %q", front.Code, front.Body.String())
	}
	if got := front.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("/ content type %q", got)
	}

	join := get(handler, "/join", nil)
	want = `<a href="https://cluster.example/">/nix/store/abc-tabernetes</a>`
	if join.Code != http.StatusOK || join.Body.String() != want {
		t.Errorf("/join: %d %q", join.Code, join.Body.String())
	}
}

// TestPageThatDoesNotParse checks that a page with a blank the agent
// cannot fill is found when the handler is made, not when a visitor
// asks for the page: by making a handler over such a front page.
func TestPageThatDoesNotParse(t *testing.T) {
	pages := fstest.MapFS{
		"index.html": {Data: []byte("{{.NoSuchField}}")},
		"join.html":  {Data: []byte("join page")},
	}
	agent := New(Config{Log: log.New(io.Discard, "", 0), MaxNodes: 3, StateDir: t.TempDir()})
	if _, err := agent.Handler(Site{Pages: pages}); err == nil {
		t.Error("a page with an unknown blank made a handler")
	}
}

// TestPages checks that the site's routes can be registered together,
// that the front page is served at the root, and that the join page is
// served isolated from other origins, which is what lets it frame a
// container's page.
func TestPages(t *testing.T) {
	handler, _ := testSite(t)

	front := get(handler, "/", nil)
	if front.Code != http.StatusOK || front.Body.String() != "front page" {
		t.Errorf("/: %d %q", front.Code, front.Body.String())
	}

	join := get(handler, "/join", nil)
	if join.Code != http.StatusOK || join.Body.String() != "join page" {
		t.Errorf("/join: %d %q", join.Code, join.Body.String())
	}
	if got := join.Header().Get("Cross-Origin-Embedder-Policy"); got != "require-corp" {
		t.Errorf("/join embedder policy %q", got)
	}
}

// TestCluster checks the cluster's description for an agent nothing
// has joined: JSON with no nodes, room for as many as the agent's
// limit, and totals that begin now, and the same JSON gzipped for a
// browser that takes gzip.
func TestCluster(t *testing.T) {
	handler, _ := testSite(t)
	response := get(handler, "/api/cluster", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("%d", response.Code)
	}
	var view View
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Nodes) != 0 || view.Room != 3 || view.Totals.Joins != 0 || view.Totals.Since.IsZero() {
		t.Errorf("%+v", view)
	}
	if !strings.Contains(response.Body.String(), `"nodes":[]`) || !strings.Contains(response.Body.String(), `"events":[]`) {
		t.Errorf("empty lists are not lists: %s", response.Body.String())
	}

	zipped := get(handler, "/api/cluster", map[string]string{"Accept-Encoding": "gzip"})
	reader, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	unzipped, _ := io.ReadAll(reader)
	if !strings.Contains(string(unzipped), `"room":3`) {
		t.Errorf("gzipped: %s", unzipped)
	}
}

// TestAskUnknown checks that a request for a Service the agent does not
// expose is not found, whatever the cluster has by that name.
func TestAskUnknown(t *testing.T) {
	handler, _ := testSite(t)
	if response := get(handler, "/apps/kubernetes/", nil); response.Code != http.StatusNotFound {
		t.Errorf("%d", response.Code)
	}
}

// TestAskers checks that a visitor may ask a few times at once and is
// then refused, and that another visitor is not refused with them.
func TestAskers(t *testing.T) {
	var limits askers
	for request := 0; request < asksAtOnce; request++ {
		if !limits.allow("203.0.113.7") {
			t.Fatalf("request %d was refused", request)
		}
	}
	if limits.allow("203.0.113.7") {
		t.Error("a request past the allowance was let through")
	}
	if !limits.allow("203.0.113.8") {
		t.Error("another visitor was refused")
	}
}

// TestShare checks a shared file's address and answer: served under the
// share's version with its own type and leave to keep it for good,
// gzipped for a browser that takes gzip and plain for one that does
// not, and that a file outside the shared directories, a wrong version
// are not found, and that a path that climbs out is not served.
func TestShare(t *testing.T) {
	handler, share := testSite(t)
	address := share.Path("unkernel/unkernel.wasm")
	if !strings.HasPrefix(address, "/static/") {
		t.Fatalf("address %q", address)
	}

	plain := get(handler, address, nil)
	if plain.Code != http.StatusOK || plain.Body.Len() != 7000 {
		t.Errorf("plain: %d, %d bytes", plain.Code, plain.Body.Len())
	}
	if got := plain.Header().Get("Content-Type"); got != "application/wasm" {
		t.Errorf("type %q", got)
	}
	if got := plain.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("cache control %q", got)
	}

	zipped := get(handler, address, map[string]string{"Accept-Encoding": "gzip, br"})
	if zipped.Header().Get("Content-Encoding") != "gzip" || zipped.Body.Len() >= 7000 {
		t.Errorf("gzip: encoding %q, %d bytes", zipped.Header().Get("Content-Encoding"), zipped.Body.Len())
	}
	reader, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	unzipped, _ := io.ReadAll(reader)
	if len(unzipped) != 7000 {
		t.Errorf("unzipped to %d bytes", len(unzipped))
	}

	for _, missing := range []string{
		share.Path("page/guest.js"),
		"/static/000000000000/unkernel/unkernel.wasm",
		share.Path("unkernel/nothing.wasm"),
	} {
		if response := get(handler, missing, map[string]string{"Accept-Encoding": "gzip"}); response.Code != http.StatusNotFound {
			t.Errorf("%s: %d", missing, response.Code)
		}
	}

	// The router answers a path that climbs with a redirect to the path
	// it climbs to, which is one of the refused paths above. Either
	// way the file is not served.
	climbing := get(handler, share.Path("unkernel/../page/guest.js"), nil)
	if climbing.Code == http.StatusOK || strings.Contains(climbing.Body.String(), "not shared") {
		t.Errorf("a path that climbs out was served: %d", climbing.Code)
	}
}

// TestContainerUnknown checks that a container's address with an id no
// run has is not found.
func TestContainerUnknown(t *testing.T) {
	handler, _ := testSite(t)
	if response := get(handler, "/c/0123456789abcdef/", nil); response.Code != http.StatusNotFound {
		t.Errorf("%d", response.Code)
	}
}

// TestClientAddress checks whose address a request is taken to be from:
// the last one in the proxy's header, which the proxy wrote itself, and
// the connection's own when there is no header.
func TestClientAddress(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/ws", nil)
	request.RemoteAddr = "10.0.0.9:4000"
	if got := clientAddress(request); got != "10.0.0.9" {
		t.Errorf("no header: %q", got)
	}
	request.Header.Set(forwardedFor, "1.2.3.4, 203.0.113.7")
	if got := clientAddress(request); got != "203.0.113.7" {
		t.Errorf("with a header: %q", got)
	}
}
