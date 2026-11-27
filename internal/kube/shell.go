package kube

import (
	"regexp"

	corev1 "k8s.io/api/core/v1"
)

// AnnotationShell is the annotation a pod lets its tab have a shell
// with. The value names the user the shell runs as, by name or by
// number:
//
//	tabernetes.dev/shell: nobody
//
// The shell is a second program in the pod's container, which is what
// `kubectl exec` starts on any other node. A pod without the
// annotation gives its tab no shell: whoever wrote the pod knows what
// its volumes and its neighbours are worth, and the tab is a stranger.
const AnnotationShell = "tabernetes.dev/shell"

// shellUserPattern is what a user's name or number looks like. The
// value is handed to tabc as a word of its command line, so nothing
// that could be read as a flag passes.
var shellUserPattern = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}|[0-9]{1,10})$`)

// ShellUser returns the user a pod lets its tab have a shell as, and
// whether the pod allows a shell at all.
func ShellUser(pod *corev1.Pod) (string, bool) {
	user := pod.Annotations[AnnotationShell]
	if !shellUserPattern.MatchString(user) {
		return "", false
	}
	return user, true
}
