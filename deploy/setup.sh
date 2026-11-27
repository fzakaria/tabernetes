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

# The most tabs that may be nodes at once, and how many of them from
# one address. The agent turns tabs away sooner than MAX_NODES when the
# machine runs short of memory, which is the limit that matters; this
# one is a ceiling. Both can be set in the environment, for a test that
# brings every node from one address.
MAX_NODES=${MAX_NODES:-1000}
MAX_NODES_PER_ADDRESS=${MAX_NODES_PER_ADDRESS:-4}

# How many residents the machine keeps: headless browsers of its own
# with the join page open, so that a lone visitor finds a cluster with
# nodes in it. None unless asked for. Each takes about 250 MB.
RESIDENTS=${RESIDENTS:-0}

# The browser a resident is: Chrome's headless shell, at a version that
# is known to boot the guest.
CHROME_VERSION=158.0.8127.88
CHROME_ROOT=/opt/chrome-headless-shell
CHROME_URL=https://storage.googleapis.com/chrome-for-testing-public/$CHROME_VERSION/linux64/chrome-headless-shell-linux64.zip

# Where tabs fetch the kernel from when the place has the very file the
# bundle has: jsDelivr, which serves what unkernel publishes to npm.
# The agent checks, and serves its own copy otherwise. An empty value
# keeps every download on this machine.
UNKERNEL_MIRROR=${UNKERNEL_MIRROR-https://cdn.jsdelivr.net/npm/unkernel@{version\}/dist}

# Where the machine is, for the site's map, as a time zone's name. The
# machine's own clock is on UTC, which is nowhere.
ZONE=${ZONE:-America/Los_Angeles}

# The demo: the counter and a pod for every tab (examples/), and the
# counter's web Service open to the site's visitors. DEMO=no leaves the
# cluster empty.
DEMO=${DEMO:-yes}
EXPOSE=
if [ "$DEMO" = yes ]; then
  EXPOSE=counter=default/web:8000
fi

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
if [ "$DEMO" = yes ]; then
  # The pod for every tab was a Deployment of a fixed size before it
  # was a DaemonSet.
  k3s kubectl delete deployment everywhere --ignore-not-found
  k3s kubectl apply -f "$BUNDLE/examples/counter.yaml" -f "$BUNDLE/examples/everywhere.yaml"
fi

# The residents' word: made once, and kept, so that residents that are
# already running still say the right one after a new bundle.
WORD_FILE=/etc/tabernetes/resident-word
if [ ! -s "$WORD_FILE" ]; then
  (umask 077 && od -An -N16 -tx1 /dev/urandom | tr -d ' \n' > "$WORD_FILE")
fi
RESIDENT_WORD=$(cat "$WORD_FILE")

# The agent. Its settings hold the residents' word, so they are root's
# to read.
(umask 077 && cat > /etc/tabernetes/env <<ENV
PUBLIC_URL=$PUBLIC_URL
MAX_NODES=$MAX_NODES
MAX_NODES_PER_ADDRESS=$MAX_NODES_PER_ADDRESS
ZONE=$ZONE
EXPOSE=$EXPOSE
RESIDENT_WORD=$RESIDENT_WORD
UNKERNEL_MIRROR=$UNKERNEL_MIRROR
ENV
)
chmod 600 /etc/tabernetes/env
cp "$BUNDLE/deploy/tabernetes.service" /etc/systemd/system/tabernetes.service
systemctl daemon-reload
systemctl enable tabernetes.service >/dev/null 2>&1
systemctl restart tabernetes.service
sleep 2
systemctl --no-pager --lines=5 status tabernetes.service

# The residents: the browser, if the machine has none of this version,
# then as many units as were asked for, and no more.
cp "$BUNDLE/deploy/tabernetes-resident@.service" /etc/systemd/system/
systemctl daemon-reload
if [ "$RESIDENTS" -gt 0 ] && [ ! -x "$CHROME_ROOT/$CHROME_VERSION/chrome-headless-shell" ]; then
  unpack=$(mktemp -d)
  curl -sfL "$CHROME_URL" -o "$unpack/chrome.zip"
  unzip -q "$unpack/chrome.zip" -d "$unpack"
  mkdir -p "$CHROME_ROOT/$CHROME_VERSION"
  cp -r "$unpack/chrome-headless-shell-linux64/." "$CHROME_ROOT/$CHROME_VERSION/"
  rm -r "$unpack"
fi
if [ "$RESIDENTS" -gt 0 ]; then
  ln -sfn "$CHROME_ROOT/$CHROME_VERSION" "$CHROME_ROOT/current"
  if ldd "$CHROME_ROOT/current/chrome-headless-shell" | grep "not found"; then
    echo "the browser needs the libraries above; no residents were started" >&2
    RESIDENTS=0
  fi
fi
for unit in $(systemctl list-units --plain --no-legend --all 'tabernetes-resident@*' | awk '{print $1}'); do
  number=${unit#tabernetes-resident@}
  number=${number%.service}
  if [ "$number" -gt "$RESIDENTS" ]; then
    systemctl disable --now "$unit" >/dev/null 2>&1
  fi
done
number=1
while [ "$number" -le "$RESIDENTS" ]; do
  systemctl enable "tabernetes-resident@$number.service" >/dev/null 2>&1
  systemctl restart "tabernetes-resident@$number.service"
  number=$((number + 1))
done
