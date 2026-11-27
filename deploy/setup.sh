#!/bin/sh
# Set the cluster's machine up, from the bundle in /opt/tabernetes:
#
#   setup.sh <public address>
#
# Installs k3s if the machine has none, with a plain bridge for the pod
# network, then the agent as a service. Safe to run again after a new
# bundle is copied over: k3s is left as it is, and the agent restarts.
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: setup.sh <public address>" >&2
  exit 2
fi
PUBLIC_URL=$1
BUNDLE=/opt/tabernetes

# How many tabs may be nodes at once. Each node's pod is two processes
# here, about 12 MB together, and the machine is small.
MAX_NODES=${MAX_NODES:-100}

# The pod range and the Service range. Neither is k3s's default, which
# collides with the addresses some hosts give their machines.
POD_CIDR=10.244.0.0/16
SERVICE_CIDR=10.96.0.0/16
CLUSTER_DNS=10.96.0.10

# The pod network: one bridge for every pod, the cluster's own and the
# tabs'. k3s reads the system's CNI directories once its own network is
# switched off, and brings the plugins itself.
mkdir -p /etc/cni/net.d /opt/cni/bin /etc/tabernetes /var/lib/tabernetes/data
cp "$BUNDLE/deploy/cluster.conflist" /etc/cni/net.d/10-cluster.conflist

if ! command -v k3s >/dev/null 2>&1; then
  # Docker's rules drop what a bridge forwards, and the machine has no
  # use for a second container runtime.
  systemctl disable --now docker.service docker.socket containerd.service >/dev/null 2>&1 || true

  # No node is given a pod range of its own: there are far more tabs
  # than there are ranges, and every pod is on the one bridge anyway.
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server \
    --cluster-cidr=$POD_CIDR --service-cidr=$SERVICE_CIDR --cluster-dns=$CLUSTER_DNS \
    --flannel-backend=none --disable-network-policy --disable-cloud-controller \
    --disable=traefik --disable=servicelb --disable=metrics-server --disable=local-storage \
    --kube-controller-manager-arg=allocate-node-cidrs=false \
    --write-kubeconfig-mode=644" sh -
fi
for plugin in bridge host-local loopback portmap; do
  ln -sf /var/lib/rancher/k3s/data/cni/$plugin /opt/cni/bin/$plugin
done

# The API server, before anything is asked of it.
until k3s kubectl get --raw /readyz >/dev/null 2>&1; do
  sleep 1
done
k3s kubectl apply -f "$BUNDLE/deploy/manifests/runtimeclass.yaml"

# The agent.
cat > /etc/tabernetes/env <<ENV
PUBLIC_URL=$PUBLIC_URL
MAX_NODES=$MAX_NODES
ENV
cp "$BUNDLE/deploy/tabernetes.service" /etc/systemd/system/tabernetes.service
systemctl daemon-reload
systemctl enable tabernetes.service >/dev/null 2>&1
systemctl restart tabernetes.service
sleep 2
systemctl --no-pager --lines=5 status tabernetes.service
