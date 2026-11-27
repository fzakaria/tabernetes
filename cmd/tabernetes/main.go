// tabernetes is the kubelet of a Kubernetes cluster whose nodes are
// browser tabs. It runs on one machine beside the API server, serves
// the page a browser opens to become a node, and runs each pod bound to
// such a node with tabc, in that tab.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/fzakaria/tabernetes/internal/agent"
	"github.com/fzakaria/tabernetes/internal/kube"
	"github.com/fzakaria/tabernetes/internal/runtime"
	"github.com/fzakaria/tabernetes/web"
)

// version is tabernetes's own version, set when the package is built.
var version = "dev"

// What one tab offers the scheduler besides memory: one CPU and room
// for one pod. One pod a tab keeps "which phone runs what" plain.
const (
	nodeCPU  = "1"
	nodePods = "1"
)

// runtimePrefix makes tabc's version the name a node reports its
// container runtime under.
const runtimePrefix = "tabc://"

// probeAddress is somewhere to point a socket, to learn which of this
// machine's addresses a connection out would leave from. Nothing is
// sent to it: the address is one reserved for documentation.
const probeAddress = "192.0.2.1:9"

// shutdownTimeout is how long requests in flight get when the agent
// stops.
const shutdownTimeout = 5 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "tabernetes: %v\n", err)
		os.Exit(1)
	}
}

// run reads the flags, starts the agent and its site, and waits for a
// signal.
func run() error {
	var (
		kubeconfig    = flag.String("kubeconfig", "/etc/rancher/k3s/k3s.yaml", "the kubeconfig the agent reaches the API server with")
		listen        = flag.String("listen", ":8000", "where the site listens")
		publicURL     = flag.String("public-url", "http://localhost:8000", "the site's address as a visitor types it")
		tabcBinary    = flag.String("tabc", "tabc", "the tabc command")
		shareDir      = flag.String("share", "", "tabc's share directory: the kernel, the SDK and the terminal every container's page loads")
		stateDir      = flag.String("state", "/var/lib/tabernetes", "where pods' logs and volumes are kept")
		pluginDir     = flag.String("cni-plugins", "/opt/cni/bin", "where the CNI plugins are")
		networkConfig = flag.String("cni-config", "/etc/tabernetes/tabs.json", "the CNI plugin configuration of the network tabs' pods are put on")
		clusterDNS    = flag.String("cluster-dns", "10.96.0.10", "the cluster DNS service's address")
		clusterDomain = flag.String("cluster-domain", "cluster.local", "the cluster's domain")
		address       = flag.String("address", "", "the address the API server reaches the agent on (default: this machine's)")
		kubeletPort   = flag.Int("kubelet-port", 10260, "the port a node gives as its kubelet's")
		maxNodes      = flag.Int("max-nodes", 100, "how many tabs may be nodes at once")
		maxPerAddress = flag.Int("max-nodes-per-address", 4, "how many nodes may come from one address")
		nodeMemory    = flag.String("node-memory", "256Mi", "the memory one tab offers")
		hostPathRoot  = flag.String("host-path-root", "", "the one directory a pod's hostPath volume may be under (default: none)")
		guestKernel   = flag.String("guest-kernel", "7.3.4-unkernel", "the kernel version a node reports: the guest's")
		kubeletCert   = flag.String("kubelet-cert", "/var/lib/rancher/k3s/agent/serving-kubelet.crt", "the certificate the agent answers the API server's kubelet requests with")
		kubeletKey    = flag.String("kubelet-key", "/var/lib/rancher/k3s/agent/serving-kubelet.key", "the certificate's key")
		kubeletCA     = flag.String("kubelet-client-ca", "/var/lib/rancher/k3s/agent/client-ca.crt", "the authority whose clients may make kubelet requests: the API server's")
		runtimeClass  = flag.String("runtime-class", "tab", "the RuntimeClass whose pods run in tabs")
		zone          = flag.String("zone", "", "the time zone the machine is in, where the site's map puts the cluster (as America/Los_Angeles)")
		expose        = flag.String("expose", "", "Services a visitor of the site may send requests to, as name=namespace/service:port, separated by commas")
		showVersion   = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)

	// The pieces: tabc, the pod network, the cluster.
	if *shareDir == "" {
		return errors.New("--share is needed: tabc's share directory")
	}
	share, err := agent.OpenShare(*shareDir)
	if err != nil {
		return fmt.Errorf("--share: %w", err)
	}
	tabc := runtime.Tabc{Binary: *tabcBinary}
	tabcVersion, err := tabc.Version(context.Background())
	if err != nil {
		return err
	}
	network, err := os.ReadFile(*networkConfig)
	if err != nil {
		return fmt.Errorf("--cni-config: %w", err)
	}
	cluster, err := kube.Connect(*kubeconfig)
	if err != nil {
		return err
	}
	serverVersion, err := cluster.Version()
	if err != nil {
		return fmt.Errorf("the API server: %w", err)
	}
	memory, err := resource.ParseQuantity(*nodeMemory)
	if err != nil {
		return fmt.Errorf("--node-memory: %w", err)
	}
	if *address == "" {
		*address, err = ownAddress()
		if err != nil {
			return fmt.Errorf("--address: %w", err)
		}
	}
	logger.Printf("tabernetes %s: API server %s, tabc %s, reachable at %s", version, serverVersion, tabcVersion, *address)

	exposed, err := parseExposed(*expose)
	if err != nil {
		return fmt.Errorf("--expose: %w", err)
	}

	tabs := agent.New(agent.Config{
		Cluster: cluster,
		Tabc:    tabc,
		Network: &runtime.Network{PluginDir: *pluginDir, Config: network, DNS: *clusterDNS, Domain: *clusterDomain},
		Node: kube.NodeConfig{
			Address:        *address,
			KubeletPort:    int32(*kubeletPort),
			Version:        serverVersion,
			RuntimeVersion: runtimePrefix + tabcVersion,
			KernelVersion:  *guestKernel,
			CPU:            resource.MustParse(nodeCPU),
			Memory:         memory,
			Pods:           resource.MustParse(nodePods),
		},
		Limits:             runtime.Limits{MaxMemoryMiB: memory.Value() >> 20, HostPathRoot: *hostPathRoot},
		MaxNodes:           *maxNodes,
		MaxNodesPerAddress: *maxPerAddress,
		RuntimeClass:       *runtimeClass,
		StateDir:           *stateDir,
		Zone:               *zone,
		Exposed:            exposed,
		Log:                logger,
	})

	// The agent and the site end together, on a signal.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server := &http.Server{
		Addr:    *listen,
		Handler: tabs.Handler(agent.Site{Pages: web.Files, Share: share, PublicURL: *publicURL}),
	}
	serving := make(chan error, 1)
	go func() { serving <- server.ListenAndServe() }()
	logger.Printf("the site is on %s, as %s", *listen, *publicURL)

	// The kubelet's own port, which is where `kubectl logs` ends up.
	// Without it the cluster still runs pods, so a failure here is said
	// and passed over.
	kubelet, err := kubeletServer(net.JoinHostPort(*address, strconv.Itoa(*kubeletPort)), *kubeletCert, *kubeletKey, *kubeletCA, tabs.KubeletHandler())
	if err != nil {
		logger.Printf("no kubelet port, so no `kubectl logs`: %v", err)
	} else {
		go func() {
			if err := kubelet.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
				logger.Printf("the kubelet port: %v", err)
			}
		}()
		defer kubelet.Close()
		logger.Printf("kubelet requests are answered on %s", kubelet.Addr)
	}

	running := make(chan error, 1)
	go func() { running <- tabs.Run(ctx) }()

	select {
	case err := <-serving:
		stop()
		<-running
		return err
	case err := <-running:
		shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		server.Shutdown(shutdown)
		return err
	}
}

// parseExposed reads the Services a visitor may send requests to:
// "counter=default/web:8000", and more of the same after commas.
func parseExposed(text string) (map[string]agent.Exposed, error) {
	exposed := map[string]agent.Exposed{}
	for _, entry := range strings.Split(text, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, target, hasName := strings.Cut(entry, "=")
		namespace, rest, hasNamespace := strings.Cut(target, "/")
		service, portText, hasPort := strings.Cut(rest, ":")
		port, err := strconv.Atoi(portText)
		if !hasName || !hasNamespace || !hasPort || name == "" || namespace == "" || service == "" || err != nil || port <= 0 {
			return nil, fmt.Errorf("%q is not name=namespace/service:port", entry)
		}
		exposed[name] = agent.Exposed{Namespace: namespace, Service: service, Port: port}
	}
	return exposed, nil
}

// kubeletServer returns a server for the API server's kubelet requests.
// Each node says its kubelet is at this address, and the API server
// connects as it does to any kubelet: it checks the server's
// certificate, and shows one of its own. Only a client the cluster's
// authority vouches for is answered, since a pod's log is not for
// every pod that can reach the port.
func kubeletServer(address, certFile, keyFile, clientCAFile string, handler http.Handler) (*http.Server, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	authority, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, err
	}
	clients := x509.NewCertPool()
	if !clients.AppendCertsFromPEM(authority) {
		return nil, fmt.Errorf("%s holds no certificate", clientCAFile)
	}
	return &http.Server{
		Addr:    address,
		Handler: handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{certificate},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    clients,
			MinVersion:   tls.VersionTLS12,
		},
	}, nil
}

// ownAddress returns the address this machine would connect out from.
// No packet is sent: a UDP socket only has to be pointed somewhere for
// the system to pick the address.
func ownAddress() (string, error) {
	conn, err := net.Dial("udp4", probeAddress)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	return host, err
}
