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

# What this machine was last set up with stays as it was unless this
# run says otherwise: someone who deploys a new bundle without naming
# every setting again does not undo the ones that were named before.
SETTINGS=/etc/tabernetes/settings
if [ -f "$SETTINGS" ]; then
  previous=$(cat "$SETTINGS")
  for line in $previous; do
    name=${line%%=*}
    eval "current=\${$name-}"
    if [ -z "$current" ]; then
      eval "$name=\${line#*=}"
    fi
  done
fi

# The most tabs that may be nodes at once, and how many of them from
# one address. The agent turns tabs away sooner than MAX_NODES when the
# machine runs short of memory, which is the limit that matters; this
# one is a ceiling. Both can be set in the environment, for a test that
# brings every node from one address.
MAX_NODES=${MAX_NODES:-1000}
MAX_NODES_PER_ADDRESS=${MAX_NODES_PER_ADDRESS:-4}

# How many seeds the machine keeps: headless browsers of its own with
# the join page open, so that a lone visitor finds a cluster with nodes
# in it. None unless asked for. Each takes about 250 MB, and they join
# from this machine's one address, so MAX_NODES_PER_ADDRESS is the most
# there can be.
SEEDS=${SEEDS:-0}

# The browser a seed is: Chrome's headless shell, at a version that
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

# The demo: the counter, whose web server every tab runs, and the
# painter of the picture those web servers render (examples/). The
# site's visitors may ask the counter for its count and nothing else,
# and the painter for anything it answers. The same web servers are
# exposed a second time for their tally, which a node's own page asks
# its own pod for, so that a tally is not counted as a visitor's
# request. DEMO=no leaves the cluster empty.
DEMO=${DEMO:-yes}
EXPOSE=
if [ "$DEMO" = yes ]; then
  EXPOSE=counter=default/web:8000/json,tally=default/web:8000/tally,picture=default/picture:8000
fi

# The pod network: one bridge for every pod, the cluster's own and the
# tabs'. k3s reads the system's CNI directories once its own network is
# switched off, and brings the plugins itself.
mkdir -p /etc/cni/net.d /opt/cni/bin /etc/tabernetes /etc/rancher/k3s /var/lib/tabernetes/data
cp "$BUNDLE/deploy/cluster.conflist" /etc/cni/net.d/10-cluster.conflist

# The directory pods' volumes are under gets a filesystem of its own,
# of a fixed size. A pod on a tab writes to its volume from a kernel
# the tab's owner controls, so nothing bounds what is written but the
# room there is: a volume that is filled must not be the machine's disk
# that is filled, with k3s on it.
DATA_DIR=/var/lib/tabernetes/data
DATA_IMAGE=/var/lib/tabernetes/data.img
DATA_SIZE=512M
if ! mountpoint -q "$DATA_DIR"; then
  # Pods are writing to the directory as it is. The agent's stopping
  # stops them, and what they wrote is carried over.
  systemctl stop tabernetes.service >/dev/null 2>&1 || true
  if [ ! -f "$DATA_IMAGE" ]; then
    truncate -s "$DATA_SIZE" "$DATA_IMAGE"
    mkfs.ext4 -q -F -m 0 "$DATA_IMAGE"
  fi
  kept=$(mktemp -d /var/lib/tabernetes/data-before.XXXXXX)
  cp -a "$DATA_DIR/." "$kept/"
  find "$DATA_DIR" -mindepth 1 -delete
  mount -o loop "$DATA_IMAGE" "$DATA_DIR"
  cp -a "$kept/." "$DATA_DIR/"
  rm -rf "$kept"
fi

# The filesystem is mounted again when the machine starts.
if ! grep -q "^$DATA_IMAGE " /etc/fstab; then
  echo "$DATA_IMAGE $DATA_DIR ext4 loop,nofail 0 0" >>/etc/fstab
fi

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
  # What the demo was before every tab's pod was the counter's web
  # server: a pod for every tab that only told the time, and web
  # servers on three of the tabs.
  k3s kubectl delete deployment everywhere web --ignore-not-found
  k3s kubectl delete daemonset everywhere --ignore-not-found
  k3s kubectl apply -f "$BUNDLE/examples/counter.yaml" -f "$BUNDLE/examples/picture.yaml"

  # The painter reads its program once, when the painter starts. The
  # version of the ConfigMap that holds the program is written on the
  # painter's pod as an annotation, so the Deployment replaces the pod
  # when the program changed and leaves the pod alone when not.
  program=$(k3s kubectl get configmap painter -o jsonpath='{.metadata.resourceVersion}')
  k3s kubectl patch deployment picture --patch \
    "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"tabernetes.dev/program\":\"$program\"}}}}}"
fi

# Where addresses are, for the site's map: DB-IP's free database of
# cities, which is published each month. The agent reads it on this
# machine, so no visitor's address goes anywhere to be looked up. A
# month that is not published yet leaves the file as it was, and
# without any file the map goes by each browser's time zone.
GEO_DIR=/var/lib/tabernetes/geo
GEO_DB=$GEO_DIR/dbip-city-lite.mmdb
mkdir -p "$GEO_DIR"
if [ ! -s "$GEO_DB" ] || [ -n "$(find "$GEO_DB" -mtime +30)" ]; then
  if curl -sfL "https://download.db-ip.com/free/dbip-city-lite-$(date +%Y-%m).mmdb.gz" -o "$GEO_DB.gz"; then
    gunzip -c "$GEO_DB.gz" > "$GEO_DB.new" && mv "$GEO_DB.new" "$GEO_DB"
  fi
  rm -f "$GEO_DB.gz"
fi

# The settings a later run starts from. None of them is a secret, and
# none has a space in it.
mkdir -p /etc/tabernetes
cat > "$SETTINGS" <<KEPT
MAX_NODES=$MAX_NODES
MAX_NODES_PER_ADDRESS=$MAX_NODES_PER_ADDRESS
SEEDS=$SEEDS
ZONE=$ZONE
DEMO=$DEMO
KEPT

# The agent.
(cat > /etc/tabernetes/env <<ENV
PUBLIC_URL=$PUBLIC_URL
MAX_NODES=$MAX_NODES
MAX_NODES_PER_ADDRESS=$MAX_NODES_PER_ADDRESS
ZONE=$ZONE
EXPOSE=$EXPOSE
UNKERNEL_MIRROR=$UNKERNEL_MIRROR
ENV
)
chmod 644 /etc/tabernetes/env
cp "$BUNDLE/deploy/tabernetes.service" /etc/systemd/system/tabernetes.service
systemctl daemon-reload
systemctl enable tabernetes.service >/dev/null 2>&1
systemctl restart tabernetes.service
sleep 2
systemctl --no-pager --lines=5 status tabernetes.service

# The seeds: the browser, if the machine has none of this version, then
# as many units as were asked for, and no more. What was set up under
# the seeds' earlier name goes.
for unit in $(systemctl list-units --plain --no-legend --all 'tabernetes-resident@*' | awk '{print $1}'); do
  systemctl disable --now "$unit" >/dev/null 2>&1 || true
done
rm -f /etc/systemd/system/tabernetes-resident@.service /etc/tabernetes/resident-word
systemctl reset-failed 'tabernetes-resident@*' >/dev/null 2>&1 || true
cp "$BUNDLE/deploy/tabernetes-seed@.service" /etc/systemd/system/
systemctl daemon-reload
if [ "$SEEDS" -gt 0 ] && [ ! -x "$CHROME_ROOT/$CHROME_VERSION/chrome-headless-shell" ]; then
  unpack=$(mktemp -d)
  curl -sfL "$CHROME_URL" -o "$unpack/chrome.zip"
  unzip -q "$unpack/chrome.zip" -d "$unpack"
  mkdir -p "$CHROME_ROOT/$CHROME_VERSION"
  cp -r "$unpack/chrome-headless-shell-linux64/." "$CHROME_ROOT/$CHROME_VERSION/"
  rm -r "$unpack"
fi
if [ "$SEEDS" -gt 0 ]; then
  ln -sfn "$CHROME_ROOT/$CHROME_VERSION" "$CHROME_ROOT/current"
  if ldd "$CHROME_ROOT/current/chrome-headless-shell" | grep "not found"; then
    echo "the browser needs the libraries above; no seeds were started" >&2
    SEEDS=0
  fi
fi
for unit in $(systemctl list-units --plain --no-legend --all 'tabernetes-seed@*' | awk '{print $1}'); do
  number=${unit#tabernetes-seed@}
  number=${number%.service}
  if [ "$number" -gt "$SEEDS" ]; then
    systemctl disable --now "$unit" >/dev/null 2>&1
  fi
done
number=1
while [ "$number" -le "$SEEDS" ]; do
  systemctl enable "tabernetes-seed@$number.service" >/dev/null 2>&1
  systemctl restart "tabernetes-seed@$number.service"
  number=$((number + 1))
done
