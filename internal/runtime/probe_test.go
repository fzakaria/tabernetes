package runtime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// TestProbeFor checks which readiness probes the agent makes: an HTTP
// GET, with its port given by number or by the container's name for
// one, and the pod's own period and timeout where it sets them. A
// probe that runs a command, opens a socket, or names a port the
// container does not have is not one the agent makes.
func TestProbeFor(t *testing.T) {
	ports := []corev1.ContainerPort{{Name: "http", ContainerPort: 8000}}
	get := func(port intstr.IntOrString) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: port}}}
	}

	byNumber, ok := ProbeFor(corev1.Container{Ports: ports, ReadinessProbe: get(intstr.FromInt32(8000))})
	if !ok || byNumber.Port != 8000 || byNumber.Path != "/healthz" || byNumber.Period != defaultProbePeriod {
		t.Errorf("by number: %+v, %v", byNumber, ok)
	}

	byName, ok := ProbeFor(corev1.Container{Ports: ports, ReadinessProbe: get(intstr.FromString("http"))})
	if !ok || byName.Port != 8000 {
		t.Errorf("by name: %+v, %v", byName, ok)
	}

	timed := get(intstr.FromInt32(8000))
	timed.PeriodSeconds, timed.TimeoutSeconds = 7, 3
	if probe, _ := ProbeFor(corev1.Container{ReadinessProbe: timed}); probe.Period != 7*time.Second || probe.Timeout != 3*time.Second {
		t.Errorf("timed: %+v", probe)
	}

	refused := []corev1.Container{
		{},
		{ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"true"}}}}},
		{ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(6379)}}}},
		{ReadinessProbe: get(intstr.FromString("metrics"))},
	}
	for index, container := range refused {
		if _, ok := ProbeFor(container); ok {
			t.Errorf("container %d has a probe", index)
		}
	}
}

// TestProbePasses checks a probe against a server: a 200 passes, a 503
// fails, and a port nothing listens on fails.
func TestProbePasses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	host, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)

	healthy := Probe{Port: port, Path: "/healthz", Timeout: time.Second}
	if err := healthy.Passes(context.Background(), host); err != nil {
		t.Errorf("a 200: %v", err)
	}
	unhealthy := Probe{Port: port, Path: "/other", Timeout: time.Second}
	if err := unhealthy.Passes(context.Background(), host); err == nil {
		t.Error("a 503 passed")
	}

	server.Close()
	if err := healthy.Passes(context.Background(), host); err == nil {
		t.Error("a closed port passed")
	}
}
