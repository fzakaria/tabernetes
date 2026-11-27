#!/bin/sh
# Set the cluster's machine up, from the bundle in /opt/tabernetes:
#
#   setup.sh <public address>
#
# Installs k3s if the machine has none, with a plain bridge for the pod
# network, then the agent as a service. Safe to run again after a new
# bundle is copied over: k3s is restarted only if its settings changed,
# and the agent restarts.
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: setup.sh <public address>" >&2
  exit 2
fi
PUBLIC_URL=$1
BUNDLE=/opt/tabernetes

# How many tabs may be nodes at once, and how many of them from one
# address. Each node's pod is two processes here, and the machine is
# small. Both can be set in the environment, for a test that brings
# every node from one address.
MAX_NODES=${MAX_NODES:-100}
MAX_NODES_PER_ADDRESS=${MAX_NODES_PER_ADDRESS:-4}

# The pod network: one bridge for every pod, the cluster's own and the
# tabs'. k3s reads the system's CNI directories once its own network is
# switched off, and brings the plugins itself.
mkdir -p /etc/cni/net.d /opt/cni/bin /etc/tabernetes /etc/rancher/k3s /var/lib/tabernetes/data
cp "$BUNDLE/deploy/cluster.conflist" /etc/cni/net.d/10-cluster.conflist

# k3s's settings. A k3s that is already running is restarted only when
# the settings changed.
K3S_CONFIG=/etc/rancher/k3s/config.yaml
changed=no
if ! cmp -s "$BUNDLE/deploy/k3s.yaml" "$K3S_CONFIG"; then
  cp "$BUNDLE/deploy/k3s.yaml" "$K3S_CONFIG"
  changed=yes
fi

if ! command -v k3s >/dev/null 2>&1; then
  # Docker's rules drop what a bridge forwards, and the machine has no
  # use for a second container runtime.
  systemctl disable --now docker.service docker.socket containerd.service >/dev/null 2>&1 || true
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC=server sh -
elif [ "$changed" = yes ]; then
  systemctl restart k3s.service
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
MAX_NODES_PER_ADDRESS=$MAX_NODES_PER_ADDRESS
ENV
cp "$BUNDLE/deploy/tabernetes.service" /etc/systemd/system/tabernetes.service
systemctl daemon-reload
systemctl enable tabernetes.service >/dev/null 2>&1
systemctl restart tabernetes.service
sleep 2
systemctl --no-pager --lines=5 status tabernetes.service
