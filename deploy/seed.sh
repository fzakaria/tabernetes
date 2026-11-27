#!/bin/sh
# Run one seed: a headless browser with the join page open, in the time
# zone that is this seed's.
#
#   seed.sh <number>
#
# The page is asked to keep asking for a place (#keep), since nobody is
# in front of this browser to press a button when it is turned away.
set -eu
number=$1

# The time zones seeds are started in, in order. A seed past the end
# of the list starts again at the first.
ZONES="Europe/Lisbon Asia/Tokyo America/Sao_Paulo Africa/Lagos Australia/Sydney Asia/Kolkata Europe/Berlin America/Toronto America/Mexico_City Africa/Johannesburg Europe/London America/New_York Asia/Singapore America/Bogota Europe/Istanbul America/Chicago"

# The zone at this seed's place in the list.
count=$(echo "$ZONES" | wc -w)
index=$(((number - 1) % count + 1))
zone=$(echo "$ZONES" | cut -d " " -f "$index")

# The browser's own sandbox needs user namespaces, which Ubuntu keeps
# from programs it has no profile for. The page is the cluster's own,
# and the unit's settings are the fence.
TZ=$zone exec /opt/chrome-headless-shell/current/chrome-headless-shell \
  --no-sandbox \
  --disable-gpu \
  --disable-dev-shm-usage \
  --user-data-dir="$HOME/profile" \
  --remote-debugging-port=0 \
  "http://127.0.0.1:8000/join#keep"
