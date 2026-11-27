# Design

tabernetes is one program, the agent, on one machine beside a
Kubernetes API server. This says how the agent makes a browser tab a
node, what was measured, and what is missing.

## The pieces

```
the cluster's machine                          a phone
┌──────────────────────────────────┐           ┌─────────────────────────┐
│ k3s: API server, scheduler       │           │ browser tab             │
│                                  │           │                         │
│ the agent (cmd/tabernetes)       │  HTTPS,   │  join page              │
│   a Node and a Lease per tab     │◄─────────►│   └ a frame per pod:    │
│   per pod: a network namespace   │ WebSocket │      tabc's page, which │
│     and in it `tabc run --link`, │           │      boots Linux and    │
│     which serves the page, the   │           │      runs the program   │
│     image's files and the pod's  │           │                         │
│     connections                  │           │                         │
└──────────────────────────────────┘           └─────────────────────────┘
```

The tab is the pod's CPU and memory. Everything else a pod has is on
the machine.

## A tab becomes a node

The join page (`web/join.js`) opens a WebSocket and says what the
browser is: the device's model where the browser gives one, the
platform, the browser and its version. The agent turns that into a
name, such as `pixel-9-3fa2`, and creates a Node with it
(`internal/kube/node.go`):

- arm64 and Linux, whatever the device, since the guest is an emulated
  aarch64 machine;
- the browser and platform as the OS image, unkernel's kernel as the
  kernel version and tabc as the container runtime, which is what
  `kubectl get nodes -o wide` then prints;
- one CPU, 256 MiB and room for one pod;
- a taint and a label, `tabernetes.dev/tab`, which the RuntimeClass
  `tab` tolerates and selects, so that no pod lands on a tab that did
  not ask for one.

Everything the browser says is a stranger's text. `internal/tab` is
the one place it is made into names and label values.

The agent renews every node's Lease every 20 s. It does not wait for a
Lease to run out to notice a tab has gone: it pings each tab every 5 s,
and a tab that does not answer in 10 s, or whose socket closes, has its
Node deleted on the spot. A phone that locks its screen stops running
the page, so the page holds a screen wake lock while it is a node.

## A pod runs in a tab

The agent watches every scheduled pod and takes the ones bound to its
nodes (`internal/agent/pods.go`). For each:

1. The image is fetched once into tabc's cache, with `tabc pull`.
2. A network namespace is made for the pod, and a CNI bridge plugin
   gives it an address (`internal/runtime/sandbox.go`). The address is
   on the same bridge as every other pod's.
3. `tabc run --link` is started inside the namespace
   (`internal/runtime/args.go` turns the pod into its flags). tabc
   prints the address of the container's page.
4. The page of the pod's node is told to open a frame on the
   container, at `/c/<id>/` on the agent.
5. When tabc says the program is running, the pod is Running and
   Ready, with the namespace's address as its pod IP.

tabc's page server listens on loopback inside the namespace. The agent
carries the tab's requests there by making its socket from a thread
that has moved into the namespace, which is how tabc is used unchanged.
The id in the address is 128 random bits that only the node's page is
told.

The kernel and the SDK are 11 MB and the same for every container. The
agent answers requests for them with a redirect to one address of its
own that a browser keeps for good, gzipped, so a phone fetches about
5 MB the first time and nothing the second.

A pod's connections are made by tabc, so they leave from the pod's
namespace and address. tabc resolves names with the cluster's DNS
there, with the pod's namespace on the search path. Each port the pod
names is listened on by tabc in the namespace and carried into the tab.
A Service therefore reaches a pod on a phone, and a pod on a phone
reaches a Service, with nothing in Kubernetes aware of the difference.

## When things end

- **The program exits.** The pod's restart policy decides, as with a
  kubelet. Under Always the program is started again in the same
  namespace, after a wait that doubles.
- **The container cannot start.** Under Always the pod stays Pending
  with the reason, and the start is retried. The pod is not marked
  Failed: a controller answers a failed pod with a new one, and a pod
  that cannot start would be replaced without end.
- **The tab closes.** The Node is deleted first and then its pods, so
  that the replacements the pods' controllers make cannot be bound to
  the node that is leaving. Measured: a web server's replacement was
  Running on another tab about a second after its tab was closed.
- **The agent restarts.** Every socket closes, the old agent removes
  its nodes on the way out, and each page asks for a place again by
  itself. A new agent also removes whatever nodes, containers and
  namespaces an agent that was killed left.
- **The API server restarts.** The agent's pods keep running, and its
  watches reconnect.

A pod's volumes are directories on the machine. That is why the demo's
Redis keeps its count when its tab closes: the replacement, on another
phone, opens the same append-only file.

## Logs

A pod's output is written to a file on the machine and kept until the
pod is gone from the API server. Every node gives the machine's address
and one port as its kubelet's, and the agent answers the API server's
log request there with the cluster's own kubelet certificate, to
clients the cluster's authority vouches for. k3s must be told to
connect to kubelets directly (`egress-selector-mode: disabled`): its
default is a tunnel to the node's k3s agent, which a tab does not have.

## Ready

A pod is Running when tabc says its program has started, and Ready at
the same moment unless the pod has a readiness probe. The agent makes
one kind of probe, an HTTP GET from the machine to the pod's address,
and the pod is Ready while the probe passes. A probe that runs a
command has no shell to run it in from outside the tab, and a probe
that only opens a socket would always pass: tabc accepts a connection
on a pod's port before the program in the guest is listening. That is
also why a web server without a probe drops the first requests a
Service sends it.

## The site

The agent serves the cluster's own page (`web/`), which reads one JSON
description of the cluster every three seconds (`/api/cluster`,
`internal/agent/view.go`). The description is put together at most
once a second and served gzipped from memory, so a crowd of readers
costs the agent one description a second.

What the page shows comes from three places:

- **The agent's own state**: the nodes, each node's pods, and the
  Services in front of them, which the agent lists every five seconds.
  That is the topology.
- **The ledger** (`internal/agent/ledger.go`): counts that begin when
  the cluster first starts and are kept in a file across restarts, the
  last 60 events and the last 240 boots. A boot is timed from the tab
  being told of its container to tabc reporting the program running,
  to the nearest quarter second.
- **Counters on the connections** between each tab and its pod's tabc,
  which is where "carried" comes from: the image's files as the guest
  reads them, the program's output and the pod's own traffic.

A node's place on the globe is the city its browser's time zone is
named after (`web/zones.json`, from tzdata), set a little apart from
other nodes of the same city. The agent never looks an address up, and
no address is in the description. The land is 1817 dots from Natural
Earth's outline (`web/land.json`). `nix run .#globe-data` writes both
tables.

## Asking the cluster

The agent can be started with Services its visitors may send requests
to (`--expose counter=default/web:8000`). A GET for
`/apps/counter/...` is carried to that Service's cluster address, and
the Service passes it to one of its pods, on whoever's phone that is.
The page's "Ask the cluster" button does this and shows which node
answered.

Only a GET is carried, only to a Service named when the agent started,
without the visitor's own headers, and no more than two a second for
one address. A request that fails is tried again by the page, since
the pod it was sent to may have just lost its tab.

## The walls

A pod on a tab is run by a browser nobody vetted. Two things follow.

The tab can read and change everything in the pod: its memory, its
files as it sees them, what it sends. Nothing secret belongs in such a
pod, and a service account's token is never mounted into one.

The tab can make the pod open connections, from the machine. The
machine's firewall (`deploy/firewall.sh`) lets a tab's pod open
connections to other pods and to nothing else. Checked from a pod in a
tab: a Service answered, and the internet by address and by name, the
API server and the machine's own ports were all refused. Names still
resolve through the cluster's DNS, which forwards outside names, so a
pod can ask about any name.

Joining is limited to 100 nodes at once and four from one address.

## Measured

2026-11-26, on an exe.dev machine with 2 CPUs and 8 GB, k3s 1.37, 40
headless Chromiums on another machine as the nodes, one Alpine pod
each:

|                                                         |            |
| ------------------------------------------------------- | ---------- |
| one pod, from `kubectl apply` to Running                | 6 s        |
| 40 pods Running after scaling a Deployment from 1 to 40 | 24 s       |
| a closed tab's pod Running on another tab               | about 1 s  |
| the machine's memory with no nodes                      | 825 MB     |
| the machine's memory a node with a pod                  | 9 MB       |
| the agent's own memory at 40 nodes                      | 70 MB      |
| the machine's load at 40 idle pods                      | 0.5        |
| a node tab's memory in the browser, 128 MiB guest       | 214 MB     |
| a first join's download, gzipped                        | about 5 MB |

k3s itself is 650 MB of the 825. At 9 MB a node, 100 nodes are under a
gigabyte.

Not measured: phones, more than 40 nodes, a pod doing real work on
every node at once, and the API server with hundreds of Leases.

## Missing

- `kubectl exec`, liveness probes and probes that are not an HTTP GET,
  init containers, more than one container.
- Config maps and secrets, as volumes or as variables.
- UDP.
- A pod keeps running through its tab closing only by being started
  again elsewhere. Moving a running pod needs the guest's memory
  copied out of the tab, which unkernel can do and nothing here does
  yet.
- Redis's first start on a new data directory fails once on a rename
  in the volume and succeeds on the restart. The cause is in how tabc
  serves a volume's ownership, and is not found yet.
