package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// Where iproute2 keeps named network namespaces, and the files it puts
// over /etc for a program run inside one (ip-netns(8)).
const (
	namespaceDir    = "/run/netns"
	namespaceEtcDir = "/etc/netns"
	resolvFile      = "resolv.conf"
)

// ipCommand is iproute2's command.
const ipCommand = "ip"

// The interface a pod's namespace gets, as with every runtime.
const podInterface = "eth0"

// The CNI commands, and the variables a plugin reads its call from
// (the CNI specification).
const (
	cniAdd         = "ADD"
	cniDelete      = "DEL"
	cniEnvCommand  = "CNI_COMMAND"
	cniEnvID       = "CNI_CONTAINERID"
	cniEnvNetns    = "CNI_NETNS"
	cniEnvIfName   = "CNI_IFNAME"
	cniEnvPath     = "CNI_PATH"
	cniFileMode    = 0o644
	cniDirMode     = 0o755
	dnsOptions     = "options ndots:5"
	serviceDomain  = "svc"
	resolvTemplate = "nameserver %s\nsearch %s\n%s\n"
)

// Network is the pod network the agent puts every tab's pod on: one CNI
// plugin, and the cluster's DNS.
type Network struct {
	// PluginDir is where the CNI plugins are.
	PluginDir string
	// Config is the plugin's configuration: one plugin's, with a name
	// and a version, not a list of plugins.
	Config []byte
	// DNS is the cluster DNS service's address, and Domain the
	// cluster's domain, "cluster.local".
	DNS    string
	Domain string
}

// Sandbox is one pod's network namespace on the agent's machine, with
// the pod's address in it. The pod's tabc runs inside, so every
// connection the guest makes leaves from the pod's address, and every
// port the guest opens listens on it.
type Sandbox struct {
	// Name is the namespace's name, and the pod's id to the plugin.
	Name string
	// IP is the pod's address.
	IP string

	network *Network
}

// cniResult is what a plugin prints after ADD, as far as the agent
// reads it.
type cniResult struct {
	IPs []struct {
		Address string `json:"address"`
	} `json:"ips"`
}

// cniType is the part of a plugin's configuration that names the
// plugin.
type cniType struct {
	Type string `json:"type"`
}

// Create makes a namespace named `name` for a pod of the Kubernetes
// namespace `podNamespace`, gives the namespace an address, and
// arranges for a program run inside to resolve names as the pod would.
func (n *Network) Create(ctx context.Context, name, podNamespace string) (*Sandbox, error) {
	sandbox := &Sandbox{Name: name, network: n}
	if output, err := exec.CommandContext(ctx, ipCommand, "netns", "add", name).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("namespace %s: %v: %s", name, err, bytes.TrimSpace(output))
	}

	// The address, from the plugin.
	output, err := n.plugin(ctx, cniAdd, name)
	if err != nil {
		sandbox.Destroy(context.WithoutCancel(ctx))
		return nil, err
	}
	var result cniResult
	if err := json.Unmarshal(output, &result); err != nil || len(result.IPs) == 0 {
		sandbox.Destroy(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("namespace %s: the plugin gave no address: %s", name, bytes.TrimSpace(output))
	}
	sandbox.IP, _, _ = strings.Cut(result.IPs[0].Address, "/")

	// The resolver's file. `ip netns exec` puts the file over
	// /etc/resolv.conf for the program it runs, so tabc resolves a
	// Service's name the way a pod of this namespace does.
	etc := filepath.Join(namespaceEtcDir, name)
	if err := os.MkdirAll(etc, cniDirMode); err != nil {
		sandbox.Destroy(context.WithoutCancel(ctx))
		return nil, err
	}
	search := strings.Join([]string{
		podNamespace + "." + serviceDomain + "." + n.Domain,
		serviceDomain + "." + n.Domain,
		n.Domain,
	}, " ")
	resolv := fmt.Sprintf(resolvTemplate, n.DNS, search, dnsOptions)
	if err := os.WriteFile(filepath.Join(etc, resolvFile), []byte(resolv), cniFileMode); err != nil {
		sandbox.Destroy(context.WithoutCancel(ctx))
		return nil, err
	}
	return sandbox, nil
}

// Destroy gives the address back and removes the namespace. Every step
// is tried, and the first failure is returned.
func (s *Sandbox) Destroy(ctx context.Context) error {
	_, pluginErr := s.network.plugin(ctx, cniDelete, s.Name)
	output, err := exec.CommandContext(ctx, ipCommand, "netns", "delete", s.Name).CombinedOutput()
	if err != nil {
		err = fmt.Errorf("namespace %s: %v: %s", s.Name, err, bytes.TrimSpace(output))
	}
	removeErr := os.RemoveAll(filepath.Join(namespaceEtcDir, s.Name))
	return errors.Join(pluginErr, err, removeErr)
}

// Command returns a command that runs a program inside the namespace.
func (s *Sandbox) Command(ctx context.Context, program string, args ...string) *exec.Cmd {
	words := append([]string{"netns", "exec", s.Name, program}, args...)
	return exec.CommandContext(ctx, ipCommand, words...)
}

// Dial connects to an address from inside the namespace. tabc's page
// server listens on the namespace's loopback, which nothing outside the
// namespace reaches any other way.
func (s *Sandbox) Dial(ctx context.Context, address string) (net.Conn, error) {
	return dialIn(ctx, filepath.Join(namespaceDir, s.Name), address)
}

// plugin runs the network's CNI plugin for a namespace and returns what
// the plugin printed.
func (n *Network) plugin(ctx context.Context, command, name string) ([]byte, error) {
	var kind cniType
	if err := json.Unmarshal(n.Config, &kind); err != nil || kind.Type == "" {
		return nil, fmt.Errorf("the pod network's configuration names no plugin")
	}

	plugin := exec.CommandContext(ctx, filepath.Join(n.PluginDir, kind.Type))
	plugin.Env = append(os.Environ(),
		cniEnvCommand+"="+command,
		cniEnvID+"="+name,
		cniEnvNetns+"="+filepath.Join(namespaceDir, name),
		cniEnvIfName+"="+podInterface,
		cniEnvPath+"="+n.PluginDir,
	)
	plugin.Stdin = bytes.NewReader(n.Config)
	var stderr bytes.Buffer
	plugin.Stderr = &stderr
	output, err := plugin.Output()
	if err != nil {
		// A plugin reports its error as JSON on standard output.
		return nil, fmt.Errorf("plugin %s %s for %s: %v: %s %s", kind.Type, command, name, err,
			bytes.TrimSpace(output), bytes.TrimSpace(stderr.Bytes()))
	}
	return output, nil
}

// Leftovers returns the names of the namespaces with a prefix: what an
// agent that was killed left behind.
func Leftovers(prefix string) ([]string, error) {
	entries, err := os.ReadDir(namespaceDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// Adopt returns the sandbox of a namespace that already exists, for
// destroying it.
func (n *Network) Adopt(name string) *Sandbox {
	return &Sandbox{Name: name, network: n}
}

// threadNamespace is the calling thread's own network namespace.
const threadNamespace = "/proc/thread-self/ns/net"

// dialIn connects to an address from inside a network namespace.
//
// A socket belongs to the namespace its thread was in when the socket
// was made. So the goroutine is pinned to its thread, the thread moves
// into the namespace, the connection is made, and the thread moves
// back. A thread that cannot move back is left pinned and dies with
// the goroutine, since Go would otherwise hand the thread, still in
// the pod's namespace, to other goroutines.
func dialIn(ctx context.Context, namespacePath, address string) (net.Conn, error) {
	type dialed struct {
		conn net.Conn
		err  error
	}
	result := make(chan dialed, 1)

	// A goroutine of its own, so that a thread that has to die takes
	// nothing else with it.
	go func() {
		goruntime.LockOSThread()

		home, err := os.Open(threadNamespace)
		if err != nil {
			goruntime.UnlockOSThread()
			result <- dialed{nil, err}
			return
		}
		defer home.Close()
		target, err := os.Open(namespacePath)
		if err != nil {
			goruntime.UnlockOSThread()
			result <- dialed{nil, err}
			return
		}
		defer target.Close()

		if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
			goruntime.UnlockOSThread()
			result <- dialed{nil, fmt.Errorf("entering %s: %w", namespacePath, err)}
			return
		}
		var dialer net.Dialer
		conn, dialErr := dialer.DialContext(ctx, "tcp4", address)

		if err := unix.Setns(int(home.Fd()), unix.CLONE_NEWNET); err != nil {
			// Left locked: the thread ends when this goroutine does.
			if conn != nil {
				conn.Close()
			}
			result <- dialed{nil, fmt.Errorf("leaving %s: %w", namespacePath, err)}
			return
		}
		goruntime.UnlockOSThread()
		result <- dialed{conn, dialErr}
	}()

	outcome := <-result
	return outcome.conn, outcome.err
}
