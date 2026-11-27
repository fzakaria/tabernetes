package agent

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/fzakaria/tabernetes/internal/kube"
)

// TestAskIsTimed checks that the ledger records how long each request
// a visitor sends into the cluster takes, and how the request ended:
// by exposing a Service that is a server of the test's own, asking it
// once while the server answers and once after the server has closed,
// and then asking for a Service the agent does not expose, which is no
// request into the cluster and so is not recorded.
func TestAskIsTimed(t *testing.T) {
	// The Service: a server that answers every request.
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "an answer")
	}))
	defer service.Close()
	host, portText, err := net.SplitHostPort(service.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	// An agent that exposes the Service, and has listed it, behind
	// the one route under test.
	agent := New(Config{
		Log:      log.New(io.Discard, "", 0),
		MaxNodes: 3,
		StateDir: t.TempDir(),
		Exposed:  map[string]Exposed{"counter": {Namespace: "default", Service: "web", Port: port}},
	})
	agent.services = []kube.Service{{Namespace: "default", Name: "web", ClusterIP: host}}
	handler := http.NewServeMux()
	handler.HandleFunc(pathApps, agent.serveApp)

	// A request a pod answers.
	if response := get(handler, "/apps/counter/json", nil); response.Code != http.StatusOK || response.Body.String() != "an answer" {
		t.Fatalf("with the Service up: %d %q", response.Code, response.Body.String())
	}

	// A request no pod answers, and one that never enters the cluster.
	service.Close()
	if response := get(handler, "/apps/counter/json", nil); response.Code != http.StatusBadGateway {
		t.Fatalf("with the Service down: %d", response.Code)
	}
	if response := get(handler, "/apps/other/json", nil); response.Code != http.StatusNotFound {
		t.Fatalf("for a Service not exposed: %d", response.Code)
	}

	totals, _, _, asks := agent.ledger.view()
	if totals.Asked != 2 || len(asks) != 2 {
		t.Fatalf("asked %d, with %d asks kept: %+v", totals.Asked, len(asks), asks)
	}
	for index, status := range []int{http.StatusOK, http.StatusBadGateway} {
		ask := asks[index]
		if ask.App != "counter" || ask.Status != status || ask.Seconds <= 0 || ask.At.IsZero() {
			t.Errorf("ask %d: %+v, want status %d", index, ask, status)
		}
	}
}

// TestClusterListsAsks checks that the cluster's description lists the
// asks, and lists none as an empty list for a page to count, by
// reading the description of an agent nobody has asked anything of.
func TestClusterListsAsks(t *testing.T) {
	handler, _ := testSite(t)
	response := get(handler, "/api/cluster", nil)
	if got := response.Body.String(); !strings.Contains(got, `"asks":[]`) {
		t.Errorf("no empty list of asks in %s", got)
	}
}
