#!/bin/sh
# What a tab's pod may reach: other pods, and nothing else.
#
# A pod on a tab is a stranger's browser speaking from the cluster's
# machine. Every connection its program makes leaves from the pod's
# address here, so without these rules a visitor's tab would be a way
# onto the internet from this machine's address, and onto the machine
# itself: the API server, the kubelet, ssh.
#
# Tabs' pods get addresses in TABS, the upper half of the pod range
# (deploy/tabs.json). The cluster's own pods are in the lower half and
# are left alone. The script can be run again: each chain is emptied and
# filled, and each jump is added once.
set -eu

# The pod range, and the half of it tabs' pods are in.
PODS=10.244.0.0/16
TABS=10.244.128.0/17

# The chains: for what tabs' pods send on through the machine, and for
# what they send to the machine itself.
FORWARD_CHAIN=TABERNETES-FORWARD
INPUT_CHAIN=TABERNETES-INPUT

for chain in "$FORWARD_CHAIN" "$INPUT_CHAIN"; do
  iptables -N "$chain" 2>/dev/null || iptables -F "$chain"
done

# Answers to connections someone else opened always pass: a Service or
# a port forwarded from outside reaches a tab's pod, and the pod's
# replies go back where the connection came from.
iptables -A "$FORWARD_CHAIN" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
iptables -A "$INPUT_CHAIN" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN

# A new connection from a tab's pod passes to another pod. A Service's
# address has been rewritten to a pod's by this point, so Services pass
# too, cluster DNS among them.
iptables -A "$FORWARD_CHAIN" -d "$PODS" -j RETURN
iptables -A "$FORWARD_CHAIN" -j REJECT
iptables -A "$INPUT_CHAIN" -j REJECT

iptables -C FORWARD -s "$TABS" -j "$FORWARD_CHAIN" 2>/dev/null || iptables -I FORWARD 1 -s "$TABS" -j "$FORWARD_CHAIN"
iptables -C INPUT -s "$TABS" -j "$INPUT_CHAIN" 2>/dev/null || iptables -I INPUT 1 -s "$TABS" -j "$INPUT_CHAIN"
