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
  last 60 events, the last 240 boots and the last 240 requests visitors
  sent into the cluster. A boot is timed from the tab being told of its
  container to tabc reporting the program running, to the nearest
  quarter second. A request is timed from the agent handing it to the
  Service to the answer being back, which leaves out the way between
  the visitor and the machine.
- **Counters on the connections** between each tab and its pod's tabc,
  which is where "carried" comes from: the image's files as the guest
  reads them, the program's output and the pod's own traffic.

A node's place on the globe is roughly where its address is: the agent
looks the address up in a database of cities on its own machine
(`internal/geo`, DB-IP's free database, which `deploy/setup.sh` fetches
each month) and gives the page a city, a country and coordinates
rounded to a tenth of a degree. The address goes nowhere to be looked
up and is not in the description. A node the database does not know is
placed at the city its browser's time zone is named after
(`web/zones.json`, from tzdata). Nodes of one place are set a little
apart. The land is 1817 dots from Natural Earth's outline
(`web/land.json`). `nix run .#globe-data` writes both tables.

## Asking the cluster

The agent can be started with Services its visitors may send requests
to (`--expose counter=default/web:8000`). A GET for
`/apps/counter/...` is carried to that Service's cluster address, and
the Service passes it to one of its pods, on whoever's phone that is.
The page's "Ask the cluster" button does this and shows which node
answered.

Only a GET is carried, only to a Service named when the agent started,
without the visitor's own headers, and no more than two a second for
one address and one Service. An entry can end with a path
(`--expose counter=default/web:8000/json`), and then only that path of
the Service and the paths under it are carried: what else the Service's
pods answer is for the cluster and not for visitors. A request that
fails is tried again by the page, since the pod it was sent to may have
just lost its tab.

## A picture every tab renders

The site's "Render a picture" button has every tab work on one thing: a
picture of the Mandelbrot set, 8 tiles by 8, each tile rendered by
whichever tab's pod was asked for it. The site shows the tiles as they
arrive and whose device rendered each.

A tab has room for one pod, so the work is done by the pod every tab
already has, the counter's web server (`examples/counter.yaml`). A GET
for `/tile/<picture>/<tiles to a side>/<tile>` runs an awk program in
the guest and answers with the tile's pixels and the node's name. What
a tile costs is fixed in the pod and not in the request: 12 pixels to a
side and 64 turns for a point. The guest does about 7000 turns a
second, so a tile takes from a tenth of a second to a little over one.
The web Service is exposed to visitors for `/json` alone, so no visitor
can ask a phone for a tile.

The painter (`examples/picture.yaml`) is a pod on the cluster's own
machine, a hundred and some lines of Python, and is exposed whole as
`picture`. It finds the tabs through a Service without an address
(`web-pods`), whose name the cluster's DNS answers with every pod that
is ready, and gives each pod one tile at a time, so a fast phone
renders more tiles than a slow one. A pod that fails or takes more than
8 seconds loses its tile to another pod. A picture that is not whole
after 90 seconds is left as it is.

Nobody says what is rendered: the pods have a list of four pictures and
the painter goes round it. One picture is rendered at a time, and the
painter starts no other for a minute after, so the button is no way to
keep strangers' phones busy. The page reads the painter once a second
while a picture is rendered and every ten seconds otherwise, asking
only for the tiles it does not have.

The agent knows none of this. To the agent the painter is one more
exposed Service.

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

## How many nodes

Four nodes may join from one address. How many the cluster takes in
all is the machine's to say (`internal/machine`, read from `/proc`
every two seconds):

- A tab is turned away while the machine has less than 1 GiB of memory
  it could still give out (`--reserve-memory`). A node costs it about
  10 MB, so on the demo's 8 GB that is somewhere past 500 nodes.
- A tab is asked to wait, and asks again by itself, while the machine's
  CPUs have been more than 90% busy (`--busy-above`). Work at a lowered
  priority is not counted, since the kernel takes its time back for
  anything else. The seeds run that way, so four seeds rendering a
  picture on both CPUs keep nobody out.
- `--max-nodes` is a ceiling over both, 1000.
- Pods take turns at the machine's part of starting, eight at a time
  (`--starts-at-once`): a crowd that joins at once is every one of them
  a node at once, and their pods a queue.

## The kernel comes from a mirror

A tab's first join downloads about 5 MB, and 4.5 MB of it is the
kernel. unkernel is published to npm, so jsDelivr serves the same file.
With `--unkernel-mirror` the agent fetches the kernel from the mirror
once at its start, and sends tabs there only if the bytes are its own
copy's. The scripts beside the kernel stay with the agent: a worker's
script has to come from the page's own origin.

## Seeds

A cluster nobody is visiting has no nodes, and a lone visitor would
find the counter with no pod to answer. `SEEDS` makes the machine keep
that many headless browsers of its own with the join page open
(`deploy/seed.sh`). The agent knows nothing of them: a seed joins from
the machine's own address like any visitor and counts against that
address's limit. Each is started in a time zone of its own, and since
the database of cities does not know the machine's own address, the
globe draws each at its time zone's city. An idle seed takes about 1%
of a core and 240 MB.

## Measured

2026-11-26, on an exe.dev machine with 2 CPUs and 8 GB, k3s 1.37.
Headless Chromiums on another machine were the nodes, joining 20 every
15 seconds until 307 were nodes, each with one Alpine pod. The run was
cut short there by a deploy, not by the machine.

|                                                   |              |
| ------------------------------------------------- | ------------ |
| one pod, from `kubectl apply` to Running          | 6 s          |
| a tab's pod, from the tab being told to Running   | 1.3 to 3 s   |
| the same while 80 nodes a minute join             | 5 to 10 s    |
| a closed tab's pod Running on another tab         | about 1 s    |
| the machine's memory with no nodes                | 825 MB       |
| the machine's memory a node with a pod            | 10 MB        |
| memory still spare at 307 nodes                   | 3.7 GB       |
| the machine's CPUs while 80 nodes a minute join   | 43% busy     |
| a request through the site to a pod, median       | 174 ms       |
| the same, 95th percentile, during the run         | 435 ms       |
| a node tab's memory in the browser, 128 MiB guest | 214 MB       |
| a first join's download from the machine, gzipped | about 0.5 MB |
| a first join's download from jsDelivr             | 4.5 MB       |

k3s itself is 650 MB of the 825.

Not measured: phones in number, more than 307 nodes, a pod doing real
work on every node at once.

## Missing

- `kubectl exec`, liveness probes and probes that are not an HTTP GET,
  init containers, more than one container.
- Config maps and secrets, as volumes or as variables.
- UDP.
- A pod keeps running through its tab closing only by being started
  again elsewhere. unkernel can save a running guest and resume it in
  another tab, and a branch of tabc does it (`tabc park`: the same
  process, half a second's pause, a snapshot of 4 to 12 MB). The agent
  does not use it yet. Kubernetes binds a pod to one node for life, so
  the agent would have to hand a parked container to the replacement
  pod that lands on another tab.
- Redis's first start on a new data directory fails once on a rename
  in the volume and succeeds on the restart. The cause is in how tabc
  serves a volume's ownership, and is not found yet.
