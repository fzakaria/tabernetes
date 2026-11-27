package agent

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/fzakaria/tabernetes/internal/kube"
)

// The Service the tests expose, and the port the Service listens on.
const (
	testApp         = "counter"
	testServicePort = 8000
)

// podOn puts a running pod on a node of the agent's, with the labels
// the test's Service selects and the address a stand-in for the pod's
// program listens on.
func podOn(agent *Agent, nodeName, podName, ip string) {
	node := agent.nodes[nodeName]
	if node == nil {
		node = &Node{Name: nodeName}
		agent.nodes[nodeName] = node
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Name:      podName,
		UID:       types.UID(podName),
		Labels:    map[string]string{"app": "web"},
	}}
	running := &run{pod: pod, node: node, id: podName}
	running.show(ip, true, 0)
	agent.runs[pod.UID] = running
}

// TestAskOneNode checks that a request for an exposed Service's pod on
// one node reaches that node's pod and no other, and that a node
// without such a pod is answered with an error and not with another
// node's pod: by putting a stand-in for a pod's program behind the
// Service on one node, and asking for that node and for another.
func TestAskOneNode(t *testing.T) {
	// The pod's program, which says what was asked of it.
	program := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("answered " + r.URL.Path))
	}))
	defer program.Close()
	host, portText, err := net.SplitHostPort(program.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	agent := testAgent(t, func(config *Config) {
		config.Exposed = map[string]Exposed{testApp: {Namespace: "default", Service: "web", Port: testServicePort}}
	})
	agent.services = []kube.Service{{
		Namespace: "default",
		Name:      "web",
		ClusterIP: "192.0.2.1",
		Ports:     []int32{testServicePort},
		Targets:   map[int32]int32{testServicePort: int32(port)},
		Selector:  map[string]string{"app": "web"},
	}}
	podOn(agent, "pixel-1", "web-1", host)

	mux := http.NewServeMux()
	mux.HandleFunc(pathNodeApps, agent.serveNodeApp)

	// The node with the pod answers, with the rest of the path.
	response := get(mux, "/nodes/pixel-1/apps/"+testApp+"/tally", nil)
	if response.Code != http.StatusOK || response.Body.String() != "answered /tally" {
		t.Errorf("the node's pod: %d %q", response.Code, response.Body.String())
	}

	// A node without a pod of the Service has nothing to answer with.
	response = get(mux, "/nodes/pixel-2/apps/"+testApp+"/", nil)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "answered") {
		t.Errorf("a node without the pod: %d %q", response.Code, response.Body.String())
	}

	// A Service the agent does not expose is not found on any node.
	response = get(mux, "/nodes/pixel-1/apps/redis/", nil)
	if response.Code != http.StatusNotFound {
		t.Errorf("a Service that is not exposed: %d", response.Code)
	}
}
