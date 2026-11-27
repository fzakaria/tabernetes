package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/fzakaria/tabernetes/internal/kube"
	"github.com/fzakaria/tabernetes/internal/runtime"
)

// shellTestTimeout is how long a test waits on a shell's socket.
const shellTestTimeout = 10 * time.Second

// fakeTabc writes a stand-in for tabc that prints the words it was run
// with and then repeats whatever is typed, and returns the stand-in's
// path.
func fakeTabc(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tabc")
	script := "#!/bin/sh\necho \"ran: $*\"\nexec cat\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// shellServer returns an agent whose tabc is the stand-in, with one
// running pod that lets its tab have a shell as nobody and one that
// does not, and a server with the shell's route.
func shellServer(t *testing.T) (*Agent, *httptest.Server) {
	t.Helper()
	agent := testAgent(t, func(config *Config) {
		config.Tabc = runtime.Tabc{Binary: fakeTabc(t)}
	})
	podOn(agent, "pixel-1", "open", "127.0.0.1")
	podOn(agent, "pixel-1", "shut", "127.0.0.1")
	for _, running := range agent.runs {
		agent.containers[running.id] = running
	}
	agent.containers["open"].pod.Annotations = map[string]string{kube.AnnotationShell: "nobody"}

	mux := http.NewServeMux()
	mux.HandleFunc(pathShell, agent.serveShell)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return agent, server
}

// openShell opens the socket of a container's shell and says how large
// the terminal is.
func openShell(ctx context.Context, t *testing.T, server *httptest.Server, id string) *websocket.Conn {
	t.Helper()
	address := "ws" + strings.TrimPrefix(server.URL, "http") + "/shells/" + id
	conn, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := wsjson.Write(ctx, conn, shellSize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	return conn
}

// readUntil reads a shell's output until the output holds some text,
// and fails the test if the socket ends first.
func readUntil(ctx context.Context, t *testing.T, conn *websocket.Conn, text string) string {
	t.Helper()
	var output strings.Builder
	for !strings.Contains(output.String(), text) {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q after %q: %v", text, output.String(), err)
		}
		output.Write(data)
	}
	return output.String()
}

// TestShell checks that a tab gets a shell in a container whose pod
// allows one: tabc is asked to run a shell there, on a terminal, as the
// user the pod names, and what the tab types reaches the program and
// what the program prints reaches the tab. A stand-in for tabc prints
// its own command line and repeats its input.
func TestShell(t *testing.T) {
	_, server := shellServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), shellTestTimeout)
	defer cancel()

	conn := openShell(ctx, t, server, "open")
	output := readUntil(ctx, t, conn, "open sh")
	for _, word := range []string{"ran: exec", "-it", "-u nobody"} {
		if !strings.Contains(output, word) {
			t.Errorf("tabc was not run with %q: %q", word, output)
		}
	}

	if err := conn.Write(ctx, websocket.MessageBinary, []byte("typed by the tab\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(ctx, t, conn, "typed by the tab")
}

// TestShellRefused checks who gets no shell: a container whose pod does
// not allow one, a container that already has a shell open, and a
// container the agent does not run. The first two are told why as the
// socket closes, and nothing is run for any of them.
func TestShellRefused(t *testing.T) {
	_, server := shellServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), shellTestTimeout)
	defer cancel()

	// A pod that does not allow a shell.
	shut := openShell(ctx, t, server, "shut")
	_, _, err := shut.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Errorf("a pod that allows no shell: %v", err)
	}

	// A second shell beside one that is open.
	first := openShell(ctx, t, server, "open")
	readUntil(ctx, t, first, "ran:")
	second := openShell(ctx, t, server, "open")
	_, _, err = second.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
		t.Errorf("a second shell: %v", err)
	}

	// A container nobody runs.
	address := "ws" + strings.TrimPrefix(server.URL, "http") + "/shells/nothing"
	if _, response, err := websocket.Dial(ctx, address, nil); err == nil || response == nil || response.StatusCode != http.StatusNotFound {
		t.Errorf("a container nobody runs: %v", err)
	}
}

// TestShellTurnedAwayWhenMemoryIsShort checks that how many shells are
// open at once is the machine's to say, as how many nodes there are is:
// a shell is given while the machine has more memory than the agent
// keeps back, whatever the number already open, and refused once the
// machine has less. A stand-in for /proc is read before each asking.
func TestShellTurnedAwayWhenMemoryIsShort(t *testing.T) {
	proc := t.TempDir()
	agent := testAgent(t, func(config *Config) {
		config.Proc = proc
		config.ReserveMemory = gibibyte
	})

	// With memory to spare, every container that asks is given a
	// shell, and none is given a second.
	writeVitals(t, proc, 2*gibibyte, 100, 100)
	agent.readVitals()
	const many = 200
	for index := 0; index < many; index++ {
		if _, err := agent.takeShell(strconv.Itoa(index)); err != nil {
			t.Fatalf("shell %d with memory to spare: %v", index, err)
		}
	}
	if _, err := agent.takeShell("0"); err != errShellOpen {
		t.Errorf("a second shell in one container: %v", err)
	}

	// With memory short, nobody is given one.
	writeVitals(t, proc, gibibyte/2, 200, 200)
	agent.readVitals()
	if _, err := agent.takeShell("one more"); err != errShellsBusy {
		t.Errorf("with memory short: %v", err)
	}
}
