package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// linkPattern is the address `tabc run --link` prints for a browser to
// open: tabc's page server on loopback, and the container's secret.
var linkPattern = regexp.MustCompile(`^\s*(http://127\.0\.0\.1:\d+/t/[0-9a-f]+/)\s*$`)

// tabcPrefix starts the lines tabc itself writes among the program's.
const tabcPrefix = "tabc:"

// linkTimeout is how long tabc gets to print the address. The image is
// in the cache by then, so tabc has only its supervisor to start.
const linkTimeout = time.Minute

// stopGrace is how long a container gets to end after it is asked to,
// before its processes are killed.
const stopGrace = 10 * time.Second

// killGrace is how long a killed tabc is waited for.
const killGrace = 3 * time.Second

// The columns of `tabc list` the agent reads, counted from zero, and
// how many columns a line has at the least.
const (
	listColumnID     = 0
	listColumnName   = 1
	listColumnStatus = 3
	listColumnsMin   = 4
)

// StatusRunning is what `tabc list` says of a container whose program
// runs.
const StatusRunning = "running"

// ExitUnknown is the exit status of a container whose tabc ended some
// way other than exiting.
const ExitUnknown = 255

// Tabc is the tabc command.
type Tabc struct {
	// Binary is tabc's path.
	Binary string
}

// Container is one `tabc run --link` in a pod's sandbox.
type Container struct {
	// Link is the page's address on the sandbox's loopback.
	Link *url.URL

	command *exec.Cmd
	// done is closed when tabc has ended, and exit is its status then.
	done chan struct{}
	exit int

	stopOnce sync.Once
}

// Pull fetches an image into tabc's cache, from the agent's own
// network. The pod's namespace has no way out to a registry.
func (t Tabc) Pull(ctx context.Context, image string) error {
	output, err := exec.CommandContext(ctx, t.Binary, "pull", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pulling %s: %v: %s", image, err, lastLine(output))
	}
	return nil
}

// Start runs tabc inside a sandbox with the plan's words and waits for
// the page's address. The program's output goes to log.
func (t Tabc) Start(sandbox *Sandbox, args []string, log io.Writer) (*Container, error) {
	// The command outlives the call that started it, so its context is
	// its own: Stop ends the container.
	command := sandbox.Command(context.Background(), t.Binary, args...)
	command.Stdout = log
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	container := &Container{command: command, done: make(chan struct{})}

	// tabc's standard error: its own lines until the address, which are
	// kept in case tabc fails, then the program's, which go to the log.
	linked := make(chan *url.URL, 1)
	var preamble bytes.Buffer
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		reader := bufio.NewReader(stderr)
		for {
			line, err := reader.ReadString('\n')
			if match := linkPattern.FindStringSubmatch(strings.TrimRight(line, "\r\n")); match != nil {
				if link, parseErr := url.Parse(match[1]); parseErr == nil {
					linked <- link
					break
				}
			}
			preamble.WriteString(line)
			if err != nil {
				return
			}
		}

		// From the reader, which may already hold the program's first
		// words.
		io.Copy(log, reader)
	}()

	// The exit status, once the output has been read to its end.
	go func() {
		<-copied
		err := command.Wait()
		container.exit = exitStatus(err)
		close(container.done)
	}()

	select {
	case container.Link = <-linked:
		return container, nil
	case <-container.done:
		return nil, fmt.Errorf("tabc ended before the page was up: %s", lastLine(preamble.Bytes()))
	case <-time.After(linkTimeout):
		container.Stop()
		return nil, fmt.Errorf("tabc printed no address in %s", linkTimeout)
	}
}

// Done is closed when the container has ended.
func (c *Container) Done() <-chan struct{} {
	return c.done
}

// Exit is the container's exit status, once Done is closed.
func (c *Container) Exit() int {
	return c.exit
}

// Stop asks the container to end and waits for it to. tabc passes the
// signal on to the program. A tabc that does not end in time is killed,
// which can leave its supervisor behind: Remove is what ends that.
func (c *Container) Stop() {
	c.stopOnce.Do(func() {
		c.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-c.done:
			return
		case <-time.After(stopGrace):
		}

		// The wait after the kill is bounded too: a supervisor left
		// behind holds tabc's output open, and Done closes only when the
		// output has ended.
		c.command.Process.Kill()
		select {
		case <-c.done:
		case <-time.After(killGrace):
		}
	})
}

// Listed is one container as `tabc list` shows it.
type Listed struct {
	ID     string
	Status string
}

// List returns tabc's containers by name.
func (t Tabc) List(ctx context.Context) (map[string]Listed, error) {
	output, err := exec.CommandContext(ctx, t.Binary, "list").Output()
	if err != nil {
		return nil, fmt.Errorf("tabc list: %w", err)
	}
	return parseList(output), nil
}

// parseList reads the table `tabc list` prints: a header, then a line a
// container.
func parseList(output []byte) map[string]Listed {
	containers := map[string]Listed{}
	lines := strings.Split(string(output), "\n")
	for index, line := range lines {
		fields := strings.Fields(line)
		if index == 0 || len(fields) < listColumnsMin {
			continue
		}
		containers[fields[listColumnName]] = Listed{ID: fields[listColumnID], Status: fields[listColumnStatus]}
	}
	return containers
}

// Remove deletes a container by id, whatever state tabc left it in.
// The supervisor of a tabc that had to be killed is still there, and
// this is what ends it.
func (t Tabc) Remove(ctx context.Context, id string) error {
	output, err := exec.CommandContext(ctx, t.Binary, "delete", "--force", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tabc delete %s: %v: %s", id, err, lastLine(output))
	}
	return nil
}

// Version returns tabc's version, as "1.0.0".
func (t Tabc) Version(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, t.Binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("tabc --version: %w", err)
	}
	return parseVersion(output), nil
}

// versionPrefix starts the line of `tabc --version` that has tabc's own
// version.
const versionPrefix = "tabc version "

// parseVersion reads tabc's version out of what `tabc --version`
// prints.
func parseVersion(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		if version, ok := strings.CutPrefix(strings.TrimSpace(line), versionPrefix); ok {
			return version
		}
	}
	return "unknown"
}

// exitStatus returns a command's exit status from what Wait returned.
func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return exit.ExitCode()
	}
	return ExitUnknown
}

// lastLine returns the last line of a command's output that says
// something, without tabc's prefix: the error, as a rule.
func lastLine(output []byte) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(output), "\r", "")), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	return strings.TrimSpace(strings.TrimPrefix(last, tabcPrefix))
}
