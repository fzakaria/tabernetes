// The globe: the world as dots, each node roughly where its address
// is, or at its time zone's city when the agent could not tell, and a
// line from each node that runs a pod to the cluster's own machine.
//
// Drawn on a canvas, by hand. The land is a list of points on the
// sphere (land.json, from tools/globe-data.py), and everything else is
// a few hundred points more, so each frame is some thousands of
// multiplications and one pass of drawing.

import { scatter } from "./format.js";

const RADIANS = Math.PI / 180;

// How the globe sits and turns: tilted to show the northern half, where
// most of the land and most of the phones are, turning this many
// degrees a second when left alone, and for this long left alone after
// a drag before it turns by itself again.
const TILT_DEGREES = 22;
const TILT_LIMIT_DEGREES = 70;
const SPIN_DEGREES_PER_SECOND = 5;
const IDLE_AFTER_DRAG_MS = 3000;

// How much of the canvas the sphere fills, leaving room for the arcs
// that rise off it.
const SPHERE_FILL = 0.4;

// How far nodes of one city are set apart, in degrees, so that a room
// full of phones is a cluster of dots and not one dot.
const SCATTER_LATITUDE = 2.4;
const SCATTER_LONGITUDE = 3.2;

// The arcs: how high the middle of one rises off the sphere, as a part
// of the radius, and how many straight pieces one is drawn in.
const ARC_LIFT = 0.28;
const ARC_PIECES = 36;

// Picking a node: how near a dot the pointer has to be, in pixels, how
// far a press may move and still be a pick and not a turn of the
// globe, and the ring drawn round a dot the pointer is on and round
// the one that was picked.
const PICK_REACH_PX = 14;
const PICK_SLOP_PX = 5;
const RING_HOVER_PX = 7;
const RING_PICKED_PX = 9;

// What the machine is called where a node's name would be. No node has
// the name: a node's name has no space in it.
export const MACHINE = "control plane";

// How long a ring spreads from a node that has just joined, how long a
// packet takes along an arc, and the most packets in flight at once.
const PULSE_MS = 2200;
const PACKET_MS = 1100;
const PACKETS_MOST = 60;

// The colours, which stay the same in a light page: the globe sits in
// a panel that is always dark.
const COLOR = {
  sphereInner: "#16233f",
  sphereOuter: "#0a1122",
  rim: "rgba(110, 160, 255, 0.55)",
  land: [150, 180, 245],
  idle: "#e8eeff",
  busy: "#6ee7a8",
  hub: "#ffd27a",
  arc: "rgba(110, 231, 168, 0.4)",
  packet: "#ffffff",
  asked: "#ffd27a",
  ring: "#ffffff",
};

// A point of the sphere from a latitude and a longitude.
function onSphere(latitude, longitude) {
  const phi = latitude * RADIANS;
  const lambda = longitude * RADIANS;
  return [Math.cos(phi) * Math.sin(lambda), Math.sin(phi), Math.cos(phi) * Math.cos(lambda)];
}

// The point a part of the way from one point of the sphere to another,
// along the great circle between them.
function between(from, to, part) {
  const dot = Math.min(1, Math.max(-1, from[0] * to[0] + from[1] * to[1] + from[2] * to[2]));
  const angle = Math.acos(dot);
  if (angle < 1e-6) {
    return from;
  }
  const a = Math.sin((1 - part) * angle) / Math.sin(angle);
  const b = Math.sin(part * angle) / Math.sin(angle);
  return [a * from[0] + b * to[0], a * from[1] + b * to[1], a * from[2] + b * to[2]];
}

// Make a globe on a canvas. `land` is land.json and `zones` is
// zones.json. `onPick` is called with a node's name when its dot is
// picked, with MACHINE for the machine's, and with null when the pick
// is let go. The returned object is told the cluster each time the page
// reads it, and when a visitor's request was answered.
export function createGlobe(canvas, land, zones, onPick) {
  const context = canvas.getContext("2d");

  // The land, as points of the sphere.
  const dots = [];
  for (let index = 0; index < land.length; index += 2) {
    dots.push(onSphere(land[index], land[index + 1]));
  }

  // What is drawn besides the land: the machine, the nodes by name,
  // and what is in flight.
  let hub = null;
  let nodes = new Map();
  let pulses = [];
  let packets = [];

  // Where each dot in view was last drawn, for the pointer to find, the
  // name of the dot the pointer is on, and the name of the one picked.
  let spots = [];
  let hovered = null;
  let picked = null;

  // How the globe is turned: the longitude and latitude facing the
  // viewer, in degrees.
  let spin = 0;
  let tilt = TILT_DEGREES;
  let dragging = null;
  let lastDrag = -Infinity;
  let lastFrame = performance.now();
  let visible = true;
  const still = matchMedia("(prefers-reduced-motion: reduce)").matches;

  // Where a node is on the sphere: where the agent says its address
  // is, or else its time zone's city, set a little apart from other
  // nodes of the same place. Null for a node with neither.
  function place(node) {
    const entry = node.placed ? [node.latitude, node.longitude] : zones[node.zone];
    if (!entry) {
      return null;
    }
    const latitude = entry[0] + (scatter(node.name, 1) - 0.5) * SCATTER_LATITUDE;
    const longitude = entry[1] + (scatter(node.name, 2) - 0.5) * SCATTER_LONGITUDE;
    return onSphere(latitude, longitude);
  }

  // A point of the sphere as the viewer sees it: across, up, and
  // towards the viewer, which is positive on the near side.
  function turned(point) {
    const lambda = -spin * RADIANS;
    const phi = tilt * RADIANS;
    const x = point[0] * Math.cos(lambda) + point[2] * Math.sin(lambda);
    const z1 = -point[0] * Math.sin(lambda) + point[2] * Math.cos(lambda);
    const y = point[1] * Math.cos(phi) - z1 * Math.sin(phi);
    const z = point[1] * Math.sin(phi) + z1 * Math.cos(phi);
    return [x, y, z];
  }

  // The path of an arc between two points of the sphere, lifted off it
  // in the middle, as points the viewer sees.
  function arc(from, to) {
    const path = [];
    for (let piece = 0; piece <= ARC_PIECES; piece++) {
      const part = piece / ARC_PIECES;
      const flat = between(from, to, part);
      const lift = 1 + ARC_LIFT * Math.sin(Math.PI * part);
      path.push(turned([flat[0] * lift, flat[1] * lift, flat[2] * lift]));
    }
    return path;
  }

  // Whether a point is in front of the sphere, or beside it: a lifted
  // point behind the sphere still shows past its edge.
  function shows(point) {
    return point[2] > 0 || point[0] * point[0] + point[1] * point[1] > 1;
  }

  function draw(now) {
    const size = canvas.clientWidth;
    const scale = devicePixelRatio || 1;
    if (canvas.width !== Math.round(size * scale)) {
      canvas.width = canvas.height = Math.round(size * scale);
    }
    context.setTransform(scale, 0, 0, scale, 0, 0);
    context.clearRect(0, 0, size, size);

    const center = size / 2;
    const radius = size * SPHERE_FILL;
    const at = (point) => [center + point[0] * radius, center - point[1] * radius];
    spots = [];

    // A ring round a dot the pointer is on, or the one picked.
    const ring = (name, x, y) => {
      if (name !== picked && name !== hovered) {
        return;
      }
      context.beginPath();
      context.arc(x, y, name === picked ? RING_PICKED_PX : RING_HOVER_PX, 0, 2 * Math.PI);
      context.lineWidth = name === picked ? 2 : 1.2;
      context.strokeStyle = COLOR.ring;
      context.stroke();
    };

    // The sphere, and the light at its edge.
    const body = context.createRadialGradient(
      center - radius * 0.3,
      center - radius * 0.35,
      radius * 0.1,
      center,
      center,
      radius,
    );
    body.addColorStop(0, COLOR.sphereInner);
    body.addColorStop(1, COLOR.sphereOuter);
    context.beginPath();
    context.arc(center, center, radius, 0, 2 * Math.PI);
    context.fillStyle = body;
    context.fill();
    context.lineWidth = 1.2;
    context.strokeStyle = COLOR.rim;
    context.shadowColor = COLOR.rim;
    context.shadowBlur = radius * 0.12;
    context.stroke();
    context.shadowBlur = 0;

    // The land: dots, fainter towards the edge.
    const dotRadius = Math.max(0.9, radius / 150);
    for (const dot of dots) {
      const seen = turned(dot);
      if (seen[2] <= 0) {
        continue;
      }
      const [x, y] = at(seen);
      context.beginPath();
      context.arc(x, y, dotRadius, 0, 2 * Math.PI);
      context.fillStyle = `rgba(${COLOR.land.join(",")},${0.18 + 0.5 * seen[2]})`;
      context.fill();
    }

    // The arcs, from each node that runs a pod to the machine.
    if (hub) {
      context.lineWidth = 1.1;
      context.strokeStyle = COLOR.arc;
      for (const node of nodes.values()) {
        if (!node.at || !node.busy) {
          continue;
        }
        const path = arc(node.at, hub);
        context.beginPath();
        let pen = false;
        for (const point of path) {
          if (!shows(point)) {
            pen = false;
            continue;
          }
          const [x, y] = at(point);
          if (pen) {
            context.lineTo(x, y);
          } else {
            context.moveTo(x, y);
          }
          pen = true;
        }
        context.stroke();
      }
    }

    // What is in flight along the arcs.
    packets = packets.filter((packet) => now - packet.started < PACKET_MS);
    for (const packet of packets) {
      const node = nodes.get(packet.node);
      if (!node?.at || !hub) {
        continue;
      }
      const part = (now - packet.started) / PACKET_MS;
      if (part < 0) {
        // A packet that follows another has not set out yet.
        continue;
      }
      const path = arc(node.at, hub);
      const point = path[Math.round((packet.toHub ? part : 1 - part) * ARC_PIECES)];
      if (!shows(point)) {
        continue;
      }
      const [x, y] = at(point);
      context.beginPath();
      context.arc(x, y, packet.asked ? 3.2 : 1.8, 0, 2 * Math.PI);
      context.fillStyle = packet.asked ? COLOR.asked : COLOR.packet;
      context.shadowColor = context.fillStyle;
      context.shadowBlur = 8;
      context.fill();
      context.shadowBlur = 0;
    }

    // The nodes: a dot for a visitor's, and a circle for a resident,
    // which is a browser of the machine's own.
    for (const [name, node] of nodes) {
      if (!node.at) {
        continue;
      }
      const seen = turned(node.at);
      if (seen[2] <= 0) {
        continue;
      }
      const [x, y] = at(seen);
      const color = node.busy ? COLOR.busy : COLOR.idle;
      context.beginPath();
      context.arc(x, y, 3, 0, 2 * Math.PI);
      context.shadowColor = color;
      context.shadowBlur = 10;
      if (node.resident) {
        context.lineWidth = 1.4;
        context.strokeStyle = color;
        context.stroke();
      } else {
        context.fillStyle = color;
        context.fill();
      }
      context.shadowBlur = 0;
      spots.push({ name, x, y });
      ring(name, x, y);
    }

    // A ring spreading from each node that has just joined.
    pulses = pulses.filter((pulse) => now - pulse.started < PULSE_MS);
    for (const pulse of pulses) {
      const node = nodes.get(pulse.node);
      if (!node?.at) {
        continue;
      }
      const seen = turned(node.at);
      if (seen[2] <= 0) {
        continue;
      }
      const part = (now - pulse.started) / PULSE_MS;
      const [x, y] = at(seen);
      context.beginPath();
      context.arc(x, y, 3 + part * 26, 0, 2 * Math.PI);
      context.lineWidth = 1.5;
      context.strokeStyle = `rgba(232, 238, 255, ${0.8 * (1 - part)})`;
      context.stroke();
    }

    // The machine.
    if (hub) {
      const seen = turned(hub);
      if (seen[2] > 0) {
        const [x, y] = at(seen);
        context.beginPath();
        context.arc(x, y, 5.5, 0, 2 * Math.PI);
        context.lineWidth = 2;
        context.strokeStyle = COLOR.hub;
        context.shadowColor = COLOR.hub;
        context.shadowBlur = 12;
        context.stroke();
        context.shadowBlur = 0;
        context.beginPath();
        context.arc(x, y, 2, 0, 2 * Math.PI);
        context.fillStyle = COLOR.hub;
        context.fill();
        spots.push({ name: MACHINE, x, y });
        ring(MACHINE, x, y);
      }
    }
  }

  // The name of the dot nearest a pointer's event, or null when no dot
  // is within reach.
  function under(event) {
    const box = canvas.getBoundingClientRect();
    const x = event.clientX - box.left;
    const y = event.clientY - box.top;
    let nearest = null;
    let nearestDistance = PICK_REACH_PX;
    for (const spot of spots) {
      const distance = Math.hypot(spot.x - x, spot.y - y);
      if (distance <= nearestDistance) {
        nearest = spot.name;
        nearestDistance = distance;
      }
    }
    return nearest;
  }

  // Pick a dot, or let the pick go with null, and say so.
  function pick(name) {
    if (name === picked) {
      return;
    }
    picked = name;
    onPick?.(name);
  }

  function frame(now) {
    const elapsed = Math.min(100, now - lastFrame);
    lastFrame = now;
    // A picked dot stays where it was picked: the globe waits.
    if (!dragging && !still && picked === null && now - lastDrag > IDLE_AFTER_DRAG_MS) {
      spin = (spin + (SPIN_DEGREES_PER_SECOND * elapsed) / 1000) % 360;
    }
    if (visible) {
      draw(now);
    }
    requestAnimationFrame(frame);
  }

  // Turning the globe by hand.
  canvas.addEventListener("pointerdown", (event) => {
    dragging = { x: event.clientX, y: event.clientY, spin, tilt };
    canvas.setPointerCapture(event.pointerId);
  });
  canvas.addEventListener("pointermove", (event) => {
    if (!dragging) {
      // The pointer over a dot shows that the dot can be picked.
      hovered = under(event);
      canvas.style.cursor = hovered ? "pointer" : "";
      return;
    }
    const perPixel = 180 / (canvas.clientWidth * SPHERE_FILL * 2);
    spin = dragging.spin - (event.clientX - dragging.x) * perPixel;
    tilt = Math.max(
      -TILT_LIMIT_DEGREES,
      Math.min(TILT_LIMIT_DEGREES, dragging.tilt + (event.clientY - dragging.y) * perPixel),
    );
  });
  const release = () => {
    if (dragging) {
      dragging = null;
      lastDrag = performance.now();
    }
  };
  canvas.addEventListener("pointerup", (event) => {
    // A press that went nowhere is a pick: of the dot under it, or of
    // nothing, which lets the last pick go.
    const moved = dragging
      ? Math.hypot(event.clientX - dragging.x, event.clientY - dragging.y)
      : Infinity;
    release();
    if (moved <= PICK_SLOP_PX) {
      pick(under(event));
    }
  });
  canvas.addEventListener("pointercancel", release);
  canvas.addEventListener("pointerleave", () => {
    hovered = null;
  });

  // Nothing is drawn while the globe is out of view.
  new IntersectionObserver((entries) => {
    visible = entries.at(-1).isIntersecting;
  }).observe(canvas);
  document.addEventListener("visibilitychange", () => {
    lastFrame = performance.now();
  });

  requestAnimationFrame(frame);

  return {
    // The cluster as just read. A node that was not there before gets a
    // ring, and a node whose pods have carried bytes since the last
    // reading gets packets along its arc.
    update(view) {
      const first = hub === null && nodes.size === 0;
      const machine = zones[view.machine.zone];
      hub = machine ? onSphere(machine[0], machine[1]) : null;
      if (first && machine) {
        // Start with the machine's side of the world facing.
        spin = machine[1];
      }

      const now = performance.now();
      const next = new Map();
      for (const node of view.nodes) {
        const before = nodes.get(node.name);
        const carried = node.pods.reduce((sum, pod) => sum + pod.down + pod.up, 0);
        const entry = {
          at: before?.at ?? place(node),
          busy: node.pods.some((pod) => pod.running),
          resident: node.resident,
          carried,
        };
        if (!before && !first) {
          pulses.push({ node: node.name, started: now });
        }
        if (before && carried > before.carried && packets.length < PACKETS_MOST) {
          packets.push({ node: node.name, started: now, toHub: true });
          packets.push({ node: node.name, started: now + PACKET_MS / 3, toHub: false });
        }
        next.set(node.name, entry);
      }
      nodes = next;

      // A picked node that has left is picked no longer.
      if (picked !== null && picked !== MACHINE && !nodes.has(picked)) {
        pick(null);
      }
    },

    // Let the pick go: the card that showed it was closed.
    unpick() {
      pick(null);
    },

    // A visitor's request was answered by a pod on this node: a packet
    // out to the node and one back.
    asked(name) {
      const now = performance.now();
      packets.push({ node: name, started: now, toHub: false, asked: true });
      packets.push({ node: name, started: now + PACKET_MS, toHub: true, asked: true });
    },

    // Turn the globe to a node and pick it, as if its dot had been
    // pressed. A node the globe does not have is not picked.
    show(name) {
      const node = nodes.get(name);
      if (!node?.at) {
        return;
      }
      spin = Math.atan2(node.at[0], node.at[2]) / RADIANS;
      pick(name);
    },
  };
}
