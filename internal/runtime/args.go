// Package runtime runs a pod's container in a tab: a network namespace
// for the pod on the agent's machine, and a `tabc run --link` inside
// the namespace, whose page the tab loads.
package runtime

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// The guest's memory in MiB: what a container that asks for none gets,
// which is the least tabc gives a guest.
const defaultMemoryMiB = 128

// bytesPerMiB turns a Kubernetes quantity into tabc's unit.
const bytesPerMiB = 1 << 20

// guestCPUs is how many CPUs a guest in a tab has. More than one needs
// shared memory, which a page in a frame of another page is not given
// on every browser, and a phone has few cores to lend.
const guestCPUs = "1"

// publishAddress is where a container's ports listen: every address of
// the pod's network namespace, so on the pod's own address.
const publishAddress = "0.0.0.0"

// serviceAccountMount is where Kubernetes mounts a pod's token for the
// API server. A tab is a stranger's device, and gets no token.
const serviceAccountMount = "/var/run/secrets/kubernetes.io/serviceaccount"

// Limits is what the agent allows a pod.
type Limits struct {
	// MaxMemoryMiB is the most memory a guest gets, whatever its pod
	// asks for.
	MaxMemoryMiB int64
	// HostPathRoot is the one directory a hostPath volume may be under.
	// Empty refuses every hostPath volume.
	HostPathRoot string
}

// Plan is a pod as tabc is going to run it.
type Plan struct {
	// Image is the container's image, to fetch before the run.
	Image string
	// Args are the words after `tabc`.
	Args []string
	// EmptyDirs are the directories to make before the run, one for
	// each emptyDir volume the container mounts.
	EmptyDirs []string
	// Skipped says what of the pod the run leaves out, for an event.
	Skipped []string
}

// PlanFor returns how tabc runs a pod: the pod's one container, named
// `name` in tabc, with the pod's volumes under `podDir` on the agent's
// machine. A pod that cannot be run is an error that says why.
func PlanFor(pod *corev1.Pod, name, podDir string, limits Limits) (Plan, error) {
	if len(pod.Spec.Containers) != 1 {
		return Plan{}, fmt.Errorf("a tab runs one container, and the pod has %d", len(pod.Spec.Containers))
	}
	if len(pod.Spec.InitContainers) > 0 {
		return Plan{}, fmt.Errorf("a tab runs no init containers, and the pod has %d", len(pod.Spec.InitContainers))
	}
	container := pod.Spec.Containers[0]
	plan := Plan{Image: container.Image}

	args := []string{"run", "--link", "--name", name, "--cpus", guestCPUs}
	args = append(args, "-m", strconv.FormatInt(memoryMiB(container, limits), 10)+"m")

	// The environment: the values the pod spells out. A value taken
	// from a secret, a config map or the pod's own fields is left out.
	for _, variable := range container.Env {
		if variable.ValueFrom != nil {
			plan.Skipped = append(plan.Skipped, "env "+variable.Name+" (valueFrom)")
			continue
		}
		args = append(args, "-e", variable.Name+"="+variable.Value)
	}
	if len(container.EnvFrom) > 0 {
		plan.Skipped = append(plan.Skipped, "envFrom")
	}

	if container.WorkingDir != "" {
		args = append(args, "-w", container.WorkingDir)
	}
	if security := container.SecurityContext; security != nil && security.RunAsUser != nil {
		args = append(args, "-u", strconv.FormatInt(*security.RunAsUser, 10))
	}

	// The ports the container names, each on the pod's address. A port
	// the pod does not name is not reachable.
	for _, port := range container.Ports {
		if port.Protocol != "" && port.Protocol != corev1.ProtocolTCP {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("port %d (%s)", port.ContainerPort, port.Protocol))
			continue
		}
		number := strconv.Itoa(int(port.ContainerPort))
		args = append(args, "-p", publishAddress+":"+number+":"+number)
	}

	// The volumes: directories of the agent's machine.
	volumes := map[string]corev1.Volume{}
	for _, volume := range pod.Spec.Volumes {
		volumes[volume.Name] = volume
	}
	for _, mount := range container.VolumeMounts {
		if mount.MountPath == serviceAccountMount {
			continue
		}
		volume := volumes[mount.Name]
		var source string
		switch {
		case volume.HostPath != nil:
			if !under(limits.HostPathRoot, volume.HostPath.Path) {
				return Plan{}, fmt.Errorf("volume %s: hostPath %s is not under %s", volume.Name, volume.HostPath.Path, limits.HostPathRoot)
			}
			source = filepath.Clean(volume.HostPath.Path)
		case volume.EmptyDir != nil:
			source = filepath.Join(podDir, "volumes", volume.Name)
			plan.EmptyDirs = append(plan.EmptyDirs, source)
		default:
			plan.Skipped = append(plan.Skipped, "volume "+mount.Name)
			continue
		}
		spec := source + ":" + mount.MountPath
		if mount.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}

	// A pod's command replaces the image's entry point, and its args the
	// image's arguments. tabc, like docker, takes one word as the entry
	// point; the command's other words go in front of the arguments.
	words := container.Args
	if len(container.Command) > 0 {
		args = append(args, "--entrypoint", container.Command[0])
		words = append(append([]string{}, container.Command[1:]...), container.Args...)
	}
	args = append(args, container.Image)
	plan.Args = append(args, words...)
	return plan, nil
}

// memoryMiB returns the guest's memory: the container's limit, or what
// the container requests when there is no limit, held between tabc's
// least and the agent's most.
func memoryMiB(container corev1.Container, limits Limits) int64 {
	asked := container.Resources.Limits.Memory()
	if asked.IsZero() {
		asked = container.Resources.Requests.Memory()
	}

	// Rounded up, so that a pod asking for a fraction of a MiB more than
	// a whole number gets it.
	amount := (asked.Value() + bytesPerMiB - 1) / bytesPerMiB
	if amount < defaultMemoryMiB {
		amount = defaultMemoryMiB
	}
	if limits.MaxMemoryMiB > 0 && amount > limits.MaxMemoryMiB {
		amount = limits.MaxMemoryMiB
	}
	return amount
}

// under reports whether path is root or inside root.
func under(root, path string) bool {
	if root == "" || !filepath.IsAbs(path) {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, "../"))
}
