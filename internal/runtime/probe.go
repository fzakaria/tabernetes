package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// What a probe is given when its pod says nothing: how often it is
// made, how long one try may take, and the path asked for.
const (
	defaultProbePeriod  = 2 * time.Second
	defaultProbeTimeout = time.Second
	defaultProbePath    = "/"
)

// The statuses that count as a pass: every success and every redirect,
// as with a kubelet.
const (
	probePassFrom = http.StatusOK
	probePassTo   = http.StatusBadRequest
)

// Probe is a container's readiness probe as the agent makes it: an HTTP
// GET of a path on a port of the pod.
type Probe struct {
	Port   int
	Path   string
	Period time.Duration
	// Timeout is how long one try may take.
	Timeout time.Duration
}

// ProbeFor returns a container's readiness probe, and whether the
// container has one the agent can make. Only an HTTP GET is: a tab's
// pod has no shell to run a command in from outside, and tabc accepts a
// connection on a pod's port before the program listens, so a
// connection alone says nothing.
func ProbeFor(container corev1.Container) (Probe, bool) {
	spec := container.ReadinessProbe
	if spec == nil || spec.HTTPGet == nil {
		return Probe{}, false
	}

	// The port, by number or by the name the container gave one.
	port := spec.HTTPGet.Port.IntValue()
	if port == 0 {
		for _, named := range container.Ports {
			if named.Name == spec.HTTPGet.Port.String() {
				port = int(named.ContainerPort)
			}
		}
	}
	if port == 0 {
		return Probe{}, false
	}

	probe := Probe{Port: port, Path: spec.HTTPGet.Path, Period: defaultProbePeriod, Timeout: defaultProbeTimeout}
	if probe.Path == "" {
		probe.Path = defaultProbePath
	}
	if spec.PeriodSeconds > 0 {
		probe.Period = time.Duration(spec.PeriodSeconds) * time.Second
	}
	if spec.TimeoutSeconds > 0 {
		probe.Timeout = time.Duration(spec.TimeoutSeconds) * time.Second
	}
	return probe, true
}

// Passes makes the probe once against a pod's address.
func (p Probe) Passes(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()

	target := "http://" + net.JoinHostPort(address, strconv.Itoa(p.Port)) + p.Path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode < probePassFrom || response.StatusCode >= probePassTo {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	return nil
}
