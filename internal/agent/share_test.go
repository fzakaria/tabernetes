package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testShare returns a share with a kernel, a script beside it, the file
// that names unkernel's version, and the terminal's directory, which a
// share is not a share without.
func testShare(t *testing.T, kernel string) *Share {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range map[string]string{
		"unkernel/unkernel.wasm": kernel,
		"unkernel/web.mjs":       "the SDK",
		"unkernel/VERSION":       "1.2.3\n",
		"xterm/xterm.js":         "terminal",
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
	return share
}

// mirrorOf returns a stand-in for a content network that serves one
// kernel, under the path of unkernel's version 1.2.3, and the address
// to give the share for it, with a blank where the version goes.
func mirrorOf(t *testing.T, kernel string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/unkernel@1.2.3/dist/unkernel.wasm" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(kernel))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/unkernel@{version}/dist"
}

// TestMirror checks that a tab is sent to a mirror for the kernel once
// the mirror has been seen to serve the share's own kernel, with the
// share's version in the address, and that the script beside the
// kernel still comes from the agent: by pointing a share at a stand-in
// that serves the same bytes.
func TestMirror(t *testing.T) {
	kernel := strings.Repeat("kernel ", 1000)
	share := testShare(t, kernel)
	base := mirrorOf(t, kernel)

	if got := share.Location("unkernel/unkernel.wasm"); got != share.Path("unkernel/unkernel.wasm") {
		t.Errorf("before a mirror is known, the kernel is at %q", got)
	}
	if err := share.UseMirror(context.Background(), http.DefaultClient, base); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(base, "{version}", "1.2.3", 1) + "/unkernel.wasm"
	if got := share.Location("unkernel/unkernel.wasm"); got != want {
		t.Errorf("the kernel is at %q, want %q", got, want)
	}
	if got := share.Location("unkernel/web.mjs"); got != share.Path("unkernel/web.mjs") {
		t.Errorf("the script is at %q", got)
	}
}

// TestMirrorWithOtherBytes checks that a mirror whose kernel is not the
// share's is refused, and tabs keep getting the agent's own: by
// pointing a share at a stand-in that serves a different kernel.
func TestMirrorWithOtherBytes(t *testing.T) {
	share := testShare(t, strings.Repeat("kernel ", 1000))
	base := mirrorOf(t, "another kernel")

	if err := share.UseMirror(context.Background(), http.DefaultClient, base); err == nil {
		t.Error("a mirror with another kernel was taken")
	}
	if got := share.Location("unkernel/unkernel.wasm"); got != share.Path("unkernel/unkernel.wasm") {
		t.Errorf("the kernel is at %q", got)
	}
}
