<p align="center">
  <img src="web/mark.svg" width="64" height="64" alt="" />
</p>

<h1 align="center">tabernetes</h1>

<p align="center">
  <b>A Kubernetes cluster whose nodes are browser tabs.</b>
</p>

Open a page and your phone is a node. The scheduler sends it a pod, and
the pod's program runs on a Linux kernel booted in the tab. Close the
tab and the node is gone, and Kubernetes starts the pod on someone
else's phone.

```console
$ kubectl get nodes -o wide
NAME                  STATUS   VERSION        OS-IMAGE                KERNEL-VERSION           CONTAINER-RUNTIME
linux-chromium-ef05   Ready    v1.37.2+k3s1   Chromium 157 on Linux   7.3.4-unkernel (arm64)   tabc://1.0.0
tabernetes            Ready    v1.37.2+k3s1   Ubuntu 24.04.5 LTS      6.12.115 (amd64)         containerd://2.3.7-k3s1

$ kubectl apply -f deploy/manifests/hello.yaml
$ kubectl get pod hello -o wide
NAME    READY   STATUS    AGE   IP             NODE
hello   1/1     Running   6s    10.244.128.2   linux-chromium-ef05

$ kubectl logs hello
hello from aarch64 Linux 7.3.4+, in a browser tab
```

The control plane is an ordinary Kubernetes, k3s on one small machine.
The nodes are whoever has the join page open.

## Running a pod in a tab

A pod asks for a tab by its RuntimeClass, and is otherwise a pod:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: hello
spec:
  runtimeClassName: tab
  containers:
    - name: hello
      image: alpine
      command: ["sh", "-c", "echo hello from $(uname -m); sleep 3600"]
```

The guest is aarch64 on every device, so the image needs a
`linux/arm64` variant. [deploy/manifests/counter.yaml](deploy/manifests/counter.yaml)
is two web servers and the Redis they count visits in, each in a tab of
its own, with a Service in front of each.

## How it works

1. One agent runs beside the API server and plays kubelet for every tab
   at once.
2. A browser that opens the join page holds a WebSocket to the agent.
   The agent registers a Node for it, named after the device.
3. When the scheduler binds a pod to that Node, the agent gives the pod
   a network namespace and an address on the machine, and starts the
   container there with [tabc], a runtime that runs a container in a
   browser tab.
4. The agent tells the page, and the page opens a frame on the
   container. The frame boots [unkernel], a Linux kernel compiled to
   WebAssembly, and runs the program.
5. The tab supplies the CPU and the memory. The container's files and
   its network stay on the machine: every connection the program makes
   leaves from the pod's address there, so a Service reaches a pod on a
   phone like any other pod.
6. When the socket closes, the agent deletes the Node and its pods, and
   their controllers make new pods for other nodes.

[docs/design.md](docs/design.md) has the details, the measurements and
what is missing.

## What a tab's pod is

- One container, one CPU, 128 to 256 MiB of memory.
- `command`, `args`, `env`, `workingDir`, TCP `ports`, `emptyDir` and
  `hostPath` volumes, `restartPolicy`. `kubectl logs` works.
  `kubectl exec`, probes, init containers, config maps and secrets do
  not, yet.
- Able to reach other pods and Services, and nothing else. A pod on a
  tab is a stranger's browser speaking from the cluster's machine, so
  the machine's firewall refuses it the internet, the API server and
  the machine itself.
- Readable by its node. The tab holds the pod's memory, so nothing
  secret belongs in one.

## Running your own

On any Linux machine with systemd, root and a kernel that has bridges
and network namespaces:

```console
$ nix run github:fzakaria/tabernetes#deploy -- <ssh host> <public address>
```

That copies the agent and tabc over, installs k3s, and starts the
agent on port 8000. Put HTTPS in front of the port and open the
address.

## Hacking

```console
$ nix build          # the agent, one static file
$ nix flake check    # the tests
$ nix fmt            # before committing
```

## Credits

[tabc] runs the containers and [unkernel], by Pooya Parsa, is the
kernel they run on.

## License

MIT, please see [LICENSE](LICENSE).

[tabc]: https://github.com/fzakaria/tabc
[unkernel]: https://github.com/pithings/unkernel
