package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestShellUser checks which pods let their tab have a shell, and as
// whom: a pod says so with an annotation that names a user, and a pod
// without the annotation, or with one that is no user's name, gives no
// shell.
func TestShellUser(t *testing.T) {
	cases := []struct {
		annotations map[string]string
		user        string
		allowed     bool
	}{
		{nil, "", false},
		{map[string]string{AnnotationShell: "nobody"}, "nobody", true},
		{map[string]string{AnnotationShell: "65534"}, "65534", true},
		{map[string]string{AnnotationShell: ""}, "", false},
		{map[string]string{AnnotationShell: "-it"}, "", false},
		{map[string]string{AnnotationShell: "a b"}, "", false},
	}
	for _, test := range cases {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: test.annotations}}
		user, allowed := ShellUser(pod)
		if user != test.user || allowed != test.allowed {
			t.Errorf("%v: %q, %v", test.annotations, user, allowed)
		}
	}
}
