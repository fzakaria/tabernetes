package runtime

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// limits is what the tests' agent allows.
var limits = Limits{MaxMemoryMiB: 256, HostPathRoot: "/var/lib/tabernetes/data"}

// podWith returns a pod with one container.
func podWith(container corev1.Container, volumes ...corev1.Volume) *corev1.Pod {
	return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{container}, Volumes: volumes}}
}

// TestPlanForMinimal checks the words for a pod that names only an
// image: link mode, the container's name, one CPU, the default memory,
// and the image last.
func TestPlanForMinimal(t *testing.T) {
	plan, err := PlanFor(podWith(corev1.Container{Name: "main", Image: "alpine:3.22"}), "c1", "/pods/p", limits)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--link", "--name", "c1", "--cpus", "1", "-m", "128m", "alpine:3.22"}
	if !reflect.DeepEqual(plan.Args, want) {
		t.Errorf("got  %q\nwant %q", plan.Args, want)
	}
	if plan.Image != "alpine:3.22" {
		t.Errorf("image %q", plan.Image)
	}
}

// TestPlanForCommand checks the three ways a pod names its program: a
// command replaces the entry point and its other words lead the
// arguments, args alone follow the image, and both together are the
// command and then the args.
func TestPlanForCommand(t *testing.T) {
	cases := []struct {
		command, args []string
		wantTail      []string
	}{
		{[]string{"redis-server", "--appendonly", "yes"}, nil, []string{"--entrypoint", "redis-server", "redis", "--appendonly", "yes"}},
		{nil, []string{"--port", "7000"}, []string{"redis", "--port", "7000"}},
		{[]string{"sh", "-c"}, []string{"echo hi"}, []string{"--entrypoint", "sh", "redis", "-c", "echo hi"}},
	}
	for _, c := range cases {
		plan, err := PlanFor(podWith(corev1.Container{Image: "redis", Command: c.command, Args: c.args}), "c1", "/pods/p", limits)
		if err != nil {
			t.Fatal(err)
		}
		tail := plan.Args[len(plan.Args)-len(c.wantTail):]
		if !reflect.DeepEqual(tail, c.wantTail) {
			t.Errorf("command %q args %q: got %q, want %q", c.command, c.args, tail, c.wantTail)
		}
	}
}

// TestPlanForEnvPortsAndUser checks the flags for a literal variable, a
// TCP port, a working directory and a user, and that a variable from a
// secret and a UDP port are left out and said to be.
func TestPlanForEnvPortsAndUser(t *testing.T) {
	user := int64(1000)
	container := corev1.Container{
		Image: "app",
		Env: []corev1.EnvVar{
			{Name: "MODE", Value: "demo"},
			{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{}},
		},
		Ports: []corev1.ContainerPort{
			{ContainerPort: 6379},
			{ContainerPort: 53, Protocol: corev1.ProtocolUDP},
		},
		WorkingDir:      "/srv",
		SecurityContext: &corev1.SecurityContext{RunAsUser: &user},
	}
	plan, err := PlanFor(podWith(container), "c1", "/pods/p", limits)
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(plan.Args, " ")
	for _, want := range []string{"-e MODE=demo", "-p 0.0.0.0:6379:6379", "-w /srv", "-u 1000"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q is not in %q", want, joined)
		}
	}
	for _, unwanted := range []string{"TOKEN", ":53"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("%q is in %q", unwanted, joined)
		}
	}
	if len(plan.Skipped) != 2 {
		t.Errorf("skipped %q", plan.Skipped)
	}
}

// TestPlanForMemory checks that the guest's memory is the container's
// limit, else its request, rounded up to a MiB, never under tabc's
// least and never over the agent's most.
func TestPlanForMemory(t *testing.T) {
	cases := []struct {
		limit, request string
		want           string
	}{
		{"", "", "128m"},
		{"200Mi", "64Mi", "200m"},
		{"", "192Mi", "192m"},
		{"16Mi", "", "128m"},
		{"4Gi", "", "256m"},
		{"150M", "", "144m"},
	}
	for _, c := range cases {
		container := corev1.Container{Image: "app"}
		if c.limit != "" {
			container.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(c.limit)}
		}
		if c.request != "" {
			container.Resources.Requests = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(c.request)}
		}
		plan, err := PlanFor(podWith(container), "c1", "/pods/p", limits)
		if err != nil {
			t.Fatal(err)
		}
		got := plan.Args[indexOf(plan.Args, "-m")+1]
		if got != c.want {
			t.Errorf("limit %q request %q: got %s, want %s", c.limit, c.request, got, c.want)
		}
	}
}

// TestPlanForVolumes checks that a hostPath under the allowed root and
// an emptyDir are mounted, the emptyDir from a directory of the pod's
// that the plan lists to be made, that the service account's token is
// passed over in silence, and that a config map is skipped and said to
// be.
func TestPlanForVolumes(t *testing.T) {
	container := corev1.Container{
		Image: "app",
		VolumeMounts: []corev1.VolumeMount{
			{Name: "data", MountPath: "/data"},
			{Name: "scratch", MountPath: "/tmp/scratch", ReadOnly: true},
			{Name: "kube-api-access-x", MountPath: serviceAccountMount},
			{Name: "settings", MountPath: "/etc/app"},
		},
	}
	pod := podWith(container,
		corev1.Volume{Name: "data", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/tabernetes/data/redis"}}},
		corev1.Volume{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		corev1.Volume{Name: "kube-api-access-x", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{}}},
		corev1.Volume{Name: "settings", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}}},
	)
	plan, err := PlanFor(pod, "c1", "/pods/p", limits)
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(plan.Args, " ")
	for _, want := range []string{"-v /var/lib/tabernetes/data/redis:/data", "-v /pods/p/volumes/scratch:/tmp/scratch:ro"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q is not in %q", want, joined)
		}
	}
	if !reflect.DeepEqual(plan.EmptyDirs, []string{"/pods/p/volumes/scratch"}) {
		t.Errorf("empty dirs %q", plan.EmptyDirs)
	}
	if !reflect.DeepEqual(plan.Skipped, []string{"volume settings"}) {
		t.Errorf("skipped %q", plan.Skipped)
	}
}

// TestPlanForRefusals checks the pods a tab cannot run: two containers,
// an init container, and a hostPath outside the allowed root, by a
// plain path and by one that climbs out.
func TestPlanForRefusals(t *testing.T) {
	two := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "a"}, {Image: "b"}}}}
	if _, err := PlanFor(two, "c1", "/pods/p", limits); err == nil {
		t.Error("two containers were planned")
	}

	withInit := podWith(corev1.Container{Image: "a"})
	withInit.Spec.InitContainers = []corev1.Container{{Image: "setup"}}
	if _, err := PlanFor(withInit, "c1", "/pods/p", limits); err == nil {
		t.Error("an init container was planned")
	}

	for _, path := range []string{"/etc", "/var/lib/tabernetes/data/../../../etc", "relative"} {
		pod := podWith(
			corev1.Container{Image: "a", VolumeMounts: []corev1.VolumeMount{{Name: "v", MountPath: "/v"}}},
			corev1.Volume{Name: "v", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path}}},
		)
		if _, err := PlanFor(pod, "c1", "/pods/p", limits); err == nil {
			t.Errorf("hostPath %s was planned", path)
		}
	}
}

// indexOf returns where a word is in a list, or -1.
func indexOf(words []string, word string) int {
	for index, candidate := range words {
		if candidate == word {
			return index
		}
	}
	return -1
}
