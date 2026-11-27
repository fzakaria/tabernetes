package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/creack/pty"
	"golang.org/x/time/rate"

	"github.com/fzakaria/tabernetes/internal/kube"
)

// pathShell is where a tab's page opens a shell in a container the tab
// runs: /shells/<the container's id>. The id is the secret the tab
// loads the container's page with, so only the tab that runs a
// container can ask for a shell in it.
const pathShell = "GET /shells/{id}"

// What a shell may cost the machine. The program runs in the tab, but
// everything typed and printed passes through a tabc of the machine's,
// as it does for `kubectl exec` on any node: about 3 MB of the
// machine's memory for as long as the shell is open. How many are open
// at once is the machine's to say, by the rule that says how many tabs
// may be nodes.
const (
	// shellIdle is how long a shell stays open with nothing typed, and
	// shellLongest how long a shell stays open at all.
	shellIdle    = 5 * time.Minute
	shellLongest = 20 * time.Minute
	// shellBytesPerSecond is how fast a shell's output is carried to
	// the tab, and shellBurst how much is carried at once after a quiet
	// spell. A program that prints without end is slowed to this.
	shellBytesPerSecond = 32 << 10
	shellBurst          = 128 << 10
	// shellChunk is how much of the output is read at a time.
	shellChunk = 8 << 10
	// shellInputLimit is the most a tab may send in one message, which
	// is a paste of that size.
	shellInputLimit = 64 << 10
	// shellSizeTimeout is how long a tab gets to say how large its
	// terminal is.
	shellSizeTimeout = 10 * time.Second
	// shellMostRows and shellMostCols bound the terminal's size.
	shellMostRows = 500
	shellMostCols = 500
)

// Why a shell is not opened, or is closed.
var (
	errShellOpen  = errors.New("this pod already has a shell open")
	errShellsBusy = errors.New("the cluster's machine has no room for another shell now; try again in a minute")
)

// What a tab is told as its shell's socket closes.
const (
	reasonShellRefused = "this pod gives its tab no shell"
	reasonShellDown    = "the pod's program is not running"
	reasonShellEnded   = "the shell ended"
	reasonShellIdle    = "the shell was closed after five minutes with nothing typed"
	reasonShellLongest = "the shell was closed after twenty minutes"
	reasonShellFailed  = "the shell could not be started"
)

// shellSize is the size of a tab's terminal, which the tab says first
// and again whenever the terminal's size changes.
type shellSize struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// window returns the size as a terminal takes one, or false for a size
// no terminal has.
func (s shellSize) window() (*pty.Winsize, bool) {
	if s.Rows < 1 || s.Rows > shellMostRows || s.Cols < 1 || s.Cols > shellMostCols {
		return nil, false
	}
	return &pty.Winsize{Rows: uint16(s.Rows), Cols: uint16(s.Cols)}, true
}

// shells remembers which containers have a shell open.
type shells struct {
	mu   sync.Mutex
	open map[string]bool
}

// takeShell gives a container a shell's place, or says why not: the
// container has a shell already, or the machine is as short of memory
// or as busy as turns a new node away. The place is given back by
// calling what takeShell returns.
func (a *Agent) takeShell(id string) (func(), error) {
	a.mu.Lock()
	refused := a.pressed()
	a.mu.Unlock()
	if refused != nil {
		return nil, errShellsBusy
	}
	return a.shells.take(id)
}

// take gives a container a shell's place, or says that the container
// has a shell already. The place is given back by calling what take
// returns.
func (s *shells) take(id string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open[id] {
		return nil, errShellOpen
	}
	if s.open == nil {
		s.open = map[string]bool{}
	}
	s.open[id] = true
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.open, id)
	}, nil
}

// serveShell gives a tab a shell in a container the tab runs, for a pod
// that allows one. The shell is a second program in the container,
// started by `tabc exec` on a terminal of the machine's: what the tab
// types goes to that terminal and what the program prints comes back,
// both over the socket. The same path, from the kubelet down, is what
// `kubectl exec -it` takes on any node.
func (a *Agent) serveShell(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.mu.Lock()
	running := a.containers[id]
	a.mu.Unlock()
	if running == nil {
		http.NotFound(w, r)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.origins})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(shellInputLimit)

	// Whether this container may have a shell now. A page cannot read
	// why a socket was not opened, so the answer is given as an open
	// socket closes.
	user, allowed := kube.ShellUser(running.pod)
	if !allowed {
		conn.Close(websocket.StatusPolicyViolation, reasonShellRefused)
		return
	}
	if _, isRunning, _ := running.shown(); !isRunning {
		conn.Close(websocket.StatusTryAgainLater, reasonShellDown)
		return
	}
	release, err := a.takeShell(id)
	if err != nil {
		conn.Close(websocket.StatusTryAgainLater, err.Error())
		return
	}
	defer release()

	// How large the tab's terminal is.
	ctx, cancel := context.WithTimeout(r.Context(), shellLongest)
	defer cancel()
	var size shellSize
	sizeCtx, sizeCancel := context.WithTimeout(ctx, shellSizeTimeout)
	err = wsjson.Read(sizeCtx, conn, &size)
	sizeCancel()
	window, sized := size.window()
	if err != nil || !sized {
		conn.Close(websocket.StatusPolicyViolation, "no terminal size")
		return
	}

	// The shell, on a terminal of its own. Closing the terminal hangs
	// the program up, and the kill is for a program that stays.
	command := a.config.Tabc.Shell(id, user)
	terminal, err := pty.StartWithSize(command, window)
	if err != nil {
		a.log.Printf("shell in %s: %v", id, err)
		conn.Close(websocket.StatusInternalError, reasonShellFailed)
		return
	}
	defer func() {
		terminal.Close()
		command.Process.Kill()
		command.Wait()
	}()

	// What the program prints, to the tab, no faster than a shell may.
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		a.carryShellOutput(ctx, terminal, conn)
	}()

	// What the tab sends, to the program: bytes are typed, and text is
	// the terminal's new size.
	typed := make(chan struct{}, 1)
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if kind == websocket.MessageText {
				resizeShell(terminal, data)
				continue
			}
			if _, err := terminal.Write(data); err != nil {
				return
			}
			select {
			case typed <- struct{}{}:
			default:
			}
		}
	}()

	// The shell lasts until its program ends, the tab closes the
	// socket, the container goes, nothing has been typed for a while,
	// or the shell has been open as long as one may be.
	idle := time.NewTimer(shellIdle)
	defer idle.Stop()
	for {
		select {
		case <-typed:
			idle.Reset(shellIdle)
		case <-printed:
			conn.Close(websocket.StatusNormalClosure, reasonShellEnded)
			return
		case <-gone:
			return
		case <-idle.C:
			conn.Close(websocket.StatusNormalClosure, reasonShellIdle)
			return
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, reasonShellLongest)
			return
		case <-running.node.Lost():
			conn.Close(websocket.StatusGoingAway, reasonShellEnded)
			return
		}
	}
}

// carryShellOutput sends what a shell's terminal prints to the tab's
// socket until the terminal ends, at the rate a shell is allowed.
func (a *Agent) carryShellOutput(ctx context.Context, terminal *os.File, conn *websocket.Conn) {
	limiter := rate.NewLimiter(shellBytesPerSecond, shellBurst)
	buffer := make([]byte, shellChunk)
	for {
		count, err := terminal.Read(buffer)
		if count > 0 {
			if limiter.WaitN(ctx, count) != nil {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			writeErr := conn.Write(writeCtx, websocket.MessageBinary, buffer[:count])
			cancel()
			if writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// resizeShell gives a shell's terminal the size a tab says its own
// terminal has now. A message that is no size is ignored.
func resizeShell(terminal *os.File, message []byte) {
	var size shellSize
	if err := json.Unmarshal(message, &size); err != nil {
		return
	}
	window, sized := size.window()
	if !sized {
		return
	}
	pty.Setsize(terminal, window)
}
